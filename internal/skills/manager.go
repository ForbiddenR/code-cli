package skills

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// GitIgnoreChecker reports whether a path is ignored by its repository.
type GitIgnoreChecker interface {
	IsIgnored(context.Context, string, string) (bool, error)
}

// DiscoveryConfig supplies explicit host facts for standalone discovery.
type DiscoveryConfig struct {
	WorkingDirectory      string
	HomeDirectory         string
	ManagedRoots          []string
	ManagedCommandRoots   []string
	AdditionalDirectories []string
	DisableUser           bool
	DisableProject        bool
	DisableLegacy         bool
	Bundled               *BundledRegistry
	GitIgnore             GitIgnoreChecker
}

// Manager owns refreshable discovery and activation state.
type Manager struct {
	mu             sync.Mutex
	config         DiscoveryConfig
	workingDir     string
	homeDir        string
	dynamicRoots   map[string]struct{}
	checkedDirs    map[string]struct{}
	checkingDirs   map[string]chan struct{}
	activated      map[string]bool
	subscribers    map[uint64]func(ChangeSet)
	nextSubscriber uint64
	snapshot       atomic.Pointer[Snapshot]
}

// NewManager constructs and performs the initial discovery pass.
func NewManager(ctx context.Context, config DiscoveryConfig) (*Manager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workingDir, err := normalizeAbsoluteDirectory(config.WorkingDirectory, "working directory")
	if err != nil {
		return nil, err
	}
	homeDir := ""
	if strings.TrimSpace(config.HomeDirectory) != "" {
		homeDir, err = normalizeAbsoluteDirectory(config.HomeDirectory, "home directory")
		if err != nil {
			return nil, err
		}
	}
	config.ManagedRoots, err = normalizeDirectoryList(config.ManagedRoots, "managed skill root")
	if err != nil {
		return nil, err
	}
	config.ManagedCommandRoots, err = normalizeDirectoryList(config.ManagedCommandRoots, "managed command root")
	if err != nil {
		return nil, err
	}
	config.AdditionalDirectories, err = normalizeDirectoryList(config.AdditionalDirectories, "additional directory")
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		config:       config,
		workingDir:   workingDir,
		homeDir:      homeDir,
		dynamicRoots: make(map[string]struct{}),
		checkedDirs:  make(map[string]struct{}),
		checkingDirs: make(map[string]chan struct{}),
		activated:    make(map[string]bool),
		subscribers:  make(map[uint64]func(ChangeSet)),
	}
	if manager.config.GitIgnore == nil {
		manager.config.GitIgnore = commandGitIgnoreChecker{}
	}
	bundled := manager.bundledDefinitions()
	manager.mu.Lock()
	snapshot, err := manager.buildSnapshotLocked(ctx, bundled)
	manager.mu.Unlock()
	if err != nil {
		return nil, err
	}
	manager.snapshot.Store(snapshot)
	return manager, nil
}

// Snapshot returns the current immutable catalog.
func (manager *Manager) Snapshot() *Snapshot {
	if manager == nil {
		return nil
	}
	return manager.snapshot.Load()
}

// Summaries returns the current model-invocable summaries.
func (manager *Manager) Summaries() []Summary {
	return manager.Snapshot().Summaries()
}

// Invoke expands a skill from the current snapshot.
func (manager *Manager) Invoke(ctx context.Context, name string, options InvocationOptions) (InvocationPlan, error) {
	return manager.Snapshot().Invoke(ctx, name, options)
}

// Refresh rescans all configured and previously discovered roots.
func (manager *Manager) Refresh(ctx context.Context) error {
	if manager == nil {
		return errors.New("skill manager is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bundled := manager.bundledDefinitions()
	manager.mu.Lock()
	previous := manager.snapshot.Load()
	next, err := manager.buildSnapshotLocked(ctx, bundled)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	manager.snapshot.Store(next)
	callbacks := manager.callbacksLocked()
	change := compareSnapshots(previous, next)
	manager.mu.Unlock()
	notifySubscribers(callbacks, change)
	return nil
}

// ObservePaths discovers nested roots and activates matching conditional skills.
func (manager *Manager) ObservePaths(ctx context.Context, paths []string) error {
	if manager == nil {
		return errors.New("skill manager is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bundled := manager.bundledDefinitions()
	var diagnostics []Diagnostic
	var relativePaths []string
	var candidates []string
	seenCandidates := make(map[string]struct{})
	for _, observed := range paths {
		absolute, err := filepath.Abs(observed)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: observed, Err: err})
			continue
		}
		absolute, err = resolvePathForContainment(filepath.Clean(absolute))
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: observed, Err: err})
			continue
		}
		inside, err := pathInside(manager.workingDir, absolute)
		if err != nil || !inside {
			continue
		}
		if relative, relativeErr := filepath.Rel(manager.workingDir, absolute); relativeErr == nil {
			relativePaths = append(relativePaths, filepath.ToSlash(relative))
		}
		current := observedDirectory(absolute)
		for current != manager.workingDir {
			inside, err := pathInside(manager.workingDir, current)
			if err != nil || !inside {
				break
			}
			candidate := filepath.Join(current, ".claude", "skills")
			if _, seen := seenCandidates[candidate]; !seen {
				seenCandidates[candidate] = struct{}{}
				candidates = append(candidates, candidate)
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	slices.Sort(candidates)
	unchecked, err := manager.claimDirectoryChecks(ctx, candidates)
	if err != nil {
		return err
	}
	checksSucceeded := false
	defer func() {
		manager.finishDirectoryChecks(unchecked, checksSucceeded)
	}()

	var discovered []string
	for _, candidate := range unchecked {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.IsDir() {
			continue
		}
		ignored, ignoreErr := manager.config.GitIgnore.IsIgnored(ctx, manager.workingDir, candidate)
		if ignoreErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: candidate, Err: ignoreErr})
		}
		if ignored {
			continue
		}
		canonical, canonicalErr := canonicalRoot(candidate)
		if canonicalErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: candidate, Err: canonicalErr})
			continue
		}
		contained, containmentErr := pathInside(manager.workingDir, canonical)
		if containmentErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: candidate, Err: containmentErr})
			continue
		}
		if !contained {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: candidate, Err: errors.New("dynamic skill root resolves outside working directory")})
			continue
		}
		discovered = append(discovered, canonical)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	manager.mu.Lock()
	previous := manager.snapshot.Load()
	originalDynamicRoots := manager.dynamicRoots
	originalActivated := manager.activated
	manager.dynamicRoots = maps.Clone(originalDynamicRoots)
	manager.activated = maps.Clone(originalActivated)
	changed := false
	for _, root := range discovered {
		if _, exists := manager.dynamicRoots[root]; !exists {
			manager.dynamicRoots[root] = struct{}{}
			changed = true
		}
	}
	for _, definition := range previous.Definitions() {
		identity := previous.identity(definition.Name)
		if manager.activated[identity] || len(definition.Metadata.Paths) == 0 {
			continue
		}
		for _, relative := range relativePaths {
			if matchesPathPatterns(definition.Metadata.Paths, relative) {
				manager.activated[identity] = true
				changed = true
				break
			}
		}
	}
	if !changed && len(diagnostics) == 0 {
		manager.dynamicRoots = originalDynamicRoots
		manager.activated = originalActivated
		checksSucceeded = true
		manager.finishDirectoryChecksLocked(unchecked, true)
		unchecked = nil
		manager.mu.Unlock()
		return nil
	}
	next, err := manager.buildSnapshotLocked(ctx, bundled)
	if err != nil {
		manager.dynamicRoots = originalDynamicRoots
		manager.activated = originalActivated
		manager.mu.Unlock()
		return err
	}
	activatedAfterDiscovery := false
	for _, definition := range next.Definitions() {
		identity := next.identity(definition.Name)
		if manager.activated[identity] || len(definition.Metadata.Paths) == 0 {
			continue
		}
		for _, relative := range relativePaths {
			if matchesPathPatterns(definition.Metadata.Paths, relative) {
				manager.activated[identity] = true
				activatedAfterDiscovery = true
				break
			}
		}
	}
	if activatedAfterDiscovery {
		next, err = manager.buildSnapshotLocked(ctx, bundled)
		if err != nil {
			manager.dynamicRoots = originalDynamicRoots
			manager.activated = originalActivated
			manager.mu.Unlock()
			return err
		}
	}
	next.diagnostics = append(next.diagnostics, diagnostics...)
	manager.snapshot.Store(next)
	callbacks := manager.callbacksLocked()
	change := compareSnapshots(previous, next)
	checksSucceeded = true
	manager.finishDirectoryChecksLocked(unchecked, true)
	unchecked = nil
	manager.mu.Unlock()
	notifySubscribers(callbacks, change)
	return nil
}

// ResetSession clears dynamic discovery and sticky activation before rebuilding.
func (manager *Manager) ResetSession(ctx context.Context) error {
	if manager == nil {
		return errors.New("skill manager is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bundled := manager.bundledDefinitions()
	manager.mu.Lock()
	previous := manager.snapshot.Load()
	originalDynamicRoots := manager.dynamicRoots
	originalCheckedDirs := manager.checkedDirs
	originalActivated := manager.activated
	manager.dynamicRoots = make(map[string]struct{})
	manager.checkedDirs = make(map[string]struct{})
	manager.activated = make(map[string]bool)
	next, err := manager.buildSnapshotLocked(ctx, bundled)
	if err != nil {
		manager.dynamicRoots = originalDynamicRoots
		manager.checkedDirs = originalCheckedDirs
		manager.activated = originalActivated
		manager.mu.Unlock()
		return err
	}
	manager.snapshot.Store(next)
	callbacks := manager.callbacksLocked()
	change := compareSnapshots(previous, next)
	manager.mu.Unlock()
	notifySubscribers(callbacks, change)
	return nil
}

// Subscribe registers a best-effort snapshot-change callback.
func (manager *Manager) Subscribe(callback func(ChangeSet)) func() {
	if manager == nil || callback == nil {
		return func() {}
	}
	manager.mu.Lock()
	manager.nextSubscriber++
	id := manager.nextSubscriber
	manager.subscribers[id] = callback
	manager.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			manager.mu.Lock()
			delete(manager.subscribers, id)
			manager.mu.Unlock()
		})
	}
}

func (manager *Manager) bundledDefinitions() []loadedDefinition {
	if manager == nil || manager.config.Bundled == nil {
		return nil
	}
	return manager.config.Bundled.loadedDefinitions()
}

func (manager *Manager) buildSnapshotLocked(ctx context.Context, bundled []loadedDefinition) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded := append([]loadedDefinition(nil), bundled...)
	managedRoots := make([]Root, 0, len(manager.config.ManagedRoots))
	for _, root := range manager.config.ManagedRoots {
		managedRoots = append(managedRoots, Root{Path: root, Source: SourceManaged, Required: true})
	}
	managed, diagnostics, err := loadRoots(managedRoots, false)
	if err != nil {
		return nil, err
	}
	loaded = append(loaded, managed...)

	dynamicPaths := make([]string, 0, len(manager.dynamicRoots))
	for root := range manager.dynamicRoots {
		dynamicPaths = append(dynamicPaths, root)
	}
	slices.SortFunc(dynamicPaths, func(left, right string) int {
		leftDepth := pathDepth(left)
		rightDepth := pathDepth(right)
		if leftDepth != rightDepth {
			return rightDepth - leftDepth
		}
		return strings.Compare(left, right)
	})
	for _, root := range dynamicPaths {
		canonical, canonicalErr := canonicalRoot(root)
		if canonicalErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: root, Err: canonicalErr})
			continue
		}
		contained, containmentErr := pathInside(manager.workingDir, canonical)
		if containmentErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: root, Err: containmentErr})
			continue
		}
		if !contained {
			diagnostics = append(diagnostics, Diagnostic{Source: SourceDynamicProject, Path: root, Err: errors.New("dynamic skill root resolves outside working directory")})
			continue
		}
		definitions, rootDiagnostics, loadErr := loadRoot(Root{Path: canonical, Source: SourceDynamicProject}, false)
		if loadErr != nil {
			return nil, loadErr
		}
		loaded = append(loaded, definitions...)
		diagnostics = append(diagnostics, rootDiagnostics...)
	}

	baseRoots := manager.baseRoots()
	base, baseDiagnostics, err := loadRoots(baseRoots, false)
	if err != nil {
		return nil, err
	}
	loaded = append(loaded, base...)
	diagnostics = append(diagnostics, baseDiagnostics...)

	if !manager.config.DisableLegacy {
		legacyRoots := manager.legacyRoots()
		legacy, legacyDiagnostics, loadErr := loadRoots(legacyRoots, false)
		if loadErr != nil {
			return nil, loadErr
		}
		loaded = append(loaded, legacy...)
		diagnostics = append(diagnostics, legacyDiagnostics...)
	}
	return assembleSnapshot(loaded, diagnostics, manager.activated), nil
}

func (manager *Manager) baseRoots() []Root {
	var roots []Root
	if !manager.config.DisableUser && manager.homeDir != "" {
		roots = append(roots, Root{Path: filepath.Join(manager.homeDir, ".claude", "skills"), Source: SourceUser})
	}
	if !manager.config.DisableProject {
		for _, directory := range projectDirectories(manager.workingDir, manager.homeDir) {
			roots = append(roots, Root{Path: filepath.Join(directory, ".claude", "skills"), Source: SourceProject})
		}
	}
	for _, directory := range manager.config.AdditionalDirectories {
		roots = append(roots, Root{Path: filepath.Join(directory, ".claude", "skills"), Source: SourceAdditional})
	}
	return roots
}

func (manager *Manager) legacyRoots() []Root {
	var roots []Root
	for _, root := range manager.config.ManagedCommandRoots {
		roots = append(roots, Root{Path: root, Source: SourceLegacyCommand, Legacy: true, Required: true})
	}
	if !manager.config.DisableUser && manager.homeDir != "" {
		roots = append(roots, Root{Path: filepath.Join(manager.homeDir, ".claude", "commands"), Source: SourceLegacyCommand, Legacy: true})
	}
	if !manager.config.DisableProject {
		for _, directory := range projectDirectories(manager.workingDir, manager.homeDir) {
			roots = append(roots, Root{Path: filepath.Join(directory, ".claude", "commands"), Source: SourceLegacyCommand, Legacy: true})
		}
	}
	for _, directory := range manager.config.AdditionalDirectories {
		roots = append(roots, Root{Path: filepath.Join(directory, ".claude", "commands"), Source: SourceLegacyCommand, Legacy: true})
	}
	return roots
}

func normalizeAbsoluteDirectory(value, label string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s %q is not absolute", label, value)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", label, value, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat %s %q: %w", label, value, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory", label, value)
	}
	return resolved, nil
}

func normalizeDirectoryList(values []string, label string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeAbsoluteDirectory(value, label)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func projectDirectories(workingDir, homeDir string) []string {
	var result []string
	for current := workingDir; ; current = filepath.Dir(current) {
		result = append(result, current)
		if current == homeDir || filepath.Dir(current) == current {
			break
		}
	}
	return result
}

func (manager *Manager) claimDirectoryChecks(ctx context.Context, candidates []string) ([]string, error) {
	owned := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		for {
			manager.mu.Lock()
			if _, checked := manager.checkedDirs[candidate]; checked {
				manager.mu.Unlock()
				break
			}
			if done, checking := manager.checkingDirs[candidate]; checking {
				manager.mu.Unlock()
				select {
				case <-ctx.Done():
					manager.finishDirectoryChecks(owned, false)
					return nil, ctx.Err()
				case <-done:
				}
				continue
			}
			manager.checkingDirs[candidate] = make(chan struct{})
			manager.mu.Unlock()
			owned = append(owned, candidate)
			break
		}
	}
	return owned, nil
}

func (manager *Manager) finishDirectoryChecks(candidates []string, succeeded bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.finishDirectoryChecksLocked(candidates, succeeded)
}

func (manager *Manager) finishDirectoryChecksLocked(candidates []string, succeeded bool) {
	for _, candidate := range candidates {
		done, checking := manager.checkingDirs[candidate]
		if !checking {
			continue
		}
		if succeeded {
			manager.checkedDirs[candidate] = struct{}{}
		}
		delete(manager.checkingDirs, candidate)
		close(done)
	}
}

func resolvePathForContainment(value string) (string, error) {
	current := value
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve observed path %q: %w", value, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve observed path %q: %w", value, err)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func observedDirectory(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}

func pathDepth(value string) int {
	return len(strings.Split(filepath.Clean(value), string(filepath.Separator)))
}

func (manager *Manager) callbacksLocked() []func(ChangeSet) {
	callbacks := make([]func(ChangeSet), 0, len(manager.subscribers))
	for _, callback := range manager.subscribers {
		callbacks = append(callbacks, callback)
	}
	return callbacks
}

func compareSnapshots(previous, next *Snapshot) ChangeSet {
	previousNames := make(map[string]struct{})
	for _, definition := range previous.Definitions() {
		previousNames[definition.Name] = struct{}{}
	}
	nextNames := make(map[string]struct{})
	for _, definition := range next.Definitions() {
		nextNames[definition.Name] = struct{}{}
	}
	var change ChangeSet
	for name := range nextNames {
		if _, exists := previousNames[name]; !exists {
			change.Added = append(change.Added, name)
		}
	}
	for name := range previousNames {
		if _, exists := nextNames[name]; !exists {
			change.Removed = append(change.Removed, name)
		}
	}
	slices.Sort(change.Added)
	slices.Sort(change.Removed)
	change.Diagnostics = next.Diagnostics()
	return change
}

func notifySubscribers(callbacks []func(ChangeSet), change ChangeSet) {
	for _, callback := range callbacks {
		func() {
			defer func() { _ = recover() }()
			callback(change)
		}()
	}
}

type commandGitIgnoreChecker struct{}

func (commandGitIgnoreChecker) IsIgnored(ctx context.Context, workingDirectory, path string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", workingDirectory, "check-ignore", "-q", "--", path)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 128) {
		return false, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, fmt.Errorf("git check-ignore unavailable: %w", err)
	}
	return false, fmt.Errorf("git check-ignore: %w", err)
}
