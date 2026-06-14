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
	slug        string
}

// Storage is an in-memory storage backend. Useful for development and testing.
type Storage struct {
	mu    sync.RWMutex
	files map[string]*file
}

func New() *Storage {
	return &Storage{files: make(map[string]*file)}
}

func fileMeta(id string, f *file) *storage.FileMeta {
	return &storage.FileMeta{
		ID:          id,
		Owner:       f.owner,
		ContentType: f.contentType,
		Size:        int64(len(f.data)),
		Public:      f.public,
		Slug:        f.slug,
	}
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

func (s *Storage) Stat(_ context.Context, id string) (*storage.FileMeta, error) {
	s.mu.RLock()
	f, ok := s.files[id]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file not found: %s", id)
	}

	return fileMeta(id, f), nil
}

func (s *Storage) Get(_ context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	s.mu.RLock()
	f, ok := s.files[id]
	s.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("file not found: %s", id)
	}

	return io.NopCloser(bytes.NewReader(f.data)), fileMeta(id, f), nil
}

func (s *Storage) GetRange(_ context.Context, id string, start, length int64) (io.ReadCloser, error) {
	s.mu.RLock()
	f, ok := s.files[id]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file not found: %s", id)
	}

	end := start + length
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}

	return io.NopCloser(bytes.NewReader(f.data[start:end])), nil
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

	return fileMeta(id, f), nil
}

func (s *Storage) Delete(_ context.Context, id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[id]
	if !ok {
		return fmt.Errorf("file not found: %s", id)
	}
	if f.owner != owner {
		return fmt.Errorf("permission denied")
	}
	delete(s.files, id)
	return nil
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

func (s *Storage) SetSlug(_ context.Context, id, owner, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[id]
	if !ok {
		return fmt.Errorf("file not found: %s", id)
	}
	if f.owner != owner {
		return fmt.Errorf("permission denied")
	}
	f.slug = slug
	return nil
}
