// Command szmelc-router is the single binary in front of every free and
// low-cost LLM source: CLI, OpenAI-compatible server and agentic task router.
package main

import (
	"os"

	"github.com/CYBERSZP0NT/szmelc-router/internal/cli"
)

// Build info, set by -ldflags (see Makefile).
var (
	version = "dev"
	commit  = "none"
	date    = ""
)

func main() {
	cli.SetVersionInfo(version, commit, date)
	os.Exit(cli.Execute(os.Args[1:]))
}
