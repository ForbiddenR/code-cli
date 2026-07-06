package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const defaultAPIDir = "claude-code-source/src/services/api"

// Options controls the Phase 0 audit scanner.
type Options struct {
	// RepoRoot is the repository root containing claude-code-source. If empty,
	// Run auto-detects it from the current working directory and its parents.
	RepoRoot string

	// APIDir is the API service directory relative to RepoRoot.
	// It defaults to claude-code-source/src/services/api.
	APIDir string
}

// Report is the complete Phase 0 audit result.
type Report struct {
	RepoRoot string         `json:"repo_root"`
	APIDir   string         `json:"api_dir"`
	Files    []string       `json:"files"`
	Exports  []ExportReport `json:"exports"`
	Warnings []string       `json:"warnings,omitempty"`
}

// ExportReport combines one exported symbol with its external callers.
type ExportReport struct {
	Export         Export   `json:"export"`
	Callers        []Caller `json:"callers"`
	Recommendation string   `json:"recommendation"`
}

// Export describes an exported TypeScript symbol in services/api.
type Export struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// Caller describes references to an exported symbol from outside services/api.
type Caller struct {
	File  string `json:"file"`
	Lines []int  `json:"lines"`
}

var (
	declExportRE  = regexp.MustCompile(`^\s*export\s+(?:declare\s+)?(?:async\s+)?(function|const|let|var|class|type|interface|enum)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	namedExportRE = regexp.MustCompile(`^\s*export\s*\{([^}]+)\}`)
	defaultRE     = regexp.MustCompile(`^\s*export\s+default\b`)
)

// Run audits services/api exports and finds references outside that directory.
func Run(opts Options) (*Report, error) {
	apiDir := opts.APIDir
	if apiDir == "" {
		apiDir = defaultAPIDir
	}

	repoRoot, err := resolveRepoRoot(opts.RepoRoot, apiDir)
	if err != nil {
		return nil, err
	}

	apiAbs := filepath.Join(repoRoot, filepath.FromSlash(apiDir))
	info, err := os.Stat(apiAbs)
	if err != nil {
		return nil, fmt.Errorf("stat API directory %q: %w", apiAbs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("API path %q is not a directory", apiAbs)
	}

	apiFiles, err := collectSourceFiles(apiAbs, nil)
	if err != nil {
		return nil, err
	}

	var warnings []string
	exports, parseWarnings := parseExports(repoRoot, apiFiles)
	warnings = append(warnings, parseWarnings...)

	searchFiles, err := collectSourceFiles(repoRoot, func(path string, entry fs.DirEntry) bool {
		if entry.IsDir() {
			return shouldSkipDir(path, entry.Name())
		}
		return strings.HasPrefix(path, apiAbs+string(os.PathSeparator))
	})
	if err != nil {
		return nil, err
	}

	callersByName, err := findCallers(repoRoot, searchFiles, exports)
	if err != nil {
		return nil, err
	}

	reports := make([]ExportReport, 0, len(exports))
	for _, exp := range exports {
		callers := callersByName[exp.Name]
		reports = append(reports, ExportReport{
			Export:         exp,
			Callers:        callers,
			Recommendation: recommendationFor(exp, callers),
		})
	}

	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Export.File == reports[j].Export.File {
			return reports[i].Export.Line < reports[j].Export.Line
		}
		return reports[i].Export.File < reports[j].Export.File
	})

	files := make([]string, 0, len(apiFiles))
	for _, file := range apiFiles {
		files = append(files, relSlash(repoRoot, file))
	}
	sort.Strings(files)

	return &Report{
		RepoRoot: repoRoot,
		APIDir:   filepath.ToSlash(apiDir),
		Files:    files,
		Exports:  reports,
		Warnings: warnings,
	}, nil
}

// Markdown renders the audit as the compatibility matrix requested by Phase 0.
func (r *Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Phase 0 API Audit\n\n")
	b.WriteString(fmt.Sprintf("Repository root: `%s`\n\n", r.RepoRoot))
	b.WriteString(fmt.Sprintf("API directory: `%s`\n\n", r.APIDir))
	b.WriteString(fmt.Sprintf("Scanned API files: %d\n\n", len(r.Files)))

	if len(r.Warnings) > 0 {
		b.WriteString("## Warnings\n\n")
		for _, warning := range r.Warnings {
			b.WriteString(fmt.Sprintf("- %s\n", warning))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Compatibility Matrix\n\n")
	b.WriteString("| Existing export | Kind | Defined at | External callers | Recommendation |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")

	if len(r.Exports) == 0 {
		b.WriteString("| _No exports found_ |  |  |  |  |\n")
		return b.String()
	}

	for _, item := range r.Exports {
		definedAt := fmt.Sprintf("`%s:%d`", item.Export.File, item.Export.Line)
		b.WriteString(fmt.Sprintf(
			"| `%s` | %s | %s | %s | %s |\n",
			escapeTable(item.Export.Name),
			escapeTable(item.Export.Kind),
			definedAt,
			escapeTable(formatCallers(item.Callers)),
			escapeTable(item.Recommendation),
		))
	}

	return b.String()
}

// JSON renders the audit as indented JSON.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func resolveRepoRoot(repoRoot string, apiDir string) (string, error) {
	if repoRoot != "" {
		abs, err := filepath.Abs(repoRoot)
		if err != nil {
			return "", err
		}
		return abs, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	current := wd
	for {
		candidate := filepath.Join(current, filepath.FromSlash(apiDir))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return "", fmt.Errorf("could not find %q from %q or its parents", apiDir, wd)
}

func collectSourceFiles(root string, skip func(path string, entry fs.DirEntry) bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip != nil && skip(path, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if isSourceFile(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func shouldSkipDir(_ string, name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "build", "coverage", ".next", ".turbo", ".claude":
		return true
	default:
		return false
	}
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		return true
	default:
		return false
	}
}

func parseExports(repoRoot string, files []string) ([]Export, []string) {
	seen := map[string]bool{}
	var exports []Export
	var warnings []string

	for _, file := range files {
		parsed, err := parseExportsFromFile(repoRoot, file)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		for _, exp := range parsed {
			key := fmt.Sprintf("%s:%s:%d", exp.File, exp.Name, exp.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			exports = append(exports, exp)
		}
	}

	return exports, warnings
}

func parseExportsFromFile(repoRoot string, file string) ([]Export, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	var exports []Export
	scanner := bufio.NewScanner(handle)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		relFile := relSlash(repoRoot, file)

		if match := declExportRE.FindStringSubmatch(line); len(match) == 3 {
			exports = append(exports, Export{Name: match[2], Kind: match[1], File: relFile, Line: lineNo})
			continue
		}

		if match := namedExportRE.FindStringSubmatch(line); len(match) == 2 {
			for _, name := range parseNamedExportList(match[1]) {
				exports = append(exports, Export{Name: name, Kind: "re-export", File: relFile, Line: lineNo})
			}
			continue
		}

		if defaultRE.MatchString(line) {
			exports = append(exports, Export{Name: "default", Kind: "default", File: relFile, Line: lineNo})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return exports, nil
}

func parseNamedExportList(raw string) []string {
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "type ") && strings.TrimSpace(strings.TrimPrefix(part, "type ")) == "" {
			continue
		}
		part = strings.TrimPrefix(part, "type ")
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if len(fields) >= 3 && fields[1] == "as" {
			names = append(names, fields[2])
			continue
		}
		names = append(names, fields[0])
	}
	return names
}

func findCallers(repoRoot string, files []string, exports []Export) (map[string][]Caller, error) {
	if len(exports) == 0 {
		return map[string][]Caller{}, nil
	}

	patterns := map[string]*regexp.Regexp{}
	for _, exp := range exports {
		if exp.Name == "default" {
			continue
		}
		if _, ok := patterns[exp.Name]; !ok {
			patterns[exp.Name] = regexp.MustCompile(`\b` + regexp.QuoteMeta(exp.Name) + `\b`)
		}
	}

	result := map[string][]Caller{}
	for _, file := range files {
		matches, err := findMatchesInFile(file, patterns)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			continue
		}
		rel := relSlash(repoRoot, file)
		for name, lines := range matches {
			result[name] = append(result[name], Caller{File: rel, Lines: lines})
		}
	}

	for name := range result {
		sort.Slice(result[name], func(i, j int) bool {
			return result[name][i].File < result[name][j].File
		})
	}

	return result, nil
}

func findMatchesInFile(file string, patterns map[string]*regexp.Regexp) (map[string][]int, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	matches := map[string][]int{}
	scanner := bufio.NewScanner(handle)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		for name, pattern := range patterns {
			if pattern.MatchString(line) {
				matches[name] = append(matches[name], lineNo)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}

func recommendationFor(exp Export, callers []Caller) string {
	if exp.Kind == "default" {
		return "inspect manually; default export usage cannot be mapped by symbol name"
	}
	if len(callers) == 0 {
		return "candidate for removal or internal-only rewrite"
	}
	if exp.Kind == "type" || exp.Kind == "interface" {
		return "keep contract until callers migrate to new core/API types"
	}
	return "keep compatibility shim, then migrate callers"
}

func formatCallers(callers []Caller) string {
	if len(callers) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(callers))
	for _, caller := range callers {
		parts = append(parts, fmt.Sprintf("%s:%s", caller.File, compactLines(caller.Lines)))
	}
	return strings.Join(parts, "<br>")
}

func compactLines(lines []int) string {
	if len(lines) == 0 {
		return ""
	}
	const maxLines = 8
	items := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == maxLines {
			items = append(items, fmt.Sprintf("+%d more", len(lines)-maxLines))
			break
		}
		items = append(items, fmt.Sprintf("%d", line))
	}
	return strings.Join(items, ",")
}

func escapeTable(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func relSlash(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// ErrUnsupportedFormat is returned by command code for unknown output formats.
var ErrUnsupportedFormat = errors.New("unsupported output format")
