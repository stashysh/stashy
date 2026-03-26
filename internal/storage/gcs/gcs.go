package gcs

import (
	"context"
	"errors"
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
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
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
		Public:      attrs.Metadata["public"] == "true",
	}, nil
}

func (s *Storage) Update(ctx context.Context, id, owner, contentType string, r io.Reader) (*storage.FileMeta, error) {
	obj := s.bucket.Object(id)

	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("getting object attrs: %w", err)
	}

	if attrs.Metadata["owner"] != owner {
		return nil, fmt.Errorf("permission denied")
	}

	w := obj.NewWriter(ctx)
	w.ContentType = contentType
	w.Metadata = attrs.Metadata

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
		Public:      attrs.Metadata["public"] == "true",
	}, nil
}

func (s *Storage) Delete(ctx context.Context, id, owner string) error {
	obj := s.bucket.Object(id)

	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
			return fmt.Errorf("file not found: %s", id)
		}
		return fmt.Errorf("getting object attrs: %w", err)
	}

	if attrs.Metadata["owner"] != owner {
		return fmt.Errorf("permission denied")
	}

	if err := obj.Delete(ctx); err != nil {
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}

func (s *Storage) SetPublic(ctx context.Context, id string, public bool) error {
	obj := s.bucket.Object(id)

	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcstorage.ErrObjectNotExist) {
			return fmt.Errorf("file not found: %s", id)
		}
		return fmt.Errorf("getting object attrs: %w", err)
	}

	meta := attrs.Metadata
	if meta == nil {
		meta = make(map[string]string)
	}

	if public {
		meta["public"] = "true"
	} else {
		delete(meta, "public")
	}

	_, err = obj.Update(ctx, gcstorage.ObjectAttrsToUpdate{Metadata: meta})
	if err != nil {
		return fmt.Errorf("updating object metadata: %w", err)
	}
	return nil
}
