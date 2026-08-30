package main

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/corpus"
)

// genBytes materializes a fixed corpus once, outside any benchmark's
// timed region, so each Benchmark below measures logq's own pipeline
// throughput against real bytes — not corpus.Generator's own
// synthesis cost.
func genBytes(b *testing.B, seed int64, lines int64) []byte {
	b.Helper()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, corpus.NewGenerator(seed, lines)); err != nil {
		b.Fatalf("genBytes: %v", err)
	}
	return buf.Bytes()
}

// BenchmarkFilterPassthrough measures the common-case query shape: a
// plain filter, streamed straight through with no buffering stage.
func BenchmarkFilterPassthrough(b *testing.B) {
	data := genBytes(b, 1, 20_000)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runCtx(context.Background(), []string{`exists(status)`}, bytes.NewReader(data), io.Discard, io.Discard)
	}
}

// BenchmarkStatsGroupBySequential measures the aggregation path's cost
// at -j 1 (the default): count/avg/percentile aggregators over a
// moderate-cardinality group-by.
func BenchmarkStatsGroupBySequential(b *testing.B) {
	data := genBytes(b, 1, 20_000)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runCtx(context.Background(), []string{"-j", "1", `| stats count(), avg(duration_ms), p95(duration_ms) by service`}, bytes.NewReader(data), io.Discard, io.Discard)
	}
}

// BenchmarkStatsGroupByParallel4 is the -j 4 counterpart to
// BenchmarkStatsGroupBySequential, same query and corpus — the pair
// together is what BENCHMARKS.md's parallelism section is measured
// from.
func BenchmarkStatsGroupByParallel4(b *testing.B) {
	data := genBytes(b, 1, 20_000)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runCtx(context.Background(), []string{"-j", "4", `| stats count(), avg(duration_ms), p95(duration_ms) by service`}, bytes.NewReader(data), io.Discard, io.Discard)
	}
}

// BenchmarkTableRender measures the buffered-renderer path (table must
// see every row before it can compute column widths) against the same
// corpus and query shape as BenchmarkFilterPassthrough, isolating
// rendering overhead from filtering overhead.
func BenchmarkTableRender(b *testing.B) {
	data := genBytes(b, 1, 20_000)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runCtx(context.Background(), []string{"-o", "table", `exists(status)`}, bytes.NewReader(data), io.Discard, io.Discard)
	}
}
