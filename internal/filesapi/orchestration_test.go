package filesapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDownloadAndSaveFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/files/file_save/content" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte("saved content"))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	basePath := t.TempDir()
	got := client.DownloadAndSaveFile(context.Background(), File{FileID: "file_save", RelativePath: "nested/file.txt"}, basePath, "session-1")
	wantPath := filepath.Join(basePath, "session-1", "uploads", "nested", "file.txt")
	if !got.Success || got.FileID != "file_save" || got.Path != wantPath || got.BytesWritten != int64(len("saved content")) || got.Error != "" {
		t.Fatalf("DownloadAndSaveFile() = %#v", got)
	}
	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "saved content" {
		t.Fatalf("content = %q", string(content))
	}
}

func TestDownloadAndSaveFileRejectsInvalidPath(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got := client.DownloadAndSaveFile(context.Background(), File{FileID: "file_bad", RelativePath: "../secret.txt"}, t.TempDir(), "session-1")
	if got.Success || got.FileID != "file_bad" || got.Path != "" || !strings.Contains(got.Error, "invalid file path") || calls != 0 {
		t.Fatalf("DownloadAndSaveFile() = %#v, calls = %d", got, calls)
	}
}

func TestDownloadSessionFilesPreservesOrderWithLimit(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	basePath := t.TempDir()
	files := []File{
		{FileID: "file_one", RelativePath: "one.txt"},
		{FileID: "file_two", RelativePath: "two.txt"},
	}
	got := client.DownloadSessionFiles(context.Background(), files, basePath, "session-1", 1)
	if len(got) != 2 || !got[0].Success || !got[1].Success || got[0].FileID != "file_one" || got[1].FileID != "file_two" {
		t.Fatalf("DownloadSessionFiles() = %#v", got)
	}
	wantPaths := []string{"/v1/files/file_one/content", "/v1/files/file_two/content"}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestUploadSessionFiles(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/files" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader() error = %v", err)
		}
		parts := readMultipartParts(t, reader)
		if parts["purpose"] != "user_data" || parts["file"] == "" {
			t.Fatalf("parts = %#v", parts)
		}

		mu.Lock()
		calls++
		id := calls
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"file_uploaded_` + string(rune('0'+id)) + `"}`))
	}))
	defer server.Close()

	first := writeTempFile(t, "one")
	secondDir := t.TempDir()
	second := filepath.Join(secondDir, "second.txt")
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	client := newTestClient(t, server)
	got := client.UploadSessionFiles(context.Background(), []LocalFile{
		{Path: first, RelativePath: "one.txt"},
		{Path: second, RelativePath: "two.txt"},
	}, 1)
	if len(got) != 2 || !got[0].Success || !got[1].Success || got[0].FileID != "file_uploaded_1" || got[1].FileID != "file_uploaded_2" {
		t.Fatalf("UploadSessionFiles() = %#v", got)
	}
}
