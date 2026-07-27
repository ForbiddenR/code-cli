// Package systemprompt builds the retained coding-agent system prompt.
package systemprompt

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"code-cli/internal/core"
	"code-cli/internal/skills"
)

const (
	maxSkillDescriptionRunes = 500
	maxSkillListingRunes     = 8_000
)

// Environment contains host-observed facts rendered into the dynamic prompt.
type Environment struct {
	WorkingDirectory             string
	AdditionalWorkingDirectories []string
	IsGitRepository              bool
	Platform                     string
	Shell                        string
	OSVersion                    string
	Model                        core.ModelID
}

// Options controls deterministic system-prompt construction.
type Options struct {
	Environment         Environment
	Tools               []core.ToolDefinition
	Skills              []skills.Summary
	EnablePromptCaching bool
}

// Build returns the stable policy block followed by dynamic environment context.
func Build(options Options) ([]core.SystemBlock, error) {
	environment, err := normalizeEnvironment(options.Environment)
	if err != nil {
		return nil, err
	}

	enabledTools := enabledToolNames(options.Tools)
	stable := core.TextSystemBlock(buildStablePrompt(enabledTools))
	if options.EnablePromptCaching {
		stable.CacheControl = &core.CacheControl{Type: core.CacheControlEphemeral}
	}

	dynamic := core.TextSystemBlock(buildDynamicPrompt(environment, enabledTools, options.Skills))
	return []core.SystemBlock{stable, dynamic}, nil
}

func normalizeEnvironment(environment Environment) (Environment, error) {
	workingDirectory := environment.WorkingDirectory
	if strings.TrimSpace(workingDirectory) == "" {
		return Environment{}, errors.New("working directory is required")
	}
	if !filepath.IsAbs(workingDirectory) {
		return Environment{}, fmt.Errorf("working directory %q is not absolute", environment.WorkingDirectory)
	}
	workingDirectory = filepath.Clean(workingDirectory)

	seen := map[string]struct{}{workingDirectory: {}}
	additional := make([]string, 0, len(environment.AdditionalWorkingDirectories))
	for _, directory := range environment.AdditionalWorkingDirectories {
		if strings.TrimSpace(directory) == "" {
			continue
		}
		if !filepath.IsAbs(directory) {
			return Environment{}, fmt.Errorf("additional working directory %q is not absolute", directory)
		}
		directory = filepath.Clean(directory)
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		additional = append(additional, directory)
	}
	slices.Sort(additional)

	return Environment{
		WorkingDirectory:             workingDirectory,
		AdditionalWorkingDirectories: additional,
		IsGitRepository:              environment.IsGitRepository,
		Platform:                     strings.TrimSpace(environment.Platform),
		Shell:                        strings.TrimSpace(environment.Shell),
		OSVersion:                    strings.TrimSpace(environment.OSVersion),
		Model:                        core.ModelID(strings.TrimSpace(environment.Model.String())),
	}, nil
}

func enabledToolNames(definitions []core.ToolDefinition) map[string]struct{} {
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func buildStablePrompt(enabledTools map[string]struct{}) string {
	sections := []string{
		`You are an interactive coding agent that helps users with software engineering tasks. Use the available tools when they are needed to inspect, change, or verify the user's code.`,
		`# Doing tasks

- Read and understand relevant existing code before proposing or making changes.
- Implement only what the user requested. Do not add speculative features, compatibility layers, abstractions, comments, or validation without a concrete need.
- Prefer the simplest complete implementation that matches the surrounding code's naming, structure, and style.
- Validate at system boundaries such as user input, files, processes, and external APIs. Avoid introducing command injection, path traversal, XSS, SQL injection, or other security vulnerabilities.
- Diagnose failures before changing approach; do not blindly repeat an identical failed action.
- Verify completed work with the available tests, builds, or direct checks. Report failures and skipped checks faithfully, and state successful results plainly.`,
		`# Executing actions with care

Freely perform local, reversible work within the user's request. Before an action that is destructive, hard to reverse, externally visible, or changes shared state, confirm with the user unless they explicitly or durably authorized that action. Approval applies only to the stated scope. Investigate unexpected files, processes, branches, or configuration before deleting or overwriting them. Never use destructive actions merely to bypass an obstacle or safety check.`,
	}

	if toolSection := buildToolSection(enabledTools); toolSection != "" {
		sections = append(sections, toolSection)
	}

	sections = append(sections, `# Tone and style

Keep user-facing text concise, direct, and accurate. Lead with the answer, action, result, or blocker rather than a long preamble. Use GitHub-flavored Markdown when useful. When referencing code, use file_path:line_number so the user can navigate to it. Do not use emojis unless the user asks.`)
	return strings.Join(sections, "\n\n")
}

func buildToolSection(enabledTools map[string]struct{}) string {
	items := make([]string, 0, 6)
	_, hasBash := enabledTools["Bash"]
	_, hasGrep := enabledTools["Grep"]

	if hasGrep {
		items = append(items, "Use Grep rather than invoking grep or ripgrep through Bash when searching file contents.")
	}
	if hasBash {
		guidance := "Use Bash for shell commands and terminal operations."
		if hasGrep {
			guidance += " Prefer the dedicated Grep tool for content searches."
		}
		guidance += " If a Bash action is denied, adjust the approach instead of retrying the identical command."
		items = append(items, guidance)
	}
	if _, exists := enabledTools["WebFetch"]; exists {
		items = append(items, "Use WebFetch to retrieve a specific URL through the host's local HTTP client and apply its prompt to the fetched content. Do not describe it as an Anthropic-hosted fetch tool.")
	}
	if _, exists := enabledTools["WebSearch"]; exists {
		items = append(items, "Use WebSearch to search the public web through Anthropic's hosted search capability; use WebFetch when a specific URL is already known.")
	}
	if _, exists := enabledTools["SendUserMessage"]; exists {
		items = append(items, "SendUserMessage communicates outward to the user. Put the complete message in the tool input and confirm first when sending was not already requested or authorized.")
	}
	if _, exists := enabledTools["Skill"]; exists {
		items = append(items, "Use Skill to load a configured local skill from the available-skills list. Invoke exact listed names only; never guess skills or use it for built-in CLI commands.")
	}
	if len(items) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("# Using tools")
	for _, item := range items {
		builder.WriteString("\n\n- ")
		builder.WriteString(item)
	}
	return builder.String()
}

func buildDynamicPrompt(environment Environment, enabledTools map[string]struct{}, summaries []skills.Summary) string {
	var builder strings.Builder
	builder.WriteString("# Environment")
	writeFact(&builder, "Working directory", environment.WorkingDirectory)
	if environment.IsGitRepository {
		writeRawFact(&builder, "Git repository", "yes")
	} else {
		writeRawFact(&builder, "Git repository", "no")
	}
	if len(environment.AdditionalWorkingDirectories) > 0 {
		quotedDirectories := make([]string, len(environment.AdditionalWorkingDirectories))
		for index, directory := range environment.AdditionalWorkingDirectories {
			quotedDirectories[index] = strconv.QuoteToGraphic(directory)
		}
		writeRawFact(&builder, "Additional working directories", strings.Join(quotedDirectories, ", "))
	}
	writeOptionalFact(&builder, "Platform", environment.Platform)
	writeOptionalFact(&builder, "Shell", environment.Shell)
	writeOptionalFact(&builder, "OS version", environment.OSVersion)
	writeOptionalFact(&builder, "Model", environment.Model.String())

	if _, enabled := enabledTools["Skill"]; enabled {
		if listing := buildSkillListing(summaries); listing != "" {
			builder.WriteString("\n\n# Available skills\n\n")
			builder.WriteString("Use Skill with an exact name listed below. Names are Go-quoted so control and delimiter characters are unambiguous; pass the decoded name to Skill. One leading / is accepted as user shorthand. Do not guess names or invoke built-in CLI commands through Skill.\n\n")
			builder.WriteString(listing)
		}
	}
	return builder.String()
}

func writeFact(builder *strings.Builder, name, value string) {
	writeRawFact(builder, name, strconv.QuoteToGraphic(value))
}

func writeRawFact(builder *strings.Builder, name, value string) {
	builder.WriteString("\n- ")
	builder.WriteString(name)
	builder.WriteString(": ")
	builder.WriteString(value)
}

func writeOptionalFact(builder *strings.Builder, name, value string) {
	if value != "" {
		writeFact(builder, name, value)
	}
}

func buildSkillListing(summaries []skills.Summary) string {
	normalized := make([]skills.Summary, 0, len(summaries))
	for _, summary := range summaries {
		name := summary.Name
		if name != strings.TrimSpace(name) || !validSkillName(name) {
			continue
		}
		normalized = append(normalized, skills.Summary{
			Name:        name,
			Description: strings.TrimSpace(summary.Description),
		})
	}
	slices.SortFunc(normalized, func(left, right skills.Summary) int {
		if compared := strings.Compare(left.Name, right.Name); compared != 0 {
			return compared
		}
		return strings.Compare(left.Description, right.Description)
	})

	seen := make(map[string]struct{}, len(normalized))
	var builder strings.Builder
	usedRunes := 0
	for _, summary := range normalized {
		if _, exists := seen[summary.Name]; exists {
			continue
		}
		seen[summary.Name] = struct{}{}

		prefix := "- " + strconv.Quote(summary.Name)
		if builder.Len() > 0 {
			prefix = "\n" + prefix
		}
		prefixRunes := utf8.RuneCountInString(prefix)
		remaining := maxSkillListingRunes - usedRunes
		if prefixRunes > remaining {
			break
		}
		builder.WriteString(prefix)
		usedRunes += prefixRunes

		if summary.Description == "" {
			continue
		}
		remaining = maxSkillListingRunes - usedRunes
		if remaining <= 2 {
			continue
		}
		descriptionLimit := min(maxSkillDescriptionRunes, remaining-2)
		description := truncateRunes(summary.Description, descriptionLimit)
		builder.WriteString(": ")
		builder.WriteString(description)
		usedRunes += 2 + utf8.RuneCountInString(description)
	}
	return builder.String()
}

func validSkillName(name string) bool {
	return name != "" && name != "." && name != ".." && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\\x00")
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	const marker = "…"
	if limit == 1 {
		return marker
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + marker
}
