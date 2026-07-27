package skills

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// PromptBuilder constructs bundled skill instructions from raw arguments.
type PromptBuilder func(context.Context, string) (string, error)

// BundledDefinition defines one skill compiled or registered by a host.
type BundledDefinition struct {
	Name          string
	Aliases       []string
	Description   string
	Metadata      Metadata
	UserInvocable *bool
	Prompt        string
	BuildPrompt   PromptBuilder
	Files         map[string]string
	Enabled       func() bool
}

// BundledContent is the immutable prompt implementation attached to a definition.
type BundledContent struct {
	promptText string
	builder    PromptBuilder
	extractor  *bundledExtractor
}

// BundledRegistry stores programmatically supplied standalone skills.
type BundledRegistry struct {
	mu          sync.RWMutex
	cacheRoot   string
	processRoot string
	definitions []BundledDefinition
	names       map[string]string
	extractors  map[string]*bundledExtractor
}

// NewBundledRegistry creates an empty bundled registry.
func NewBundledRegistry(cacheRoot string) (*BundledRegistry, error) {
	if strings.TrimSpace(cacheRoot) == "" {
		return nil, errors.New("bundled skill cache root is empty")
	}
	absolute, err := filepath.Abs(cacheRoot)
	if err != nil {
		return nil, fmt.Errorf("make bundled skill cache root absolute: %w", err)
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate bundled skill cache nonce: %w", err)
	}
	return &BundledRegistry{
		cacheRoot:   absolute,
		processRoot: filepath.Join(absolute, hex.EncodeToString(nonceBytes)),
		names:       make(map[string]string),
		extractors:  make(map[string]*bundledExtractor),
	}, nil
}

// Register adds one bundled definition. Names and aliases are unique.
func (registry *BundledRegistry) Register(definition BundledDefinition) error {
	if registry == nil {
		return errors.New("bundled skill registry is nil")
	}
	definition.Name = strings.TrimSpace(definition.Name)
	if err := validateCanonicalName(definition.Name); err != nil {
		return err
	}
	if definition.Prompt == "" && definition.BuildPrompt == nil {
		return fmt.Errorf("bundled skill %q has no prompt", definition.Name)
	}
	if definition.Prompt != "" && definition.BuildPrompt != nil {
		return fmt.Errorf("bundled skill %q has both static and dynamic prompts", definition.Name)
	}
	definition.Metadata = cloneMetadata(definition.Metadata)
	if definition.Metadata.Context == "" {
		definition.Metadata.Context = "inline"
	}
	definition.Metadata.UserInvocable = true
	if definition.UserInvocable != nil {
		definition.Metadata.UserInvocable = *definition.UserInvocable
	}
	if definition.Metadata.Description == "" {
		definition.Metadata.Description = strings.TrimSpace(definition.Description)
	}
	definition.Aliases = append([]string(nil), definition.Aliases...)
	definition.Files = cloneStringMap(definition.Files)

	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, candidate := range append([]string{definition.Name}, definition.Aliases...) {
		if err := validateCanonicalName(candidate); err != nil {
			return fmt.Errorf("bundled skill %q alias: %w", definition.Name, err)
		}
		if owner, exists := registry.names[candidate]; exists {
			return fmt.Errorf("bundled skill name or alias %q collides with %q", candidate, owner)
		}
	}
	for _, candidate := range append([]string{definition.Name}, definition.Aliases...) {
		registry.names[candidate] = definition.Name
	}
	registry.definitions = append(registry.definitions, definition)
	if len(definition.Files) > 0 {
		registry.extractors[definition.Name] = &bundledExtractor{
			root:  filepath.Join(registry.processRoot, definition.Name),
			files: cloneStringMap(definition.Files),
		}
	}
	return nil
}

// DefaultBundledMetadata returns the upstream-style invocation defaults.
func DefaultBundledMetadata() Metadata {
	return Metadata{UserInvocable: true, Context: "inline"}
}

// Definitions returns defensive copies in registration order.
func (registry *BundledRegistry) Definitions() []BundledDefinition {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]BundledDefinition, len(registry.definitions))
	for index, definition := range registry.definitions {
		result[index] = cloneBundledDefinition(definition)
	}
	return result
}

func (registry *BundledRegistry) loadedDefinitions() []loadedDefinition {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	definitions := make([]BundledDefinition, len(registry.definitions))
	extractors := make(map[string]*bundledExtractor, len(registry.extractors))
	for index, definition := range registry.definitions {
		definitions[index] = cloneBundledDefinition(definition)
	}
	maps.Copy(extractors, registry.extractors)
	registry.mu.RUnlock()

	result := make([]loadedDefinition, 0, len(definitions))
	for _, bundled := range definitions {
		if bundled.Enabled != nil && !bundled.Enabled() {
			continue
		}
		metadata := cloneMetadata(bundled.Metadata)
		if metadata.Description == "" {
			metadata.Description = bundled.Description
		}
		content := &BundledContent{
			promptText: bundled.Prompt,
			builder:    bundled.BuildPrompt,
			extractor:  extractors[bundled.Name],
		}
		definition := Definition{
			Name:     bundled.Name,
			Aliases:  append([]string(nil), bundled.Aliases...),
			Source:   SourceBundled,
			Body:     bundled.Prompt,
			Metadata: metadata,
			Bundled:  content,
		}
		result = append(result, loadedDefinition{definition: definition, identity: "bundled:" + bundled.Name})
	}
	return result
}

func (content *BundledContent) prompt(ctx context.Context, args *string) (string, string, error) {
	if content == nil {
		return "", "", errors.New("bundled skill content is nil")
	}
	prompt := content.promptText
	if content.builder != nil {
		rawArgs := ""
		if args != nil {
			rawArgs = *args
		}
		var err error
		prompt, err = content.builder(ctx, rawArgs)
		if err != nil {
			return "", "", err
		}
	}
	directory := ""
	if content.extractor != nil {
		var err error
		directory, err = content.extractor.extract()
		if err != nil {
			return "", "", err
		}
	}
	return prompt, directory, nil
}

func cloneBundledDefinition(definition BundledDefinition) BundledDefinition {
	definition.Aliases = append([]string(nil), definition.Aliases...)
	definition.Metadata = cloneMetadata(definition.Metadata)
	definition.Files = cloneStringMap(definition.Files)
	if definition.UserInvocable != nil {
		value := *definition.UserInvocable
		definition.UserInvocable = &value
	}
	return definition
}

func cloneBundledContent(content BundledContent) BundledContent {
	return BundledContent{promptText: content.promptText, builder: content.builder, extractor: content.extractor}
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	maps.Copy(result, value)
	return result
}

func validateCanonicalName(name string) error {
	if name == "" || name == "." || name == ".." || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid skill name %q", name)
	}
	return nil
}

func sortedBundledFileNames(files map[string]string) []string {
	return slices.Sorted(maps.Keys(files))
}
