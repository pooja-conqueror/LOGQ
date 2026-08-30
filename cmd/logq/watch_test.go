package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/formats"
)

func TestWatchFlag_BareEnablesDefaultInterval(t *testing.T) {
	var w watchFlag
	if err := w.Set(""); err != nil {
		t.Fatalf("Set(\"\") error = %v", err)
	}
	if !w.enabled {
		t.Fatal("enabled = false, want true")
	}
	if w.interval != time.Second {
		t.Fatalf("interval = %v, want 1s (the default)", w.interval)
	}
}

func TestWatchFlag_ExplicitSecondsValue(t *testing.T) {
	var w watchFlag
	if err := w.Set("2.5"); err != nil {
		t.Fatalf("Set(\"2.5\") error = %v", err)
	}
	if w.interval != 2500*time.Millisecond {
		t.Fatalf("interval = %v, want 2.5s", w.interval)
	}
}

func TestWatchFlag_RejectsNonPositive(t *testing.T) {
	for _, s := range []string{"0", "-1"} {
		var w watchFlag
		if err := w.Set(s); err == nil {
			t.Errorf("Set(%q) error = nil, want an error (must be > 0)", s)
		}
	}
}

func TestWatchFlag_RejectsGarbage(t *testing.T) {
	var w watchFlag
	if err := w.Set("not-a-number"); err == nil {
		t.Fatal("Set(\"not-a-number\") error = nil, want an error")
	}
}

func TestWatchFlag_IsBoolFlag(t *testing.T) {
	var w watchFlag
	if !w.IsBoolFlag() {
		t.Fatal("IsBoolFlag() = false, want true — this is what makes bare -w (no =value) valid")
	}
}

func TestExtractLine_SplitsOnNewlineAndStripsCR(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("first\r\nsecond\nthird")

	line1, ok1 := extractLine(&buf)
	if !ok1 || string(line1) != "first" {
		t.Fatalf("line1 = %q (ok=%v), want \"first\"", line1, ok1)
	}
	line2, ok2 := extractLine(&buf)
	if !ok2 || string(line2) != "second" {
		t.Fatalf("line2 = %q (ok=%v), want \"second\"", line2, ok2)
	}
	_, ok3 := extractLine(&buf)
	if ok3 {
		t.Fatal("ok3 = true, want false — \"third\" has no trailing newline yet")
	}
	if buf.String() != "third" {
		t.Fatalf("remaining buf = %q, want %q (the incomplete tail preserved)", buf.String(), "third")
	}
}

func TestExtractLine_CompletesAcrossTwoAppends(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("partial")
	if _, ok := extractLine(&buf); ok {
		t.Fatal("extractLine should report ok=false with no newline yet")
	}
	buf.WriteString(" line\n")
	line, ok := extractLine(&buf)
	if !ok || string(line) != "partial line" {
		t.Fatalf("line = %q (ok=%v), want \"partial line\"", line, ok)
	}
}

func TestDetectWatchFormat_ForcedFormatsBypassDetection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(path, []byte("not json at all"), 0o644)

	cases := map[string]formats.Format{
		"jsonl":  formats.FormatJSONL,
		"logfmt": formats.FormatLogfmt,
		"plain":  formats.FormatPlain,
	}
	for forced, want := range cases {
		got, err := detectWatchFormat(path, forced, 1<<20)
		if err != nil {
			t.Fatalf("detectWatchFormat(%q) error = %v", forced, err)
		}
		if got != want {
			t.Fatalf("detectWatchFormat(%q) = %v, want %v", forced, got, want)
		}
	}
}

func TestDetectWatchFormat_AutoDetectsFromExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	os.WriteFile(path, []byte(`{"a":1}`+"\n"), 0o644)

	got, err := detectWatchFormat(path, "auto", 1<<20)
	if err != nil {
		t.Fatalf("detectWatchFormat error = %v", err)
	}
	if got.String() != "jsonl" {
		t.Fatalf("detected format = %v, want jsonl", got)
	}
}

func TestRunCtx_WatchModeTailsNewContentUntilContextCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	os.WriteFile(path, []byte(`{"x":0}`+"\n"), 0o644) // pre-existing — must be skipped

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	var outBuf, errBuf bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runCtx(ctx, []string{"-w=0.05", `exists(x)`, path}, strings.NewReader(""), &outBuf, &errBuf)
	}()

	time.Sleep(150 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile error = %v", err)
	}
	f.WriteString(`{"x":1}` + "\n")
	f.Close()

	code := <-done
	if code != exitInterrupted {
		t.Fatalf("exit = %d, want %d (watch mode stops via context cancellation like any other interrupt)", code, exitInterrupted)
	}
	out := outBuf.String()
	if strings.Contains(out, `"x":0`) {
		t.Fatalf("out = %q, must not contain pre-existing content", out)
	}
	if !strings.Contains(out, `"x":1`) {
		t.Fatalf("out = %q, want it to contain the newly-appended line", out)
	}
	if !strings.Contains(errBuf.String(), "watch stopped") {
		t.Fatalf("errOut = %q, want a watch-stopped message", errBuf.String())
	}
}

func TestRunCtx_WatchModeStatsSnapshotGrows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	os.WriteFile(path, []byte(""), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	var outBuf, errBuf bytes.Buffer
	done := make(chan int, 1)
	go func() {
		// -f jsonl forced explicitly: the file starts empty, and
		// auto-detection with nothing to sample correctly falls back to
		// plain text (formats.Detect's own documented behavior) — this
		// test is about Snapshot growth, not detection, which already
		// has its own dedicated test above.
		done <- runCtx(ctx, []string{"-f", "jsonl", "-w=0.05", `| stats count() by service`, path}, strings.NewReader(""), &outBuf, &errBuf)
	}()

	time.Sleep(150 * time.Millisecond) // let the bootstrap poll happen before appending
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile error = %v", err)
	}
	f.WriteString(`{"service":"a"}` + "\n")
	f.Close()
	time.Sleep(300 * time.Millisecond)

	<-done
	if !strings.Contains(errBuf.String(), "SNAPSHOT") {
		t.Fatalf("errOut = %q, want it to mention SNAPSHOT", errBuf.String())
	}
	if !strings.Contains(outBuf.String(), `"service":"a","count":1`) {
		t.Fatalf("out = %q, want a service=a count=1 snapshot row", outBuf.String())
	}
}
