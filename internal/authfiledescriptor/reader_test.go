package authfiledescriptor

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestCredentialReadsWellKnownFileWhenNoFileDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" file_token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := DefaultReader()
	reader.Getenv = func(string) string { return "" }

	got := reader.Credential(CredentialSource{EnvVar: EnvOAuthTokenFileDescriptor, WellKnownPath: path, Label: "OAuth token"})
	if got != "file_token" {
		t.Fatalf("Credential() = %q", got)
	}
}

func TestCredentialReadsFileDescriptorAndCaches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fd_token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer file.Close()

	reader := DefaultReader()
	reader.Getenv = func(key string) string {
		if key == EnvOAuthTokenFileDescriptor {
			return strconv.Itoa(int(file.Fd()))
		}
		return ""
	}
	source := CredentialSource{EnvVar: EnvOAuthTokenFileDescriptor, WellKnownPath: filepath.Join(t.TempDir(), "fallback"), Label: "OAuth token"}
	if got := reader.Credential(source); got != "fd_token" {
		t.Fatalf("Credential() = %q", got)
	}
	file.Close()
	if got := reader.Credential(source); got != "fd_token" {
		t.Fatalf("cached Credential() = %q", got)
	}
}

func TestCredentialPersistsFileDescriptorTokenForRemote(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "source-token")
	persistPath := filepath.Join(tempDir, "remote", ".oauth_token")
	if err := os.WriteFile(sourcePath, []byte("fd_token"), 0o600); err != nil {
		t.Fatalf("write source token: %v", err)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer file.Close()

	reader := DefaultReader()
	reader.Getenv = func(key string) string {
		switch key {
		case EnvOAuthTokenFileDescriptor:
			return strconv.Itoa(int(file.Fd()))
		case EnvRemote:
			return "true"
		default:
			return ""
		}
	}
	got := reader.Credential(CredentialSource{EnvVar: EnvOAuthTokenFileDescriptor, WellKnownPath: persistPath, Label: "OAuth token"})
	if got != "fd_token" {
		t.Fatalf("Credential() = %q", got)
	}
	content, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("read persisted token: %v", err)
	}
	if string(content) != "fd_token" {
		t.Fatalf("persisted token = %q", string(content))
	}
}

func TestCredentialDoesNotPersistOutsideRemote(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "source-token")
	persistPath := filepath.Join(tempDir, "remote", ".api_key")
	if err := os.WriteFile(sourcePath, []byte("fd_token"), 0o600); err != nil {
		t.Fatalf("write source token: %v", err)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer file.Close()

	reader := DefaultReader()
	reader.Getenv = func(key string) string {
		if key == EnvAPIKeyFileDescriptor {
			return strconv.Itoa(int(file.Fd()))
		}
		return ""
	}
	if got := reader.Credential(CredentialSource{EnvVar: EnvAPIKeyFileDescriptor, WellKnownPath: persistPath, Label: "API key"}); got != "fd_token" {
		t.Fatalf("Credential() = %q", got)
	}
	if _, err := os.Stat(persistPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("persisted token stat error = %v", err)
	}
}

func TestCredentialFallsBackToFileWhenFileDescriptorReadFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fallback_token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := DefaultReader()
	reader.Getenv = func(key string) string {
		if key == EnvOAuthTokenFileDescriptor {
			return "999999"
		}
		return ""
	}

	got := reader.Credential(CredentialSource{EnvVar: EnvOAuthTokenFileDescriptor, WellKnownPath: path, Label: "OAuth token"})
	if got != "fallback_token" {
		t.Fatalf("Credential() = %q", got)
	}
}

func TestCredentialRejectsInvalidFileDescriptorWithoutFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fallback_token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := DefaultReader()
	reader.Getenv = func(key string) string {
		if key == EnvOAuthTokenFileDescriptor {
			return "not-a-fd"
		}
		return ""
	}

	if got := reader.Credential(CredentialSource{EnvVar: EnvOAuthTokenFileDescriptor, WellKnownPath: path, Label: "OAuth token"}); got != "" {
		t.Fatalf("Credential() = %q", got)
	}
}

func TestCredentialUsesPlatformFileDescriptorPath(t *testing.T) {
	reader := &Reader{GOOS: "darwin"}
	if got := reader.fdPath(7); got != "/dev/fd/7" {
		t.Fatalf("darwin fdPath = %q", got)
	}
	reader.GOOS = "linux"
	if got := reader.fdPath(7); got != "/proc/self/fd/7" {
		t.Fatalf("linux fdPath = %q", got)
	}
}
