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
	Owner       string
	ContentType string
	Size        int64
	Public      bool
}

// Storage is the abstraction over file storage backends (memory, S3, GCS, etc.).
type Storage interface {
	// Put stores data and returns metadata about the created file.
	Put(ctx context.Context, owner, contentType string, r io.Reader) (*FileMeta, error)

	// Get retrieves a file by ID. The caller must close the returned ReadCloser.
	Get(ctx context.Context, id string) (io.ReadCloser, *FileMeta, error)

	// Update replaces the content of an existing file. Returns error if not found or not owned.
	Update(ctx context.Context, id, owner, contentType string, r io.Reader) (*FileMeta, error)

	// SetPublic sets the public visibility of a file.
	SetPublic(ctx context.Context, id string, public bool) error
}
