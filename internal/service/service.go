package service

import (
	"context"
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
	store storage.Storage
}

var _ stashyv1alpha1connect.StorageServiceHandler = (*StorageService)(nil)

func New(store storage.Storage) *StorageService {
	return &StorageService{store: store}
}

func (s *StorageService) CreateFile(
	ctx context.Context,
	stream *connect.ClientStream[stashyv1alpha1.CreateFileRequest],
) (*connect.Response[stashyv1alpha1.CreateFileResponse], error) {
	owner, _ := auth.UserIDFromContext(ctx)

	// Read first chunk to determine content type before starting storage write.
	var contentType string
	var firstData []byte
	for stream.Receive() {
		msg := stream.Msg()
		if msg.File == nil {
			continue
		}
		contentType = msg.File.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
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
		Id: putResult.meta.ID,
	}), nil
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
