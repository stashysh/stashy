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
	Slug        string
}

// Storage is the abstraction over file storage backends (memory, S3, GCS, etc.).
type Storage interface {
	// Put stores data and returns metadata about the created file.
	Put(ctx context.Context, owner, contentType string, r io.Reader) (*FileMeta, error)

	// Stat retrieves file metadata by ID without opening the file body.
	Stat(ctx context.Context, id string) (*FileMeta, error)

	// Get retrieves a file by ID. The caller must close the returned ReadCloser.
	Get(ctx context.Context, id string) (io.ReadCloser, *FileMeta, error)

	// GetRange retrieves a byte range by ID. The caller must close the returned ReadCloser.
	GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error)

	// Update replaces the content of an existing file. Returns error if not found or not owned.
	Update(ctx context.Context, id, owner, contentType string, r io.Reader) (*FileMeta, error)

	// Delete removes a file. Returns error if not found or not owned.
	Delete(ctx context.Context, id, owner string) error

	// SetPublic sets the public visibility of a file.
	SetPublic(ctx context.Context, id string, public bool) error

	// SetSlug sets the file's slug, or clears it when slug is empty.
	// Returns error if not found or not owned.
	SetSlug(ctx context.Context, id, owner, slug string) error
}
