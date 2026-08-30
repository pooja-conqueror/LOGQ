// Command logq queries log files with a one-line filter/aggregate expression.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA timezone database in the binary — see --tz below

	"github.com/pooja-conqueror/LOGQ/internal/agg"
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

// Ceilings a --max-line/--max-query flag value must not exceed — the
// spec's own stated caps ("--max-line up to 16MB", "--max-query up to
// 65536"), not just a bare default a user could otherwise raise without
// limit.
const (
	maxLineCeiling  = 16 << 20 // 16MB
	maxQueryCeiling = 65536
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
      --max-groups N     stats cardinality guard: at most N distinct
                         groups tracked before newer keys collapse into a
                         single (other) row (default 10000).
      --max-sample N     percentile (p50/p95/p99) reservoir cap per group;
                         exact under this many values, approximate (with
                         a "*"-marked cell) beyond it (default 100000).
      --seed N           percentile reservoir PRNG seed — fixed by
                         default so approximate percentiles stay
                         reproducible across runs (default 0).
      --levels LIST      extend/override level-ordinal names, comma-
                         separated name=NUMBER pairs (e.g.
                         "critical=55"); unmentioned names keep their
                         built-in ordinal.
      --max-depth N      max JSON object/array nesting depth before a
                         line is rejected as malformed (default 32).
      --max-line N       max input line length in bytes; a longer line
                         is skipped whole and counted, never truncated
                         silently (default 1MB, up to 16MB).
      --max-query N      max query text length in characters, checked
                         before any parsing begins (default 8192, up to
                         65536).
  -j, --workers N        parallelize stats aggregation across N shard
                         goroutines (default 1 — sequential). Only stats'
                         own per-group math is parallelized; record
                         decoding/filtering always stays single-threaded.
                         No effect on a query with no "by"/"every" (one
                         group only, nothing to shard).
  -w, --watch[=SECONDS]  poll FILE(s) for new content instead of reading
                         once to EOF (default interval 1s; requires at
                         least one real FILE, not stdin). now() is
                         re-evaluated fresh every poll, so a relative
                         bound (--since -1h, ts >= -1h) behaves as a
                         genuine rolling window. A stats query's full
                         accumulated result is re-emitted every poll,
                         labeled SNAPSHOT on stderr — batch mode's
                         byte-identical-output determinism guarantee
                         (§15) is explicitly scoped OUT of watch mode by
                         design (see README).
  -h, --help             show this help and exit
      --version          show version and exit

Results go to stdout only; diagnostics and the end-of-run summary go to
stderr. logq is under active development — see README.md's Honest Limits
section for what isn't wired up yet.

SIGINT/SIGTERM: the first signal stops reading new input, flushes
whatever partial results exist (labeled PARTIAL on stderr), and exits
130. A second signal within 2 seconds exits immediately, no flush
guaranteed. Writing to a closed downstream pipe (e.g. "| head -1") exits
0 silently, never a "write error."
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the whole CLI, parameterized on its I/O so it's testable without
// spawning a subprocess (see main_test.go). It owns the real OS signal
// context; runCtx is the version with that context injectable, so a test
// can simulate an interrupt deterministically without sending a real
// signal to the test process itself.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// §14: one root context from signal.NotifyContext — the first
	// SIGINT/SIGTERM cancels it, which the read loop below checks to
	// stop pulling in new input (still flushing whatever partial
	// results already exist). A genuinely impatient second signal within
	// 2s of the first forces an immediate exit with no flush guarantee —
	// NotifyContext itself only ever reacts to the first occurrence
	// (that's what cancels ctx), so catching a real second one needs its
	// own raw signal.Notify watch, armed only once the first has fired.
	ctx, stopNotify := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopNotify()
	go func() {
		<-ctx.Done()
		second := make(chan os.Signal, 1)
		signal.Notify(second, os.Interrupt, syscall.SIGTERM)
		select {
		case <-second:
			fmt.Fprintln(stderr, "logq: second interrupt — exiting immediately, no flush")
			os.Exit(exitInterrupted)
		case <-time.After(2 * time.Second):
		}
	}()
	return runCtx(ctx, args, stdin, stdout, stderr)
}

// runCtx is run's actual implementation, taking its cancellation context
// as a parameter.
func runCtx(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logq", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we print our own usage/errors, not flag's

	format := fs.String("f", "auto", "input format: auto|jsonl|logfmt|plain")
	fs.StringVar(format, "format", "auto", "input format: auto|jsonl|logfmt|plain")
	output := fs.String("o", "raw", "output format: raw|jsonl|table|csv")
	fs.StringVar(output, "output", "raw", "output format: raw|jsonl|table|csv")
	tz := fs.String("tz", "UTC", "IANA zone for naive timestamps and --since/--until \"now\"")
	since := fs.String("since", "", "drop records older than this (RFC3339 or a duration like -1h)")
	until := fs.String("until", "", "drop records newer than this (RFC3339, \"now\", or a duration)")
	maxGroups := fs.Int("max-groups", pipeline.DefaultMaxGroups, "stats cardinality guard")
	maxSample := fs.Int("max-sample", agg.DefaultMaxSample, "percentile reservoir cap per group")
	seed := fs.Int64("seed", agg.DefaultReservoirSeed, "percentile reservoir PRNG seed")
	levels := fs.String("levels", "", "extend/override level ordinals: name=NUM,...")
	maxDepth := fs.Int("max-depth", formats.DefaultMaxDepth, "max JSON nesting depth")
	maxLine := fs.Int("max-line", formats.DefaultMaxLine, "max input line length in bytes")
	maxQuery := fs.Int("max-query", query.DefaultMaxQueryLen, "max query text length in characters")
	workers := fs.Int("j", 1, "parallelize stats aggregation across N shard goroutines")
	fs.IntVar(workers, "workers", 1, "parallelize stats aggregation across N shard goroutines")
	watchOpt := &watchFlag{}
	fs.Var(watchOpt, "w", "watch mode: poll for new content (optional =SECONDS interval)")
	fs.Var(watchOpt, "watch", "watch mode: poll for new content (optional =SECONDS interval)")
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

	if *maxGroups < 1 {
		fmt.Fprintf(stderr, "logq: --max-groups must be >= 1, got %d\n", *maxGroups)
		return exitUsage
	}
	if *maxSample < 1 {
		fmt.Fprintf(stderr, "logq: --max-sample must be >= 1, got %d\n", *maxSample)
		return exitUsage
	}
	if *maxDepth < 1 {
		fmt.Fprintf(stderr, "logq: --max-depth must be >= 1, got %d\n", *maxDepth)
		return exitUsage
	}
	if *maxLine < 1 || *maxLine > maxLineCeiling {
		fmt.Fprintf(stderr, "logq: --max-line must be between 1 and %d (16MB), got %d\n", maxLineCeiling, *maxLine)
		return exitUsage
	}
	if *maxQuery < 1 || *maxQuery > maxQueryCeiling {
		fmt.Fprintf(stderr, "logq: --max-query must be between 1 and %d, got %d\n", maxQueryCeiling, *maxQuery)
		return exitUsage
	}
	if *workers < 1 {
		fmt.Fprintf(stderr, "logq: --workers/-j must be >= 1, got %d\n", *workers)
		return exitUsage
	}
	if watchOpt.enabled {
		if len(files) == 0 {
			fmt.Fprintln(stderr, "logq: -w/--watch requires at least one real FILE argument, not stdin")
			return exitUsage
		}
		if slices.Contains(files, "-") {
			fmt.Fprintln(stderr, "logq: -w/--watch requires real FILE arguments — \"-\" (stdin) can't be watched")
			return exitUsage
		}
	}
	levelOverrides, err := parseLevelsFlag(*levels)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
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
	q, err := query.ParseQueryWithLimit(queryText, *maxQuery)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitCompile
	}
	var levelTable map[string]int
	if levelOverrides != nil {
		levelTable = eval.MergeLevelTable(levelOverrides)
	}
	cf, err := eval.CompileWithLevelTable(q.Filter, levelTable)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitCompile
	}
	// Watch mode always forces sequential stats (workers=1 in effect)
	// regardless of -j: Stats.Snapshot's non-destructive re-flush (used
	// for SNAPSHOT re-emission every poll) has no ParallelStats
	// equivalent — a concurrent shard's live state can't be peeked
	// without pausing its worker goroutine, real extra machinery for a
	// mode whose poll interval already rate-limits how often this
	// matters. Documented, not silently different: -j is simply not
	// honored under -w.
	pl, statsStage, err := buildPipeline(q.Stages, loc, *maxGroups, *maxSample, *seed, *workers, watchOpt.enabled)
	if err != nil {
		// e.g. NewFields' S-8 duplicate-output-column check — still a
		// compile-time failure, before any I/O.
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitCompile
	}
	stagesPresent := len(q.Stages) > 0

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

	if watchOpt.enabled {
		return runWatch(ctx, out, buffered, cf, pl, statsStage, stagesPresent, files, *format, *output, loc, *since, *until, watchOpt.interval, *maxDepth, *maxLine, stderr)
	}

	sources, closeAll, err := openSources(files, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "logq: %v\n", err)
		return exitIO
	}
	defer closeAll()

	// pl is ONE shared pipeline instance across every source, not rebuilt
	// per file — this is what makes `limit N` (or a bounded `sort ...
	// limit N`) count correctly across multiple files, not per-file, and
	// what lets a satisfied limit stop reading LATER files entirely, not
	// just the rest of the current one.
	var totalLines, malformed, droppedByWindow int
	for _, src := range sources {
		n, m, d, done, srcErr := processSource(ctx, out, buffered, cf, pl, stagesPresent, now, src, *format, *output, loc, sinceBound, untilBound, *maxDepth, *maxLine)
		totalLines += n
		malformed += m
		droppedByWindow += d
		if srcErr != nil {
			return writeExitCode(stderr, srcErr)
		}
		if done {
			break // e.g. limit's count reached, or an interrupt (ctx cancelled) — no reason to open later files at all
		}
	}

	// Flush any buffering stage (sort, stats) — its held records still
	// need to go through the same rendering path a normally-streamed
	// record would, always, regardless of whether the loop above stopped
	// early (including on interrupt — this is the "flush whatever
	// partial results exist" half of §14's first-signal behavior).
	var flushErr error
	pl.Flush(func(rec *eval.Record) {
		if flushErr != nil {
			return
		}
		flushErr = renderRecord(out, buffered, rec, nil, *output, stagesPresent)
	})
	if flushErr != nil {
		return writeExitCode(stderr, flushErr)
	}

	if buffered != nil {
		if err := buffered.Flush(out); err != nil {
			return writeExitCode(stderr, err)
		}
	}

	// Flush results before the diagnostic summary, so on a terminal the
	// matched records appear before the line that describes them.
	if err := out.Flush(); err != nil {
		return writeExitCode(stderr, err)
	}

	// §14: a signal-triggered stop is reported honestly as PARTIAL, not
	// silently folded into an ordinary exit 0 — checked here (after
	// everything possible has already been flushed) rather than
	// threaded as a special return value through processSource, since
	// ctx.Err() already answers "was this run interrupted at all,
	// anywhere" directly.
	interrupted := ctx.Err() != nil
	if interrupted {
		fmt.Fprintf(stderr, "logq: PARTIAL (interrupted at %d lines) — flushed partial results\n", totalLines)
	}
	if malformed > 0 || droppedByWindow > 0 {
		// A minimal placeholder for Phase 10's full per-field counter
		// summary (internal/summarize) — a handful of counts only, but
		// still honest: nothing is ever silently dropped without a trace.
		fmt.Fprintf(stderr, "logq: %d line(s) read, %d malformed, %d dropped by --since/--until\n",
			totalLines, malformed, droppedByWindow)
	}
	if interrupted {
		return exitInterrupted
	}
	return exitOK
}

// writeExitCode maps a write-path error to its exit code. Go's runtime
// doesn't deliver SIGPIPE as a signal for a write to stdout/stderr — it
// surfaces as an ordinary write error instead (EC-40: "SIGPIPE via
// | head -1 -> silent exit 0") — so a broken downstream pipe (the reader
// simply stopped wanting more input, e.g. `head -1`) is detected by
// recognizing that specific error, not by catching a signal, and is
// never printed as an alarming "write error."
func writeExitCode(stderr io.Writer, err error) int {
	if isBrokenPipe(err) {
		return exitOK
	}
	fmt.Fprintf(stderr, "logq: write error: %v\n", err)
	return exitIO
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
// build (NewFields' S-8 duplicate-column check) is a compile-time error,
// exactly like an invalid query itself. loc is the run's resolved --tz
// location, needed by StatsStage's window-bucket alignment (§8.1) even
// when no "every" clause is present, for API uniformity. maxGroups/
// maxSample/seed/workers are --max-groups/--max-sample/--seed/
// --workers's values, passed straight through to
// pipeline.NewParallelStats (workers=1 there is the plain, sequential
// *Stats path — no parallel overhead unless actually asked for).
// forceSequentialStats (set for -w) always uses NewStatsWithLimits
// directly instead, ignoring workers entirely — see runCtx's own comment
// on why watch mode can't use ParallelStats. statsStage is non-nil only
// when a stats stage exists AND forceSequentialStats was set — it's
// watch mode's own handle for periodic Snapshot() calls; batch mode has
// no use for it (nil), since Pipeline.Flush already drives every stage's
// Flush generically without needing a concrete reference to any one of
// them.
func buildPipeline(stages []query.Stage, loc *time.Location, maxGroups, maxSample int, seed int64, workers int, forceSequentialStats bool) (pl *pipeline.Pipeline, statsStage *pipeline.Stats, err error) {
	execStages := make([]pipeline.Stage, 0, len(stages))
	for _, st := range stages {
		switch s := st.(type) {
		case *query.FieldsStage:
			fs, ferr := pipeline.NewFields(s)
			if ferr != nil {
				return nil, nil, ferr
			}
			execStages = append(execStages, fs)
		case *query.SortStage:
			execStages = append(execStages, pipeline.NewSort(s))
		case *query.LimitStage:
			execStages = append(execStages, pipeline.NewLimit(s.Limit))
		case *query.StatsStage:
			if forceSequentialStats {
				ss, serr := pipeline.NewStatsWithLimits(s, loc, maxGroups, maxSample, seed)
				if serr != nil {
					return nil, nil, serr
				}
				statsStage = ss
				execStages = append(execStages, ss)
				continue
			}
			ss, serr := pipeline.NewParallelStats(s, loc, maxGroups, maxSample, seed, workers)
			if serr != nil {
				return nil, nil, serr
			}
			execStages = append(execStages, ss)
		default:
			return nil, nil, fmt.Errorf("internal error: unrecognized stage type %T", st)
		}
	}
	return pipeline.New(execStages...), statsStage, nil
}

// parseLevelsFlag parses --levels' "name=NUM,name2=NUM2" syntax into an
// override map ready for eval.MergeLevelTable. An empty string means no
// overrides at all — nil, not an empty map, so the common "no --levels
// given" case costs nothing beyond a nil check downstream.
func parseLevelsFlag(s string) (map[string]int, error) {
	if s == "" {
		return nil, nil
	}
	out := make(map[string]int)
	for _, pair := range strings.Split(s, ",") {
		name, numText, ok := strings.Cut(pair, "=")
		if !ok || name == "" || numText == "" {
			return nil, fmt.Errorf("invalid --levels entry %q, want name=NUMBER", pair)
		}
		n, err := strconv.Atoi(numText)
		if err != nil {
			return nil, fmt.Errorf("invalid --levels entry %q: ordinal must be an integer", pair)
		}
		out[name] = n
	}
	return out, nil
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
// the caller stops the whole run on it (exit 4). maxDepth/maxLine are
// --max-depth/--max-line's values. ctx being Done (§14: first SIGINT/
// SIGTERM) is treated exactly like the pipeline's own "done" signal —
// stop reading new lines, return with done=true so the caller stops
// opening any further sources too, but nothing already decoded is
// discarded.
func processSource(ctx context.Context, out io.Writer, buffered bufferedRenderer, cf *eval.CompiledFilter, pl *pipeline.Pipeline, stagesPresent bool, now time.Time, src io.Reader, forcedFormat, output string, loc *time.Location, since, until *time.Time, maxDepth, maxLine int) (linesRead, malformed, droppedByWindow int, done bool, err error) {
	gzr, err := formats.MaybeGunzip(src)
	if err != nil {
		return 0, 0, 0, false, err
	}
	lr := formats.NewLineReader(gzr, maxLine)

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
		outcome, pipelineDone, werr := processLine(out, buffered, cf, pl, stagesPresent, now, line, srcFormat, loc, since, until, output, maxDepth)
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
	// Still ctx-checked per line, same as the main loop below: an
	// interrupt arriving during/right after the sampling phase itself
	// should stop just as promptly, not process a whole buffered batch
	// unconditionally first.
	for _, line := range sample {
		if ctx.Err() != nil {
			return linesRead, malformed, droppedByWindow, true, nil
		}
		stop, werr := process(line)
		if werr != nil {
			return linesRead, malformed, droppedByWindow, false, werr
		}
		if stop {
			return linesRead, malformed, droppedByWindow, true, nil
		}
	}
	for {
		if ctx.Err() != nil {
			return linesRead, malformed, droppedByWindow, true, nil
		}
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
func processLine(out io.Writer, buffered bufferedRenderer, cf *eval.CompiledFilter, pl *pipeline.Pipeline, stagesPresent bool, now time.Time, line []byte, format formats.Format, loc *time.Location, since, until *time.Time, output string, maxDepth int) (outcome lineOutcome, done bool, err error) {
	rec, decErr := decodeLine(line, format, maxDepth)
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
// never fail — there's nothing to parse, only to wrap. maxDepth is
// --max-depth's value, consulted only for jsonl.
func decodeLine(line []byte, format formats.Format, maxDepth int) (*eval.Record, error) {
	switch format {
	case formats.FormatLogfmt:
		return logfmtx.DecodeLine(line)
	case formats.FormatPlain:
		return formats.DecodePlainLine(line), nil
	default: // formats.FormatJSONL
		res, err := formats.DecodeLine(line, maxDepth)
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
