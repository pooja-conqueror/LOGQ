package main

import (
	"context"
	"io"
	"runtime"
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/corpus"
)

// TestSoak_MemoryStaysBoundedAcrossCorpusScale is the automated,
// CI-sized counterpart to scripts/gen-corpus.go's manual 2GB run: it
// proves logq's streaming architecture actually holds, not just that
// it's claimed to. runtime.MemStats.HeapAlloc is this project's
// documented, honest proxy for OS-level RSS — Go has no portable RSS
// API without cgo or per-platform syscalls, and this dev environment
// itself has no C compiler available (the same reason `make race`
// exists but couldn't be run/verified in-session).
//
// Rather than asserting an absolute ceiling (fragile: it'd depend on
// the whole go test binary's own baseline overhead, not just logq's
// usage), this test compares HeapAlloc sampled at two checkpoints — 10%
// and 100% of the way through a corpus generated on the fly and piped
// straight into a streaming filter query (no stats/sort/table
// buffering stage, which would legitimately grow with input size).
// Processing 10x more input should NOT cost anywhere close to 10x more
// heap — that delta between checkpoints, not either checkpoint's
// absolute value, is the actual streaming-boundedness claim.
//
// This is intentionally a much smaller corpus than the real 2GB target
// — enough lines to show a clear trend without slowing down every
// `go test ./...` run. Run `scripts/gen-corpus.go` directly for a real,
// full-scale, manual soak verification.
func TestSoak_MemoryStaysBoundedAcrossCorpusScale(t *testing.T) {
	const totalLines = 200_000
	const checkpoint1 = totalLines / 10 // 10%
	const checkpoint2 = totalLines      // 100%

	// The heap growth an 18KB-ish record's worth of per-line processing
	// (decode, evaluate, discard) can plausibly leave behind between
	// samples — GC timing means this can't be exactly zero, but it must
	// stay flat, not scale with the extra 180,000 lines processed
	// between the two checkpoints.
	const maxCheckpointDeltaBytes = 48 * 1024 * 1024

	var heapAtCheckpoint1, heapAtCheckpoint2 uint64
	gen := corpus.NewGenerator(42, totalLines)
	gen.OnLine = func(n int64) {
		switch n {
		case checkpoint1:
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			heapAtCheckpoint1 = m.HeapAlloc
		case checkpoint2:
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			heapAtCheckpoint2 = m.HeapAlloc
		}
	}

	code := runCtx(context.Background(), []string{`exists(status)`}, gen, io.Discard, io.Discard)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if heapAtCheckpoint1 == 0 || heapAtCheckpoint2 == 0 {
		t.Fatalf("checkpoint sampling never fired: checkpoint1=%d checkpoint2=%d — OnLine hook wiring broke", heapAtCheckpoint1, heapAtCheckpoint2)
	}

	var delta int64
	if heapAtCheckpoint2 > heapAtCheckpoint1 {
		delta = int64(heapAtCheckpoint2 - heapAtCheckpoint1)
	}
	t.Logf("HeapAlloc at %d lines: %d bytes; at %d lines: %d bytes; delta: %d bytes (envelope: %d bytes)",
		checkpoint1, heapAtCheckpoint1, checkpoint2, heapAtCheckpoint2, delta, maxCheckpointDeltaBytes)
	if delta > maxCheckpointDeltaBytes {
		t.Fatalf("heap grew %d bytes between 10%% and 100%% of a %d-line corpus (checkpoint1=%d checkpoint2=%d) — want growth under %d bytes; this suggests memory use is scaling with input size instead of staying streamed/bounded",
			delta, totalLines, heapAtCheckpoint1, heapAtCheckpoint2, maxCheckpointDeltaBytes)
	}
}
