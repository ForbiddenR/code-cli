//go:build linux

package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bundledSnapshot(registry *BundledRegistry) *Snapshot {
	return assembleSnapshot(registry.loadedDefinitions(), nil, nil)
}

func TestBundledRegistryValidationDefaultsAndCopies(t *testing.T) {
	if _, err := NewBundledRegistry(""); err == nil {
		t.Fatal("empty cache root accepted")
	}
	registry, err := NewBundledRegistry(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	var nilRegistry *BundledRegistry
	if err := nilRegistry.Register(BundledDefinition{Name: "x", Prompt: "x"}); err == nil {
		t.Fatal("nil registry Register succeeded")
	}
	invalid := []BundledDefinition{
		{Name: "", Prompt: "x"},
		{Name: "../x", Prompt: "x"},
		{Name: "x"},
		{Name: "x", Prompt: "x", BuildPrompt: func(context.Context, string) (string, error) { return "", nil }},
	}
	for _, definition := range invalid {
		if err := registry.Register(definition); err == nil {
			t.Errorf("Register(%#v) succeeded", definition)
		}
	}

	hooks := map[string]any{"pre": []any{map[string]any{"command": "original"}}}
	files := map[string]string{"asset.txt": "original"}
	definition := BundledDefinition{
		Name: "demo", Aliases: []string{"alias"}, Description: "fallback", Prompt: "prompt",
		Metadata: Metadata{AllowedTools: []string{"Read"}, Hooks: hooks}, Files: files,
	}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	definition.Aliases[0] = "mutated"
	definition.Metadata.AllowedTools[0] = "mutated"
	files["asset.txt"] = "mutated"
	hooks["pre"].([]any)[0].(map[string]any)["command"] = "mutated"
	got := registry.Definitions()
	if len(got) != 1 || got[0].Aliases[0] != "alias" || got[0].Metadata.AllowedTools[0] != "Read" || got[0].Files["asset.txt"] != "original" {
		t.Fatalf("stored definition was mutated: %#v", got)
	}
	if got[0].Metadata.Hooks["pre"].([]any)[0].(map[string]any)["command"] != "original" {
		t.Fatal("nested hooks were not defensively copied")
	}
	if !got[0].Metadata.UserInvocable || got[0].Metadata.Context != "inline" || got[0].Metadata.Description != "fallback" {
		t.Fatalf("defaults = %#v", got[0].Metadata)
	}
	got[0].Files["asset.txt"] = "again"
	if registry.Definitions()[0].Files["asset.txt"] != "original" {
		t.Fatal("Definitions returned mutable state")
	}
	if err := registry.Register(BundledDefinition{Name: "alias", Prompt: "collision"}); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision error = %v", err)
	}
	if err := registry.Register(BundledDefinition{Name: "other", Aliases: []string{" bad "}, Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "invalid skill name") {
		t.Fatalf("alias validation error = %v", err)
	}
}

func TestBundledRegistryEnabledUserInvocationAndBuilder(t *testing.T) {
	registry, err := NewBundledRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(BundledDefinition{Name: "disabled", Prompt: "x", Enabled: func() bool { return false }}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(BundledDefinition{Name: "user-off", Prompt: "x", UserInvocable: new(false)}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if err := registry.Register(BundledDefinition{
		Name: "dynamic", Aliases: []string{"dyn"}, Metadata: Metadata{ArgumentNames: []string{"first"}},
		BuildPrompt: func(ctx context.Context, raw string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			calls.Add(1)
			return "$first|$ARGUMENTS|raw=" + raw, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := bundledSnapshot(registry)
	if _, ok := snapshot.Lookup("disabled"); ok {
		t.Fatal("disabled bundled skill loaded")
	}
	if _, err := snapshot.Invoke(context.Background(), "user-off", InvocationOptions{Origin: OriginUser}); !errors.Is(err, ErrUserInvocationOff) {
		t.Fatalf("user invocation error = %v", err)
	}
	plan, err := snapshot.Invoke(context.Background(), "dyn", InvocationOptions{Origin: OriginUser, Args: new("one two")})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "dynamic" || plan.Instructions != "one|one two|raw=one two" || calls.Load() != 1 {
		t.Fatalf("plan=%#v calls=%d", plan, calls.Load())
	}
}

func TestBundledEnablementCallbackMayReenterRegistry(t *testing.T) {
	registry, err := NewBundledRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(BundledDefinition{
		Name:   "reentrant",
		Prompt: "prompt",
		Enabled: func() bool {
			return registry.Register(BundledDefinition{Name: "registered-during-callback", Prompt: "later"}) == nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan []loadedDefinition, 1)
	go func() {
		done <- registry.loadedDefinitions()
	}()
	select {
	case definitions := <-done:
		if len(definitions) != 1 || definitions[0].definition.Name != "reentrant" {
			t.Fatalf("definitions = %#v", definitions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("enablement callback deadlocked while reentering registry")
	}
	if len(registry.Definitions()) != 2 {
		t.Fatalf("registered definitions = %#v", registry.Definitions())
	}
}

func TestBundledExtractionModesAndConcurrentInvoke(t *testing.T) {
	registry, err := NewBundledRegistry(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(BundledDefinition{
		Name: "extract", Prompt: "dir=${CLAUDE_SKILL_DIR}",
		Files: map[string]string{"README.txt": "readme", "nested/tool.sh": "tool"},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := bundledSnapshot(registry)
	const goroutines = 24
	plans := make(chan InvocationPlan, goroutines)
	errs := make(chan error, goroutines)
	var wait sync.WaitGroup
	for range goroutines {
		wait.Go(func() {
			plan, invokeErr := snapshot.Invoke(context.Background(), "extract", InvocationOptions{Origin: OriginUser})
			if invokeErr != nil {
				errs <- invokeErr
				return
			}
			plans <- plan
		})
	}
	wait.Wait()
	close(plans)
	close(errs)
	for invokeErr := range errs {
		t.Errorf("concurrent Invoke: %v", invokeErr)
	}
	var directory string
	for plan := range plans {
		prefix := "Base directory for this skill: "
		line := strings.SplitN(plan.Instructions, "\n", 2)[0]
		gotDirectory := strings.TrimPrefix(line, prefix)
		if gotDirectory == line {
			t.Fatalf("instructions missing directory: %q", plan.Instructions)
		}
		if directory == "" {
			directory = gotDirectory
		} else if gotDirectory != directory {
			t.Fatalf("directories differ: %q and %q", directory, gotDirectory)
		}
		if !strings.Contains(plan.Instructions, "dir="+gotDirectory) {
			t.Fatalf("skill dir substitution missing: %q", plan.Instructions)
		}
	}
	for relative, want := range map[string]string{"README.txt": "readme", "nested/tool.sh": "tool"} {
		content, readErr := os.ReadFile(filepath.Join(directory, relative))
		if readErr != nil || string(content) != want {
			t.Fatalf("file %q = %q, %v", relative, content, readErr)
		}
	}
	for path, want := range map[string]os.FileMode{
		directory: 0o700, filepath.Join(directory, "nested"): 0o700,
		filepath.Join(directory, "README.txt"): 0o600, filepath.Join(directory, "nested", "tool.sh"): 0o600,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != want {
			t.Fatalf("mode %q = %v, %v, want %v", path, info.Mode().Perm(), statErr, want)
		}
	}
}

func TestBundledExtractionRejectsInvalidPaths(t *testing.T) {
	invalid := []string{"", "/absolute", `back\\slash`, ".", "..", "a/../b", "a//b", "a/./b", "nul\x00name"}
	for _, name := range invalid {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			if err := validateBundledFilePath(name); !errors.Is(err, ErrInvalidBundledFile) {
				t.Fatalf("validateBundledFilePath(%q) = %v", name, err)
			}
			registry, err := NewBundledRegistry(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(BundledDefinition{Name: "bad", Prompt: "x", Files: map[string]string{name: "content"}}); err != nil {
				t.Fatal(err)
			}
			_, err = bundledSnapshot(registry).Invoke(context.Background(), "bad", InvocationOptions{Origin: OriginUser})
			if !errors.Is(err, ErrInvalidBundledFile) {
				t.Fatalf("Invoke error = %v", err)
			}
		})
	}
}

func TestBundledExtractionResistsDirectorySymlink(t *testing.T) {
	registry, err := NewBundledRegistry(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(BundledDefinition{Name: "attack", Prompt: "x", Files: map[string]string{"linked/payload": "owned"}}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	skillRoot := filepath.Join(registry.processRoot, "attack")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	_, err = bundledSnapshot(registry).Invoke(context.Background(), "attack", InvocationOptions{Origin: OriginUser})
	if err == nil {
		t.Fatal("symlink attack unexpectedly succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "payload")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("payload escaped extraction root: %v", statErr)
	}
}

func TestSortedBundledFileNames(t *testing.T) {
	if got := sortedBundledFileNames(map[string]string{"z": "", "a": "", "m": ""}); !reflect.DeepEqual(got, []string{"a", "m", "z"}) {
		t.Fatalf("sorted names = %v", got)
	}
}
