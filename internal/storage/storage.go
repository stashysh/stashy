package storage

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash"
	"hash/crc32"
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
	// reports what was written. contentType is stamped on the object where
	// the backend supports it (S3/GCS) as a write-only courtesy for direct
	// bucket access; serving always uses the database value.
	Put(ctx context.Context, id, contentType string, r io.Reader) (Stored, error)

	// Get retrieves a file's bytes by ID. The caller must close the returned ReadCloser.
	Get(ctx context.Context, id string) (io.ReadCloser, error)

	// GetRange retrieves a byte range by ID. The caller must close the returned ReadCloser.
	GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error)

	// Delete removes a file's bytes. Deleting a missing file is not an error.
	Delete(ctx context.Context, id string) error
}

// Stored describes the bytes a Put wrote.
type Stored struct {
	Size int64
	// Checksum is the CRC32C of the bytes, encoded by EncodeCRC32C. Backends
	// report it natively where they can (GCS, S3) and compute it otherwise.
	Checksum string
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// NewCRC32C returns a hash computing the CRC32C (Castagnoli) checksum.
func NewCRC32C() hash.Hash32 {
	return crc32.New(castagnoli)
}

// EncodeCRC32C encodes a CRC32C checksum the way GCS and S3 report it: the
// 4-byte big-endian value, base64-encoded.
func EncodeCRC32C(sum uint32) string {
	return base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, sum))
}
