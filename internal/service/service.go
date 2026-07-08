package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/api/httpbody"

	stashyv1alpha1 "github.com/stashysh/stashy/gen/stashy/v1alpha1"
	"github.com/stashysh/stashy/gen/stashy/v1alpha1/stashyv1alpha1connect"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage"
)

const chunkSize = 64 * 1024 // 64KB

type StorageService struct {
	store    storage.Storage
	db       *db.DB
	hostname string
}

var _ stashyv1alpha1connect.StorageServiceHandler = (*StorageService)(nil)

func New(store storage.Storage, database *db.DB, hostname string) *StorageService {
	return &StorageService{store: store, db: database, hostname: strings.TrimRight(hostname, "/")}
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
func fileError(err error) error {
	switch {
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
func (s *StorageService) canonicalURL(f *db.File) string {
	if f.Slug != "" {
		return s.hostname + "/" + f.ID + "/" + f.Slug
	}
	return s.hostname + "/" + f.ID
}

// putFile streams r into storage under a fresh id and records the metadata
// row. Bytes are written first; if the insert fails the orphaned bytes are
// removed so the database stays the source of truth.
func (s *StorageService) putFile(ctx context.Context, owner, contentType string, r io.Reader) (*db.File, error) {
	id, err := storage.NewID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	size, err := s.store.Put(ctx, id, r)
	if err != nil {
		return nil, err
	}

	f, err := s.db.CreateFile(ctx, id, owner, contentType, size)
	if err != nil {
		if derr := s.store.Delete(ctx, id); derr != nil {
			log.Printf("cleaning up %s after failed insert: %v", id, derr)
		}
		return nil, err
	}
	return f, nil
}

// replaceFile overwrites an existing file's bytes and content metadata after
// verifying ownership.
func (s *StorageService) replaceFile(ctx context.Context, id, owner, contentType string, r io.Reader) error {
	if err := s.db.CheckFileOwner(ctx, id, owner); err != nil {
		return err
	}

	size, err := s.store.Put(ctx, id, r)
	if err != nil {
		return err
	}
	return s.db.UpdateFileContent(ctx, id, owner, contentType, size)
}

func (s *StorageService) CreateFile(
	ctx context.Context,
	stream *connect.ClientStream[stashyv1alpha1.CreateFileRequest],
) (*connect.Response[stashyv1alpha1.CreateFileResponse], error) {
	owner, _ := auth.UserIDFromContext(ctx)

	// Read first chunk to get content type.
	var contentType string
	var firstData []byte
	for stream.Receive() {
		msg := stream.Msg()
		if msg.File == nil {
			continue
		}
		ct, err := validateContentType(msg.File.ContentType)
		if err != nil {
			return nil, err
		}
		contentType = ct
		firstData = msg.File.Data
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
		putResult.file, putResult.err = s.putFile(ctx, owner, contentType, pr)
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
		if msg.File == nil {
			continue
		}
		if _, err := pw.Write(msg.File.Data); err != nil {
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

	return connect.NewResponse(&stashyv1alpha1.CreateFileResponse{
		Id:  putResult.file.ID,
		Url: s.hostname + "/" + putResult.file.ID,
	}), nil
}

func (s *StorageService) ReplaceFile(
	ctx context.Context,
	stream *connect.ClientStream[stashyv1alpha1.ReplaceFileRequest],
) (*connect.Response[stashyv1alpha1.ReplaceFileResponse], error) {
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
	if msg.File != nil {
		ct = msg.File.ContentType
	}
	contentType, err := validateContentType(ct)
	if err != nil {
		return nil, err
	}
	var firstData []byte
	if msg.File != nil {
		firstData = msg.File.Data
	}

	pr, pw := io.Pipe()
	var updateErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		updateErr = s.replaceFile(ctx, id, owner, contentType, pr)
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
		if msg.File == nil {
			continue
		}
		if _, err := pw.Write(msg.File.Data); err != nil {
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

	return connect.NewResponse(&stashyv1alpha1.ReplaceFileResponse{}), nil
}

// UpdateFile updates a file's mutable fields. Currently only the slug.
func (s *StorageService) UpdateFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.UpdateFileRequest],
) (*connect.Response[stashyv1alpha1.UpdateFileResponse], error) {
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

	f, err := s.db.GetFile(ctx, id)
	if err != nil {
		return nil, fileError(err)
	}

	return connect.NewResponse(&stashyv1alpha1.UpdateFileResponse{
		Url: s.canonicalURL(f),
	}), nil
}

func (s *StorageService) DeleteFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.DeleteFileRequest],
) (*connect.Response[stashyv1alpha1.DeleteFileResponse], error) {
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
	return connect.NewResponse(&stashyv1alpha1.DeleteFileResponse{}), nil
}

func (s *StorageService) PublishFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.PublishFileRequest],
) (*connect.Response[stashyv1alpha1.PublishFileResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	if err := s.db.SetFilePublic(ctx, req.Msg.Id, owner, true); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&stashyv1alpha1.PublishFileResponse{}), nil
}

func (s *StorageService) UnpublishFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.UnpublishFileRequest],
) (*connect.Response[stashyv1alpha1.UnpublishFileResponse], error) {
	owner, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	if err := s.db.SetFilePublic(ctx, req.Msg.Id, owner, false); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&stashyv1alpha1.UnpublishFileResponse{}), nil
}

func (s *StorageService) GetFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.GetFileRequest],
	stream *connect.ServerStream[stashyv1alpha1.GetFileResponse],
) error {
	f, err := s.db.GetFile(ctx, req.Msg.Id)
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
			chunk := &stashyv1alpha1.GetFileResponse{
				File: &httpbody.HttpBody{
					Data: buf[:n],
				},
			}
			if first {
				chunk.File.ContentType = f.ContentType
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
