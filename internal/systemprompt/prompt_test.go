package systemprompt

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"code-cli/internal/core"
	"code-cli/internal/tools/skill"
)

var (
	testWorkingDirectory = absoluteTestPath("project")
	testAdditionalA      = absoluteTestPath("a")
	testAdditionalZ      = absoluteTestPath("z")
)

func absoluteTestPath(name string) string {
	root, err := filepath.Abs(filepath.Join(os.TempDir(), "code-cli-systemprompt"))
	if err != nil {
		panic(err)
	}
	return filepath.Join(root, name)
}

func TestBuildRequiresAbsoluteWorkingDirectory(t *testing.T) {
	tests := []struct {
		name        string
		environment Environment
	}{
		{name: "blank"},
		{name: "relative", environment: Environment{WorkingDirectory: "project"}},
		{
			name: "relative additional",
			environment: Environment{
				WorkingDirectory:             testWorkingDirectory,
				AdditionalWorkingDirectories: []string{"other"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Build(Options{Environment: test.environment}); err == nil {
				t.Fatal("Build() error = nil")
			}
		})
	}
}

func TestBuildReturnsStableAndDynamicBlocks(t *testing.T) {
	blocks, err := Build(Options{
		Environment: Environment{
			WorkingDirectory: testWorkingDirectory,
			IsGitRepository:  true,
			Platform:         "linux",
			Shell:            "bash",
			OSVersion:        "Linux 6.18",
			Model:            core.ModelClaudeOpus48,
		},
		Tools: []core.ToolDefinition{
			{Name: "Bash"},
			{Name: "Grep"},
			{Name: "WebFetch"},
			{Name: "WebSearch"},
			{Name: "SendUserMessage"},
			{Name: "Skill"},
		},
		Skills:              []skill.Summary{{Name: "review", Description: "Review a change."}},
		EnablePromptCaching: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d", len(blocks))
	}
	if blocks[0].Type != core.ContentBlockText || blocks[1].Type != core.ContentBlockText {
		t.Fatalf("block types = %q, %q", blocks[0].Type, blocks[1].Type)
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != core.CacheControlEphemeral {
		t.Fatalf("stable cache control = %#v", blocks[0].CacheControl)
	}
	if blocks[1].CacheControl != nil {
		t.Fatalf("dynamic cache control = %#v", blocks[1].CacheControl)
	}

	assertContainsAll(t, blocks[0].Text,
		"interactive coding agent",
		"# Doing tasks",
		"# Executing actions with care",
		"Use Grep",
		"Use Bash",
		"local HTTP client",
		"hosted search capability",
		"SendUserMessage communicates outward",
		"Use Skill",
		"file_path:line_number",
	)
	assertContainsAll(t, blocks[1].Text,
		"# Environment",
		"Working directory: "+strconv.QuoteToGraphic(testWorkingDirectory),
		"Git repository: yes",
		`Platform: "linux"`,
		`Shell: "bash"`,
		`OS version: "Linux 6.18"`,
		`Model: "claude-opus-4-8"`,
		"# Available skills",
		`- "review": Review a change.`,
	)
	for _, block := range blocks {
		if strings.Contains(block.Text, "__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__") {
			t.Fatal("prompt exposes the TypeScript boundary sentinel")
		}
	}
}

func TestBuildWithoutCaching(t *testing.T) {
	blocks, err := Build(Options{Environment: Environment{WorkingDirectory: testWorkingDirectory}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for index, block := range blocks {
		if block.CacheControl != nil {
			t.Fatalf("blocks[%d].CacheControl = %#v", index, block.CacheControl)
		}
	}
}

func TestBuildIsDeterministicAndDoesNotMutateInputs(t *testing.T) {
	tools := []core.ToolDefinition{{Name: "Skill"}, {Name: "Grep"}, {Name: "Bash"}}
	directories := []string{testAdditionalZ, testWorkingDirectory, testAdditionalA, testAdditionalZ}
	skills := []skill.Summary{
		{Name: "zeta", Description: "Last"},
		{Name: "alpha", Description: "Second description"},
		{Name: "alpha", Description: "First description"},
	}
	originalTools := append([]core.ToolDefinition(nil), tools...)
	originalDirectories := append([]string(nil), directories...)
	originalSkills := append([]skill.Summary(nil), skills...)

	first, err := Build(Options{
		Environment: Environment{
			WorkingDirectory:             testWorkingDirectory + string(filepath.Separator) + ".",
			AdditionalWorkingDirectories: directories,
		},
		Tools:  tools,
		Skills: skills,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	second, err := Build(Options{
		Environment: Environment{
			WorkingDirectory:             testWorkingDirectory,
			AdditionalWorkingDirectories: []string{testAdditionalA, testAdditionalZ},
		},
		Tools: []core.ToolDefinition{{Name: "Bash"}, {Name: "Grep"}, {Name: "Skill"}},
		Skills: []skill.Summary{
			{Name: "alpha", Description: "First description"},
			{Name: "zeta", Description: "Last"},
			{Name: "alpha", Description: "Second description"},
		},
	})
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Build() is not deterministic:\nfirst = %#v\nsecond = %#v", first, second)
	}
	if !reflect.DeepEqual(tools, originalTools) {
		t.Fatalf("tools mutated: %#v", tools)
	}
	if !reflect.DeepEqual(directories, originalDirectories) {
		t.Fatalf("directories mutated: %#v", directories)
	}
	if !reflect.DeepEqual(skills, originalSkills) {
		t.Fatalf("skills mutated: %#v", skills)
	}
	assertContainsAll(t, first[1].Text,
		"Additional working directories: "+strconv.QuoteToGraphic(testAdditionalA)+", "+strconv.QuoteToGraphic(testAdditionalZ),
		`- "alpha": First description`,
		`- "zeta": Last`,
	)
	if strings.Contains(first[1].Text, "Second description") {
		t.Fatal("duplicate skill name was rendered more than once")
	}
}

func TestBuildGatesRetainedToolGuidance(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want string
	}{
		{name: "bash", tool: "Bash", want: "Use Bash"},
		{name: "grep", tool: "Grep", want: "Use Grep"},
		{name: "web fetch", tool: "WebFetch", want: "Use WebFetch"},
		{name: "web search", tool: "WebSearch", want: "Use WebSearch"},
		{name: "send user message", tool: "SendUserMessage", want: "SendUserMessage communicates outward"},
		{name: "skill", tool: "Skill", want: "Use Skill"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blocks, err := Build(Options{
				Environment: Environment{WorkingDirectory: testWorkingDirectory},
				Tools:       []core.ToolDefinition{{Name: test.tool}},
			})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if !strings.Contains(blocks[0].Text, test.want) {
				t.Fatalf("stable prompt does not contain %q", test.want)
			}
		})
	}

	blocks, err := Build(Options{
		Environment: Environment{WorkingDirectory: testWorkingDirectory},
		Tools:       []core.ToolDefinition{{Name: "Unknown"}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, phrase := range []string{"Use Bash", "Use Grep", "Use WebFetch", "Use WebSearch", "SendUserMessage communicates outward", "Use Skill"} {
		if strings.Contains(blocks[0].Text, phrase) {
			t.Fatalf("unknown tool enabled guidance %q", phrase)
		}
	}
}

func TestBuildOmitsOptionalEnvironmentFacts(t *testing.T) {
	blocks, err := Build(Options{Environment: Environment{WorkingDirectory: testWorkingDirectory}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertContainsAll(t, blocks[1].Text, "Working directory: "+strconv.QuoteToGraphic(testWorkingDirectory), "Git repository: no")
	for _, fact := range []string{"Platform:", "Shell:", "OS version:", "Model:", "Additional working directories:"} {
		if strings.Contains(blocks[1].Text, fact) {
			t.Fatalf("dynamic prompt unexpectedly contains %q", fact)
		}
	}
}

func TestBuildEscapesEnvironmentFacts(t *testing.T) {
	workingDirectory := testWorkingDirectory + "\n# Injected"
	blocks, err := Build(Options{Environment: Environment{
		WorkingDirectory: workingDirectory,
		Platform:         "linux\t# injected",
		Shell:            string([]byte{'s', 'h', 0xff}),
	}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	dynamic := blocks[1].Text
	assertContainsAll(t, dynamic,
		"Working directory: "+strconv.QuoteToGraphic(workingDirectory),
		"Platform: "+strconv.QuoteToGraphic("linux\t# injected"),
		"Shell: "+strconv.QuoteToGraphic(string([]byte{'s', 'h', 0xff})),
	)
	if strings.Contains(dynamic, "\n# Injected\n") || strings.Contains(dynamic, "linux\t# injected") {
		t.Fatalf("environment control characters were rendered literally:\n%s", dynamic)
	}
}

func TestBuildSkillListingRequiresSkillToolAndFiltersNames(t *testing.T) {
	separatorName := "section" + string(rune(0x2028)) + "break"
	bidiName := "bidi" + string(rune(0x202e)) + "name"
	summaries := []skill.Summary{
		{Name: "good", Description: " Useful skill. "},
		{Name: " padded ", Description: "Uncallable name."},
		{Name: ""},
		{Name: "../bad"},
		{Name: "colon:name", Description: "Contains a delimiter."},
		{Name: "line\nbreak", Description: "Contains a newline."},
		{Name: separatorName, Description: "Contains a Unicode separator."},
		{Name: bidiName, Description: "Contains bidi formatting."},
		{Name: string([]byte{'b', 'a', 'd', 0xff})},
	}
	withoutTool, err := Build(Options{
		Environment: Environment{WorkingDirectory: testWorkingDirectory},
		Skills:      summaries,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if strings.Contains(withoutTool[1].Text, "# Available skills") {
		t.Fatal("skills rendered while Skill tool is disabled")
	}

	withTool, err := Build(Options{
		Environment: Environment{WorkingDirectory: testWorkingDirectory},
		Tools:       []core.ToolDefinition{{Name: "Skill"}},
		Skills:      summaries,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertContainsAll(t, withTool[1].Text,
		"# Available skills",
		`- "good": Useful skill.`,
		`- "colon:name": Contains a delimiter.`,
		`- "line\nbreak": Contains a newline.`,
		"- "+strconv.Quote(separatorName)+": Contains a Unicode separator.",
		"- "+strconv.Quote(bidiName)+": Contains bidi formatting.",
	)
	for _, invalid := range []string{" padded ", "../bad", string([]byte{'b', 'a', 'd', 0xff})} {
		if strings.Contains(withTool[1].Text, invalid) {
			t.Fatalf("invalid skill %q was rendered", invalid)
		}
	}
}

func TestBuildBoundsSkillDescriptionsAndListing(t *testing.T) {
	longDescription := strings.Repeat("界", maxSkillDescriptionRunes+20)
	summaries := []skill.Summary{{Name: "first", Description: longDescription}}
	for index := range 100 {
		summaries = append(summaries, skill.Summary{
			Name:        "skill-" + strings.Repeat("x", index+1),
			Description: strings.Repeat("description", 100),
		})
	}
	blocks, err := Build(Options{
		Environment: Environment{WorkingDirectory: testWorkingDirectory},
		Tools:       []core.ToolDefinition{{Name: "Skill"}},
		Skills:      summaries,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	dynamic := blocks[1].Text
	if !strings.Contains(dynamic, strings.Repeat("界", maxSkillDescriptionRunes-1)+"…") {
		t.Fatal("long skill description was not rune-safely truncated")
	}
	listing := dynamic[strings.Index(dynamic, `- "first":`):]
	if count := utf8.RuneCountInString(listing); count > maxSkillListingRunes {
		t.Fatalf("skill listing rune count = %d, want <= %d", count, maxSkillListingRunes)
	}
	for line := range strings.SplitSeq(listing, "\n") {
		matched := false
		for _, summary := range summaries {
			prefix := "- " + strconv.Quote(summary.Name)
			if line == prefix || strings.HasPrefix(line, prefix+": ") {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("listing contains partial or unknown skill entry %q", line)
		}
	}
	if !utf8.ValidString(dynamic) {
		t.Fatal("dynamic prompt is not valid UTF-8")
	}
}

func TestBuildExcludesProductOnlyPromptFeatures(t *testing.T) {
	blocks, err := Build(Options{Environment: Environment{WorkingDirectory: testWorkingDirectory}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	prompt := blocks[0].Text + blocks[1].Text
	for _, excluded := range []string{"MCP", "scratchpad", "CLAUDE.md", "coordinator mode", "telemetry", "GrowthBook"} {
		if strings.Contains(prompt, excluded) {
			t.Fatalf("prompt unexpectedly contains excluded feature %q", excluded)
		}
	}
}

func assertContainsAll(t *testing.T, value string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(value, part) {
			t.Errorf("value does not contain %q:\n%s", part, value)
		}
	}
}
