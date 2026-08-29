// Command logq queries log files with a one-line filter/aggregate expression.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
	_ "time/tzdata" // embed the IANA timezone database in the binary — see --tz below

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/formats"
	"github.com/pooja-conqueror/LOGQ/internal/logfmtx"
	"github.com/pooja-conqueror/LOGQ/internal/pipeline"
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
  -o, --output FORMAT   output format: raw|jsonl|table|csv (default raw).
                         table/csv buffer every matched record — the header
                         must print before any row, and depends on having
                         seen the records first — so they cannot stream
                         like raw/jsonl do; see README's Honest Limits.
      --tz ZONE          IANA zone for interpreting naive timestamps and for
                         --since/--until "now" (default UTC). The zone
                         database is embedded in this binary (time/tzdata) —
                         works even on a host with no system tzdata at all.
      --since BOUND      drop records older than BOUND; RFC3339 or a
                         duration like -1h (relative to the run's frozen
                         "now"). Records with no resolvable timestamp are
                         dropped too when this is set.
      --until BOUND      drop records newer than BOUND; RFC3339, "now", or
                         a duration. Same drop-if-no-timestamp rule as
                         --since.
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
	output := fs.String("o", "raw", "output format: raw|jsonl|table|csv")
	fs.StringVar(output, "output", "raw", "output format: raw|jsonl|table|csv")
	tz := fs.String("tz", "UTC", "IANA zone for naive timestamps and --since/--until \"now\"")
	since := fs.String("since", "", "drop records older than this (RFC3339 or a duration like -1h)")
	until := fs.String("until", "", "drop records newer than this (RFC3339, \"now\", or a duration)")
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
	switch *output {
	case "raw", "jsonl", "table", "csv":
	default:
		fmt.Fprintf(stderr, "logq: --output %q not recognized (want raw|jsonl|table|csv)\n", *output)
		return exitUsage
	}

	loc, err := time.LoadLocation(*tz)
	if err != nil {
		fmt.Fprintf(stderr, "logq: invalid --tz %q: %v\n", *tz, err)
		return exitUsage
	}

	// Frozen once, here, before any record is read — this is what
	// batch-mode determinism (§15) and the timestamp±duration coercion
	// both depend on, and what --since/--until "now"/relative durations
	// are computed against. Watch mode (Phase 9) re-evaluates this per
	// poll tick instead; everything today runs in batch mode.
	now := time.Now()

	var sinceBound, untilBound *time.Time
	if *since != "" {
		t, err := parseTimeBound(*since, now)
		if err != nil {
			fmt.Fprintf(stderr, "logq: invalid --since: %v\n", err)
			return exitUsage
		}
		sinceBound = &t
	}
	if *until != "" {
		t, err := parseTimeBound(*until, now)
		if err != nil {
			fmt.Fprintf(stderr, "logq: invalid --until: %v\n", err)
			return exitUsage
		}
		untilBound = &t
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
	pl, err := buildPipeline(q.Stages)
	if err != nil {
		// e.g. NewFields' S-8 duplicate-output-column check — still a
		// compile-time failure, before any I/O.
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitCompile
	}
	stagesPresent := len(q.Stages) > 0

	sources, closeAll, err := openSources(files, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitIO
	}
	defer closeAll()

	out := bufio.NewWriter(stdout)
	defer out.Flush()

	// table/csv can't stream row-by-row (the header must print before any
	// row, and depends on having seen the records first) — buffered is
	// nil for raw/jsonl, which still stream normally.
	var buffered bufferedRenderer
	switch *output {
	case "table":
		buffered = render.NewTable()
	case "csv":
		buffered = render.NewCSV()
	}

	// pl is ONE shared pipeline instance across every source, not rebuilt
	// per file — this is what makes `limit N` (or a bounded `sort ...
	// limit N`) count correctly across multiple files, not per-file, and
	// what lets a satisfied limit stop reading LATER files entirely, not
	// just the rest of the current one.
	var totalLines, malformed, droppedByWindow int
	for _, src := range sources {
		n, m, d, done, srcErr := processSource(out, buffered, cf, pl, stagesPresent, now, src, *format, *output, loc, sinceBound, untilBound)
		totalLines += n
		malformed += m
		droppedByWindow += d
		if srcErr != nil {
			fmt.Fprintf(stderr, "logq: %v\n", srcErr)
			return exitIO
		}
		if done {
			break // e.g. limit's count reached — no reason to open later files at all
		}
	}

	// Flush any buffering stage (sort) — its held records still need to
	// go through the same rendering path a normally-streamed record
	// would, always, regardless of whether the loop above stopped early.
	var flushErr error
	pl.Flush(func(rec *eval.Record) {
		if flushErr != nil {
			return
		}
		flushErr = renderRecord(out, buffered, rec, nil, *output, stagesPresent)
	})
	if flushErr != nil {
		fmt.Fprintf(stderr, "logq: write error: %v\n", flushErr)
		return exitIO
	}

	if buffered != nil {
		if err := buffered.Flush(out); err != nil {
			fmt.Fprintf(stderr, "logq: write error: %v\n", err)
			return exitIO
		}
	}

	// Flush results before the diagnostic summary, so on a terminal the
	// matched records appear before the line that describes them.
	if err := out.Flush(); err != nil {
		fmt.Fprintf(stderr, "logq: write error: %v\n", err)
		return exitIO
	}
	if malformed > 0 || droppedByWindow > 0 {
		// A minimal placeholder for Phase 10's full per-field counter
		// summary (internal/summarize) — a handful of counts only, but
		// still honest: nothing is ever silently dropped without a trace.
		fmt.Fprintf(stderr, "logq: %d line(s) read, %d malformed, %d dropped by --since/--until\n",
			totalLines, malformed, droppedByWindow)
	}
	return exitOK
}

// parseTimeBound parses a --since/--until value: the literal "now", an
// absolute RFC3339 timestamp, or a duration (e.g. "-1h") added to now.
func parseTimeBound(s string, now time.Time) (time.Time, error) {
	if s == "now" {
		return now, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("%q is neither RFC3339, \"now\", nor a duration like -1h", s)
}

// bufferedRenderer is implemented by the output formats that can't stream
// row-by-row (table, csv — see render.Table/render.CSV's doc comments for
// why). nil for raw/jsonl, which render immediately per matched line.
type bufferedRenderer interface {
	Add(rec *eval.Record)
	Flush(w io.Writer) error
}

// buildPipeline converts the parsed Stage AST into executable
// pipeline.Stage values, once, before any I/O — a stage that fails to
// build (NewFields' S-8 duplicate-column check, or a StatsStage — parses
// fine as of commit 28, but its actual aggregation engine doesn't exist
// until later in Phase 8) is a compile-time error, exactly like an
// invalid query itself. A StatsStage specifically gets its own clear
// message here rather than falling through to the generic default case,
// which would otherwise read as an alarming "internal error" for what's
// actually just an honest, expected, temporary gap.
func buildPipeline(stages []query.Stage) (*pipeline.Pipeline, error) {
	execStages := make([]pipeline.Stage, 0, len(stages))
	for _, st := range stages {
		switch s := st.(type) {
		case *query.FieldsStage:
			fs, err := pipeline.NewFields(s)
			if err != nil {
				return nil, err
			}
			execStages = append(execStages, fs)
		case *query.SortStage:
			execStages = append(execStages, pipeline.NewSort(s))
		case *query.LimitStage:
			execStages = append(execStages, pipeline.NewLimit(s.Limit))
		case *query.StatsStage:
			return nil, fmt.Errorf("the 'stats' stage parses but its aggregation engine is not implemented yet")
		default:
			return nil, fmt.Errorf("internal error: unrecognized stage type %T", st)
		}
	}
	return pipeline.New(execStages...), nil
}

// renderRecord writes rec per the requested output mode.
//
// When pipeline stages are present, raw's byte-verbatim losslessness
// guarantee (§11.6) no longer applies — fields can transform a record's
// content entirely, and sort can hold and reorder it far from its
// original position in the stream — so raw falls back to jsonl
// serialization of the final record uniformly whenever ANY stage ran,
// rather than trying to selectively preserve original bytes only for the
// stage combinations that happen not to touch content. line is the
// original source bytes and may be nil (always is, for a record reaching
// this via Pipeline.Flush) — safe exactly because stagesPresent is always
// true whenever a flushed record exists at all (only a stage, sort, ever
// buffers), so the nil is never actually dereferenced.
func renderRecord(out io.Writer, buffered bufferedRenderer, rec *eval.Record, line []byte, output string, stagesPresent bool) error {
	switch output {
	case "jsonl":
		return render.JSONL(out, rec)
	case "table", "csv":
		buffered.Add(rec)
		return nil
	default: // "raw"
		if stagesPresent {
			return render.JSONL(out, rec)
		}
		return render.Raw(out, line)
	}
}

// processSource fully processes one source: transparent gzip unwrap, line
// splitting, format detection (or the forced --format), timestamp
// resolution, --since/--until filtering, query filtering, and rendering.
// It returns per-source counts — detection and its "auto" sampling are
// per-source (§9.2: "Detection cached per file"), never shared across
// multiple files in one run. A non-nil err is a fatal read/write failure;
// the caller stops the whole run on it (exit 4).
func processSource(out io.Writer, buffered bufferedRenderer, cf *eval.CompiledFilter, pl *pipeline.Pipeline, stagesPresent bool, now time.Time, src io.Reader, forcedFormat, output string, loc *time.Location, since, until *time.Time) (linesRead, malformed, droppedByWindow int, done bool, err error) {
	gzr, err := formats.MaybeGunzip(src)
	if err != nil {
		return 0, 0, 0, false, err
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
			return 0, 0, 0, false, detErr
		}
		srcFormat, sample = detected, s
	}

	// process reports stop=true once the shared pipeline has signaled it
	// will never accept another record (e.g. limit's count reached) — the
	// caller then stops reading this source, and run() stops opening any
	// further ones too.
	process := func(line []byte) (stop bool, err error) {
		linesRead++
		outcome, pipelineDone, werr := processLine(out, buffered, cf, pl, stagesPresent, now, line, srcFormat, loc, since, until, output)
		if werr != nil {
			return false, werr
		}
		switch outcome {
		case outcomeMalformed:
			malformed++
		case outcomeDroppedByWindow:
			droppedByWindow++
		}
		return pipelineDone, nil
	}

	// The sample lines were already consumed from lr during auto-detection
	// (or is empty, when forcedFormat skipped detection entirely) — they
	// must be processed before reading any further, or they'd be lost.
	for _, line := range sample {
		stop, werr := process(line)
		if werr != nil {
			return linesRead, malformed, droppedByWindow, false, werr
		}
		if stop {
			return linesRead, malformed, droppedByWindow, true, nil
		}
	}
	for {
		line, lerr := lr.ReadLine()
		if line != nil {
			stop, werr := process(line)
			if werr != nil {
				return linesRead, malformed, droppedByWindow, false, werr
			}
			if stop {
				return linesRead, malformed, droppedByWindow, true, nil
			}
		}
		if lerr != nil {
			if lerr != io.EOF {
				return linesRead, malformed, droppedByWindow, false, lerr
			}
			break
		}
	}

	return linesRead, malformed, droppedByWindow, false, nil
}

// lineOutcome distinguishes why a line didn't produce output, for the
// caller's counters.
type lineOutcome int

const (
	outcomeMatched lineOutcome = iota
	outcomeFilteredOut
	outcomeMalformed
	outcomeDroppedByWindow
)

// processLine decodes one line under the given format, resolves its
// timestamp, applies --since/--until, evaluates the compiled query
// filter, runs the record through the pipeline stages, and renders it if
// everything passes. err is non-nil only for a genuine write failure to
// out, which the caller treats as fatal. done propagates the pipeline's
// own done signal (§ Stage: "no more input needs to be read at all") —
// true once, for instance, limit's count has been reached.
func processLine(out io.Writer, buffered bufferedRenderer, cf *eval.CompiledFilter, pl *pipeline.Pipeline, stagesPresent bool, now time.Time, line []byte, format formats.Format, loc *time.Location, since, until *time.Time, output string) (outcome lineOutcome, done bool, err error) {
	rec, decErr := decodeLine(line, format)
	if decErr != nil {
		return outcomeMalformed, false, nil
	}

	if t, _, ok := eval.ResolveRecordTimestamp(rec, loc); ok {
		rec.Time = t
		rec.HasTime = true
	}

	if since != nil || until != nil {
		// D-1: records are never dropped for an unresolvable timestamp
		// EXCEPT under an explicit --since/--until bound, where they are
		// dropped and counted rather than silently passed through.
		if !rec.HasTime {
			return outcomeDroppedByWindow, false, nil
		}
		if since != nil && rec.Time.Before(*since) {
			return outcomeDroppedByWindow, false, nil
		}
		if until != nil && rec.Time.After(*until) {
			return outcomeDroppedByWindow, false, nil
		}
	}

	if !cf.Eval(rec, now) {
		return outcomeFilteredOut, false, nil
	}

	out2, keep, pipelineDone := pl.Process(rec)
	if !keep {
		return outcomeFilteredOut, pipelineDone, nil
	}

	werr := renderRecord(out, buffered, out2, line, output, stagesPresent)
	return outcomeMatched, pipelineDone, werr
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
