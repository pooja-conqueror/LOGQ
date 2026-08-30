package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/formats"
	"github.com/pooja-conqueror/LOGQ/internal/pipeline"
	"github.com/pooja-conqueror/LOGQ/internal/watch"
)

// watchFlag implements flag.Value for -w/--watch[=SECONDS]: a bare "-w"
// means "watch mode, default poll interval"; "-w=5"/"--watch=5" means
// "watch mode, 5-second poll interval." Plain flag.Bool doesn't support
// an optional value at all, which is why this needs its own small Value
// type rather than reusing a stdlib flag kind directly — IsBoolFlag is
// the documented hook the flag package itself checks to allow the
// bare-flag ("-w" with no "=value") form.
type watchFlag struct {
	enabled  bool
	interval time.Duration
}

func (w *watchFlag) String() string {
	if !w.enabled {
		return "false"
	}
	return w.interval.String()
}

func (w *watchFlag) Set(s string) error {
	w.enabled = true
	if s == "" || s == "true" {
		w.interval = watch.DefaultPollInterval
		return nil
	}
	secs, err := parsePositiveSeconds(s)
	if err != nil {
		return fmt.Errorf("invalid --watch value %q, want a number of seconds: %w", s, err)
	}
	w.interval = secs
	return nil
}

func (w *watchFlag) IsBoolFlag() bool { return true }

func parsePositiveSeconds(s string) (time.Duration, error) {
	var secs float64
	if _, err := fmt.Sscanf(s, "%g", &secs); err != nil {
		return 0, err
	}
	if secs <= 0 {
		return 0, fmt.Errorf("must be > 0")
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// extractLine pulls the next complete '\n'-terminated line out of buf,
// CRLF-stripped the same way formats.LineReader normalizes a trailing
// '\r' — leaving any incomplete trailing data in buf for a later poll to
// complete. ok is false when no complete line is available yet.
//
// This is deliberately a smaller rule set than formats.LineReader's own
// (no BOM-strip, no oversized-line skip-and-count) — an intentional,
// documented (README) scope cut for watch mode specifically: batch
// mode's LineReader is built around a single io.Reader consumed to EOF
// in one pass, and adapting its oversize/BOM bookkeeping to a
// poll-driven, multi-chunk-append stream would be real, separate
// machinery for comparatively little real-world benefit in a mode whose
// own core value (live tailing) doesn't depend on it.
func extractLine(buf *bytes.Buffer) (line []byte, ok bool) {
	b := buf.Bytes()
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return nil, false
	}
	raw := b[:i]
	if n := len(raw); n > 0 && raw[n-1] == '\r' {
		raw = raw[:n-1]
	}
	line = append([]byte(nil), raw...) // copy — buf.Next below invalidates b
	buf.Next(i + 1)
	return line, true
}

// watchedFile is one -w target's live state: its Tailer, the format its
// lines decode as (detected once, up front, exactly like batch mode's
// own per-source detection — never re-detected per poll), and a
// per-file buffer holding any not-yet-newline-terminated tail between
// polls.
type watchedFile struct {
	path   string
	tailer *watch.Tailer
	format formats.Format
	buf    bytes.Buffer
}

// detectWatchFormat determines path's format for watch mode: forced
// format wins outright; "auto" samples from the file's CURRENT full
// content, read from the true beginning via a separate, short-lived
// os.Open — never through the file's own Tailer, which deliberately
// skips existing content (§ Tailer.Poll's own EOF-skip bootstrap). This
// reuses the exact same formats.DetectFromReader batch mode already
// relies on, just against a throwaway reader used only for detection.
func detectWatchFormat(path, forcedFormat string, maxLine int) (formats.Format, error) {
	switch forcedFormat {
	case "jsonl":
		return formats.FormatJSONL, nil
	case "logfmt":
		return formats.FormatLogfmt, nil
	case "plain":
		return formats.FormatPlain, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	lr := formats.NewLineReader(f, maxLine)
	detected, _, err := formats.DetectFromReader(lr)
	if err != nil {
		return 0, err
	}
	return detected, nil
}

// runWatch is -w's main loop: poll every interval, decode/filter/render
// any newly-appended lines from each watched file, and — if a stats
// stage is present — re-emit its full current SNAPSHOT (§14: "snapshots
// labeled SNAPSHOT re-emitted every poll-interval") every tick, without
// ever clearing its accumulated state (Stats.Snapshot, unlike Flush).
// now() is re-evaluated fresh every tick (unlike batch mode's once-
// frozen now) — this is what makes a relative bound like `ts >= -1h` or
// `--since -1h` behave as an actual rolling window in watch mode, rather
// than freezing to whatever "now" was when the session started.
func runWatch(ctx context.Context, out *bufio.Writer, buffered bufferedRenderer, cf *eval.CompiledFilter, pl *pipeline.Pipeline, statsStage *pipeline.Stats, stagesPresent bool, files []string, forcedFormat, output string, loc *time.Location, sinceRaw, untilRaw string, interval time.Duration, maxDepth, maxLine int, stderr io.Writer) int {
	watched := make([]*watchedFile, len(files))
	for i, path := range files {
		format, err := detectWatchFormat(path, forcedFormat, maxLine)
		if err != nil {
			fmt.Fprintf(stderr, "logq: %s: %v\n", path, err)
			return exitIO
		}
		watched[i] = &watchedFile{path: path, tailer: watch.NewTailer(path), format: format}
	}
	defer func() {
		for _, w := range watched {
			_ = w.tailer.Close()
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, "logq: watch stopped (interrupted)")
			return exitInterrupted
		case <-ticker.C:
			now := time.Now()
			sinceBound, err := watchBound(sinceRaw, now)
			if err != nil {
				fmt.Fprintf(stderr, "logq: invalid --since: %v\n", err)
				return exitUsage
			}
			untilBound, err := watchBound(untilRaw, now)
			if err != nil {
				fmt.Fprintf(stderr, "logq: invalid --until: %v\n", err)
				return exitUsage
			}

			for _, w := range watched {
				data, rotated, pollErr := w.tailer.Poll()
				if pollErr != nil {
					fmt.Fprintf(stderr, "logq: watch: %s: %v\n", w.path, pollErr)
					continue
				}
				if rotated {
					w.buf.Reset()
				}
				if len(data) == 0 {
					continue
				}
				w.buf.Write(data)
				for {
					line, ok := extractLine(&w.buf)
					if !ok {
						break
					}
					if len(line) == 0 {
						continue
					}
					_, _, werr := processLine(out, buffered, cf, pl, stagesPresent, now, line, w.format, loc, sinceBound, untilBound, output, maxDepth)
					if werr != nil {
						return writeExitCode(stderr, werr)
					}
				}
			}

			if statsStage != nil {
				fmt.Fprintf(stderr, "logq: SNAPSHOT (poll at %s)\n", now.Format(time.RFC3339))
				for _, rec := range statsStage.Snapshot() {
					if werr := renderRecord(out, buffered, rec, nil, output, stagesPresent); werr != nil {
						return writeExitCode(stderr, werr)
					}
				}
				if buffered != nil {
					if err := buffered.Flush(out); err != nil {
						return writeExitCode(stderr, err)
					}
				}
			}

			if err := out.Flush(); err != nil {
				return writeExitCode(stderr, err)
			}
		}
	}
}

// watchBound is parseTimeBound for watch mode's per-tick re-evaluation —
// "" means no bound at all (nil, nil).
func watchBound(raw string, now time.Time) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := parseTimeBound(raw, now)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
