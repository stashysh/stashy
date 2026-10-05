package storage_test

import (
	"strings"
	"testing"

	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/local"
	"github.com/stashysh/stashy/internal/storage/memory"
)

// helloCRC32C is the CRC32C of "hello" as GCS and S3 report it.
const helloCRC32C = "mnG7TA=="

func TestEncodeCRC32C(t *testing.T) {
	h := storage.NewCRC32C()
	h.Write([]byte("hello"))
	if got := storage.EncodeCRC32C(h.Sum32()); got != helloCRC32C {
		t.Fatalf("EncodeCRC32C = %q, want %q", got, helloCRC32C)
	}
}

func TestPutReportsChecksum(t *testing.T) {
	disk, err := local.New(t.TempDir())
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	for name, store := range map[string]storage.Storage{"memory": memory.New(), "local": disk} {
		stored, err := store.Put(t.Context(), "hello000000000000000a", "text/plain", strings.NewReader("hello"))
		if err != nil {
			t.Fatalf("%s: Put: %v", name, err)
		}
		if stored.Size != 5 || stored.Checksum != helloCRC32C {
			t.Errorf("%s: Put = %+v, want size 5 and checksum %s", name, stored, helloCRC32C)
		}
	}
}
