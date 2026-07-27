package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gitIgnoreFunc func(context.Context, string, string) (bool, error)

func (function gitIgnoreFunc) IsIgnored(ctx context.Context, workingDirectory, path string) (bool, error) {
	return function(ctx, workingDirectory, path)
}

type fakeGitIgnore struct {
	mu      sync.Mutex
	ignored map[string]bool
	errors  map[string]error
	calls   []string
}

func (checker *fakeGitIgnore) IsIgnored(_ context.Context, _ string, path string) (bool, error) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	checker.calls = append(checker.calls, path)
	return checker.ignored[path], checker.errors[path]
}

func newTestManager(t *testing.T, config DiscoveryConfig) *Manager {
	t.Helper()
	manager, err := NewManager(context.Background(), config)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

func definitionSources(snapshot *Snapshot) map[string]Source {
	result := make(map[string]Source)
	for _, definition := range snapshot.Definitions() {
		result[definition.Name] = definition.Source
	}
	return result
}

func TestManagerSourceAwareDiscoveryAndLegacyCommands(t *testing.T) {
	top := t.TempDir()
	home := filepath.Join(top, "home")
	work := filepath.Join(home, "project", "sub")
	managed := filepath.Join(top, "managed-skills")
	managedCommands := filepath.Join(top, "managed-commands")
	additional := filepath.Join(top, "additional")
	for _, directory := range []string{work, managed, managedCommands, additional} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, managed, "managed", "managed")
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "user", "user")
	writeSkill(t, filepath.Join(work, ".claude", "skills"), "project", "project")
	writeSkill(t, filepath.Join(additional, ".claude", "skills"), "additional", "additional")
	writeLegacy(t, managedCommands, "admin/deploy.md", "managed legacy")
	writeLegacy(t, filepath.Join(home, ".claude", "commands"), "usercmd.md", "user legacy")
	writeLegacy(t, filepath.Join(work, ".claude", "commands"), "group/child.md", "project legacy")
	writeLegacy(t, filepath.Join(additional, ".claude", "commands"), "nested/SKILL.md", "directory legacy")

	manager := newTestManager(t, DiscoveryConfig{
		WorkingDirectory: work, HomeDirectory: home,
		ManagedRoots: []string{managed}, ManagedCommandRoots: []string{managedCommands},
		AdditionalDirectories: []string{additional}, GitIgnore: &fakeGitIgnore{},
	})
	want := map[string]Source{
		"managed": SourceManaged, "user": SourceUser, "project": SourceProject, "additional": SourceAdditional,
		"admin:deploy": SourceLegacyCommand, "usercmd": SourceLegacyCommand, "group:child": SourceLegacyCommand, "nested": SourceLegacyCommand,
	}
	if got := definitionSources(manager.Snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("sources = %#v, want %#v", got, want)
	}
	for _, name := range []string{"admin:deploy", "group:child", "nested", "usercmd"} {
		definition, ok := manager.Snapshot().Lookup(name)
		if !ok || definition.Source != SourceLegacyCommand {
			t.Fatalf("legacy lookup %q = %#v, %v", name, definition, ok)
		}
	}
}

func TestManagerPrecedenceAndDiscoveryDiagnostics(t *testing.T) {
	top := t.TempDir()
	home := filepath.Join(top, "home")
	work := filepath.Join(home, "work")
	managed := filepath.Join(top, "managed")
	for _, directory := range []string{work, managed} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, managed, "same", "managed")
	writeSkill(t, filepath.Join(work, ".claude", "skills"), "same", "project")
	writeSkill(t, filepath.Join(work, ".claude", "skills"), "bad", "---\npaths: ['..']\n---\nbad")
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, HomeDirectory: home, ManagedRoots: []string{managed}, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})
	definition, ok := manager.Snapshot().Lookup("same")
	if !ok || definition.Source != SourceManaged || definition.Body != "managed" {
		t.Fatalf("precedence winner = %#v, %v", definition, ok)
	}
	diagnostics := manager.Snapshot().Diagnostics()
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Name == "same" && strings.Contains(d.Err.Error(), "collides") }) {
		t.Fatal("missing collision diagnostic")
	}
	if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Name == "bad" && strings.Contains(d.Err.Error(), "traversal") }) {
		t.Fatal("missing invalid skill diagnostic")
	}
}

func TestConditionalPathMatching(t *testing.T) {
	tests := []struct {
		patterns []string
		path     string
		want     bool
	}{
		{[]string{"*.go"}, "src/pkg/file.go", true},
		{[]string{"/src/*.go"}, "src/file.go", true},
		{[]string{"/src/*.go"}, "nested/src/file.go", false},
		{[]string{"src/**/test?.go"}, "src/a/b/test1.go", true},
		{[]string{"docs/"}, "docs/guides/start.md", true},
		{[]string{"**", "!vendor/**", "vendor/safe/**"}, "vendor/safe/file.go", true},
		{[]string{"**", "!vendor/**"}, "vendor/file.go", false},
		{[]string{"src/**"}, ".\\src\\file.go", true},
		{[]string{"["}, "file", false},
	}
	for _, test := range tests {
		if got := matchesPathPatterns(test.patterns, test.path); got != test.want {
			t.Errorf("matchesPathPatterns(%q, %q) = %v, want %v", test.patterns, test.path, got, test.want)
		}
	}
}

func TestObservePathsDynamicDiscoveryIgnoreAndActivation(t *testing.T) {
	top := t.TempDir()
	home := filepath.Join(top, "home")
	work := filepath.Join(home, "work")
	allowedDir := filepath.Join(work, "services", "allowed")
	ignoredDir := filepath.Join(work, "services", "ignored")
	errorDir := filepath.Join(work, "services", "error")
	for _, directory := range []string{allowedDir, ignoredDir, errorDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, filepath.Join(work, ".claude", "skills"), "conditional", "---\npaths: ['services/allowed/**']\n---\nconditional")
	writeSkill(t, filepath.Join(allowedDir, ".claude", "skills"), "dynamic", "dynamic")
	writeSkill(t, filepath.Join(ignoredDir, ".claude", "skills"), "ignored", "ignored")
	writeSkill(t, filepath.Join(errorDir, ".claude", "skills"), "error-skill", "error")
	allowedRoot := filepath.Join(allowedDir, ".claude", "skills")
	ignoredRoot := filepath.Join(ignoredDir, ".claude", "skills")
	errorRoot := filepath.Join(errorDir, ".claude", "skills")
	checker := &fakeGitIgnore{
		ignored: map[string]bool{ignoredRoot: true},
		errors:  map[string]error{errorRoot: errors.New("fake ignore failure")},
	}
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, HomeDirectory: home, DisableLegacy: true, GitIgnore: checker})
	if manager.Snapshot().IsActive("conditional") {
		t.Fatal("conditional skill initially active")
	}
	observedAllowed := filepath.Join(allowedDir, "file.go")
	observedIgnored := filepath.Join(ignoredDir, "file.go")
	observedError := filepath.Join(errorDir, "file.go")
	if err := manager.ObservePaths(context.Background(), []string{observedAllowed, observedIgnored, observedError, filepath.Join(top, "outside")}); err != nil {
		t.Fatalf("ObservePaths: %v", err)
	}
	if !manager.Snapshot().IsActive("conditional") {
		t.Fatal("conditional skill not activated")
	}
	for _, name := range []string{"dynamic", "error-skill"} {
		definition, ok := manager.Snapshot().Lookup(name)
		if !ok || definition.Source != SourceDynamicProject {
			t.Fatalf("dynamic lookup %q = %#v, %v", name, definition, ok)
		}
	}
	if _, ok := manager.Snapshot().Lookup("ignored"); ok {
		t.Fatal("ignored dynamic skill was loaded")
	}
	if !slices.ContainsFunc(manager.Snapshot().Diagnostics(), func(d Diagnostic) bool {
		return d.Path == errorRoot && strings.Contains(d.Err.Error(), "fake ignore failure")
	}) {
		t.Fatalf("diagnostics = %#v", manager.Snapshot().Diagnostics())
	}
	checker.mu.Lock()
	calls := append([]string(nil), checker.calls...)
	checker.mu.Unlock()
	for _, want := range []string{allowedRoot, ignoredRoot, errorRoot} {
		if !slices.Contains(calls, want) {
			t.Fatalf("git-ignore calls %v missing %q", calls, want)
		}
	}
	before := len(calls)
	if err := manager.ObservePaths(context.Background(), []string{observedAllowed}); err != nil {
		t.Fatal(err)
	}
	checker.mu.Lock()
	after := len(checker.calls)
	checker.mu.Unlock()
	if after != before {
		t.Fatalf("already checked directory was rechecked: before=%d after=%d", before, after)
	}
}

func TestObservePathsRejectsSymlinkEscapingDynamicRoot(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, filepath.Join(work, ".claude", "skills"), "conditional", "---\npaths: ['link/**']\n---\nconditional")
	writeSkill(t, filepath.Join(outside, ".claude", "skills"), "escaped", "escaped")
	if err := os.WriteFile(filepath.Join(outside, "file.go"), []byte("package escaped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})
	if err := manager.ObservePaths(context.Background(), []string{filepath.Join(link, "file.go")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Snapshot().Lookup("escaped"); ok {
		t.Fatal("dynamic skill escaped the working-directory boundary")
	}
	if manager.Snapshot().IsActive("conditional") {
		t.Fatal("external observed path activated a conditional skill")
	}
}

func TestConditionalActivationDoesNotTransferAcrossCollisions(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(work, ".claude", "skills"), "same", "---\npaths: ['nested/**']\n---\nbase")
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "same", "---\npaths: ['other/**']\n---\ndynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})
	if err := manager.ObservePaths(context.Background(), []string{observed}); err != nil {
		t.Fatal(err)
	}
	definition, ok := manager.Snapshot().Lookup("same")
	if !ok || definition.Source != SourceDynamicProject || definition.Body != "dynamic" {
		t.Fatalf("collision winner = %#v, %v", definition, ok)
	}
	if manager.Snapshot().IsActive("same") {
		t.Fatal("activation transferred to a different same-named skill")
	}
}

func TestManagerRefreshRevalidatesDynamicRootContainment(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	dynamicRoot := filepath.Join(nested, ".claude", "skills")
	writeSkill(t, dynamicRoot, "inside", "inside")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})
	if err := manager.ObservePaths(context.Background(), []string{observed}); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Snapshot().Lookup("inside"); !ok {
		t.Fatal("dynamic skill was not initially discovered")
	}
	outsideRoot := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, outsideRoot, "outside", "outside")
	if err := os.RemoveAll(dynamicRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoot, dynamicRoot); err != nil {
		t.Fatal(err)
	}
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Snapshot().Lookup("outside"); ok {
		t.Fatal("refresh loaded a dynamic root outside the working directory")
	}
}

func TestManagerBundledEnablementMayUseManagerAPIs(t *testing.T) {
	registry, err := NewBundledRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var manager *Manager
	if err := registry.Register(BundledDefinition{
		Name:   "reentrant-manager",
		Prompt: "prompt",
		Enabled: func() bool {
			if manager != nil {
				unsubscribe := manager.Subscribe(func(ChangeSet) {})
				unsubscribe()
			}
			return true
		},
	}); err != nil {
		t.Fatal(err)
	}
	manager = newTestManager(t, DiscoveryConfig{WorkingDirectory: t.TempDir(), DisableUser: true, DisableProject: true, DisableLegacy: true, Bundled: registry, GitIgnore: &fakeGitIgnore{}})
	done := make(chan error, 1)
	go func() {
		done <- manager.Refresh(context.Background())
	}()
	select {
	case refreshErr := <-done:
		if refreshErr != nil {
			t.Fatal(refreshErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bundled enablement callback deadlocked on manager API")
	}
}

func TestManagerGitIgnoreMayUseManagerAPIs(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "dynamic", "dynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var manager *Manager
	checker := gitIgnoreFunc(func(context.Context, string, string) (bool, error) {
		unsubscribe := manager.Subscribe(func(ChangeSet) {})
		unsubscribe()
		return false, nil
	})
	manager = newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: checker})
	done := make(chan error, 1)
	go func() {
		done <- manager.ObservePaths(context.Background(), []string{observed})
	}()
	select {
	case observeErr := <-done:
		if observeErr != nil {
			t.Fatal(observeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Git-ignore callback deadlocked on manager API")
	}
}

func TestConcurrentObservePathsWaitsForDiscovery(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "dynamic", "dynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	checker := gitIgnoreFunc(func(context.Context, string, string) (bool, error) {
		once.Do(func() { close(entered) })
		<-release
		return false, nil
	})
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: checker})
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- manager.ObservePaths(context.Background(), []string{observed}) }()
	<-entered
	go func() { second <- manager.ObservePaths(context.Background(), []string{observed}) }()
	select {
	case err := <-second:
		t.Fatalf("concurrent observation returned before discovery completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for index, result := range []<-chan error{first, second} {
		if err := <-result; err != nil {
			t.Fatalf("ObservePaths call %d: %v", index, err)
		}
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); !ok {
		t.Fatal("concurrent observation did not publish discovered skill")
	}
}

func TestCanceledObservePathsReleasesDiscoveryReservation(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "dynamic", "dynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var calls atomic.Int32
	checker := gitIgnoreFunc(func(ctx context.Context, _, _ string) (bool, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return false, ctx.Err()
		}
		return false, nil
	})
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: checker})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- manager.ObservePaths(ctx, []string{observed}) }()
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ObservePaths error = %v", err)
	}
	if err := manager.ObservePaths(context.Background(), []string{observed}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatalf("Git-ignore checker calls = %d", calls.Load())
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); !ok {
		t.Fatal("canceled discovery reservation prevented retry")
	}
}

func TestResetDuringSubscriberNotificationDoesNotRestoreCheckedDirectories(t *testing.T) {
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "dynamic", "dynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	checker := gitIgnoreFunc(func(context.Context, string, string) (bool, error) {
		checks.Add(1)
		return false, nil
	})
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: checker})
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	var unsubscribe func()
	unsubscribe = manager.Subscribe(func(ChangeSet) {
		unsubscribe()
		close(callbackEntered)
		<-releaseCallback
	})
	observedDone := make(chan error, 1)
	go func() { observedDone <- manager.ObservePaths(context.Background(), []string{observed}) }()
	<-callbackEntered
	if err := manager.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(releaseCallback)
	if err := <-observedDone; err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); ok {
		t.Fatal("ResetSession retained dynamic skill")
	}
	if err := manager.ObservePaths(context.Background(), []string{observed}); err != nil {
		t.Fatal(err)
	}
	if checks.Load() < 2 {
		t.Fatalf("Git-ignore checks = %d; reset directory was not rediscovered", checks.Load())
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); !ok {
		t.Fatal("dynamic skill was not rediscovered after reset")
	}
}

func TestObservePathsRollsBackStateWhenSnapshotBuildFails(t *testing.T) {
	top := t.TempDir()
	work := filepath.Join(top, "work")
	managed := filepath.Join(top, "managed")
	nested := filepath.Join(work, "nested")
	for _, directory := range []string{work, managed, nested} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "dynamic", "dynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, ManagedRoots: []string{managed}, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})
	if err := os.RemoveAll(managed); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObservePaths(context.Background(), []string{observed}); err == nil {
		t.Fatal("ObservePaths succeeded with unavailable required managed root")
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); ok {
		t.Fatal("failed observation published dynamic skill")
	}
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObservePaths(context.Background(), []string{observed}); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); !ok {
		t.Fatal("failed observation state prevented retry")
	}
}

func TestResetSessionRollsBackStateWhenSnapshotBuildFails(t *testing.T) {
	top := t.TempDir()
	work := filepath.Join(top, "work")
	managed := filepath.Join(top, "managed")
	nested := filepath.Join(work, "nested")
	for _, directory := range []string{work, managed, nested} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, filepath.Join(nested, ".claude", "skills"), "dynamic", "dynamic")
	observed := filepath.Join(nested, "file.go")
	if err := os.WriteFile(observed, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, ManagedRoots: []string{managed}, DisableUser: true, DisableProject: true, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})
	if err := manager.ObservePaths(context.Background(), []string{observed}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(managed); err != nil {
		t.Fatal(err)
	}
	if err := manager.ResetSession(context.Background()); err == nil {
		t.Fatal("ResetSession succeeded with unavailable required managed root")
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); !ok {
		t.Fatal("failed reset discarded published dynamic state")
	}
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manager.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); ok {
		t.Fatal("successful reset retained dynamic skill")
	}
}

func TestManagerRefreshResetAndSubscribers(t *testing.T) {
	top := t.TempDir()
	home := filepath.Join(top, "home")
	work := filepath.Join(home, "work")
	dynamicDir := filepath.Join(work, "nested")
	if err := os.MkdirAll(dynamicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(work, ".claude", "skills")
	writeSkill(t, projectRoot, "conditional", "---\npaths: ['nested/**']\n---\nconditional")
	writeSkill(t, filepath.Join(dynamicDir, ".claude", "skills"), "dynamic", "dynamic")
	manager := newTestManager(t, DiscoveryConfig{WorkingDirectory: work, HomeDirectory: home, DisableLegacy: true, GitIgnore: &fakeGitIgnore{}})

	var mu sync.Mutex
	var changes []ChangeSet
	unsubscribe := manager.Subscribe(func(change ChangeSet) {
		mu.Lock()
		changes = append(changes, change)
		mu.Unlock()
	})
	manager.Subscribe(func(ChangeSet) { panic("subscriber panic") })
	if err := manager.ObservePaths(context.Background(), []string{filepath.Join(dynamicDir, "file")}); err != nil {
		t.Fatal(err)
	}
	if !manager.Snapshot().IsActive("conditional") {
		t.Fatal("conditional skill not active after observation")
	}
	writeSkill(t, projectRoot, "fresh", "fresh")
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectRoot, "fresh")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.Snapshot().IsActive("conditional") {
		t.Fatal("ResetSession retained sticky activation")
	}
	if _, ok := manager.Snapshot().Lookup("dynamic"); ok {
		t.Fatal("ResetSession retained dynamic skill")
	}
	mu.Lock()
	gotChanges := append([]ChangeSet(nil), changes...)
	mu.Unlock()
	if len(gotChanges) != 4 {
		t.Fatalf("subscriber changes = %#v", gotChanges)
	}
	if !reflect.DeepEqual(gotChanges[0].Added, []string{"dynamic"}) || !reflect.DeepEqual(gotChanges[1].Added, []string{"fresh"}) || !reflect.DeepEqual(gotChanges[2].Removed, []string{"fresh"}) || !reflect.DeepEqual(gotChanges[3].Removed, []string{"dynamic"}) {
		t.Fatalf("subscriber changes = %#v", gotChanges)
	}
	unsubscribe()
	unsubscribe()
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 4 {
		t.Fatal("unsubscribed callback was invoked")
	}
}

func TestManagerValidationAndCancellation(t *testing.T) {
	tests := []DiscoveryConfig{
		{},
		{WorkingDirectory: "relative"},
		{WorkingDirectory: filepath.Join(t.TempDir(), "missing")},
	}
	for _, config := range tests {
		if _, err := NewManager(context.Background(), config); err == nil {
			t.Fatalf("NewManager(%#v) succeeded", config)
		}
	}
	var manager *Manager
	if err := manager.Refresh(context.Background()); err == nil {
		t.Fatal("nil manager Refresh succeeded")
	}
	if err := manager.ObservePaths(context.Background(), nil); err == nil {
		t.Fatal("nil manager ObservePaths succeeded")
	}
	if err := manager.ResetSession(context.Background()); err == nil {
		t.Fatal("nil manager ResetSession succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	work := t.TempDir()
	if _, err := NewManager(ctx, DiscoveryConfig{WorkingDirectory: work}); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewManager cancellation = %v", err)
	}
}
