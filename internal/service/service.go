package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/api/httpbody"
	"google.golang.org/protobuf/types/known/timestamppb"

	stashyv1 "github.com/stashysh/stashy/gen/stashy/v1"
	"github.com/stashysh/stashy/gen/stashy/v1/stashyv1connect"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage"
)

const chunkSize = 64 * 1024 // 64KB

type FileService struct {
	store    storage.Storage
	db       *db.DB
	hostname string
}

var _ stashyv1connect.FileServiceHandler = (*FileService)(nil)

func New(store storage.Storage, database *db.DB, hostname string) *FileService {
	return &FileService{store: store, db: database, hostname: strings.TrimRight(hostname, "/")}
}

// validateContentType checks and normalizes the content type from an HttpBody.
func validateContentType(ct string) (string, error) {
	if strings.HasPrefix(ct, "multipart/") {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("multipart uploads are not supported, use --data-binary with an explicit Content-Type header"))
	}
	if ct == "" {
		return "application/octet-stream", nil
	}
	return ct, nil
}

// fileError maps a db/storage-layer error to the appropriate connect code.
// errPreconditionFailed reports that an If-Match condition didn't hold.
var errPreconditionFailed = errors.New("precondition failed: the file's content has changed")

func fileError(err error) error {
	switch {
	case errors.Is(err, errPreconditionFailed):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case strings.Contains(err.Error(), "not found"):
		return connect.NewError(connect.CodeNotFound, err)
	case strings.Contains(err.Error(), "permission denied"):
		return connect.NewError(connect.CodePermissionDenied, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// canonicalURL builds the canonical public URL for a file, including its slug
// when set.
func (s *FileService) canonicalURL(f *db.File) string {
	if f.Slug != "" {
		return s.hostname + "/" + f.ID + "/" + f.Slug
	}
	return s.hostname + "/" + f.ID
}

// fileProto converts a metadata row to its API representation.
func (s *FileService) fileProto(f *db.File) *stashyv1.File {
	return &stashyv1.File{
		Id:          f.ID,
		Name:        f.Name,
		Url:         s.canonicalURL(f),
		ContentType: f.ContentType,
		Size:        f.Size,
		Checksum:    f.Checksum,
		Visibility:  f.Visibility,
		Slug:        f.Slug,
		CreatedAt:   timestamppb.New(f.CreatedAt),
		UpdatedAt:   timestamppb.New(f.UpdatedAt),
	}
}

// putFile streams r into storage under a fresh id and records the metadata
// row. Bytes are written first; if the insert fails the orphaned bytes are
// removed so the database stays the source of truth.
// meta carries the new file's owner, content type, and optional slug, name,
// and visibility; the id and size are filled in here.
func (s *FileService) putFile(ctx context.Context, meta db.File, r io.Reader) (*db.File, error) {
	id, err := storage.NewID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	stored, err := s.store.Put(ctx, id, meta.ContentType, r)
	if err != nil {
		return nil, err
	}

	meta.ID, meta.Size, meta.Checksum = id, stored.Size, stored.Checksum
	f, err := s.db.CreateFile(ctx, meta)
	if err != nil {
		if derr := s.store.Delete(ctx, id); derr != nil {
			log.Printf("cleaning up %s after failed insert: %v", id, derr)
		}
		return nil, err
	}
	return f, nil
}

// replaceFile overwrites an existing file's bytes and content metadata after
// verifying ownership, and returns the updated metadata row.
// A non-empty ifMatch is an HTTP If-Match header: the content is replaced
// only if it matches the current ETag, else errPreconditionFailed is returned
// before anything is written.
func (s *FileService) replaceFile(ctx context.Context, id, owner, contentType, ifMatch string, r io.Reader) (*db.File, error) {
	f, err := s.ownedFile(ctx, id, owner)
	if err != nil {
		return nil, err
	}
	if ifMatch != "" && !etagMatchesIfMatch(ifMatch, f.Checksum) {
		return nil, errPreconditionFailed
	}

	stored, err := s.store.Put(ctx, id, contentType, r)
	if err != nil {
		return nil, err
	}
	if err := s.db.UpdateFileContent(ctx, id, owner, contentType, stored.Size, stored.Checksum); err != nil {
		return nil, err
	}
	return s.db.GetFile(ctx, id)
}

func (s *FileService) CreateFile(
	ctx context.Context,
	stream *connect.ClientStream[stashyv1.CreateFileRequest],
) (*connect.Response[stashyv1.CreateFileResponse], error) {
	owner, _ := auth.UserIDFromContext(ctx)

	// Read first chunk to get content type, slug, name, and visibility.
	var contentType, slug, name, visibility string
	var firstData []byte
	for stream.Receive() {
		msg := stream.Msg()
		if msg.Slug != nil {
			slug = *msg.Slug
		}
		if msg.Name != nil {
			name = *msg.Name
		}
		if msg.Visibility != nil {
			visibility = *msg.Visibility
		}
		if msg.Content == nil {
			continue
		}
		ct, err := validateContentType(msg.Content.ContentType)
		if err != nil {
			return nil, err
		}
		contentType = ct
		firstData = msg.Content.Data
		break
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()

	var putResult struct {
		file *db.File
		err  error
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		putResult.file, putResult.err = s.putFile(ctx, db.File{
			Owner: owner, Slug: slug, Name: name, Visibility: visibility, ContentType: contentType,
		}, pr)
	}()

	if len(firstData) > 0 {
		if _, err := pw.Write(firstData); err != nil {
			pw.Close()
			<-done
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	for stream.Receive() {
		msg := stream.Msg()
		if msg.Content == nil {
			continue
		}
		if _, err := pw.Write(msg.Content.Data); err != nil {
			pw.Close()
			<-done
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := stream.Err(); err != nil {
		pw.CloseWithError(err)
		<-done
		return nil, err
	}

	pw.Close()
	<-done

	if putResult.err != nil {
		return nil, connect.NewError(connect.CodeInternal, putResult.err)
	}

	return connect.NewResponse(&stashyv1.CreateFileResponse{
		File: s.fileProto(putResult.file),
	}), nil
}

func (s *FileService) UpdateFileContent(
	ctx context.Context,
	stream *connect.ClientStream[stashyv1.UpdateFileContentRequest],
) (*connect.Response[stashyv1.UpdateFileContentResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	// First message contains both id (from path) and file data (from body).
	if !stream.Receive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file id is required"))
	}
	msg := stream.Msg()
	id := msg.Id
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file id is required"))
	}

	var ct string
	if msg.Content != nil {
		ct = msg.Content.ContentType
	}
	contentType, err := validateContentType(ct)
	if err != nil {
		return nil, err
	}
	var firstData []byte
	if msg.Content != nil {
		firstData = msg.Content.Data
	}

	pr, pw := io.Pipe()
	var updated *db.File
	var updateErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		updated, updateErr = s.replaceFile(ctx, id, owner, contentType, "", pr)
		// Drain the pipe on early failure (e.g. ownership check) so the
		// writer side doesn't block forever.
		if updateErr != nil {
			io.Copy(io.Discard, pr)
		}
	}()

	if len(firstData) > 0 {
		if _, err := pw.Write(firstData); err != nil {
			pw.Close()
			<-done
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	for stream.Receive() {
		msg := stream.Msg()
		if msg.Content == nil {
			continue
		}
		if _, err := pw.Write(msg.Content.Data); err != nil {
			pw.Close()
			<-done
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := stream.Err(); err != nil {
		pw.CloseWithError(err)
		<-done
		return nil, err
	}

	pw.Close()
	<-done

	if updateErr != nil {
		return nil, fileError(updateErr)
	}

	return connect.NewResponse(&stashyv1.UpdateFileContentResponse{
		File: s.fileProto(updated),
	}), nil
}

// UpdateFile updates a file's mutable fields. Currently only the slug.
func (s *FileService) UpdateFile(
	ctx context.Context,
	req *connect.Request[stashyv1.UpdateFileRequest],
) (*connect.Response[stashyv1.UpdateFileResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	id := req.Msg.Id
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file id is required"))
	}

	// A nil slug means "leave unchanged"; an empty string clears it. The slug
	// format is enforced by the protovalidate interceptor.
	if req.Msg.Slug != nil {
		if err := s.db.SetFileSlug(ctx, id, owner, *req.Msg.Slug); err != nil {
			return nil, fileError(err)
		}
	}
	if req.Msg.Name != nil {
		if err := s.db.SetFileName(ctx, id, owner, *req.Msg.Name); err != nil {
			return nil, fileError(err)
		}
	}
	if req.Msg.Visibility != nil {
		if err := s.db.SetFileVisibility(ctx, id, owner, *req.Msg.Visibility); err != nil {
			return nil, fileError(err)
		}
	}

	// SetFileSlug checks ownership, but an update with no fields skips it.
	f, err := s.ownedFile(ctx, id, owner)
	if err != nil {
		return nil, fileError(err)
	}

	return connect.NewResponse(&stashyv1.UpdateFileResponse{
		File: s.fileProto(f),
	}), nil
}

func (s *FileService) DeleteFile(
	ctx context.Context,
	req *connect.Request[stashyv1.DeleteFileRequest],
) (*connect.Response[stashyv1.DeleteFileResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	if err := s.db.DeleteFile(ctx, req.Msg.Id, owner); err != nil {
		return nil, fileError(err)
	}
	// The row is gone, so the file is already unreachable; leftover bytes
	// from a failed delete are orphans, not corruption.
	if err := s.store.Delete(ctx, req.Msg.Id); err != nil {
		log.Printf("deleting bytes for %s: %v", req.Msg.Id, err)
	}
	return connect.NewResponse(&stashyv1.DeleteFileResponse{}), nil
}

const defaultListLimit = 50

// ListFiles returns a page of the caller's files, newest first. The after
// cursor is a file id; its created_at anchors the keyset query.
func (s *FileService) ListFiles(
	ctx context.Context,
	req *connect.Request[stashyv1.ListFilesRequest],
) (*connect.Response[stashyv1.ListFilesResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	limit := defaultListLimit
	if req.Msg.Limit != nil {
		limit = int(*req.Msg.Limit)
	}

	var afterTime time.Time
	var afterID string
	if req.Msg.After != nil {
		// A missing or foreign cursor file is reported the same way so the
		// response doesn't reveal whether another user's id exists.
		f, err := s.db.GetFile(ctx, *req.Msg.After)
		if err != nil && !strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if err != nil || f.Owner != owner {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("after: file not found"))
		}
		afterTime, afterID = f.CreatedAt, f.ID
	}

	files, err := s.db.ListFiles(ctx, owner, limit, afterTime, afterID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &stashyv1.ListFilesResponse{Files: make([]*stashyv1.File, 0, len(files))}
	for i := range files {
		resp.Files = append(resp.Files, s.fileProto(&files[i]))
	}
	return connect.NewResponse(resp), nil
}

// ownedFile returns id's metadata row if owner owns it, with errors matching
// the db-layer conventions ("file not found", "permission denied").
func (s *FileService) ownedFile(ctx context.Context, id, owner string) (*db.File, error) {
	f, err := s.db.GetFile(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.Owner != owner {
		return nil, fmt.Errorf("permission denied")
	}
	return f, nil
}

func (s *FileService) GetFile(
	ctx context.Context,
	req *connect.Request[stashyv1.GetFileRequest],
) (*connect.Response[stashyv1.GetFileResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	f, err := s.ownedFile(ctx, req.Msg.Id, owner)
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&stashyv1.GetFileResponse{File: s.fileProto(f)}), nil
}

func (s *FileService) GetFileContent(
	ctx context.Context,
	req *connect.Request[stashyv1.GetFileContentRequest],
	stream *connect.ServerStream[stashyv1.GetFileContentResponse],
) error {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	f, err := s.ownedFile(ctx, req.Msg.Id, owner)
	if err != nil {
		return fileError(err)
	}

	rc, err := s.store.Get(ctx, req.Msg.Id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return connect.NewError(connect.CodeNotFound, err)
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	defer rc.Close()

	buf := make([]byte, chunkSize)
	first := true

	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			chunk := &stashyv1.GetFileContentResponse{
				Content: &httpbody.HttpBody{
					Data: buf[:n],
				},
			}
			if first {
				chunk.Content.ContentType = f.ContentType
				first = false
			}
			if err := stream.Send(chunk); err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return connect.NewError(connect.CodeInternal, readErr)
		}
	}
}
