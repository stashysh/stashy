package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"

	gcstorage "cloud.google.com/go/storage"
)

// Storage stores file bytes in Google Cloud Storage.
type Storage struct {
	bucket *gcstorage.BucketHandle
}

func New(client *gcstorage.Client, bucketName string) *Storage {
	return &Storage{bucket: client.Bucket(bucketName)}
}

func (s *Storage) Put(ctx context.Context, id string, r io.Reader) (int64, error) {
	w := s.bucket.Object(id).NewWriter(ctx)

	n, err := io.Copy(w, r)
	if err != nil {
		w.Close()
		return 0, fmt.Errorf("writing to GCS: %w", err)
	}

	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("closing GCS writer: %w", err)
	}
	return n, nil
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
