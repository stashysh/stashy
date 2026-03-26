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
	owner       string
	contentType string
	public      bool
}

// Storage is an in-memory storage backend. Useful for development and testing.
type Storage struct {
	mu    sync.RWMutex
	files map[string]*file
}

func New() *Storage {
	return &Storage{files: make(map[string]*file)}
}

func (s *Storage) Put(_ context.Context, owner, contentType string, r io.Reader) (*storage.FileMeta, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading file data: %w", err)
	}

	id, err := storage.NewID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	s.mu.Lock()
	s.files[id] = &file{data: data, owner: owner, contentType: contentType}
	s.mu.Unlock()

	return &storage.FileMeta{
		ID:          id,
		Owner:       owner,
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
		Owner:       f.owner,
		ContentType: f.contentType,
		Size:        int64(len(f.data)),
		Public:      f.public,
	}, nil
}

func (s *Storage) Update(_ context.Context, id, owner, contentType string, r io.Reader) (*storage.FileMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[id]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", id)
	}
	if f.owner != owner {
		return nil, fmt.Errorf("permission denied")
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading file data: %w", err)
	}

	f.data = data
	f.contentType = contentType

	return &storage.FileMeta{
		ID:          id,
		Owner:       owner,
		ContentType: contentType,
		Size:        int64(len(data)),
		Public:      f.public,
	}, nil
}

func (s *Storage) SetPublic(_ context.Context, id string, public bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[id]
	if !ok {
		return fmt.Errorf("file not found: %s", id)
	}
	f.public = public
	return nil
}
