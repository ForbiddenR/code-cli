package filesapi

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadFile(t *testing.T) {
	filePath := writeTempFile(t, "hello upload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/files" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("anthropic-version") != AnthropicVersion {
			t.Fatalf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("anthropic-beta") != FilesAPIBetaHeader {
			t.Fatalf("anthropic-beta = %q", r.Header.Get("anthropic-beta"))
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		if r.ContentLength <= 0 {
			t.Fatalf("content-length = %d", r.ContentLength)
		}

		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader() error = %v", err)
		}
		parts := readMultipartParts(t, reader)
		if parts["file"] != "hello upload" {
			t.Fatalf("file part = %q", parts["file"])
		}
		if parts["purpose"] != "user_data" {
			t.Fatalf("purpose = %q", parts["purpose"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"file_uploaded"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got := client.UploadFile(context.Background(), filePath, "nested/upload.txt")
	if !got.Success || got.FileID != "file_uploaded" || got.Path != "nested/upload.txt" || got.Size != int64(len("hello upload")) || got.Error != "" {
		t.Fatalf("UploadFile() = %#v", got)
	}
}

func TestUploadFileReadFailure(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	got := client.UploadFile(context.Background(), filepath.Join(t.TempDir(), "missing.txt"), "missing.txt")
	if got.Success || got.Error == "" || got.Path != "missing.txt" {
		t.Fatalf("UploadFile() = %#v", got)
	}
}

func TestUploadFileNonRetryableStatus(t *testing.T) {
	calls := 0
	filePath := writeTempFile(t, "denied")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got := client.UploadFile(context.Background(), filePath, "denied.txt")
	if got.Success || !strings.Contains(got.Error, "permission") || calls != 1 {
		t.Fatalf("UploadFile() = %#v, calls = %d", got, calls)
	}
}

func TestUploadFileRetriesServerError(t *testing.T) {
	calls := 0
	filePath := writeTempFile(t, "retry me")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":"file_retry"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got := client.UploadFile(context.Background(), filePath, "retry.txt")
	if !got.Success || got.FileID != "file_retry" || calls != 2 {
		t.Fatalf("UploadFile() = %#v, calls = %d", got, calls)
	}
}

func TestUploadFileMissingID(t *testing.T) {
	filePath := writeTempFile(t, "no id")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got := client.UploadFile(context.Background(), filePath, "no-id.txt")
	if got.Success || !strings.Contains(got.Error, "no file id") {
		t.Fatalf("UploadFile() = %#v", got)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func readMultipartParts(t *testing.T, reader *multipart.Reader) map[string]string {
	t.Helper()
	parts := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart() error = %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		parts[part.FormName()] = string(data)
	}
	return parts
}
