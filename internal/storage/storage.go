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

// Storage is the abstraction over file byte-storage backends (memory, local
// disk, S3, GCS). File metadata — owner, content type, size, visibility,
// slug — lives in the database (db.File); backends only store raw bytes
// keyed by id.
type Storage interface {
	// Put stores data under id, overwriting any existing content, and
	// returns the number of bytes written.
	Put(ctx context.Context, id string, r io.Reader) (int64, error)

	// Get retrieves a file's bytes by ID. The caller must close the returned ReadCloser.
	Get(ctx context.Context, id string) (io.ReadCloser, error)

	// GetRange retrieves a byte range by ID. The caller must close the returned ReadCloser.
	GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error)

	// Delete removes a file's bytes. Deleting a missing file is not an error.
	Delete(ctx context.Context, id string) error
}
