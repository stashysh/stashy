package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"

	gcstorage "cloud.google.com/go/storage"

	"github.com/stashysh/stashy/internal/storage"
)

// Storage stores file bytes in Google Cloud Storage.
type Storage struct {
	bucket *gcstorage.BucketHandle
}

func New(client *gcstorage.Client, bucketName string) *Storage {
	return &Storage{bucket: client.Bucket(bucketName)}
}

func (s *Storage) Put(ctx context.Context, id, contentType string, r io.Reader) (storage.Stored, error) {
	w := s.bucket.Object(id).NewWriter(ctx)
	w.ContentType = contentType

	n, err := io.Copy(w, r)
	if err != nil {
		w.Close()
		return storage.Stored{}, fmt.Errorf("writing to GCS: %w", err)
	}

	if err := w.Close(); err != nil {
		return storage.Stored{}, fmt.Errorf("closing GCS writer: %w", err)
	}
	// GCS computes a CRC32C for every object.
	return storage.Stored{Size: n, Checksum: storage.EncodeCRC32C(w.Attrs().CRC32C)}, nil
}

func (s *Storage) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	r, err := s.bucket.Object(id).NewReader(ctx)
	if err != nil {
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("opening GCS reader: %w", err)
	}
	return r, nil
}

func (s *Storage) GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error) {
	r, err := s.bucket.Object(id).NewRangeReader(ctx, start, length)
	if err != nil {
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("opening GCS range reader: %w", err)
	}
	return r, nil
}

func (s *Storage) Delete(ctx context.Context, id string) error {
	if err := s.bucket.Object(id).Delete(ctx); err != nil {
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
			return nil
		}
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}
