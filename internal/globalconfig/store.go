// Package globalconfig reads and updates Claude Code's global configuration.
package globalconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const configDirectoryEnvironment = "CLAUDE_CONFIG_DIR"

var (
	// ErrInvalidConfig identifies malformed global configuration data.
	ErrInvalidConfig = errors.New("invalid global configuration")
	// ErrUnsafePath identifies a symbolic link or non-regular config file.
	ErrUnsafePath = errors.New("unsafe global configuration path")
)

// Theme is a persisted Claude Code theme name.
type Theme string

const (
	ThemeAuto            Theme = "auto"
	ThemeDark            Theme = "dark"
	ThemeLight           Theme = "light"
	ThemeLightDaltonized Theme = "light-daltonized"
	ThemeDarkDaltonized  Theme = "dark-daltonized"
	ThemeLightANSI       Theme = "light-ansi"
	ThemeDarkANSI        Theme = "dark-ansi"
)

// Config contains the onboarding fields supported by the reduced Go rewrite.
// Nil pointers mean the corresponding property is absent from the file.
type Config struct {
	Theme                  *Theme
	HasCompletedOnboarding *bool
	LastOnboardingVersion  *string
}

type document struct {
	fields map[string]json.RawMessage
	config Config
}

type temporaryFile interface {
	io.Writer
	Chmod(os.FileMode) error
	Sync() error
	Close() error
	Name() string
}

type fileOperations struct {
	readFile   func(string, os.FileInfo) ([]byte, error)
	lstat      func(string) (os.FileInfo, error)
	stat       func(string) (os.FileInfo, error)
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (temporaryFile, error)
	remove     func(string) error
	replace    func(string, string) error
}

func defaultFileOperations() fileOperations {
	return fileOperations{
		readFile: readMatchingRegularFile,
		lstat:    os.Lstat,
		stat:     os.Stat,
		mkdirAll: os.MkdirAll,
		createTemp: func(directory, pattern string) (temporaryFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		remove:  os.Remove,
		replace: replaceFile,
	}
}

// Store serializes same-process updates to one fixed global configuration path.
// Atomic replacement prevents partial reads, but independent processes remain
// last-writer-wins until the source proper-lockfile protocol is implemented.
type Store struct {
	path string
	mu   *sync.Mutex
	fs   fileOperations
}

var storeMutex sync.Mutex

// Open resolves the source-compatible global configuration path.
func Open() (*Store, error) {
	path, err := ResolvePath()
	if err != nil {
		return nil, err
	}
	return New(path), nil
}

// New constructs a store for an explicit path without performing I/O.
func New(path string) *Store {
	path = filepath.Clean(path)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return &Store{
		path: path,
		mu:   &storeMutex,
		fs:   defaultFileOperations(),
	}
}

// ResolvePath selects the source-compatible global configuration path.
func ResolvePath() (string, error) {
	override := os.Getenv(configDirectoryEnvironment)
	home := ""
	if override == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve global configuration path: %w", err)
		}
	}
	path, err := resolvePath(home, override, os.Lstat)
	if err != nil {
		return "", fmt.Errorf("resolve global configuration path: %w", err)
	}
	return path, nil
}

func resolvePath(
	home string,
	override string,
	lstat func(string) (os.FileInfo, error),
) (string, error) {
	configHome := override
	if configHome == "" {
		configHome = filepath.Join(home, ".claude")
	}
	legacy := filepath.Join(configHome, ".config.json")
	if _, err := lstat(legacy); err == nil {
		return filepath.Abs(legacy)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect legacy global configuration %q: %w", legacy, err)
	}

	base := override
	if base == "" {
		base = home
	}
	return filepath.Abs(filepath.Join(base, ".claude.json"))
}

// Path returns the fixed path used by the store.
func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

// Load reads the supported fields without changing the file.
func (store *Store) Load() (Config, error) {
	if store == nil {
		return Config{}, errors.New("global configuration store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	document, err := store.loadDocument()
	if err != nil {
		return Config{}, err
	}
	return cloneConfig(document.config), nil
}

// Update re-reads, mutates, and atomically replaces the global configuration.
// Unknown fields are preserved. Returning an error from update performs no write.
func (store *Store) Update(update func(*Config) error) error {
	if store == nil {
		return errors.New("global configuration store is nil")
	}
	if update == nil {
		return errors.New("global configuration update is nil")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	document, err := store.loadDocument()
	if err != nil {
		return err
	}
	before := cloneConfig(document.config)
	after := cloneConfig(document.config)
	if err := update(&after); err != nil {
		return fmt.Errorf("update global configuration: %w", err)
	}
	if equalConfig(before, after) {
		return nil
	}

	if err := mergeConfig(document.fields, after); err != nil {
		return fmt.Errorf("encode global configuration fields: %w", err)
	}
	content, err := json.MarshalIndent(document.fields, "", "  ")
	if err != nil {
		return fmt.Errorf("encode global configuration: %w", err)
	}
	if err := store.replace(content); err != nil {
		return err
	}
	return nil
}

func (store *Store) loadDocument() (document, error) {
	info, err := store.inspectDestination()
	if err != nil {
		return document{}, err
	}
	if info == nil {
		return emptyDocument(), nil
	}

	content, err := store.fs.readFile(store.path, info)
	if err != nil {
		return document{}, fmt.Errorf("read global configuration %q: %w", store.path, err)
	}
	parsed, err := parseDocument(content)
	if err != nil {
		return document{}, fmt.Errorf("parse global configuration %q: %w", store.path, err)
	}
	return parsed, nil
}

func validateOpenedFile(path string, expected os.FileInfo, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expected, opened) ||
		!os.SameFile(opened, current) {
		return fmt.Errorf("%w: %q changed while opening", ErrUnsafePath, path)
	}
	return nil
}

func emptyDocument() document {
	return document{fields: make(map[string]json.RawMessage)}
}

func parseDocument(content []byte) (document, error) {
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	if len(bytes.TrimSpace(content)) == 0 {
		return emptyDocument(), nil
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return document{}, fmt.Errorf("%w: decode JSON", ErrInvalidConfig)
	}
	if fields == nil {
		return document{}, fmt.Errorf("%w: root must be an object", ErrInvalidConfig)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return document{}, err
	}

	config, err := parseConfig(fields)
	if err != nil {
		return document{}, err
	}
	return document{fields: fields, config: config}, nil
}

func parseConfig(fields map[string]json.RawMessage) (Config, error) {
	var config Config
	if raw, ok := fields["theme"]; ok {
		var value string
		if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
			return Config{}, fmt.Errorf("%w: theme must be a string", ErrInvalidConfig)
		}
		theme := Theme(value)
		config.Theme = &theme
	}
	if raw, ok := fields["hasCompletedOnboarding"]; ok {
		var value bool
		if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
			return Config{}, fmt.Errorf(
				"%w: hasCompletedOnboarding must be a boolean",
				ErrInvalidConfig,
			)
		}
		config.HasCompletedOnboarding = &value
	}
	if raw, ok := fields["lastOnboardingVersion"]; ok {
		var value string
		if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
			return Config{}, fmt.Errorf(
				"%w: lastOnboardingVersion must be a string",
				ErrInvalidConfig,
			)
		}
		config.LastOnboardingVersion = &value
	}
	return config, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: decode trailing JSON", ErrInvalidConfig)
	}
	return fmt.Errorf("%w: multiple JSON values", ErrInvalidConfig)
}

func mergeConfig(fields map[string]json.RawMessage, config Config) error {
	if err := setRaw(fields, "theme", config.Theme); err != nil {
		return err
	}
	if err := setRaw(fields, "hasCompletedOnboarding", config.HasCompletedOnboarding); err != nil {
		return err
	}
	return setRaw(fields, "lastOnboardingVersion", config.LastOnboardingVersion)
}

func setRaw[T any](fields map[string]json.RawMessage, name string, value *T) error {
	if value == nil {
		delete(fields, name)
		return nil
	}
	encoded, err := json.Marshal(*value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	fields[name] = encoded
	return nil
}

func cloneConfig(config Config) Config {
	return Config{
		Theme:                  clonePointer(config.Theme),
		HasCompletedOnboarding: clonePointer(config.HasCompletedOnboarding),
		LastOnboardingVersion:  clonePointer(config.LastOnboardingVersion),
	}
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}

func equalConfig(left, right Config) bool {
	return equalPointer(left.Theme, right.Theme) &&
		equalPointer(left.HasCompletedOnboarding, right.HasCompletedOnboarding) &&
		equalPointer(left.LastOnboardingVersion, right.LastOnboardingVersion)
}

func equalPointer[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (store *Store) inspectDestination() (os.FileInfo, error) {
	info, err := store.fs.lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect global configuration %q: %w", store.path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %q is a symbolic link", ErrUnsafePath, store.path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular file", ErrUnsafePath, store.path)
	}
	return info, nil
}

func (store *Store) replace(content []byte) (returnErr error) {
	parent := filepath.Dir(store.path)
	if err := store.fs.mkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create global configuration directory %q: %w", parent, err)
	}
	info, err := store.fs.stat(parent)
	if err != nil {
		return fmt.Errorf("inspect global configuration directory %q: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("global configuration parent %q is not a directory", parent)
	}
	if _, err := store.inspectDestination(); err != nil {
		return err
	}

	temporary, err := store.fs.createTemp(parent, "."+filepath.Base(store.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary global configuration in %q: %w", parent, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if returnErr != nil {
			_ = store.fs.remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary global configuration %q: %w", temporaryPath, err)
	}
	if err := writeAll(temporary, content); err != nil {
		return fmt.Errorf("write temporary global configuration %q: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary global configuration %q: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close temporary global configuration %q: %w", temporaryPath, err)
	}
	closed = true

	if _, err := store.inspectDestination(); err != nil {
		return err
	}
	if err := store.fs.replace(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace global configuration %q: %w", store.path, err)
	}
	return nil
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}
