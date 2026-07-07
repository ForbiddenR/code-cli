package filesapi

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildDownloadPath(t *testing.T) {
	got, ok := BuildDownloadPath("/workspace", "session-1", "dir/file.txt")
	if !ok {
		t.Fatalf("BuildDownloadPath() ok = false")
	}
	want := filepath.Join("/workspace", "session-1", "uploads", "dir", "file.txt")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestBuildDownloadPathRejectsTraversal(t *testing.T) {
	if got, ok := BuildDownloadPath("/workspace", "session-1", "../secret.txt"); ok || got != "" {
		t.Fatalf("BuildDownloadPath() = %q, %v", got, ok)
	}
	if got, ok := BuildDownloadPath("/workspace", "session-1", "dir/../../secret.txt"); ok || got != "" {
		t.Fatalf("BuildDownloadPath() = %q, %v", got, ok)
	}
}

func TestBuildDownloadPathStripsRedundantPrefixes(t *testing.T) {
	got, ok := BuildDownloadPath("/workspace", "session-1", "/uploads/file.txt")
	if !ok {
		t.Fatalf("BuildDownloadPath() ok = false")
	}
	want := filepath.Join("/workspace", "session-1", "uploads", "file.txt")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	got, ok = BuildDownloadPath("/workspace", "session-1", filepath.Join("/workspace", "session-1", "uploads", "nested", "file.txt"))
	if !ok {
		t.Fatalf("BuildDownloadPath() ok = false")
	}
	want = filepath.Join("/workspace", "session-1", "uploads", "nested", "file.txt")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestParseFileSpecs(t *testing.T) {
	got := ParseFileSpecs([]string{
		"file_1:one.txt file_2:two.txt",
		"bad-spec",
		":missing-id",
		"file_3:",
		"file_4:nested/three.txt",
	})
	want := []File{
		{FileID: "file_1", RelativePath: "one.txt"},
		{FileID: "file_2", RelativePath: "two.txt"},
		{FileID: "file_4", RelativePath: "nested/three.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFileSpecs() = %#v, want %#v", got, want)
	}
}
