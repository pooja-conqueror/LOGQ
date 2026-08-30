//go:build ignore

// gen-corpus writes a large, deterministic, realistic-shaped synthetic
// JSONL log file to disk — the full-scale (default 2GB) counterpart to
// the smaller in-memory corpus cmd/logq/soak_test.go generates
// automatically on every `go test ./...` run. This one is deliberately
// NOT wired into `go test` or CI: writing 2GB and then reading it back
// through logq takes real wall-clock time unsuitable for an inner dev
// loop, so it's a separate, manual, `go run` invocation — see `make
// soak-manual`.
//
// Usage:
//
//	go run scripts/gen-corpus.go -out corpus.jsonl -bytes 2147483648
//	go build -o bin/logq ./cmd/logq && bin/logq 'level == "error"' corpus.jsonl -o table
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/corpus"
)

func main() {
	out := flag.String("out", "corpus.jsonl", "output file path")
	bytesTarget := flag.Int64("bytes", 2*1024*1024*1024, "approximate target size in bytes (default 2GB)")
	seed := flag.Int64("seed", 1, "PRNG seed — same seed always reproduces the same corpus")
	flag.Parse()

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-corpus:", err)
		os.Exit(1)
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)
	gen := corpus.NewGenerator(*seed, 0) // unbounded — this loop decides when to stop

	var written int64
	start := time.Now()
	lastReport := start
	buf := make([]byte, 32*1024)
	for written < *bytesTarget {
		n, rerr := gen.Read(buf)
		if n > 0 {
			nw, werr := w.Write(buf[:n])
			written += int64(nw)
			if werr != nil {
				fmt.Fprintln(os.Stderr, "gen-corpus: write:", werr)
				os.Exit(1)
			}
		}
		if rerr != nil && rerr != io.EOF {
			fmt.Fprintln(os.Stderr, "gen-corpus: generate:", rerr)
			os.Exit(1)
		}
		if time.Since(lastReport) > 2*time.Second {
			fmt.Fprintf(os.Stderr, "gen-corpus: %.1f MB written (%.1f%%)\n",
				float64(written)/(1<<20), 100*float64(written)/float64(*bytesTarget))
			lastReport = time.Now()
		}
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-corpus: flush:", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "gen-corpus: done — %s, %.1f MB, %s, seed=%d\n",
		*out, float64(written)/(1<<20), time.Since(start).Round(time.Millisecond), *seed)
}
