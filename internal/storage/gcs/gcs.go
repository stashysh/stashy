package gcs

import (
	"context"
	"fmt"
	"io"

	gcstorage "cloud.google.com/go/storage"
	"github.com/stashysh/stashy/internal/storage"
)

// Storage stores files in Google Cloud Storage.
type Storage struct {
	bucket *gcstorage.BucketHandle
}

func New(client *gcstorage.Client, bucketName string) *Storage {
	return &Storage{bucket: client.Bucket(bucketName)}
}

func (s *Storage) Put(ctx context.Context, owner, contentType string, r io.Reader) (*storage.FileMeta, error) {
	id, err := storage.NewID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}
	obj := s.bucket.Object(id)

	w := obj.NewWriter(ctx)
	w.ContentType = contentType
	w.Metadata = map[string]string{"owner": owner}

	n, err := io.Copy(w, r)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("writing to GCS: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing GCS writer: %w", err)
	}

	return &storage.FileMeta{
		ID:          id,
		Owner:       owner,
		ContentType: contentType,
		Size:        n,
	}, nil
}

func (s *Storage) Get(ctx context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	obj := s.bucket.Object(id)

	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if err == gcstorage.ErrObjectNotExist {
			return nil, nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, nil, fmt.Errorf("getting object attrs: %w", err)
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("opening GCS reader: %w", err)
	}

	return r, &storage.FileMeta{
		ID:          id,
		Owner:       attrs.Metadata["owner"],
		ContentType: attrs.ContentType,
		Size:        attrs.Size,
	}, nil
}
