package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// Storage is an in-memory byte store. Useful for development and testing.
type Storage struct {
	mu    sync.RWMutex
	files map[string][]byte
}

func New() *Storage {
	return &Storage{files: make(map[string][]byte)}
}

func (s *Storage) Put(_ context.Context, id string, r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("reading file data: %w", err)
	}

	s.mu.Lock()
	s.files[id] = data
	s.mu.Unlock()

	return int64(len(data)), nil
}

func (s *Storage) Get(_ context.Context, id string) (io.ReadCloser, error) {
	s.mu.RLock()
	data, ok := s.files[id]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file not found: %s", id)
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *Storage) GetRange(_ context.Context, id string, start, length int64) (io.ReadCloser, error) {
	s.mu.RLock()
	data, ok := s.files[id]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file not found: %s", id)
	}

	end := start + length
	if end > int64(len(data)) {
		end = int64(len(data))
	}

	return io.NopCloser(bytes.NewReader(data[start:end])), nil
}

func (s *Storage) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.files, id)
	s.mu.Unlock()
	return nil
}
