package service

import (
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
)

// PublicFileHandler serves the public file namespace at the root: /{id} or
// /{id}/{slug}. It is registered as the catch-all so it doesn't conflict with
// the /v1/ API subtree, so it parses the path itself.
func (s *FileService) PublicFileHandler(sessions *auth.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		id, urlSlug, ok := splitFilePath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		f, err := s.db.GetFile(r.Context(), id)
		if err != nil {
			if errors.Is(err, db.ErrFileNotFound) {
				http.NotFound(w, r)
				return
			}
			log.Printf("PublicFileHandler: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Anything but public needs a signed-in user, and anything but internal
		// (i.e. private) needs the owner, so an unexpected value fails closed.
		if f.Visibility != db.VisibilityPublic {
			userID, ok := sessions.GetUserID(r)
			if !ok {
				if shouldRedirectToLogin(r) {
					http.Redirect(w, r, "/auth/google/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusTemporaryRedirect)
					return
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// 404 rather than 403, so a private file's existence isn't revealed.
			if f.Visibility != db.VisibilityInternal && userID != f.Owner {
				http.NotFound(w, r)
				return
			}
		}

		// Redirect any non-matching slug (a bare /{id}, a stale slug from before
		// a rename, or a typo) to the current canonical URL, so links shared
		// across renames keep working.
		if urlSlug != f.Slug {
			http.Redirect(w, r, f.Path(), http.StatusFound)
			return
		}

		s.ServeFile(w, r, id, http.Error)
	}
}

func shouldRedirectToLogin(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	accept := r.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "text/html")
}

// splitFilePath parses a root request path into a file id and optional slug.
// It reports false for anything that isn't /{id} or /{id}/{slug}.
func splitFilePath(p string) (id, slug string, ok bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return parts[0], "", true
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

// ServeFile fetches id and streams it directly to w. Metadata (content type,
// size) comes from the database; storage is only touched for the bytes.
// The caller is responsible for any authorization checks before calling this.
// Errors are written with httpError: http.Error for browsers, apiError for
// the API.
func (s *FileService) ServeFile(w http.ResponseWriter, r *http.Request, id string, httpError func(w http.ResponseWriter, message string, status int)) {
	f, err := s.db.GetFile(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrFileNotFound) {
			httpError(w, "file not found", http.StatusNotFound)
			return
		}
		log.Printf("ServeFile %s: %v", id, err)
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Set once for every path below: full GET, HEAD, and range responses.
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	if f.Checksum != "" {
		w.Header().Set("ETag", etag(f.Checksum))
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && etagMatchesIfNoneMatch(r.Header.Get("If-None-Match"), f.Checksum) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if f.Name != "" {
		// inline keeps browsers displaying the file; the filename is used
		// when it is saved. FormatMediaType encodes non-ASCII names per RFC 2231.
		if cd := mime.FormatMediaType("inline", map[string]string{"filename": f.Name}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
	}

	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		s.serveFileRange(w, r, id, f.Size, rangeHeader, httpError)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	if r.Method == http.MethodHead {
		return
	}

	rc, err := s.store.Get(r.Context(), id)
	if err != nil {
		log.Printf("ServeFile %s: %v", id, err)
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("ServeFile %s: copy: %v", id, err)
	}
}

// serveFileRange handles Range requests. Content-Type and Accept-Ranges are
// already set by ServeFile.
func (s *FileService) serveFileRange(w http.ResponseWriter, r *http.Request, id string, size int64, rangeHeader string, httpError func(w http.ResponseWriter, message string, status int)) {
	byteRange, err := parseByteRange(rangeHeader, size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		httpError(w, "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(byteRange.length(), 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", byteRange.start, byteRange.end, size))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusPartialContent)
		return
	}

	rc, err := s.store.GetRange(r.Context(), id, byteRange.start, byteRange.length())
	if err != nil {
		log.Printf("ServeFile %s: get range: %v", id, err)
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.WriteHeader(http.StatusPartialContent)
	if _, err := io.CopyN(w, rc, byteRange.length()); err != nil {
		log.Printf("ServeFile %s: copy: %v", id, err)
	}
}

type byteRange struct {
	start int64
	end   int64
}

func (r byteRange) length() int64 {
	return r.end - r.start + 1
}

func parseByteRange(header string, size int64) (byteRange, error) {
	if size <= 0 || !strings.HasPrefix(header, "bytes=") {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	spec := strings.TrimSpace(strings.TrimPrefix(header, "bytes="))
	if spec == "" || strings.Contains(spec, ",") {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	startText, endText, ok := strings.Cut(spec, "-")
	if !ok {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	if startText == "" {
		suffixLength, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || suffixLength <= 0 {
			return byteRange{}, fmt.Errorf("invalid range")
		}
		if suffixLength > size {
			suffixLength = size
		}
		return byteRange{start: size - suffixLength, end: size - 1}, nil
	}

	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 || start >= size {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	end := size - 1
	if endText != "" {
		end, err = strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return byteRange{}, fmt.Errorf("invalid range")
		}
		if end >= size {
			end = size - 1
		}
	}

	return byteRange{start: start, end: end}, nil
}

// etag is the strong HTTP entity tag for a file's content checksum.
func etag(checksum string) string {
	return `"` + checksum + `"`
}

// etagMatchesIfMatch reports whether an If-Match header matches the current
// content. If-Match uses strong comparison: weak tags never match, and a file
// without a checksum matches only "*".
func etagMatchesIfMatch(header, checksum string) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || (checksum != "" && tag == etag(checksum)) {
			return true
		}
	}
	return false
}

// etagMatchesIfNoneMatch reports whether an If-None-Match header matches the
// current content, using weak comparison.
func etagMatchesIfNoneMatch(header, checksum string) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
		if tag == "*" || tag == etag(checksum) {
			return true
		}
	}
	return false
}
