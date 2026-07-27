package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, content string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func writeLegacy(t *testing.T, root, relative, content string) string {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestLoadStrictAndDefensiveSnapshot(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "zeta", "# Zeta description\nbody")
	writeSkill(t, root, "alpha", "---\ndescription: Alpha description\n---\nalpha body")
	if err := os.Mkdir(filepath.Join(root, "missing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ordinary-file"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := LoadStrict([]string{root})
	if err != nil {
		t.Fatalf("LoadStrict: %v", err)
	}
	definitions := snapshot.Definitions()
	if got := []string{definitions[0].Name, definitions[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("definition order = %v", got)
	}
	if definitions[1].Metadata.Description != "Zeta description" {
		t.Fatalf("body-derived description = %q", definitions[1].Metadata.Description)
	}
	definitions[0].Metadata.Description = "mutated"
	if definition, _ := snapshot.Lookup("alpha"); definition.Metadata.Description != "Alpha description" {
		t.Fatal("Definitions returned mutable snapshot state")
	}
	if got := snapshot.Summaries(); !reflect.DeepEqual(got, []Summary{{Name: "alpha", Description: "Alpha description"}, {Name: "zeta", Description: "Zeta description"}}) {
		t.Fatalf("summaries = %#v", got)
	}
}

func TestLoadStrictErrors(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		_, err := LoadStrict([]string{filepath.Join(t.TempDir(), "missing")})
		if err == nil || !strings.Contains(err.Error(), "resolve skill root") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("root is file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "root")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadStrict([]string{file})
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("invalid candidate", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "bad", "---\ndescription: [\n---\nbody")
		_, err := LoadStrict([]string{root})
		if err == nil || !strings.Contains(err.Error(), `parse skill "bad"`) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink escape", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeSkill(t, outside, "escaped", "body")
		if err := os.Symlink(filepath.Join(outside, "escaped"), filepath.Join(root, "escaped")); err != nil {
			t.Fatal(err)
		}
		_, err := LoadStrict([]string{root})
		if err == nil || !strings.Contains(err.Error(), "outside configured root") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAssembleSnapshotCollisionsAndAliases(t *testing.T) {
	first := Definition{Name: "first", Aliases: []string{"alias"}, Source: SourceExplicit, File: "/first", Metadata: defaultMetadata()}
	second := Definition{Name: "alias", Source: SourceProject, File: "/second", Metadata: defaultMetadata()}
	duplicate := Definition{Name: "duplicate", Source: SourceUser, File: "/first", Metadata: defaultMetadata()}
	snapshot := assembleSnapshot([]loadedDefinition{
		{definition: first, identity: "same"},
		{definition: second, identity: "second"},
		{definition: duplicate, identity: "same"},
	}, nil, nil)
	definition, ok := snapshot.Lookup("alias")
	if !ok || definition.Name != "first" {
		t.Fatalf("alias lookup = %#v, %v", definition, ok)
	}
	if len(snapshot.Definitions()) != 1 || len(snapshot.Diagnostics()) != 2 {
		t.Fatalf("definitions=%d diagnostics=%d", len(snapshot.Definitions()), len(snapshot.Diagnostics()))
	}
	if !snapshot.IsActive("alias") {
		t.Fatal("alias should share canonical activation")
	}
}

func TestNilSnapshotMethods(t *testing.T) {
	var snapshot *Snapshot
	if snapshot.Definitions() != nil || snapshot.Summaries() != nil || snapshot.Diagnostics() != nil || snapshot.IsActive("x") {
		t.Fatal("nil snapshot accessors returned non-zero values")
	}
	if _, ok := snapshot.Lookup("x"); ok {
		t.Fatal("nil snapshot lookup succeeded")
	}
	_, err := snapshot.Invoke(context.Background(), "x", InvocationOptions{})
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("Invoke error = %v", err)
	}
}
