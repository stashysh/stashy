package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/stashysh/stashy/internal/storage"
)

type file struct {
	data        []byte
	contentType string
}

// Storage is an in-memory storage backend. Useful for development and testing.
type Storage struct {
	mu    sync.RWMutex
	files map[string]*file
}

func New() *Storage {
	return &Storage{files: make(map[string]*file)}
}

func (s *Storage) Put(_ context.Context, contentType string, r io.Reader) (*storage.FileMeta, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading file data: %w", err)
	}

	id, err := storage.NewID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	s.mu.Lock()
	s.files[id] = &file{data: data, contentType: contentType}
	s.mu.Unlock()

	return &storage.FileMeta{
		ID:          id,
		ContentType: contentType,
		Size:        int64(len(data)),
	}, nil
}

func (s *Storage) Get(_ context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	s.mu.RLock()
	f, ok := s.files[id]
	s.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("file not found: %s", id)
	}

	return io.NopCloser(bytes.NewReader(f.data)), &storage.FileMeta{
		ID:          id,
		ContentType: f.contentType,
		Size:        int64(len(f.data)),
	}, nil
}
