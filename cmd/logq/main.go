// Command logq queries log files with a one-line filter/aggregate expression.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/formats"
	"github.com/pooja-conqueror/LOGQ/internal/logfmtx"
	"github.com/pooja-conqueror/LOGQ/internal/query"
	"github.com/pooja-conqueror/LOGQ/internal/render"
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
  -f, --format FORMAT   input format: auto|jsonl|logfmt|plain (default auto —
                         sampled from the first 64 non-empty lines of each
                         source independently). Gzip-compressed sources are
                         detected and decompressed transparently regardless
                         of --format.
  -o, --output FORMAT   output format: raw|jsonl (default raw — table/csv
                         land in Phase 7)
  -h, --help             show this help and exit
      --version          show version and exit

Results go to stdout only; diagnostics and the end-of-run summary go to
stderr. logq is under active development — see README.md's Honest Limits
section for what isn't wired up yet.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the whole CLI, parameterized on its I/O so it's testable without
// spawning a subprocess (see main_test.go).
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logq", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we print our own usage/errors, not flag's

	format := fs.String("f", "auto", "input format: auto|jsonl|logfmt|plain")
	fs.StringVar(format, "format", "auto", "input format: auto|jsonl|logfmt|plain")
	output := fs.String("o", "raw", "output format: raw|jsonl")
	fs.StringVar(output, "output", "raw", "output format: raw|jsonl")
	help := fs.Bool("h", false, "show help")
	fs.BoolVar(help, "help", false, "show help")
	showVersion := fs.Bool("version", false, "show version")

	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	if *showVersion {
		fmt.Fprintf(stdout, "logq %s\n", version)
		return exitOK
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "logq: missing QUERY argument")
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	queryText, files := rest[0], rest[1:]

	switch *format {
	case "auto", "jsonl", "logfmt", "plain":
	default:
		fmt.Fprintf(stderr, "logq: --format %q not recognized (want auto|jsonl|logfmt|plain)\n", *format)
		return exitUsage
	}
	if *output != "raw" && *output != "jsonl" {
		fmt.Fprintf(stderr, "logq: --output %q not yet supported (only raw/jsonl exist so far)\n", *output)
		return exitUsage
	}

	// Query compilation happens entirely before any I/O — a bad query
	// never even opens a file (§12.3: "Query compile fail: exit 2 before
	// any I/O").
	q, err := query.ParseQuery(queryText)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitCompile
	}
	cf, err := eval.Compile(q.Filter)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitCompile
	}

	sources, closeAll, err := openSources(files, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitIO
	}
	defer closeAll()

	// Frozen once, before any record is read — this is what batch-mode
	// determinism (§15) and the timestamp±duration coercion both depend
	// on. Watch mode (Phase 9) re-evaluates this per poll tick instead.
	now := time.Now()

	out := bufio.NewWriter(stdout)
	defer out.Flush()

	var totalLines, malformed int
	for _, src := range sources {
		n, m, srcErr := processSource(out, cf, now, src, *format, *output)
		totalLines += n
		malformed += m
		if srcErr != nil {
			fmt.Fprintf(stderr, "logq: %v\n", srcErr)
			return exitIO
		}
	}

	// Flush results before the diagnostic summary, so on a terminal the
	// matched records appear before the line that describes them.
	if err := out.Flush(); err != nil {
		fmt.Fprintf(stderr, "logq: write error: %v\n", err)
		return exitIO
	}
	if malformed > 0 {
		// A minimal placeholder for Phase 10's full per-field counter
		// summary (internal/summarize) — line/malformed counts only, but
		// still honest: a malformed line is never silently dropped
		// without any trace.
		fmt.Fprintf(stderr, "logq: %d line(s) read, %d malformed and skipped\n", totalLines, malformed)
	}
	return exitOK
}

// processSource fully processes one source: transparent gzip unwrap, line
// splitting, format detection (or the forced --format), filtering, and
// rendering. It returns how many lines it read and how many were
// malformed for THIS source — detection and its "auto" sampling are
// per-source (§9.2: "Detection cached per file"), never shared across
// multiple files in one run. A non-nil err is a fatal read/write failure;
// the caller stops the whole run on it (exit 4).
func processSource(out io.Writer, cf *eval.CompiledFilter, now time.Time, src io.Reader, forcedFormat, output string) (linesRead, malformed int, err error) {
	gzr, err := formats.MaybeGunzip(src)
	if err != nil {
		return 0, 0, err
	}
	lr := formats.NewLineReader(gzr, 0)

	var srcFormat formats.Format
	var sample [][]byte
	switch forcedFormat {
	case "jsonl":
		srcFormat = formats.FormatJSONL
	case "logfmt":
		srcFormat = formats.FormatLogfmt
	case "plain":
		srcFormat = formats.FormatPlain
	default: // "auto"
		detected, s, detErr := formats.DetectFromReader(lr)
		if detErr != nil {
			return 0, 0, detErr
		}
		srcFormat, sample = detected, s
	}

	process := func(line []byte) error {
		linesRead++
		matched, werr := processLine(out, cf, now, line, srcFormat, output)
		if werr != nil {
			return werr
		}
		if !matched {
			malformed++
		}
		return nil
	}

	// The sample lines were already consumed from lr during auto-detection
	// (or is empty, when forcedFormat skipped detection entirely) — they
	// must be processed before reading any further, or they'd be lost.
	for _, line := range sample {
		if werr := process(line); werr != nil {
			return linesRead, malformed, werr
		}
	}
	for {
		line, lerr := lr.ReadLine()
		if line != nil {
			if werr := process(line); werr != nil {
				return linesRead, malformed, werr
			}
		}
		if lerr != nil {
			if lerr != io.EOF {
				return linesRead, malformed, lerr
			}
			break
		}
	}

	return linesRead, malformed, nil
}

// processLine decodes one line under the given format, evaluates the
// compiled filter, and renders it if it matches. ok is false only when the
// line failed to decode (malformed) — a line that decodes fine but simply
// doesn't match the filter is an entirely ordinary outcome, not an error,
// and still counts as ok. err is non-nil only for a genuine write failure
// to out, which the caller treats as fatal rather than silently
// continuing.
func processLine(out io.Writer, cf *eval.CompiledFilter, now time.Time, line []byte, format formats.Format, output string) (ok bool, err error) {
	rec, decErr := decodeLine(line, format)
	if decErr != nil {
		return false, nil
	}
	if !cf.Eval(rec, now) {
		return true, nil
	}
	switch output {
	case "jsonl":
		err = render.JSONL(out, rec)
	default: // "raw"
		err = render.Raw(out, line)
	}
	return true, err
}

// decodeLine dispatches to the right per-format decoder. FormatPlain can
// never fail — there's nothing to parse, only to wrap.
func decodeLine(line []byte, format formats.Format) (*eval.Record, error) {
	switch format {
	case formats.FormatLogfmt:
		return logfmtx.DecodeLine(line)
	case formats.FormatPlain:
		return formats.DecodePlainLine(line), nil
	default: // formats.FormatJSONL
		res, err := formats.DecodeLine(line, formats.DefaultMaxDepth)
		if err != nil {
			return nil, err
		}
		return res.Record, nil
	}
}

// openSources resolves the FILE positional args into readers: no files
// means read stdin; "-" means stdin explicitly, alongside real files.
// Directory args, unreadable files, etc. surface here as a wrapped error
// (mapped to exit 4 by the caller) — the friendlier E-IO hint text the
// spec describes for a directory argument specifically is a Phase 10
// polish item, not yet implemented.
func openSources(files []string, stdin io.Reader) (readers []io.Reader, closeAll func(), err error) {
	if len(files) == 0 {
		return []io.Reader{stdin}, func() {}, nil
	}

	var closers []io.Closer
	for _, name := range files {
		if name == "-" {
			readers = append(readers, stdin)
			continue
		}
		f, openErr := os.Open(name)
		if openErr != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			return nil, nil, fmt.Errorf("cannot open %s: %w", name, openErr)
		}
		readers = append(readers, f)
		closers = append(closers, f)
	}

	return readers, func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}, nil
}
