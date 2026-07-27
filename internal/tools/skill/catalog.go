package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	skillsdomain "code-cli/internal/skills"
)

// Config selects the local directories searched for skills.
type Config struct {
	Roots   []string
	Manager *skillsdomain.Manager
}

// Summary describes a skill that may be invoked by the model.
type Summary = skillsdomain.Summary

type entry struct {
	name      string
	directory string
	body      string
	metadata  metadata
}

func loadCatalog(config Config) (map[string]entry, []Summary, error) {
	entries := make(map[string]entry)
	seenFiles := make(map[string]struct{})
	for _, configuredRoot := range config.Roots {
		root, err := canonicalRoot(configuredRoot)
		if err != nil {
			return nil, nil, err
		}
		children, err := os.ReadDir(root)
		if err != nil {
			return nil, nil, fmt.Errorf("read skill root %q: %w", configuredRoot, err)
		}
		slices.SortFunc(children, func(left, right os.DirEntry) int {
			return strings.Compare(left.Name(), right.Name())
		})
		for _, child := range children {
			name := child.Name()
			if _, exists := entries[name]; exists {
				continue
			}
			candidateDirectory := filepath.Join(root, name)
			info, err := os.Stat(candidateDirectory)
			if err != nil || !info.IsDir() {
				continue
			}
			candidateFile := filepath.Join(candidateDirectory, "SKILL.md")
			canonicalFile, err := filepath.EvalSymlinks(candidateFile)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, nil, fmt.Errorf("resolve skill %q: %w", name, err)
			}
			canonicalFile, err = filepath.Abs(canonicalFile)
			if err != nil {
				return nil, nil, fmt.Errorf("make skill %q absolute: %w", name, err)
			}
			inside, err := pathInside(root, canonicalFile)
			if err != nil {
				return nil, nil, fmt.Errorf("check skill %q containment: %w", name, err)
			}
			if !inside {
				return nil, nil, fmt.Errorf("skill %q resolves outside configured root", name)
			}
			if _, exists := seenFiles[canonicalFile]; exists {
				continue
			}
			fileInfo, err := os.Stat(canonicalFile)
			if err != nil {
				return nil, nil, fmt.Errorf("stat skill %q: %w", name, err)
			}
			if !fileInfo.Mode().IsRegular() {
				return nil, nil, fmt.Errorf("skill %q SKILL.md is not a regular file", name)
			}
			content, err := os.ReadFile(canonicalFile)
			if err != nil {
				return nil, nil, fmt.Errorf("read skill %q: %w", name, err)
			}
			parsedMetadata, body, err := parseFrontmatter(string(content))
			if err != nil {
				return nil, nil, fmt.Errorf("parse skill %q: %w", name, err)
			}
			if parsedMetadata.description == "" {
				parsedMetadata.description = descriptionFromBody(body)
			}
			canonicalDirectory := filepath.Dir(canonicalFile)
			entries[name] = entry{
				name:      name,
				directory: canonicalDirectory,
				body:      body,
				metadata:  parsedMetadata,
			}
			seenFiles[canonicalFile] = struct{}{}
		}
	}

	summaries := make([]Summary, 0, len(entries))
	for _, skillEntry := range entries {
		if skillEntry.metadata.disableModelInvocation || skillEntry.metadata.forked {
			continue
		}
		description := skillEntry.metadata.description
		if skillEntry.metadata.whenToUse != "" {
			if description != "" {
				description += " "
			}
			description += skillEntry.metadata.whenToUse
		}
		summaries = append(summaries, Summary{
			Name:        skillEntry.name,
			Description: description,
		})
	}
	slices.SortFunc(summaries, func(left, right Summary) int {
		return strings.Compare(left.Name, right.Name)
	})
	return entries, summaries, nil
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

func pathInside(root, path string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func descriptionFromBody(body string) string {
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		return line
	}
	return ""
}
