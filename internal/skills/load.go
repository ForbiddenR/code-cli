package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Root describes one already-resolved discovery location.
type Root struct {
	Path     string
	Source   Source
	Legacy   bool
	Required bool
}

type loadedDefinition struct {
	definition Definition
	identity   string
}

// LoadStrict snapshots standard skill roots using the legacy strict contract.
func LoadStrict(roots []string) (*Snapshot, error) {
	locations := make([]Root, len(roots))
	for index, root := range roots {
		locations[index] = Root{Path: root, Source: SourceExplicit, Required: true}
	}
	loaded, diagnostics, err := loadRoots(locations, true)
	if err != nil {
		return nil, err
	}
	return assembleSnapshot(loaded, diagnostics, nil), nil
}

func loadRoots(roots []Root, strict bool) ([]loadedDefinition, []Diagnostic, error) {
	var loaded []loadedDefinition
	var diagnostics []Diagnostic
	for _, root := range roots {
		definitions, rootDiagnostics, err := loadRoot(root, strict)
		if err != nil {
			return nil, nil, err
		}
		loaded = append(loaded, definitions...)
		diagnostics = append(diagnostics, rootDiagnostics...)
	}
	return loaded, diagnostics, nil
}

func loadRoot(root Root, strict bool) ([]loadedDefinition, []Diagnostic, error) {
	canonical, err := canonicalRoot(root.Path)
	if err != nil {
		if !root.Required && errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		if strict || root.Required {
			return nil, nil, err
		}
		return nil, []Diagnostic{{Source: root.Source, Path: root.Path, Err: err}}, nil
	}
	if root.Legacy {
		return loadLegacyRoot(canonical, root.Source, strict)
	}
	return loadSkillsRoot(canonical, root.Source, strict)
}

func loadSkillsRoot(root string, source Source, strict bool) ([]loadedDefinition, []Diagnostic, error) {
	children, err := os.ReadDir(root)
	if err != nil {
		return handleRootError(source, root, strict, fmt.Errorf("read skill root %q: %w", root, err))
	}
	slices.SortFunc(children, func(left, right os.DirEntry) int {
		return strings.Compare(left.Name(), right.Name())
	})
	var loaded []loadedDefinition
	var diagnostics []Diagnostic
	for _, child := range children {
		name := child.Name()
		candidateDirectory := filepath.Join(root, name)
		info, statErr := os.Stat(candidateDirectory)
		if statErr != nil || !info.IsDir() {
			continue
		}
		definition, identity, candidateErr := loadSkillFile(root, name, filepath.Join(candidateDirectory, "SKILL.md"), source)
		if errors.Is(candidateErr, os.ErrNotExist) {
			continue
		}
		if candidateErr != nil {
			if strict {
				return nil, nil, candidateErr
			}
			diagnostics = append(diagnostics, Diagnostic{Source: source, Path: candidateDirectory, Name: name, Err: candidateErr})
			continue
		}
		loaded = append(loaded, loadedDefinition{definition: definition, identity: identity})
	}
	return loaded, diagnostics, nil
}

func loadSkillFile(root, name, file string, source Source) (Definition, string, error) {
	canonicalFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		return Definition{}, "", fmt.Errorf("resolve skill %q: %w", name, err)
	}
	canonicalFile, err = filepath.Abs(canonicalFile)
	if err != nil {
		return Definition{}, "", fmt.Errorf("make skill %q absolute: %w", name, err)
	}
	inside, err := pathInside(root, canonicalFile)
	if err != nil {
		return Definition{}, "", fmt.Errorf("check skill %q containment: %w", name, err)
	}
	if !inside {
		return Definition{}, "", fmt.Errorf("skill %q resolves outside configured root", name)
	}
	info, err := os.Stat(canonicalFile)
	if err != nil {
		return Definition{}, "", fmt.Errorf("stat skill %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return Definition{}, "", fmt.Errorf("skill %q SKILL.md is not a regular file", name)
	}
	content, err := os.ReadFile(canonicalFile)
	if err != nil {
		return Definition{}, "", fmt.Errorf("read skill %q: %w", name, err)
	}
	metadata, body, err := parseFrontmatter(string(content))
	if err != nil {
		return Definition{}, "", fmt.Errorf("parse skill %q: %w", name, err)
	}
	if metadata.Description == "" {
		metadata.Description = descriptionFromBody(body)
	}
	return Definition{
		Name:      name,
		Source:    source,
		Directory: filepath.Dir(canonicalFile),
		File:      canonicalFile,
		Body:      body,
		Metadata:  metadata,
	}, canonicalFile, nil
}

func loadLegacyRoot(root string, source Source, strict bool) ([]loadedDefinition, []Diagnostic, error) {
	var loaded []loadedDefinition
	var diagnostics []Diagnostic
	visited := make(map[string]struct{})
	var walk func(string, []string) error
	walk = func(directory string, namespace []string) error {
		canonicalDirectory, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return err
		}
		inside, err := pathInside(root, canonicalDirectory)
		if err != nil || !inside {
			if err != nil {
				return err
			}
			return fmt.Errorf("legacy command directory %q resolves outside configured root", directory)
		}
		if _, exists := visited[canonicalDirectory]; exists {
			return nil
		}
		visited[canonicalDirectory] = struct{}{}
		children, err := os.ReadDir(canonicalDirectory)
		if err != nil {
			return err
		}
		slices.SortFunc(children, func(left, right os.DirEntry) int {
			return strings.Compare(left.Name(), right.Name())
		})
		for _, child := range children {
			if child.Name() == "SKILL.md" {
				if len(namespace) == 0 {
					break
				}
				name := strings.Join(namespace, ":")
				definition, identity, loadErr := loadSkillFile(root, name, filepath.Join(canonicalDirectory, child.Name()), source)
				if loadErr != nil {
					return loadErr
				}
				loaded = append(loaded, loadedDefinition{definition: definition, identity: identity})
				return nil
			}
		}
		for _, child := range children {
			childPath := filepath.Join(canonicalDirectory, child.Name())
			info, statErr := os.Stat(childPath)
			if statErr != nil {
				if strict {
					return statErr
				}
				diagnostics = append(diagnostics, Diagnostic{Source: source, Path: childPath, Err: statErr})
				continue
			}
			if info.IsDir() {
				if walkErr := walk(childPath, append(namespace, child.Name())); walkErr != nil {
					if strict {
						return walkErr
					}
					diagnostics = append(diagnostics, Diagnostic{Source: source, Path: childPath, Err: walkErr})
				}
				continue
			}
			if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(child.Name()), ".md") || child.Name() == "SKILL.md" {
				continue
			}
			nameParts := append(append([]string(nil), namespace...), strings.TrimSuffix(child.Name(), filepath.Ext(child.Name())))
			name := strings.Join(nameParts, ":")
			definition, identity, loadErr := loadSkillFile(root, name, childPath, source)
			if loadErr != nil {
				if strict {
					return loadErr
				}
				diagnostics = append(diagnostics, Diagnostic{Source: source, Path: childPath, Name: name, Err: loadErr})
				continue
			}
			loaded = append(loaded, loadedDefinition{definition: definition, identity: identity})
		}
		return nil
	}
	if err := walk(root, nil); err != nil {
		return handleRootError(source, root, strict, fmt.Errorf("load legacy command root %q: %w", root, err))
	}
	return loaded, diagnostics, nil
}

func handleRootError(source Source, path string, strict bool, err error) ([]loadedDefinition, []Diagnostic, error) {
	if strict {
		return nil, nil, err
	}
	return nil, []Diagnostic{{Source: source, Path: path, Err: err}}, nil
}

func canonicalRoot(configured string) (string, error) {
	if strings.TrimSpace(configured) == "" {
		return "", errors.New("skill root is empty")
	}
	absolute, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("make skill root %q absolute: %w", configured, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve skill root %q: %w", configured, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat skill root %q: %w", configured, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skill root %q is not a directory", configured)
	}
	return resolved, nil
}

func pathInside(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func assembleSnapshot(loaded []loadedDefinition, diagnostics []Diagnostic, activated map[string]bool) *Snapshot {
	snapshot := &Snapshot{
		definitions: make(map[string]Definition),
		aliases:     make(map[string]string),
		identities:  make(map[string]string),
		active:      make(map[string]bool),
		diagnostics: append([]Diagnostic(nil), diagnostics...),
	}
	seenFiles := make(map[string]string)
	owners := make(map[string]string)
	for _, candidate := range loaded {
		definition := candidate.definition
		if owner, exists := seenFiles[candidate.identity]; exists {
			snapshot.diagnostics = append(snapshot.diagnostics, Diagnostic{Source: definition.Source, Path: definition.File, Name: definition.Name, Err: fmt.Errorf("duplicate canonical skill file already loaded as %q", owner)})
			continue
		}
		if owner, exists := owners[definition.Name]; exists {
			snapshot.diagnostics = append(snapshot.diagnostics, Diagnostic{Source: definition.Source, Path: definition.File, Name: definition.Name, Err: fmt.Errorf("skill name collides with %q", owner)})
			continue
		}
		collision := false
		for _, alias := range definition.Aliases {
			if owner, exists := owners[alias]; exists {
				snapshot.diagnostics = append(snapshot.diagnostics, Diagnostic{Source: definition.Source, Path: definition.File, Name: definition.Name, Err: fmt.Errorf("skill alias %q collides with %q", alias, owner)})
				collision = true
				break
			}
		}
		if collision {
			continue
		}
		seenFiles[candidate.identity] = definition.Name
		owners[definition.Name] = definition.Name
		for _, alias := range definition.Aliases {
			owners[alias] = definition.Name
			snapshot.aliases[alias] = definition.Name
		}
		snapshot.definitions[definition.Name] = cloneDefinition(definition)
		snapshot.identities[definition.Name] = candidate.identity
		active := len(definition.Metadata.Paths) == 0 || activated[candidate.identity]
		snapshot.active[definition.Name] = active
		if active && !definition.Metadata.DisableModelInvocation && definition.Metadata.Context != "fork" && definition.Metadata.Agent == "" && len(definition.Metadata.Hooks) == 0 && definition.Metadata.Shell == "" {
			description := definition.Metadata.Description
			if definition.Metadata.WhenToUse != "" {
				if description != "" {
					description += " "
				}
				description += definition.Metadata.WhenToUse
			}
			snapshot.summaries = append(snapshot.summaries, Summary{Name: definition.Name, Description: description})
		}
	}
	slices.SortFunc(snapshot.summaries, func(left, right Summary) int {
		return strings.Compare(left.Name, right.Name)
	})
	return snapshot
}
