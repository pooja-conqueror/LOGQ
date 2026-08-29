// Command logq queries log files with a one-line filter/aggregate expression.
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

// Exit codes, per the spec's error model (GRAMMAR.md will document these in full
// once the error model lands in Phase 10).
const (
	exitOK          = 0
	exitUsage       = 1
	exitCompile     = 2
	exitDataStrict  = 3
	exitIO          = 4
	exitInterrupted = 130
)

const usageText = `logq - query gigabytes of logs with a one-line expression

Usage:
  logq [flags] 'QUERY' [FILE|- ...]

Flags:
  -h, --help       show this help and exit
      --version    show version and exit

logq is under active development; most flags are not wired yet.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the CLI stub. Real flag parsing, query compilation, and the
// filter/render pipeline land in Phase 4 (commit 14) onward.
func run(args []string) int {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Fprint(os.Stdout, usageText)
			return exitOK
		case "--version":
			fmt.Fprintf(os.Stdout, "logq %s\n", version)
			return exitOK
		}
	}
	fmt.Fprintln(os.Stderr, "logq: not yet implemented")
	return exitUsage
}
