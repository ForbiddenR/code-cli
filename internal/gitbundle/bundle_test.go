package gitbundle

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"code-cli/internal/filesapi"
)

func TestBundleMaxBytes(t *testing.T) {
	if got := BundleMaxBytes(0); got != DefaultBundleMaxBytes {
		t.Fatalf("BundleMaxBytes(0) = %d", got)
	}
	if got := BundleMaxBytes(-1); got != DefaultBundleMaxBytes {
		t.Fatalf("BundleMaxBytes(-1) = %d", got)
	}
	if got := BundleMaxBytes(42); got != 42 {
		t.Fatalf("BundleMaxBytes(42) = %d", got)
	}
}

func TestBundleCreateArgs(t *testing.T) {
	if got := BundleCreateArgs("/tmp/a.bundle", "--all", false); strings.Join(got, " ") != "bundle create /tmp/a.bundle --all" {
		t.Fatalf("args = %#v", got)
	}
	if got := BundleCreateArgs("/tmp/a.bundle", "HEAD", true); strings.Join(got, " ") != "bundle create /tmp/a.bundle HEAD "+SeedStashRef {
		t.Fatalf("args = %#v", got)
	}
	if got := BundleCreateArgs("/tmp/a.bundle", SeedRootRef, true); strings.Join(got, " ") != "bundle create /tmp/a.bundle "+SeedRootRef {
		t.Fatalf("squashed args = %#v", got)
	}
}

func TestTreeRefForSquashAndSeedRefs(t *testing.T) {
	if got := TreeRefForSquash(true); got != SeedStashRef+"^{tree}" {
		t.Fatalf("stash tree = %q", got)
	}
	if got := TreeRefForSquash(false); got != "HEAD^{tree}" {
		t.Fatalf("head tree = %q", got)
	}
	refs := SeedRefs()
	if len(refs) != 2 || refs[0] != SeedStashRef || refs[1] != SeedRootRef {
		t.Fatalf("SeedRefs = %#v", refs)
	}
}

func TestFormatGitErrorAndTooLarge(t *testing.T) {
	long := strings.Repeat("x", 250)
	got := FormatGitError("git bundle create --all", 128, long)
	if !strings.HasPrefix(got, "git bundle create --all failed (128): ") || len(got) != len("git bundle create --all failed (128): ")+200 {
		t.Fatalf("FormatGitError = %q", got)
	}
	if TooLargeError() != "Repo is too large to bundle. Please setup GitHub on https://claude.ai/code" {
		t.Fatalf("TooLargeError = %q", TooLargeError())
	}
}

func TestCreateAndUploadGitBundleNotInRepo(t *testing.T) {
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   &scriptedRunner{},
		Uploader: &stubUploader{},
		FS:       &memoryFS{tempPath: "/tmp/seed.bundle"},
		GitRoot:  fixedRootFinder{"", false},
	}, Options{})
	if result.Success || result.Error != "Not in a git repository" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleEmptyRepo(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef: {},
		"update-ref -d " + SeedRootRef:  {},
		"for-each-ref --count=1 refs/":  {Stdout: "\n"},
	}}
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: &stubUploader{},
		FS:       &memoryFS{tempPath: "/tmp/seed.bundle"},
		GitRoot:  fixedRootFinder{"/repo", true},
	}, Options{})
	if result.Success || result.FailReason != FailEmptyRepo || result.Error != "Repository has no commits yet" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleAllSuccessWithWIP(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef:                        {},
		"update-ref -d " + SeedRootRef:                         {},
		"for-each-ref --count=1 refs/":                         {Stdout: "refs/heads/main\n"},
		"stash create":                                         {Stdout: "abc123\n"},
		"update-ref " + SeedStashRef + " abc123":               {},
		"bundle create /tmp/seed.bundle --all " + SeedStashRef: {},
	}}
	uploader := &stubUploader{result: filesapi.UploadResult{Success: true, FileID: "file_1", Size: 42}}
	fs := &memoryFS{tempPath: "/tmp/seed.bundle", sizes: map[string]int64{"/tmp/seed.bundle": 42}}
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: uploader,
		FS:       fs,
		GitRoot:  fixedRootFinder{"/repo", true},
	}, Options{MaxBytes: 100})
	if !result.Success || result.FileID != "file_1" || result.BundleSizeBytes != 42 || result.Scope != ScopeAll || !result.HasWIP {
		t.Fatalf("result = %#v", result)
	}
	if uploader.path != "/tmp/seed.bundle" || uploader.relativePath != SeedRelativePath {
		t.Fatalf("upload = %q %q", uploader.path, uploader.relativePath)
	}
	if !fs.removed["/tmp/seed.bundle"] {
		t.Fatal("bundle path was not removed")
	}
	if !runner.sawPrefix("update-ref -d "+SeedStashRef) || !runner.sawPrefix("update-ref -d "+SeedRootRef) {
		t.Fatalf("cleanup refs missing: %#v", runner.calls)
	}
}

func TestCreateAndUploadGitBundleFallsBackToHeadThenSquashed(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef:                 {},
		"update-ref -d " + SeedRootRef:                  {},
		"for-each-ref --count=1 refs/":                  {Stdout: "refs/heads/main\n"},
		"stash create":                                  {Stdout: ""},
		"bundle create /tmp/seed.bundle --all":          {},
		"bundle create /tmp/seed.bundle HEAD":           {},
		"commit-tree HEAD^{tree} -m seed":               {Stdout: "squashsha\n"},
		"update-ref " + SeedRootRef + " squashsha":      {},
		"bundle create /tmp/seed.bundle " + SeedRootRef: {},
	}}
	// First two tiers too large; squashed fits.
	sizes := map[string]int64{}
	fs := &memoryFS{tempPath: "/tmp/seed.bundle", sizes: sizes, sizeFn: func(path string, call int) int64 {
		switch call {
		case 1, 2:
			return 200
		default:
			return 50
		}
	}}
	uploader := &stubUploader{result: filesapi.UploadResult{Success: true, FileID: "file_sq", Size: 50}}
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: uploader,
		FS:       fs,
		GitRoot:  fixedRootFinder{"/repo", true},
	}, Options{MaxBytes: 100})
	if !result.Success || result.Scope != ScopeSquashed || result.HasWIP || result.FileID != "file_sq" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleTooLarge(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef:                 {},
		"update-ref -d " + SeedRootRef:                  {},
		"for-each-ref --count=1 refs/":                  {Stdout: "refs/heads/main\n"},
		"stash create":                                  {},
		"bundle create /tmp/seed.bundle --all":          {},
		"bundle create /tmp/seed.bundle HEAD":           {},
		"commit-tree HEAD^{tree} -m seed":               {Stdout: "squashsha\n"},
		"update-ref " + SeedRootRef + " squashsha":      {},
		"bundle create /tmp/seed.bundle " + SeedRootRef: {},
	}}
	fs := &memoryFS{tempPath: "/tmp/seed.bundle", sizes: map[string]int64{"/tmp/seed.bundle": 999}}
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: &stubUploader{},
		FS:       fs,
		GitRoot:  fixedRootFinder{"/repo", true},
	}, Options{MaxBytes: 100})
	if result.Success || result.FailReason != FailTooLarge || result.Error != TooLargeError() {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleGitErrorOnAll(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef:        {},
		"update-ref -d " + SeedRootRef:         {},
		"for-each-ref --count=1 refs/":         {Stdout: "refs/heads/main\n"},
		"stash create":                         {},
		"bundle create /tmp/seed.bundle --all": {Code: 128, Stderr: "boom"},
	}}
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: &stubUploader{},
		FS:       &memoryFS{tempPath: "/tmp/seed.bundle"},
		GitRoot:  fixedRootFinder{"/repo", true},
	}, Options{})
	if result.Success || result.FailReason != FailGitError || !strings.Contains(result.Error, "git bundle create --all failed (128)") {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleUploadFailure(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef:        {},
		"update-ref -d " + SeedRootRef:         {},
		"for-each-ref --count=1 refs/":         {Stdout: "refs/heads/main\n"},
		"stash create":                         {},
		"bundle create /tmp/seed.bundle --all": {},
	}}
	result := CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: &stubUploader{result: filesapi.UploadResult{Success: false, Error: "upload denied"}},
		FS:       &memoryFS{tempPath: "/tmp/seed.bundle", sizes: map[string]int64{"/tmp/seed.bundle": 10}},
		GitRoot:  fixedRootFinder{"/repo", true},
	}, Options{})
	if result.Success || result.Error != "upload denied" || result.FailReason != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleMissingDeps(t *testing.T) {
	result := CreateAndUploadGitBundle(context.Background(), Config{}, Options{})
	if result.Success || result.Error != "git command runner is required" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateAndUploadGitBundleUsesCWD(t *testing.T) {
	finder := &recordingRootFinder{root: "/repo", ok: true}
	runner := &scriptedRunner{responses: map[string]CommandResult{
		"update-ref -d " + SeedStashRef:        {},
		"update-ref -d " + SeedRootRef:         {},
		"for-each-ref --count=1 refs/":         {Stdout: "refs/heads/main\n"},
		"stash create":                         {},
		"bundle create /tmp/seed.bundle --all": {},
	}}
	_ = CreateAndUploadGitBundle(context.Background(), Config{
		Runner:   runner,
		Uploader: &stubUploader{result: filesapi.UploadResult{Success: true, FileID: "f", Size: 1}},
		FS:       &memoryFS{tempPath: "/tmp/seed.bundle", sizes: map[string]int64{"/tmp/seed.bundle": 1}},
		GitRoot:  finder,
	}, Options{CWD: "/work"})
	if finder.workdir != "/work" {
		t.Fatalf("workdir = %q", finder.workdir)
	}
}

type fixedRootFinder struct {
	root string
	ok   bool
}

func (f fixedRootFinder) FindGitRoot(string) (string, bool) { return f.root, f.ok }

type recordingRootFinder struct {
	root    string
	ok      bool
	workdir string
}

func (f *recordingRootFinder) FindGitRoot(workdir string) (string, bool) {
	f.workdir = workdir
	return f.root, f.ok
}

type scriptedRunner struct {
	responses map[string]CommandResult
	calls     []string
}

func (r *scriptedRunner) Run(_ context.Context, cwd string, args ...string) CommandResult {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, cwd+"|"+key)
	if result, ok := r.responses[key]; ok {
		return result
	}
	return CommandResult{Code: 1, Stderr: "unexpected git args: " + key}
}

func (r *scriptedRunner) sawPrefix(prefix string) bool {
	for _, call := range r.calls {
		if strings.Contains(call, prefix) {
			return true
		}
	}
	return false
}

type stubUploader struct {
	result       filesapi.UploadResult
	path         string
	relativePath string
}

func (u *stubUploader) UploadFile(_ context.Context, filePath string, relativePath string) filesapi.UploadResult {
	u.path = filePath
	u.relativePath = relativePath
	return u.result
}

type memoryFS struct {
	tempPath string
	sizes    map[string]int64
	sizeFn   func(path string, call int) int64
	statCall int
	removed  map[string]bool
}

func (fs *memoryFS) StatSize(path string) (int64, error) {
	fs.statCall++
	if fs.sizeFn != nil {
		return fs.sizeFn(path, fs.statCall), nil
	}
	if fs.sizes != nil {
		if size, ok := fs.sizes[path]; ok {
			return size, nil
		}
	}
	return 0, fmt.Errorf("stat %s: missing", path)
}

func (fs *memoryFS) Remove(path string) error {
	if fs.removed == nil {
		fs.removed = map[string]bool{}
	}
	fs.removed[path] = true
	return nil
}

func (fs *memoryFS) TempFilePath(prefix, suffix string) string {
	if fs.tempPath != "" {
		return fs.tempPath
	}
	return filepath.Join("/tmp", prefix+suffix)
}
