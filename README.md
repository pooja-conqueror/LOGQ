# logq

Query gigabytes of logs with a one-line expression — filters, time windows,
percentiles — over files bigger than RAM, from one static binary built from
one empty manifest.

```
logq [flags] 'QUERY' [FILE|- ...]
```

`logq` speaks JSONL, logfmt, and plain text natively, treats a missing field
as distinct from a JSON `null`, and gives `level` fields ordinal comparisons
(`level >= "warn"` just works). Every format, and the query language itself,
is hand-written against the Go standard library — no parsing, CLI, or
aggregation package anywhere in the dependency graph.

**Status:** under active development. This README is being filled in
incrementally, commit by commit, alongside the code — sections below are
placeholders until the corresponding feature lands.

## Zero Dependency Hackathon 2026

- **Track:** B — Parsers & Data Formats
- **Language:** Go, stdlib only (`go.mod` has no `require` block, ever)
- See `STDLIB.md` for every package this project would normally have pulled
  in, and what it uses from the standard library instead.

## Install / Build

```
go build -trimpath -buildvcs=false -o bin/logq ./cmd/logq
```

(A `Makefile` target wraps this: `make build`.)

## Usage

```
logq --version
logq --help

# filter JSONL from a file, exact-match and numeric comparisons
logq 'level == "error"' app.jsonl
logq 'status >= 500' app.jsonl

# a string field compares numerically against a number literal — the
# sanctioned string<->number coercion, not a type error
logq 'status >= 500' app.jsonl        # matches status: "502" too

# level fields compare ordinally, not alphabetically
logq 'level >= "warn"' app.jsonl      # "error" (50) >= "warn" (40) -> true

# nested field access and array indexing
logq 'url.path == "/api/x"' app.jsonl
logq 'items[0] == "first"' app.jsonl

# boolean combinators, parens, negation
logq 'level == "error" and not exists(handled)' app.jsonl
logq '(a == 1 or b == 2) and c == 3' app.jsonl

# regex match against a string field
logq 'msg ~ "auth failed"' app.jsonl

# membership test
logq 'status in [500, 502, 503]' app.jsonl

# read from stdin (no file args, or an explicit "-")
cat app.jsonl | logq 'level == "error"'

# re-serialize matches as JSON instead of raw passthrough
logq -o jsonl 'level == "error"' app.jsonl

# logfmt and plain text work too — auto-detected, or forced explicitly
logq 'level == "error"' app.log            # auto-detects jsonl/logfmt/plain
logq -f logfmt 'level == "error"' app.log
logq -f plain 'msg ~ "auth failed"' app.log

# gzip-compressed sources are decompressed transparently, any format
logq 'level == "error"' app.jsonl.gz

# "ts" is a virtual field: it always means the record's resolved
# timestamp (from whichever of ts/time/timestamp/@timestamp/t/eventTime
# actually parsed), never a literal field that merely happens to be
# named "ts" — compare it with a duration, relative to the run's frozen
# "now" (never a raw timestamp string — there's no timestamp-literal
# syntax in the language)
logq 'level == "error" and ts >= -1h' app.jsonl
logq 'exists(ts)' app.jsonl

# --since/--until drop records outside a time window; a record with no
# resolvable timestamp at all is dropped too, once either flag is set
logq --since 2026-08-29T00:00:00Z 'level == "error"' app.jsonl
logq --until now --since -24h 'exists(ts)' app.jsonl

# --tz interprets naive (no zone offset) timestamps in a given IANA
# zone; the zone database is embedded in the binary (time/tzdata) —
# works even on a host with no system tzdata installed at all
logq --tz America/New_York 'exists(ts)' app.jsonl

# -o table / -o csv: a human-scannable table (numeric columns
# right-aligned, missing fields shown as "(missing)") or RFC 4180 CSV
# (missing AND null both render as an empty cell — use -o jsonl instead
# if you need to keep that distinction)
logq -o table 'level == "error"' app.jsonl
logq -o csv 'level == "error"' app.jsonl

# pipeline stages, chained left to right: project fields, then sort
logq -o jsonl 'level == "error" | fields ts, msg' app.jsonl
logq -o jsonl 'status >= 500 | sort status desc limit 10' app.jsonl

# limit (bare, or via sort ... limit) stops reading input as soon as it's
# satisfied — including skipping any FURTHER files entirely, since the
# pipeline is one shared instance across the whole run, not per file
logq -o jsonl 'exists(n) | limit 5' a.jsonl b.jsonl c.jsonl

# once any stage runs, -o raw's byte-for-byte guarantee no longer makes
# sense (a stage may have transformed the record entirely) — it falls
# back to jsonl serialization of the final record instead, uniformly:
logq 'level == "error" | fields msg' app.jsonl   # prints jsonl, not raw bytes

# stats: group-by aggregation, one output row per group, sorted by group
# key ascending. "every" adds an event-time window (civil-day/DST-safe
# for windows >= 24h) as an extra grouping dimension, bucket first. A
# leading "|" with no filter before it means "match every record" —
# same rule any other stage-only pipeline follows.
logq 'status >= 500 | stats count(), p95(duration_ms) by url.path' app.jsonl
logq '| stats count() by service every 1h' app.jsonl

# top-K over aggregate groups: stats stays the terminal AGGREGATION
# stage, but sort/limit may still follow it, reusing the same bounded-
# heap Sort/Limit stages an ordinary record stream uses — never
# materializing more than the top K groups.
logq '| stats count() by url.path | sort count desc limit 10' app.jsonl
logq 'level == "error"' app.jsonl                 # no stages: still true byte-verbatim raw
```

`-f`/`--format` accepts `auto` (default — samples the first 64 non-empty
lines of *each source independently* and picks jsonl/logfmt/plain
deterministically; a single line that doesn't fit disqualifies a format
outright, no fuzzy scoring) or one of `jsonl`/`logfmt`/`plain` forced
explicitly. `-o`/`--output` accepts `raw`/`jsonl`/`table`/`csv`. Anything
else fails fast with a clear error rather than silently misbehaving.

Auto-detection is genuinely all-or-nothing per the spec: if even one line
in the sampled window doesn't fit a format, the whole source falls through
to the next one in the cascade (jsonl → logfmt → plain). A source that's
mostly clean JSONL with one corrupted line among the first 64 will
therefore be detected as plain text, not jsonl-with-one-bad-line — force
`-f jsonl` explicitly if that's not what you want.

Results go to stdout only. Diagnostics (a query compile error, a per-run
malformed-line count) go to stderr, never mixed into stdout.

## Query Language

The full frozen EBNF, truth tables, and windowing semantics land in
`GRAMMAR.md` as a dedicated reference doc shortly. In the meantime, both
halves of the language are fully implemented and tested: the filter half
(comparisons, `and`/`or`/`not`, `exists()`, `in [...]`, regex match,
nested paths — see `internal/query/parser.go` and `internal/eval/eval.go`)
and the pipeline-stage half (`fields`, `sort`, `limit`, `stats` — see
`internal/query/parser.go` and `internal/pipeline/`).

Three-valued logic is the one semantic worth calling out here explicitly: a
field that's absent from a record (`MISSING`) is distinct from a field
present with JSON `null`. Every comparison involving `MISSING` evaluates to
`false` — never an error — so a query never aborts partway through a file
just because one record has a different shape than the rest.

## Honest Limits

Reflects what's actually implemented as of this commit, not the eventual
full spec:

- **Input formats:** JSONL, logfmt, plain text, gzip transparency, and
  auto-detection are all implemented now (Phase 5 complete).
- **Output formats:** all four are implemented — `raw` (byte-verbatim
  passthrough), `jsonl` (re-serialized, key order preserved), `table`
  (aligned text via `text/tabwriter`, numeric columns right-aligned), and
  `csv` (RFC 4180). `table`/`csv` buffer every matched record — unlike
  `raw`/`jsonl`, they can't stream row-by-row, since the header must print
  before any row and depends on having seen the records first. For a
  truly huge passthrough result this means holding the whole result set
  in memory before printing anything; their natural use case is
  already-bounded aggregate output — e.g. `stats` results — where this
  doesn't matter.
- **Pipeline stages:** `fields`, `sort`, `limit`, and `stats` are fully
  wired now. `stats` supports `count()`, `count_distinct()`, `sum()`,
  `avg()`, `min()`, `max()`, `p50()`/`p95()`/`p99()`, an optional `by
  <paths>` grouping clause, and an optional `every DURATION` event-time
  window (civil-day/DST-safe alignment for windows >=24h — see
  `internal/agg/windowing.go`). `stats` is still the terminal aggregation
  stage — `fields` or a second `stats` can't follow it — but `sort`/`limit`
  explicitly can, reusing the same bounded-heap `Sort`/`Limit` stages
  unchanged for top-K over aggregate groups, e.g.
  `stats count() by url.path | sort count desc limit 10`. A group-key
  cardinality guard (`--max-groups`, default 10000) collapses overflow
  keys into a single `(other)` row; `count_distinct` inside `(other)`
  reports an empty-set marker rather than a merged (and misleading)
  count. `limit`/a bounded `sort ... limit N` even stop reading further
  input (including later files entirely) once satisfied, since the
  pipeline is one shared instance across the whole run, not rebuilt per
  file. `p50`/`p95`/`p99` are exact under `--max-sample` (default
  100000), approximate beyond it with a `*`-marked cell, drawn from a
  fixed-seed (`--seed`, default 0) reservoir so approximate output stays
  reproducible across runs. `--levels name=NUM,...` extends/overrides the
  level-ordinal table (§6.2) used by `level >= "warn"`-style comparisons.
- **Time features:** `--tz`/`--since`/`--until` and real timestamp
  auto-detection (via the field-priority ladder, exposed as the virtual
  `ts` path) are implemented now (Phase 6 complete). `now` is frozen once
  at process start — batch mode only; there's no watch mode yet for the
  "re-evaluate `now` per poll tick" distinction to matter.
- **Watch mode, signals, parallelism:** not yet (Phase 9).
- **Error/summary model:** malformed lines are skipped and counted with a
  single stderr line at the end of a run; the full per-field counter
  breakdown (`--on-error`, coercion-miss counts, etc.) lands in Phase 10.
- **Depth/size/query limits:** JSON nesting (default 32, `--max-depth`),
  oversized lines (default 1MB, up to 16MB, `--max-line`), and query text
  length (default 8192 characters, up to 65536, `--max-query`) are all
  configurable now. A line or query exceeding its limit is skipped/
  rejected and counted — never silently truncated.

Nothing above is hidden or silently degraded — every one of these either
fails with a clear, explicit "not yet supported" message or is documented
here plainly.

## License

MIT — see `LICENSE`.
