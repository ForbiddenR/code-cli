package brief

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExpandPath(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "work", "project")
	home := filepath.Join(string(filepath.Separator), "home", "user")
	homeFn := func() (string, error) { return home, nil }
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "  ", want: cwd},
		{name: "home", raw: "~", want: home},
		{name: "inside home", raw: " ~/file.txt ", want: filepath.Join(home, "file.txt")},
		{name: "relative", raw: " ./logs/../result.txt ", want: filepath.Join(cwd, "result.txt")},
		{name: "absolute", raw: filepath.Join(string(filepath.Separator), "tmp", "a", "..", "b"), want: filepath.Join(string(filepath.Separator), "tmp", "b")},
		{name: "other user is relative", raw: "~other/file", want: filepath.Join(cwd, "~other", "file")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := expandPath(test.raw, cwd, homeFn)
			if err != nil {
				t.Fatalf("expandPath() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("expandPath() = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := expandPath("bad\x00path", cwd, homeFn); err == nil || err.Error() != "Path contains null bytes" {
		t.Fatalf("NUL error = %v", err)
	}
	wantErr := errors.New("no home")
	if _, err := expandPath("~/file", cwd, func() (string, error) { return "", wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("home error = %v", err)
	}
}

func TestResolveAttachmentsMetadataAndDuplicates(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "photo.JPEG")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAttachments(context.Background(), []string{"photo.JPEG", file, "photo.JPEG"}, attachmentConfig{
		getwd:       func() (string, error) { return directory, nil },
		userHomeDir: os.UserHomeDir,
		stat:        os.Stat,
	})
	if err != nil {
		t.Fatalf("resolveAttachments() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("attachments = %#v", got)
	}
	for _, attachment := range got {
		if attachment.Path != file || attachment.Size != 4 || !attachment.IsImage {
			t.Fatalf("attachment = %#v", attachment)
		}
	}
}

func TestResolveAttachmentsErrors(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "workspace")
	fileInfo := fakeFileInfo{mode: fs.ModeDir}
	for _, test := range []struct {
		name string
		err  error
		info fs.FileInfo
		want string
	}{
		{name: "missing", err: &os.PathError{Op: "stat", Path: "missing", Err: fs.ErrNotExist}, want: `Attachment "missing" does not exist. Current working directory: ` + cwd + `.`},
		{name: "permission", err: &os.PathError{Op: "stat", Path: "denied", Err: syscall.EACCES}, want: `Attachment "denied" is not accessible (permission denied).`},
		{name: "eperm", err: syscall.EPERM, want: `Attachment "eperm" is not accessible (permission denied).`},
		{name: "directory", info: fileInfo, want: `Attachment "directory" is not a regular file.`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := test.name
			if test.name == "permission" {
				raw = "denied"
			}
			_, err := resolveAttachments(context.Background(), []string{raw}, attachmentConfig{
				getwd:       func() (string, error) { return cwd, nil },
				userHomeDir: os.UserHomeDir,
				stat:        func(string) (fs.FileInfo, error) { return test.info, test.err },
			})
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	wantErr := errors.New("storage unavailable")
	_, err := resolveAttachments(context.Background(), []string{"file"}, attachmentConfig{
		getwd:       func() (string, error) { return cwd, nil },
		userHomeDir: os.UserHomeDir,
		stat:        func(string) (fs.FileInfo, error) { return nil, wantErr },
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "stat attachment") {
		t.Fatalf("unexpected stat error = %v", err)
	}
}

func TestResolveAttachmentsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.txt")
	link := filepath.Join(directory, "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err := resolveAttachments(context.Background(), []string{link}, attachmentConfig{
		getwd:       func() (string, error) { return directory, nil },
		userHomeDir: os.UserHomeDir,
		stat:        os.Stat,
	})
	if err != nil || len(got) != 1 || got[0].Size != 6 {
		t.Fatalf("symlink attachment = %#v, error = %v", got, err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	_, err = resolveAttachments(context.Background(), []string{link}, attachmentConfig{
		getwd:       func() (string, error) { return directory, nil },
		userHomeDir: os.UserHomeDir,
		stat:        os.Stat,
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("broken symlink error = %v", err)
	}
}

func TestImageExtensions(t *testing.T) {
	for _, path := range []string{"a.png", "a.JPG", "a.jpeg", "a.GIF", "a.webp"} {
		if !isImagePath(path) {
			t.Errorf("isImagePath(%q) = false", path)
		}
	}
	for _, path := range []string{"a.bmp", "a.svg", "a.png.txt", "png"} {
		if isImagePath(path) {
			t.Errorf("isImagePath(%q) = true", path)
		}
	}
}

type fakeFileInfo struct {
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string              { return "fake" }
func (f fakeFileInfo) Size() int64               { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode         { return f.mode }
func (f fakeFileInfo) ModTime() (zero time.Time) { return zero }
func (f fakeFileInfo) IsDir() bool               { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any                  { return nil }
