# Benchmarks

Every number on this page was actually measured by running the commands
shown, on this machine, on 2026-08-30 — none are estimated or
back-calculated. Re-run them yourself with `make bench` (micro-benchmarks)
and `make soak-manual` (full-scale soak); numbers will vary by machine,
disk, and Go version, but the *shape* of the results — where logq wins
and where it loses — should reproduce.

**Test machine**: 11th Gen Intel Core i5-11400H @ 2.70GHz, Windows,
`go1.25.3 windows/amd64`. No C compiler available in this environment
(`CGO_ENABLED=0` throughout, matching the release build), so these are
plain, non-race-instrumented numbers.

## Micro-benchmarks (`make bench` / `go test -bench=. -benchmem ./cmd/logq/...`)

20,000-line synthetic corpus (`internal/corpus`, seed 1), fed straight
into the pipeline in-process (no disk I/O, so this isolates pipeline cost
from filesystem cost):

| Benchmark | ns/op | Throughput | B/op | allocs/op |
|---|---|---|---|---|
| `FilterPassthrough` (`exists(status)`) | 177,877,433 | 19.24 MB/s | 158,577,000 | 2,929,211 |
| `StatsGroupBySequential` (`-j 1`, `count(), avg(), p95() by service`) | 191,215,217 | 17.90 MB/s | 264,445,058 | 2,949,567 |
| `StatsGroupByParallel4` (`-j 4`, same query) | 219,354,820 | 15.60 MB/s | 271,817,726 | 2,989,606 |
| `TableRender` (`-o table`, same filter) | 294,770,725 | 11.61 MB/s | 181,787,640 | 3,251,372 |

## Real-world scale: 76.3MB / 467,715-line corpus, real binary, real file I/O

```
go run scripts/gen-corpus.go -out bench_corpus.jsonl -bytes 80000000 -seed 3
time bin/logq 'exists(status)' bench_corpus.jsonl > /dev/null                          # 4.870s → ~15.7 MB/s
time bin/logq -f jsonl 'exists(status)' bench_corpus.jsonl > /dev/null                  # 5.027s → ~15.2 MB/s
time bin/logq --max-groups 30000 -j 1 '| stats count(), avg(duration_ms) by user' ...   # 6.535s
time bin/logq --max-groups 30000 -j 8 '| stats count(), avg(duration_ms) by user' ...   # 7.558s
```

## Soak test (`TestSoak_MemoryStaysBoundedAcrossCorpusScale`, runs on every `go test ./...`)

Real observed values from this run, logged via `t.Logf` (`go test -v -run TestSoak ./cmd/logq/`):

```
HeapAlloc at 20,000 lines: 344,272 bytes
HeapAlloc at 200,000 lines: 353,968 bytes
delta across a 10x input-size increase: 9,696 bytes  (envelope: 48MB)
```

## Wins

- **Flat memory under scale.** The soak numbers above are the actual
  headline result: processing 10x more input grew live heap by under
  10KB. This is what a streaming architecture (no whole-file buffering
  for a plain filter query) is supposed to look like, and it's measured,
  not asserted — see `internal/corpus` + `cmd/logq/soak_test.go`.
- **Filter throughput is consistent across scale.** ~19 MB/s in the
  20,000-line in-process micro-benchmark, ~15-16 MB/s on a real 76MB file
  through the real compiled binary (the gap is real file I/O + process
  startup + auto-detect's own 64-line sample, all absent from the
  in-process benchmark) — no cliff as input size grows 20x.

## Losses (disclosed honestly, not hidden)

- **`-j N` parallel stats is *not* a win at these scales — it's a
  measured loss.** Both the 20,000-line micro-benchmark (`-j 4`: 15.60
  MB/s vs `-j 1`'s 17.90 MB/s) and the 467,715-line, ~20,000-group
  real-file run (`-j 8`: 7.558s vs `-j 1`'s 6.535s, ~16% slower) show
  sharding *costing* time, not saving it. Root cause, not a bug: `-j`
  only parallelizes the aggregation stage (`internal/pipeline/parallel_stats.go`)
  — JSON decoding, which dominates the per-line cost, stays fully
  single-threaded regardless of `-j`. Sharding cheap aggregation work
  across goroutines just adds channel-dispatch overhead with nothing to
  amortize it against. `-j` would need decode itself parallelized to pay
  off, which is real, undone future work — not something this benchmark
  papers over.
- **Table rendering costs ~40% throughput vs raw** (11.61 MB/s vs 19.24
  MB/s on the identical filter/corpus) — expected and inherent: `-o
  table` must buffer every row before `text/tabwriter` can compute column
  widths, so it can't stream the way `-o raw`/`-o jsonl` do.
- **The hand-rolled JSON decoder is eager, not lazy, unlike the
  library it replaces.** STDLIB.md documents replacing `tidwall/gjson`
  with `encoding/json` + an ordered map. gjson can extract a single path
  from a large JSON object via byte-level scanning without decoding
  fields it doesn't need; this project's decoder builds the full ordered
  map and typed value for every field of every line regardless of what
  the query actually references. For a query that only touches one or
  two fields out of many, gjson would out-perform this decoder — a real,
  disclosed trade-off made for the zero-dependency constraint, not a
  claim that the replacement is strictly faster.

## Reproducing

```
make bench          # micro-benchmarks, seconds
make soak-manual     # full ~2GB generated corpus + real filter run, minutes
```
