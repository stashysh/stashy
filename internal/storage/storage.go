package storage

import (
	"context"
	"io"

	gonanoid "github.com/matoous/go-nanoid/v2"
)

// NewID generates a unique file ID. Replace this to use a different ID scheme.
var NewID = func() (string, error) {
	return gonanoid.New()
}

// FileMeta holds metadata about a stored file.
type FileMeta struct {
	ID          string
	ContentType string
	Size        int64
}

// Storage is the abstraction over file storage backends (memory, S3, GCS, etc.).
type Storage interface {
	// Put stores data and returns metadata about the created file.
	Put(ctx context.Context, contentType string, r io.Reader) (*FileMeta, error)

	// Get retrieves a file by ID. The caller must close the returned ReadCloser.
	Get(ctx context.Context, id string) (io.ReadCloser, *FileMeta, error)
}
