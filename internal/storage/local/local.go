package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Storage stores file bytes on the local filesystem, one file per id.
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

func (s *Storage) Put(_ context.Context, id, _ string, r io.Reader) (int64, error) {
	f, err := os.Create(s.dataPath(id))
	if err != nil {
		return 0, fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		os.Remove(s.dataPath(id))
		return 0, fmt.Errorf("writing file: %w", err)
	}
	return n, nil
}

func (s *Storage) Get(_ context.Context, id string) (io.ReadCloser, error) {
	f, err := os.Open(s.dataPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("opening file: %w", err)
	}
	return f, nil
}

func (s *Storage) GetRange(_ context.Context, id string, start, _ int64) (io.ReadCloser, error) {
	f, err := os.Open(s.dataPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("opening file: %w", err)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("seeking file: %w", err)
	}
	return f, nil
}

func (s *Storage) Delete(_ context.Context, id string) error {
	if err := os.Remove(s.dataPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting file: %w", err)
	}
	return nil
}
