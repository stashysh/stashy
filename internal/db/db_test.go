package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()

	database, err := New(t.Context(), "sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { database.Close(context.Background()) })

	if err := database.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

func TestListFilesKeysetPagination(t *testing.T) {
	database := newTestDB(t)
	user, err := database.UpsertUser(t.Context(), "g-1", "a@b.c", "A")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// Five files with strictly increasing created_at; expected newest-first
	// order is 04, 03, 02, 01, 00.
	base := time.Now().UTC().Truncate(time.Second)
	var ids []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("file-%016d", i) // 21 chars
		if _, err := database.CreateFile(t.Context(), File{ID: id, Owner: user.ID, ContentType: "text/plain", Size: 1}); err != nil {
			t.Fatalf("CreateFile %s: %v", id, err)
		}
		if _, err := database.sql.ExecContext(t.Context(),
			`UPDATE files SET created_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Second), id); err != nil {
			t.Fatalf("staggering created_at: %v", err)
		}
		ids = append(ids, id)
	}

	var got []string
	afterTime, afterID := time.Time{}, ""
	for page := 0; page < 4; page++ {
		files, err := database.ListFiles(t.Context(), user.ID, 2, afterTime, afterID)
		if err != nil {
			t.Fatalf("ListFiles page %d: %v", page, err)
		}
		if len(files) == 0 {
			break
		}
		for _, f := range files {
			got = append(got, f.ID)
		}
		last := files[len(files)-1]
		afterTime, afterID = last.CreatedAt, last.ID
	}

	want := []string{ids[4], ids[3], ids[2], ids[1], ids[0]}
	if len(got) != len(want) {
		t.Fatalf("collected %d files across pages, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page order[%d] = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestListFilesKeysetTieBreakOnEqualTimes(t *testing.T) {
	database := newTestDB(t)
	user, err := database.UpsertUser(t.Context(), "g-1", "a@b.c", "A")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// All rows share one created_at; ordering must fall back to id DESC and
	// the cursor must still partition pages without overlap or loss.
	ts := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("file-%016d", i)
		if _, err := database.CreateFile(t.Context(), File{ID: id, Owner: user.ID, ContentType: "text/plain", Size: 1}); err != nil {
			t.Fatalf("CreateFile %s: %v", id, err)
		}
		if _, err := database.sql.ExecContext(t.Context(),
			`UPDATE files SET created_at = ? WHERE id = ?`, ts, id); err != nil {
			t.Fatalf("pinning created_at: %v", err)
		}
	}

	seen := map[string]bool{}
	afterTime, afterID := time.Time{}, ""
	for page := 0; page < 4; page++ {
		files, err := database.ListFiles(t.Context(), user.ID, 2, afterTime, afterID)
		if err != nil {
			t.Fatalf("ListFiles page %d: %v", page, err)
		}
		if len(files) == 0 {
			break
		}
		for _, f := range files {
			if seen[f.ID] {
				t.Fatalf("file %s returned on more than one page", f.ID)
			}
			seen[f.ID] = true
		}
		last := files[len(files)-1]
		afterTime, afterID = last.CreatedAt, last.ID
	}

	if len(seen) != 5 {
		t.Fatalf("collected %d distinct files across pages, want 5", len(seen))
	}
}

// CreateFile's timestamps must match what GetFile returns later, even where
// the database stores them at lower precision than Go's clock.
func TestCreateFileTimestampsMatchStored(t *testing.T) {
	database := newTestDB(t)
	user, err := database.UpsertUser(t.Context(), "g-1", "a@b.c", "A")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	const id = "time-0000000000000000" // 21 chars
	created, err := database.CreateFile(t.Context(), File{ID: id, Owner: user.ID, ContentType: "text/plain", Size: 1})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	got, err := database.GetFile(t.Context(), id)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) || !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("GetFile times = %v/%v, want %v/%v from CreateFile",
			got.CreatedAt, got.UpdatedAt, created.CreatedAt, created.UpdatedAt)
	}
}

func TestFileName(t *testing.T) {
	database := newTestDB(t)
	user, err := database.UpsertUser(t.Context(), "g-1", "a@b.c", "A")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	const id = "name-0000000000000000" // 21 chars
	if _, err := database.CreateFile(t.Context(), File{ID: id, Owner: user.ID, Name: "Quarterly report.pdf", ContentType: "application/pdf", Size: 1}); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if f, err := database.GetFile(t.Context(), id); err != nil || f.Name != "Quarterly report.pdf" {
		t.Fatalf("GetFile: name = %q, err = %v; want the created name", f.Name, err)
	}

	if err := database.SetFileName(t.Context(), id, user.ID, ""); err != nil {
		t.Fatalf("SetFileName: %v", err)
	}
	if f, _ := database.GetFile(t.Context(), id); f.Name != "" {
		t.Fatalf("name = %q after clearing, want empty", f.Name)
	}

	if err := database.SetFileName(t.Context(), id, "other-user", "x"); err == nil {
		t.Fatalf("SetFileName by another user: want an error")
	}
}
