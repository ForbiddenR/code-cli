//go:build !windows

package globalconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLoadRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := New(path)
	readFile := store.fs.readFile
	store.fs.readFile = func(path string, expected os.FileInfo) ([]byte, error) {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		if err := unix.Mkfifo(path, 0o600); err != nil {
			return nil, err
		}
		return readFile(path, expected)
	}
	_, err := store.Load()
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
	}
}
