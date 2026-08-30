package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

// cancelAfterLinesReader feeds pre-split lines one at a time and calls
// cancel() the instant the cancelAt-th line's bytes have been handed
// out — giving a test exact, deterministic control over which line
// in-flight processing is interrupted after, without racing a real OS
// signal against a real goroutine schedule (proven unreliable for this
// project in this sandboxed dev environment: see the commit-36+37 notes
// on `kill -INT` timing out against a piped background job). Each Read
// call yields at most one line's remaining bytes, so bufio's own
// internal buffering can never smuggle a later line's bytes past the
// cancellation point undetected.
type cancelAfterLinesReader struct {
	lines    [][]byte
	idx      int
	leftover []byte
	cancelAt int
	fired    bool
	cancel   context.CancelFunc
}

func (r *cancelAfterLinesReader) Read(p []byte) (int, error) {
	if len(r.leftover) == 0 {
		if r.idx >= len(r.lines) {
			return 0, io.EOF
		}
		r.leftover = r.lines[r.idx]
		r.idx++
		if !r.fired && r.idx == r.cancelAt {
			r.fired = true
			r.cancel()
		}
	}
	n := copy(p, r.leftover)
	r.leftover = r.leftover[n:]
	return n, nil
}

// TestChaos_InterruptMidStreamUnderVolume proves §14's PARTIAL-flush
// path is correct under real volume, not just the zero-lines-read edge
// case main_test.go's own TestRun_InterruptedContextReportsPartialAndExits130
// already covers (context cancelled before any input is read at all).
// Here the interrupt lands after exactly cancelAt of several thousand
// matching lines have been processed: output must contain precisely
// that many lines, in order, with nothing from beyond the cancellation
// point leaking through, the run must exit 130, and stderr must report
// PARTIAL with the correct line count.
func TestChaos_InterruptMidStreamUnderVolume(t *testing.T) {
	const total = 5000
	const cancelAt = 1777

	lines := make([][]byte, total)
	for i := range total {
		lines[i] = fmt.Appendf(nil, `{"n":%d}`+"\n", i)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterLinesReader{lines: lines, cancelAt: cancelAt, cancel: cancel}

	var outBuf, errBuf bytes.Buffer
	code := runCtx(ctx, []string{`exists(n)`}, reader, &outBuf, &errBuf)

	if code != exitInterrupted {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitInterrupted, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "PARTIAL") {
		t.Fatalf("stderr = %q, want it to mention PARTIAL", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), fmt.Sprintf("%d lines", cancelAt)) {
		t.Fatalf("stderr = %q, want it to report exactly %d lines", errBuf.String(), cancelAt)
	}

	gotLines := strings.Split(strings.TrimRight(outBuf.String(), "\n"), "\n")
	if len(gotLines) != cancelAt {
		t.Fatalf("got %d output lines, want exactly %d — interrupt must stop processing at precisely the cancellation point, no more, no less", len(gotLines), cancelAt)
	}
	for i, line := range gotLines {
		want := fmt.Sprintf(`{"n":%d}`, i)
		if line != want {
			t.Fatalf("line %d = %q, want %q — surviving output must be an exact, in-order prefix", i, line, want)
		}
	}
}
