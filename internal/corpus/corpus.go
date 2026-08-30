// Package corpus generates a deterministic, synthetic stream of
// realistic-shaped JSONL log lines without ever materializing the
// whole corpus in memory: Generator is an io.Reader that synthesizes
// each line on demand as it's pulled. That single streaming design is
// reused two ways — scripts/gen-corpus.go drains it to a file on disk
// for a real, full-scale (default 2GB) manual soak run, and
// cmd/logq/soak_test.go feeds it directly into logq's own pipeline for
// an automated, CI-sized soak run — with no disk I/O and no giant
// in-memory buffer either way.
package corpus

import (
	"fmt"
	"io"
	"math/rand"
)

var services = []string{"checkout", "auth", "search", "billing", "notify", "gateway", "inventory", "recommend"}
var levels = []string{"debug", "info", "warn", "error"}
var paths = []string{"/api/v1/orders", "/api/v1/users", "/api/v1/cart", "/api/v1/search", "/healthz"}
var messages = []string{
	"request completed",
	"upstream timeout",
	"cache miss, falling back to origin",
	"rate limit exceeded",
	"connection reset by peer",
	"retrying after backoff",
}

// Generator streams deterministic synthetic JSONL log lines. The same
// seed always produces the same sequence, so a soak run is reproducible
// — useful for comparing memory/throughput numbers across changes.
type Generator struct {
	rng      *rand.Rand
	n        int64
	total    int64 // 0 means unbounded — caller controls how much to read
	buf      []byte
	tsMillis int64

	// OnLine, if set, is called synchronously with the 1-based index of
	// each line just before it's handed to the reader — lets a caller
	// (the soak test) sample runtime.MemStats at exact, reproducible
	// points in the stream without any extra goroutine or ticker.
	OnLine func(n int64)
}

// NewGenerator returns a Generator that yields exactly totalLines lines
// (totalLines <= 0 means unbounded — Read never returns io.EOF on its
// own; the caller decides when to stop reading).
func NewGenerator(seed int64, totalLines int64) *Generator {
	return &Generator{
		rng:      rand.New(rand.NewSource(seed)),
		total:    totalLines,
		tsMillis: 1_735_689_600_000, // 2025-01-01T00:00:00Z, arbitrary fixed epoch
	}
}

func (g *Generator) Read(p []byte) (int, error) {
	if len(g.buf) == 0 {
		if g.total > 0 && g.n >= g.total {
			return 0, io.EOF
		}
		g.n++
		if g.OnLine != nil {
			g.OnLine(g.n)
		}
		g.buf = g.nextLine()
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

// nextLine synthesizes one realistic log record. Field shape (ts,
// level, service, path, status, bytes, duration_ms, user, msg)
// deliberately mirrors what a real HTTP-service access/error log looks
// like, and what the aggregation engine's own tests already exercise
// (count/sum/avg/percentiles by service, windowed by ts) — so a soak
// run against this corpus is exercising the same code paths a real
// query would.
func (g *Generator) nextLine() []byte {
	g.tsMillis += int64(g.rng.Intn(500))
	level := levels[weightedLevel(g.rng)]
	status := 200
	switch level {
	case "warn":
		status = 429
	case "error":
		status = 500 + g.rng.Intn(4)
	}
	return fmt.Appendf(nil,
		`{"ts":%d,"level":%q,"service":%q,"path":%q,"status":%d,"bytes":%d,"duration_ms":%d,"user":"u%05d","msg":%q}`+"\n",
		g.tsMillis,
		level,
		services[g.rng.Intn(len(services))],
		paths[g.rng.Intn(len(paths))],
		status,
		g.rng.Intn(65536),
		g.rng.Intn(2000),
		g.rng.Intn(20000),
		messages[g.rng.Intn(len(messages))],
	)
}

// weightedLevel skews toward info/debug, matching a real service's log
// mix far more than a uniform 1-in-4 split would — most requests
// succeed quietly.
func weightedLevel(rng *rand.Rand) int {
	switch r := rng.Intn(100); {
	case r < 55:
		return 0 // debug
	case r < 90:
		return 1 // info
	case r < 97:
		return 2 // warn
	default:
		return 3 // error
	}
}
