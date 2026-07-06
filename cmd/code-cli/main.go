package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"code-cli/internal/audit"
)

func main() {
	repoRoot := flag.String("repo", "", "repository root containing claude-code-source; auto-detected when empty")
	apiDir := flag.String("api-dir", "claude-code-source/src/services/api", "API service directory relative to repo root")
	format := flag.String("format", "markdown", "output format: markdown or json")
	flag.Parse()

	report, err := audit.Run(audit.Options{
		RepoRoot: *repoRoot,
		APIDir:   *apiDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "code-cli: %v\n", err)
		os.Exit(1)
	}

	switch strings.ToLower(strings.TrimSpace(*format)) {
	case "markdown", "md":
		fmt.Print(report.Markdown())
	case "json":
		data, err := report.JSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "code-cli: render json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "code-cli: %v %q\n", audit.ErrUnsupportedFormat, *format)
		os.Exit(2)
	}
}
