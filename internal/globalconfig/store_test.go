package globalconfig

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolvePath(t *testing.T) {
	home := t.TempDir()
	custom := t.TempDir()

	tests := []struct {
		name     string
		override string
		legacy   string
		want     string
	}{
		{
			name: "default",
			want: filepath.Join(home, ".claude.json"),
		},
		{
			name:   "default legacy",
			legacy: filepath.Join(home, ".claude", ".config.json"),
			want:   filepath.Join(home, ".claude", ".config.json"),
		},
		{
			name:     "custom",
			override: custom,
			want:     filepath.Join(custom, ".claude.json"),
		},
		{
			name:     "custom legacy",
			override: custom,
			legacy:   filepath.Join(custom, ".config.json"),
			want:     filepath.Join(custom, ".config.json"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.legacy != "" {
				if err := os.MkdirAll(filepath.Dir(test.legacy), 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(test.legacy, []byte(`{}`), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}
			got, err := resolvePath(home, test.override, os.Lstat)
			if err != nil {
				t.Fatalf("resolvePath() error = %v", err)
			}
			want, err := filepath.Abs(test.want)
			if err != nil {
				t.Fatalf("Abs() error = %v", err)
			}
			if got != want {
				t.Fatalf("resolvePath() = %q, want %q", got, want)
			}
		})
	}
}

func TestResolvePathReportsLegacyProbeFailure(t *testing.T) {
	probeErr := errors.New("probe failed")
	_, err := resolvePath("/home/test", "", func(string) (os.FileInfo, error) {
		return nil, probeErr
	})
	if !errors.Is(err, probeErr) {
		t.Fatalf("resolvePath() error = %v, want %v", err, probeErr)
	}
}

func TestLoad(t *testing.T) {
	falseValue := false
	empty := ""
	customTheme := Theme("future-theme")
	bomJSON := string(append(
		[]byte{0xef, 0xbb, 0xbf},
		[]byte(`{"theme":"light"}`)...,
	))

	tests := []struct {
		name    string
		content *string
		want    Config
	}{
		{name: "missing"},
		{name: "empty", content: new(" \n\t ")},
		{name: "empty object", content: new(`{}`)},
		{name: "BOM", content: &bomJSON, want: Config{Theme: new(ThemeLight)}},
		{
			name: "all fields and unknown values",
			content: new(`{
				"theme":"future-theme",
				"hasCompletedOnboarding":false,
				"lastOnboardingVersion":"",
				"unknown":{"nested":[1,true,null]}
			}`),
			want: Config{
				Theme:                  &customTheme,
				HasCompletedOnboarding: &falseValue,
				LastOnboardingVersion:  &empty,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".claude.json")
			if test.content != nil {
				if err := os.WriteFile(path, []byte(*test.content), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}
			got, err := New(path).Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Load() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidConfigWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "malformed", content: `{"token":"secret-value"`},
		{name: "trailing", content: `{} {"token":"secret-value"}`},
		{name: "null root", content: `null`},
		{name: "array root", content: `["secret-value"]`},
		{name: "theme null", content: `{"theme":null,"token":"secret-value"}`},
		{name: "theme object", content: `{"theme":{"secret":"secret-value"}}`},
		{name: "completion null", content: `{"hasCompletedOnboarding":null,"token":"secret-value"}`},
		{name: "completion string", content: `{"hasCompletedOnboarding":"secret-value"}`},
		{name: "version null", content: `{"lastOnboardingVersion":null,"token":"secret-value"}`},
		{name: "version array", content: `{"lastOnboardingVersion":["secret-value"]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".claude.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			_, err := New(path).Load()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Load() error = %v, want ErrInvalidConfig", err)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("Load() error leaked value: %v", err)
			}
		})
	}
}

func TestUpdatePreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	original := `{
		"apiKey":"secret-value",
		"projects":{"/workspace":{"allowedTools":["Read"]}},
		"theme":"dark",
		"hasCompletedOnboarding":false,
		"lastOnboardingVersion":"old"
	}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := New(path)
	if err := store.Update(func(config *Config) error {
		config.Theme = new(ThemeLight)
		config.HasCompletedOnboarding = new(true)
		config.LastOnboardingVersion = nil
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	var apiKey string
	if err := json.Unmarshal(fields["apiKey"], &apiKey); err != nil {
		t.Fatalf("Unmarshal(apiKey) error = %v", err)
	}
	var projects map[string]any
	if err := json.Unmarshal(fields["projects"], &projects); err != nil {
		t.Fatalf("Unmarshal(projects) error = %v", err)
	}
	wantProjects := map[string]any{
		"/workspace": map[string]any{
			"allowedTools": []any{"Read"},
		},
	}
	if apiKey != "secret-value" || !reflect.DeepEqual(projects, wantProjects) {
		t.Fatalf("unknown fields were not preserved: %s", content)
	}
	if string(fields["theme"]) != `"light"` ||
		string(fields["hasCompletedOnboarding"]) != `true` {
		t.Fatalf("known fields were not updated: %s", content)
	}
	if _, ok := fields["lastOnboardingVersion"]; ok {
		t.Fatalf("removed field remains: %s", content)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Theme == nil || *got.Theme != ThemeLight ||
		got.HasCompletedOnboarding == nil || !*got.HasCompletedOnboarding ||
		got.LastOnboardingVersion != nil {
		t.Fatalf("Load() after update = %#v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("updated mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestUpdateCreatesPrivatePath(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "config")
	path := filepath.Join(parent, ".claude.json")

	if err := New(path).Update(func(config *Config) error {
		config.Theme = new(ThemeDark)
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("Stat(parent) error = %v", err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(path) error = %v", err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %o, want 700", parentInfo.Mode().Perm())
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", fileInfo.Mode().Perm())
	}
}

func TestUpdateDoesNotRewriteNoOpOrCallbackFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	original := []byte(`{"theme":"dark","unknown":true}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := New(path)
	var replacements int
	replace := store.fs.replace
	store.fs.replace = func(source, destination string) error {
		replacements++
		return replace(source, destination)
	}
	if err := store.Update(func(*Config) error { return nil }); err != nil {
		t.Fatalf("no-op Update() error = %v", err)
	}
	if replacements != 0 {
		t.Fatalf("no-op Update() replacements = %d, want 0", replacements)
	}

	callbackErr := errors.New("callback failed")
	if err := store.Update(func(config *Config) error {
		config.Theme = new(ThemeLight)
		return callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("Update() error = %v, want %v", err, callbackErr)
	}
	assertFileContent(t, path, original)

	missingParent := filepath.Join(t.TempDir(), "missing")
	missingPath := filepath.Join(missingParent, ".claude.json")
	if err := New(missingPath).Update(func(*Config) error { return nil }); err != nil {
		t.Fatalf("missing no-op Update() error = %v", err)
	}
	if _, err := os.Stat(missingParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing no-op parent Stat() error = %v, want ErrNotExist", err)
	}
}

func TestUpdateFailureLeavesOriginalAndCleansTemporaryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	original := []byte(`{"theme":"dark","unknown":true}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tests := []struct {
		name      string
		configure func(*Store)
	}{
		{
			name: "write",
			configure: func(store *Store) {
				createTemp := store.fs.createTemp
				store.fs.createTemp = func(directory, pattern string) (temporaryFile, error) {
					file, err := createTemp(directory, pattern)
					return &failingFile{temporaryFile: file, writeErr: errors.New("write failed")}, err
				}
			},
		},
		{
			name: "sync",
			configure: func(store *Store) {
				createTemp := store.fs.createTemp
				store.fs.createTemp = func(directory, pattern string) (temporaryFile, error) {
					file, err := createTemp(directory, pattern)
					return &failingFile{temporaryFile: file, syncErr: errors.New("sync failed")}, err
				}
			},
		},
		{
			name: "close",
			configure: func(store *Store) {
				createTemp := store.fs.createTemp
				store.fs.createTemp = func(directory, pattern string) (temporaryFile, error) {
					file, err := createTemp(directory, pattern)
					return &failingFile{temporaryFile: file, closeErr: errors.New("close failed")}, err
				}
			},
		},
		{
			name: "replace",
			configure: func(store *Store) {
				store.fs.replace = func(string, string) error { return errors.New("replace failed") }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			store := New(path)
			test.configure(store)
			err := store.Update(func(config *Config) error {
				config.Theme = new(ThemeLight)
				return nil
			})
			if err == nil {
				t.Fatal("Update() error = nil")
			}
			assertFileContent(t, path, original)
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".claude.json.tmp-*"))
			if err != nil {
				t.Fatalf("Glob() error = %v", err)
			}
			if len(matches) != 0 {
				t.Fatalf("temporary files remain: %#v", matches)
			}
		})
	}
}

func TestUpdateRejectsMalformedAndUnsafeDestinations(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".claude.json")
		original := []byte(`{"theme":`)
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		err := New(path).Update(func(config *Config) error {
			config.Theme = new(ThemeDark)
			return nil
		})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("Update() error = %v, want ErrInvalidConfig", err)
		}
		assertFileContent(t, path, original)
	})

	t.Run("directory", func(t *testing.T) {
		path := t.TempDir()
		_, err := New(path).Load()
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.json")
		link := filepath.Join(directory, ".claude.json")
		original := []byte(`{"theme":"dark"}`)
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err := New(link).Load()
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
		assertFileContent(t, target, original)
	})
}

func TestLoadRejectsDestinationSwap(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".claude.json")
	moved := filepath.Join(directory, "moved.json")
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(path) error = %v", err)
	}
	if err := os.WriteFile(target, []byte(`{"apiKey":"secret-value"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	store := New(path)
	readFile := store.fs.readFile
	store.fs.readFile = func(path string, expected os.FileInfo) ([]byte, error) {
		if err := os.Rename(path, moved); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, path); err != nil {
			return nil, err
		}
		return readFile(path, expected)
	}
	_, err := store.Load()
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("symlink unavailable: %v", err)
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
	}
}

func TestUpdateSerializesCallbacksAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	stores := []*Store{New(path), New(path)}
	var active atomic.Int32
	var maximum atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range 8 {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			<-start
			if err := store.Update(func(config *Config) error {
				current := active.Add(1)
				for {
					observed := maximum.Load()
					if current <= observed || maximum.CompareAndSwap(observed, current) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				active.Add(-1)
				config.Theme = new(ThemeDark)
				return nil
			}); err != nil {
				t.Errorf("Update() error = %v", err)
			}
		}(stores[index%len(stores)])
	}
	close(start)
	wait.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent callbacks = %d, want 1", maximum.Load())
	}
}

type failingFile struct {
	temporaryFile
	writeErr error
	syncErr  error
	closeErr error
}

func (file *failingFile) Write(content []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return file.temporaryFile.Write(content)
}

func (file *failingFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.temporaryFile.Sync()
}

func (file *failingFile) Close() error {
	if file.closeErr != nil {
		_ = file.temporaryFile.Close()
		return file.closeErr
	}
	return file.temporaryFile.Close()
}

//go:fix inline
func themePointer(theme Theme) *Theme {
	return new(theme)
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

var _ io.Writer = (*failingFile)(nil)
