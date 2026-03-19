package fileParts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetPartSizeExactMultiple(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "file.bin")
	if err := os.WriteFile(path, make([]byte, 8), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	parted := NewPartedFile(file, 4)
	if got, want := parted.GetTotalParts(), uint32(2); got != want {
		t.Fatalf("unexpected total parts: got %d want %d", got, want)
	}
	if got, want := parted.GetPartSize(1), uint32(4); got != want {
		t.Fatalf("unexpected last part size: got %d want %d", got, want)
	}
}
