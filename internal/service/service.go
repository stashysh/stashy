package service

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/api/httpbody"

	stashyv1alpha1 "github.com/stashysh/stashy/gen/stashy/v1alpha1"
	"github.com/stashysh/stashy/gen/stashy/v1alpha1/stashyv1alpha1connect"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/storage"
)

const chunkSize = 64 * 1024 // 64KB

type StorageService struct {
	store    storage.Storage
	hostname string
}

var _ stashyv1alpha1connect.StorageServiceHandler = (*StorageService)(nil)

func New(store storage.Storage, hostname string) *StorageService {
	return &StorageService{store: store, hostname: strings.TrimRight(hostname, "/")}
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

// storageError maps a storage-layer error to the appropriate connect code.
func storageError(err error) error {
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
func (s *StorageService) canonicalURL(meta *storage.FileMeta) string {
	if meta.Slug != "" {
		return s.hostname + "/" + meta.ID + "/" + meta.Slug
	}
	return s.hostname + "/" + meta.ID
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
		meta *storage.FileMeta
		err  error
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		putResult.meta, putResult.err = s.store.Put(ctx, owner, contentType, pr)
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
		Id:  putResult.meta.ID,
		Url: s.hostname + "/" + putResult.meta.ID,
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
	var updateResult struct {
		meta *storage.FileMeta
		err  error
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		updateResult.meta, updateResult.err = s.store.Update(ctx, id, owner, contentType, pr)
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

	if updateResult.err != nil {
		if strings.Contains(updateResult.err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, updateResult.err)
		}
		if strings.Contains(updateResult.err.Error(), "permission denied") {
			return nil, connect.NewError(connect.CodePermissionDenied, updateResult.err)
		}
		return nil, connect.NewError(connect.CodeInternal, updateResult.err)
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
		if err := s.store.SetSlug(ctx, id, owner, *req.Msg.Slug); err != nil {
			return nil, storageError(err)
		}
	}

	meta, err := s.store.Stat(ctx, id)
	if err != nil {
		return nil, storageError(err)
	}

	return connect.NewResponse(&stashyv1alpha1.UpdateFileResponse{
		Url: s.canonicalURL(meta),
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

	if err := s.store.Delete(ctx, req.Msg.Id, owner); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if strings.Contains(err.Error(), "permission denied") {
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&stashyv1alpha1.DeleteFileResponse{}), nil
}

func (s *StorageService) PublishFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.PublishFileRequest],
) (*connect.Response[stashyv1alpha1.PublishFileResponse], error) {
	if err := s.store.SetPublic(ctx, req.Msg.Id, true); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&stashyv1alpha1.PublishFileResponse{}), nil
}

func (s *StorageService) UnpublishFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.UnpublishFileRequest],
) (*connect.Response[stashyv1alpha1.UnpublishFileResponse], error) {
	if err := s.store.SetPublic(ctx, req.Msg.Id, false); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&stashyv1alpha1.UnpublishFileResponse{}), nil
}

func (s *StorageService) GetFile(
	ctx context.Context,
	req *connect.Request[stashyv1alpha1.GetFileRequest],
	stream *connect.ServerStream[stashyv1alpha1.GetFileResponse],
) error {
	rc, meta, err := s.store.Get(ctx, req.Msg.Id)
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
				chunk.File.ContentType = meta.ContentType
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
