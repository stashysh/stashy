package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/stashysh/stashy/internal/storage"
)

type meta struct {
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Storage stores files on the local filesystem.
type Storage struct {
	dir string
}

func New(dir string) (*Storage, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating storage directory: %w", err)
	}
	return &Storage{dir: dir}, nil
}

func (s *Storage) dataPath(id string) string {
	return filepath.Join(s.dir, id+".data")
}

func (s *Storage) metaPath(id string) string {
	return filepath.Join(s.dir, id+".meta")
}

func (s *Storage) Put(_ context.Context, contentType string, r io.Reader) (*storage.FileMeta, error) {
	id := uuid.New().String()

	f, err := os.Create(s.dataPath(id))
	if err != nil {
		return nil, fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		os.Remove(s.dataPath(id))
		return nil, fmt.Errorf("writing file: %w", err)
	}

	m := meta{ContentType: contentType, Size: n}
	mf, err := os.Create(s.metaPath(id))
	if err != nil {
		os.Remove(s.dataPath(id))
		return nil, fmt.Errorf("creating meta file: %w", err)
	}
	defer mf.Close()

	if err := json.NewEncoder(mf).Encode(&m); err != nil {
		os.Remove(s.dataPath(id))
		os.Remove(s.metaPath(id))
		return nil, fmt.Errorf("writing meta: %w", err)
	}

	return &storage.FileMeta{
		ID:          id,
		ContentType: contentType,
		Size:        n,
	}, nil
}

func (s *Storage) Get(_ context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	mf, err := os.Open(s.metaPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, nil, fmt.Errorf("reading meta: %w", err)
	}
	defer mf.Close()

	var m meta
	if err := json.NewDecoder(mf).Decode(&m); err != nil {
		return nil, nil, fmt.Errorf("decoding meta: %w", err)
	}

	f, err := os.Open(s.dataPath(id))
	if err != nil {
		return nil, nil, fmt.Errorf("opening file: %w", err)
	}

	return f, &storage.FileMeta{
		ID:          id,
		ContentType: m.ContentType,
		Size:        m.Size,
	}, nil
}
