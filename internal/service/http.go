package service

import (
	"errors"
	"log"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
)

// httpGetFileContent handles GET /v1/files/{id}/content directly, bypassing
// Vanguard, so downloads get Range, HEAD, and conditional GET. Authentication
// is enforced by upstream middleware; ownership is checked here.
func (s *FileService) httpGetFileContent(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if err := s.db.CheckFileOwner(r.Context(), id, owner); err != nil {
		writeHTTPError(w, r, "httpGetFileContent", err)
		return
	}
	s.ServeFile(w, r, id)
}

// httpUpdateFileContent handles PUT /v1/files/{id}/content directly, bypassing
// Vanguard, so a failed If-Match precondition can answer 412.
func (s *FileService) httpUpdateFileContent(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	ct, err := validateContentType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f, err := s.replaceFile(r.Context(), id, owner, ct, r.Header.Get("If-Match"), r.Body)
	if err != nil {
		writeHTTPError(w, r, "httpUpdateFileContent", err)
		return
	}

	s.writeFileJSON(w, f)
}

// writeHTTPError answers a direct handler's request with the status for a
// db-layer error, logging anything unexpected under op.
func writeHTTPError(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, errPreconditionFailed):
		http.Error(w, err.Error(), http.StatusPreconditionFailed)
	case errors.Is(err, db.ErrFileNotFound):
		http.NotFound(w, r)
	case errors.Is(err, db.ErrPermissionDenied):
		http.Error(w, "permission denied", http.StatusForbidden)
	default:
		log.Printf("%s %s: %v", op, r.PathValue("id"), err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// fileJSON matches the Vanguard JSON codec options in Mount, so the
// direct handlers return the same body as the transcoded RPCs.
var fileJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// writeFileJSON writes f as the REST body of a file-returning RPC, which is
// the bare File (response_body: "file").
func (s *FileService) writeFileJSON(w http.ResponseWriter, f *db.File) {
	b, err := fileJSON.Marshal(s.fileProto(f))
	if err != nil {
		log.Printf("marshaling file %s: %v", f.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if f.Checksum != "" {
		w.Header().Set("ETag", etag(f.Checksum))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
