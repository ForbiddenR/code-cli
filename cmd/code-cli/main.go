package main

import (
	"fmt"
	"os"

	"code-cli/internal/session"
	"code-cli/internal/tui"
)

func main() {
	if err := tui.Run(session.New()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "run code-cli: %v\n", err)
		os.Exit(1)
	}
}
