package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"

	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	gstorage "google.golang.org/api/storage/v1"

	"github.com/stashysh/stashy/internal/storage"
)

// Storage stores file bytes in Google Cloud Storage. It uses the JSON API
// client rather than cloud.google.com/go/storage, which links in gRPC and
// roughly doubles the binary size.
type Storage struct {
	objects *gstorage.ObjectsService
	bucket  string
}

func New(service *gstorage.Service, bucket string) *Storage {
	return &Storage{objects: service.Objects, bucket: bucket}
}

func (s *Storage) Put(ctx context.Context, id, contentType string, r io.Reader) (storage.Stored, error) {
	// Overwriting the object with the same bytes is harmless, so the upload
	// is safe to retry. Uploads over the chunk size (16 MiB) are resumable.
	obj, err := s.objects.Insert(s.bucket, &gstorage.Object{Name: id, ContentType: contentType}).
		Media(r, googleapi.ContentType(contentType)).
		WithRetry(nil, nil).
		Context(ctx).
		Do()
	if err != nil {
		return storage.Stored{}, fmt.Errorf("writing to GCS: %w", err)
	}
	// GCS computes a CRC32C for every object and reports it encoded the way
	// storage.EncodeCRC32C does.
	return storage.Stored{Size: int64(obj.Size), Checksum: obj.Crc32c}, nil
}

func (s *Storage) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	return s.download(ctx, id, "")
}

func (s *Storage) GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error) {
	return s.download(ctx, id, fmt.Sprintf("bytes=%d-%d", start, start+length-1))
}

func (s *Storage) download(ctx context.Context, id, byteRange string) (io.ReadCloser, error) {
	var resp *http.Response
	err := retry(ctx, func() error {
		call := s.objects.Get(s.bucket, id).Context(ctx)
		if byteRange != "" {
			call.Header().Set("Range", byteRange)
		}
		var err error
		resp, err = call.Download()
		return err
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("reading from GCS: %w", err)
	}
	return resp.Body, nil
}

func (s *Storage) Delete(ctx context.Context, id string) error {
	err := retry(ctx, func() error {
		return s.objects.Delete(s.bucket, id).Context(ctx).Do()
	})
	// A retry after a delete whose response was lost also lands here.
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}

func isNotFound(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusNotFound
}

const maxAttempts = 4

// initialBackoff is the first pause between attempts; tests shorten it.
var initialBackoff = time.Second

// retry calls f until it succeeds, fails with an error that isn't
// transient, or runs out of attempts. The JSON API client only retries
// uploads itself, so reads and deletes, which are idempotent, go through
// this.
func retry(ctx context.Context, f func() error) error {
	bo := gax.Backoff{Initial: initialBackoff}
	for attempt := 1; ; attempt++ {
		err := f()
		if err == nil || attempt == maxAttempts || !isTransient(err) {
			return err
		}
		if err := gax.Sleep(ctx, bo.Pause()); err != nil {
			return err
		}
	}
}

// isTransient reports whether err is worth retrying, following the codes
// and network errors the client retries for uploads.
func isTransient(err error) bool {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}
