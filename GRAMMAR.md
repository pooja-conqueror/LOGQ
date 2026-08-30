# GRAMMAR.md

The frozen query-language grammar, truth tables, and semantic rules — the
authoritative reference for `logq 'QUERY' [FILE...]`'s query text. This
document describes what's actually implemented (`internal/lex`,
`internal/query`, `internal/eval`, `internal/agg`, `internal/pipeline`),
not an aspirational future grammar; every rule below is exercised by a
real test.

## 1. Lexical grammar

Every token carries a `{offset, line, col}` position; `col` is a
**rune count**, not a byte count, so multibyte (CJK, emoji, etc.) query
text still gets correct column numbers in error messages.

```
IDENT     = ( "_" | letter ), { "_" | letter | digit } ;
STRING    = "'" { qchar } "'" | '"' { qchar } '"' ;
qchar     = any character except the quote/backslash/newline
          | "\\", ( "\\" | "/" | "'" | '"' | "n" | "t" | "r" ) ;
NUMBER    = [ "+" | "-" ], digit, { digit }, [ ".", digit, { digit } ],
            [ ( "e" | "E" ), [ "+" | "-" ], digit, { digit } ] ;
DURATION  = [ "+" | "-" ], digit, { digit }, [ ".", digit, { digit } ],
            letter, { letter | digit | "." } ;
```

A `NUMBER` immediately followed by a letter run (no whitespace) lexes as
`DURATION` instead — the letters are kept as **raw text** and handed to
`time.ParseDuration` by the parser/evaluator, so compound forms
(`1h30m`, `-1h`) work for free without a hand-rolled unit grammar.

A bare `.` with a following digit (`.5`) or a leading sign with a
following digit (`-1`, `+2.5e3`) also starts a `NUMBER`/`DURATION` scan.

An unterminated string, a raw newline inside a string, or an unknown
backslash escape is a lexical error (`Illegal` token), surfaced by the
parser as a positioned `E-PARSE`.

**Keywords** (case-sensitive, lowercase only) — matched greedily against
what would otherwise be an `IDENT`:

```
and or not exists in true false null
stats by every count count_distinct sum avg min max p50 p95 p99
fields sort asc desc limit
```

**Operators & punctuation**: `== != < <= > >= ~ !~ | , . ( ) [ ]`

## 2. Top-level grammar

```
Query      = [ FilterExpr ], { "|", Stage } ;
```

A query with **no** leading `FilterExpr` still needs the leading `|` —
`| stats count()` means "match every record, then aggregate"; a bare
`stats count()` with no `|` is parsed as an (invalid) `FilterExpr`
starting with the `stats` keyword and rejected.

```
Stage      = FieldsStage | SortStage | LimitStage | StatsStage ;
```

### S-5 — stage ordering

`stats` is the terminal **aggregation** stage, with one explicit
exception: `sort`/`limit` may still follow it (§8.5, top-K over the
groups it produced — see §9 below), reusing the exact same `Sort`/`Limit`
stages an ordinary post-filter record stream uses. `fields` or a second
`stats` may never follow a `stats` stage, whether directly or after an
intervening `sort`/`limit` — a stats group's own output columns are the
only paths a following `sort` can meaningfully target, and re-aggregating
already-aggregated output isn't something this grammar supports.

```
stats count() by url | sort count desc limit 10     ✓ valid
stats count() by url | limit 5                       ✓ valid
stats count() | fields x                             ✗ E-PARSE
stats count() | sort count desc limit 5 | fields x    ✗ E-PARSE
stats count() | stats count()                         ✗ E-PARSE
```

## 3. Filter expression grammar

```
FilterExpr = OrExpr ;
OrExpr     = AndExpr, { "or", AndExpr } ;
AndExpr    = NotExpr, { "and", NotExpr } ;
NotExpr    = "not", NotExpr | Primary ;
Primary    = "(", FilterExpr, ")" | Comparison | ExistsCall ;

Comparison = Operand, CmpOp, Operand
           | Operand, ( "~" | "!~" ), STRING
           | Path, "in", "[", Literal, { ",", Literal }, "]" ;
CmpOp      = "==" | "!=" | "<" | "<=" | ">" | ">=" ;
ExistsCall = "exists", "(", Path, ")" ;

Operand    = Path | Literal ;
Literal    = STRING | NUMBER | DURATION | "true" | "false" | "null" ;
```

`and`/`or` short-circuit (Go's native `&&`/`||`). `not` binds tighter
than `and`/`or` and is right-recursive (`not not x` parses, redundantly).
Parenthesis nesting is capped at 100 — deeper input is a positioned
`E-PARSE`, never a stack overflow, since the recursive-descent parser
checks depth explicitly rather than trusting the runtime's call stack.

A bare `STRING` token in `Operand` position is always a string
**literal**, never a quoted path segment — `"foo" == "bar"` compares two
string literals, not a field named `foo`. The quoted-segment form of a
path is reachable only via a leading `.` (see §4).

`in` requires a `Path` on the left (a literal there is `E-PARSE`) and at
least one literal in the set (S-7 — grammar-enforced, an empty `in [...]`
can't be written at all).

## 4. Path grammar

```
Path    = [ "." ], PathSeg, { ".", PathSeg | "[", INT, "]" } ;
PathSeg = IDENT | STRING | keyword ;
```

A leading `.` is accepted before the first segment — `."http-status"` —
the documented form for a field name an unquoted `IDENT` can't carry
(hyphens, spaces, leading digits).

**Keywords are valid path segments too**, using their literal text —
`sort count desc limit 5` treats `count` as an ordinary field name, not
the `count` keyword, because a path is unambiguously expected in that
position. This is deliberately narrow: it only applies at a call site
that has *already committed* to parsing a path (`fields`'s targets,
`sort`'s target, `stats`'s `by`-clause and function arguments,
`exists(...)`'s argument, and path continuations after `.`/`[`) — the
general filter-expression operand dispatch still requires `IDENT`/`.` to
even attempt a path at all, so `count == 5` as a bare filter is
unaffected (still rejected, since `count` there is genuinely the
keyword). This is what makes a stats output column literally named
`count` usable as an ordinary `sort count desc` target (§8.5).

An out-of-range array index (`[99999999999999]`) is syntactically valid
and clamps at parse time rather than failing — a real record simply
never has that many elements, so it resolves to `MISSING` at eval time,
never a parse error.

## 5. Virtual `ts` path

The single-segment path `ts` is **virtual**: it always means the
record's *resolved* timestamp (§6.1 below), never a literal raw field
happening to be named `ts`. `ts >= -1h` behaves identically whether the
source document's real timestamp field was called `ts`, `timestamp`, or
`@timestamp`.

## 6. Pipeline stages

```
FieldsStage = "fields", Path, { ",", Path } ;
SortStage   = "sort", Path, [ "asc" | "desc" ], "limit", POSINT ;
LimitStage  = "limit", POSINT ;
```

`sort` without a trailing `limit N` is `E-PARSE` — sort's constant-memory
guarantee (a bounded top-N via `container/heap`, never "collect
everything then truncate") is a grammar-enforced fact, not a runtime
convention someone could forget to check (S-2, `POSINT` means `N >= 1`).

`fields p1, p2, ...` projects a record down to just the listed paths — a
path that doesn't resolve is simply omitted from the output, never
manufactured as a stored `MISSING` value (§11.5's jsonl contract:
"MISSING → omitted key" only holds if producers never store one).
Two paths deriving the same output column name is a compile-time
`E-TYPE` (S-8).

```
StatsStage = "stats", StatFn, { ",", StatFn },
             [ "by", Path, { ",", Path } ],
             [ "every", DURATION ] ;
StatFn     = "count", "(", ")"
           | "count_distinct", "(", Path, ")"
           | ( "sum" | "avg" | "min" | "max" ), "(", Path, ")"
           | ( "p50" | "p95" | "p99" ), "(", Path, ")" ;
```

At most one `stats` per query (see S-5, §2). Duplicate `(function, path)`
pairs are rejected at compile time (S-6 — ambiguous output columns).
`every DURATION` must be between 1ms and 365d inclusive (S-1); the raw
duration text is validated once at parse time via `time.ParseDuration`,
then re-parsed by the aggregation engine rather than exporting a
`time.Duration`-shaped AST field.

### Stats output column names

A stats row's columns, in order: the `window` column (only if `every`
was given), then each `by` path's own text (`fields`'s naming rule —
`b.c`, `items[0]`), then each `StatFn`'s derived name — the bare function
keyword for `count()` (just `count`), or that keyword plus an
underscore-joined flattening of its argument path for everything else:
`sum(duration_ms)` → `sum_duration_ms`, `count_distinct(url)` →
`count_distinct_url`, `p95(latency)` → `p95_latency`. This is
deliberately **not** the fuller call-syntax text (`sum(duration_ms)`)
S-6's own duplicate check uses internally — a stats output column must be
a plain identifier a later `sort <col>` can target, and the `Path`
grammar has no syntax for parentheses at all.

## 7. Semantic compile-time rules (S-1 .. S-8)

| Rule | Requirement | Violation |
|---|---|---|
| S-1 | `every DURATION` in [1ms, 365d] | `E-TYPE` |
| S-2 | `sort` must carry `limit N`, `N >= 1` | grammar / `E-TYPE` |
| S-3 | `p*`/`sum`/`avg` targets should be numeric — a non-numeric value is silently skipped at eval time, never fatal | (runtime counter, no compile error) |
| S-4 | `~`/`!~` pattern compiles as RE2 (`regexp.Compile`) at query-compile time | `E-TYPE` |
| S-5 | `stats` terminal except a following `sort`/`limit` (§2) | `E-PARSE` |
| S-6 | duplicate `(stat function, path)` pairs rejected | `E-PARSE` |
| S-7 | `in [...]` set has >= 1 literal (grammar-enforced — can't write an empty one) | — |
| S-8 | two paths deriving the same output column name | `E-TYPE` |

## 8. Three-valued logic

Every value is one of: a real value (Number/String/Bool/Timestamp/
Duration/Array/Object), `Null` (present, explicitly null), or `MISSING`
(a path that resolved to nothing — never stored inside a record, only
ever a `Resolve`/evaluator result).

**M-1.** `MISSING` and `Null` are always distinct — a field absent from a
record is never conflated with one present as JSON `null`.

**M-2.** Every binary operator except `exists()` returns `false` if
either operand is `MISSING` — never an error, never aborts the run.

**M-3.** `==` between two `MISSING` operands is `false` (they aren't
values to compare).

| Operator | LHS `MISSING` | RHS `MISSING` | Both real |
|---|---|---|---|
| `==` / `!=` | `false` / `true`\* | `false` / `true`\* | normal |
| `< <= > >=` | `false` | `false` | normal |
| `~` / `!~` | `false` | n/a (RHS is always a literal) | normal |
| `in [...]` | `false` | n/a | normal |
| `exists(p)` | reports presence directly — the ONE op that reads MISSING as data, not as "false" | | |

\* `!=` of two `MISSING`s is `true` (negation of M-3's `false`), not a
special case — `!=` is implemented as `not(==)` throughout.

`==`/`!=` full truth table (§1.3):

| | `Null` | real value (same kind) | real value (different kind) |
|---|---|---|---|
| `Null` | `true` | `false` | `false` |
| real (same kind) | `false` | structural equality | `false` unless a coercion applies |
| real (different kind) | `false` | `false` unless a coercion applies | `false` unless a coercion applies |

Ordering operators (`< <= > >=`) on `Null`, `Array`, or `Object` are
always `false` — these three kinds have no ordering at all (§1.4), and no
coercion changes that.

## 9. Sanctioned coercions (exactly three — never more)

1. **String ↔ Number** (`internal/eval/coerce.go`): a numeric-looking
   string coerces via `strconv.ParseFloat`, but *stricter* than
   `ParseFloat` alone — underscores (`"1_000"`), `"Inf"`, and `"NaN"` are
   all rejected, since ParseFloat's Go-numeric-literal leniency would let
   surprising values slip into a numeric comparison. Applies to `==`/`!=`
   and ordering alike.
2. **Level ordinals** (§6.2, `internal/eval/coerce.go`): when either side
   of an ordering comparison is a `Path` whose last segment is one of
   `level`, `severity`, `lvl`, `loglevel` (exact match, case-sensitive —
   these are literal field names, not a case-folded convention), both
   operands are resolved through the level table (§10 below) instead of
   compared same-kind. This check runs **before** the same-kind fast
   path — `level >= "warn"` has two same-kind Strings on paper, but the
   ordinal check fires first, so it's `50 >= 40` (ordinal), not
   `"error" >= "warn"` (alphabetical — which would give the wrong
   answer). Only applies to ordering, never `==`/`!=` (a level field's
   own literal representation already answers equality directly). An
   unrecognized token falls back to byte-wise string comparison
   (documented, surprise-free — never an error).
3. **Timestamp ± Duration**: if one operand is a `Timestamp` and the
   other a `Duration` `d`, the Duration side becomes `now() + d` before
   comparing — `ts >= -1h` becomes `ts >= (now - 1h)`. `now()` is the
   process's frozen start-of-run wall clock in batch mode (§13,
   determinism) — never re-read mid-run.

Coercion failure is never an error — it falls through to `false`
(ordering) or plain structural inequality (`==`), plus a
`skipped_nonnumeric{fn,field}`-style counter for the aggregation
functions specifically (§11).

## 10. Level ordinal table

Built-in (`internal/eval/coerce.go`), matched case-insensitively:

| Name | Ordinal |
|---|---|
| `trace` | 10 |
| `debug` | 20 |
| `info` | 30 |
| `warn` / `warning` | 40 |
| `error` | 50 |
| `fatal` | 60 |

`--levels name=NUM,name2=NUM2,...` extends/overrides this table at
startup: a name the flag mentions replaces its ordinal (or adds a new
one entirely, e.g. `critical=55`); every name it doesn't mention keeps
its built-in ordinal. Override names are lowercased before insertion,
matching the table's own case-insensitive lookup.

## 11. Total order (§1.4)

Used by `< <= > >=`, `sort`, and top-K tie-breaking. Within the same
kind only — cross-kind is always `Uncomparable` (â‡’ `false` for every
ordering operator, no coercion changes it):

| Kind | Order |
|---|---|
| Number | numeric (int/float compared as numbers, not by representation) |
| String | byte-wise lexicographic |
| Bool | `false < true` |
| Timestamp | chronological |
| Duration | nanosecond-chronological |
| Null, Array, Object | `Uncomparable` — no ordering exists |

`sort`: **`MISSING` sorts last, in BOTH directions** — stated twice in
this doc because it's the one rule everyone assumes is direction-
dependent and gets wrong. `asc` and `desc` both put every `MISSING` key
after every real value; the direction only reorders the real values.
Ties (equal keys, or two `MISSING`s) resolve by original arrival order —
`sort` is stable.

## 12. Timestamp resolution (§6.1)

**Field priority** (first *resolvable* candidate wins, not just the
first present):

```
ts, time, timestamp, @timestamp, t, eventTime
```

**Parse ladder** (first successful layout wins), for a String value:

```
RFC3339Nano → RFC3339 → "2006-01-02T15:04:05" (naive) →
"2006-01-02 15:04:05.999999999" (naive) → "2006/01/02 15:04:05" (naive) →
RFC1123 → RFC1123Z → "02/Jan/2006:15:04:05 -0700" (nginx/apache combined)
→ numeric epoch heuristic
```

"Naive" layouts (no zone offset in the source text) are interpreted in
`--tz`'s location; the rest carry their own offset/zone and parse as-is.

A Number value (JSON numbers commonly hold raw epoch values) skips the
string ladder and goes straight to the epoch heuristic: `|n| < 1e11` is
Unix seconds, `< 1e14` is Unix milliseconds, larger fails. Negative
values (pre-1970) work directly — epoch time has no zone ambiguity. A
fractional Number never resolves (the heuristic is integer-only).

A record with no resolvable timestamp at all has `ts` evaluate to
`MISSING` — normal three-valued handling applies, no special case,
*except* under an explicit `--since`/`--until` bound, where such a
record is dropped and counted rather than silently passed through.

## 13. Event-time windowing (§8.1, `every DURATION`)

Bucket assignment differs by window size, and this distinction is
load-bearing (not just an implementation detail):

- **`every` < 24h**: buckets anchor to the raw **Unix epoch instant**.
  Bucket index = `floor((ts − epoch) / D)`. Timezone-invariant by
  construction — a 1h bucket in `Asia/Kolkata` (UTC+5:30) does **not**
  align to local `:00` wall-clock minutes; this is a deliberate,
  spec-stated choice; `--tz` plays no part at all for sub-day windows.
- **`every` >= 24h**: buckets align to **local civil-calendar days** in
  `--tz`, not to raw 24h Duration multiples of an instant. A naive
  "anchor instant + `idx*24h` Duration" scheme would silently break on a
  DST transition day: adding a Duration operates on absolute time, not
  wall-clock days, so on a 23-hour spring-forward day it would land one
  hour into the *next* civil day rather than at that day's real
  midnight — misassigning that day's own late-arriving records. The
  correct approach derives the bucket's calendar date directly from
  `ts` in `--tz`, counts whole calendar days from a fixed 1970-01-01
  reference via a UTC-midnight difference (exact — UTC has no DST, so
  every calendar day there is exactly 24h), and reconstructs the
  bucket's start via `time.Date` in `--tz` — which Go resolves to the
  zone's correct absolute instant for that specific date. A multi-day
  window (`every 7d`) still counts whole calendar days per bucket, from
  that same fixed reference — not from whichever date the query happens
  to run on.

Bucket label: the bucket's start instant, formatted RFC3339 in `--tz`.

Records whose timestamp never resolves land in a synthetic `(no-ts)`
row instead of any real bucket — sorted first (§15). Records **do**
still separate by their own `by`-tuple within the `(no-ts)` partition
(a distinct row per distinct `by`-tuple among the no-timestamp records,
not one single blob) — a deliberate reading of the spec's "a synthetic
row," since collapsing group-by granularity specifically for
timestamp-less records would otherwise be a silent, surprising loss of
information a user asked for via `by`.

## 14. Aggregation functions (§8.4)

| Function | Algorithm | Memory | Starved output |
|---|---|---|---|
| `count()` | `++` | O(1) | `0` |
| `sum(p)` | int64 fast path, `math/big.Int` fallback on overflow, switches to float64 permanently on first float | O(1) | `0` |
| `avg(p)` | Welford's online mean (1962) — numerically stable, no naive sum/n drift | O(1) | `(none)` |
| `min(p)` / `max(p)` | running extremum via the total order (§11); non-orderable kinds never participate, including as the "first value seen" | O(1) | `(none)` |
| `count_distinct(p)` | salted FNV-1a (fixed 64-bit salt, **not** a random per-process seed — random would break batch-mode determinism at the cap-saturation collision boundary) into a capped hash set, cap 65536 | O(min(distinct, 65536)) | `0`; `>=65536` once the cap is exceeded |
| `p50(p)` / `p95(p)` / `p99(p)` | nearest-rank (`rank = ceil(q·n)`, 1-indexed, clamped) on a sorted sample. Exact under `--max-sample` (default 100000): the sample holds every value. Beyond it: Vitter's **Algorithm L** reservoir (1985) — explicitly not "Algorithm R" (Waterman/Knuth, one random draw per item); L computes a skip-gap directly for O(k·(1+log(N/k))) draws total. Fixed seed (`--seed`, default 0) | O(cap) | `(none)`; a trailing `*` on the rendered cell once approximate |

All numeric-input functions **skip** non-numeric values silently (never
fatal) — `sum`/`avg`/percentiles over a mixed field type just ignore the
values that don't fit. `min`/`max` similarly never lock onto a
non-orderable kind (`Null`, `Array`, `Object`, `MISSING`) as their
"first value," which would otherwise permanently block every later,
genuinely orderable value from ever being considered.

Approximateness for percentiles is a genuinely **per-group** property
(each group's reservoir caps independently) — rendered inline on the
value itself (`"142*"`) rather than as a blanket column-wide flag, since
two rows under the same `p95` column can differ in exactness depending
on how many records landed in each group.

## 15. Group keys and cardinality guard (§8.2, §8.3)

Group keys encode the resolved `by`-tuple (plus a leading virtual
window-bucket value when `every` is active) as one string: `MISSING` and
`Null` get distinct sentinel bytes, disjoint from any real value's own
text encoding — so `(missing)`, `(null)`, and a real value that happens
to render as the empty string are three genuinely different groups,
never collapsed into two. Object/Array group values get their own
recursive, order-independent (sorted-key) encoding rather than a lossy
display placeholder, so two structurally different objects never
collide into the same group the way they could if merely displayed.

**Cardinality guard**: `--max-groups` (default 10000). Once that many
distinct group keys are tracked, any further *new* key collapses into a
single shared `(other)` row — a repeat of an already-tracked key still
updates its own group normally, even after the cap is hit. `(other)`'s
`by`/`window` columns render as literal `(other)` text; every aggregate
function still computes normally over `(other)`'s merged records
**except** `count_distinct`, which reports the empty-set marker `∅`
rather than a real (and misleading, since it would conflate distinct
values across originally-separate groups) merged count.

## 16. Top-K over aggregate groups (§8.5)

`stats ... | sort <col> desc | limit K` bounds the output to the top K
groups by any stats output column — reusing the same bounded-heap `Sort`
stage (`container/heap`, O(K) memory regardless of total group count)
an ordinary post-filter record stream uses, since a stats row is just
another record flowing through the same pipeline once `stats` has
emitted it. No second, parallel top-K implementation exists anywhere in
the codebase for this case.

## 17. Canonical output ordering (§15, determinism)

Batch-mode output is byte-identical for identical (input bytes, query,
flags, version):

1. `now()` frozen once at process start.
2. Stats rows: real groups sorted **byte-wise ascending** by group key
   (Go's native string `<` is already byte-wise) — which, given
   `MISSING`'s sentinel byte and `Timestamp`'s RFC3339 text encoding,
   also yields "`(no-ts)` first, then buckets chronologically" for free,
   with no separate sort pass. `(other)` always sorts last, unconditionally.
   `MISSING`/`Null` sort before real values within their own group-column
   position (inherited from the sentinel-byte encoding).
3. Reservoir seed fixed (`--seed`, default 0).

## 18. Error codes and exit codes

`E-PARSE` and `E-TYPE` are real, literal prefixes printed by every parse/
compile error today (`ParseError`/`CompileError`'s own `Error()`
methods, e.g. `1:7: E-PARSE: ...`) — these carry a real query-text
position, which is what the full `<file>:<line>:<col>: <CODE>: <message>`
taxonomy format is shaped around. `E-DATA`/`E-IO`/`E-USE` (the spec's own
code name — not "E-USAGE") cover CLI/runtime failure classes that don't
have a query-text position to report in the first place (a bad flag
value, an unreadable file, an aborted run under `--on-error stop`); the
CLI already maps every one of them to the correct exit code (1/3/4
respectively) and prints a clear `logq: ...` message, just not yet with
this literal short-code prefix on that message text — listed here for
the exit-code mapping and taxonomy completeness, not as a claim the
three-letter code itself appears verbatim in today's output.

| Code | Meaning |
|---|---|
| `E-PARSE` | Lexical or grammatical failure — malformed query syntax (prefix printed) |
| `E-TYPE` | Semantic/compile-time failure on an otherwise well-formed query — S-1, S-4, S-5, S-6, S-8, an invalid regex pattern (prefix printed) |
| `E-DATA` | A data-level failure during a run — a malformed or oversized line under `--on-error stop` (§12.3), or mid-stream gzip corruption |
| `E-IO` | A file/stream open or read/write failure |
| `E-USE` | Invalid CLI invocation — bad flag value, missing query argument |

| Exit code | Meaning |
|---|---|
| 0 | Success |
| 1 | Usage error (bad flags, missing query) |
| 2 | Query compile failure (`E-PARSE`/`E-TYPE`) — always before any I/O |
| 3 | Data-strict failure under `--on-error stop` |
| 4 | I/O failure |
| 130 | Interrupted (SIGINT/SIGTERM) |

## 19. CLI limits reference

| Flag | Default | Ceiling | Enforced |
|---|---|---|---|
| `--max-groups` | 10000 | — (must be >= 1) | §15 above |
| `--max-sample` | 100000 | — (must be >= 1) | §14 above |
| `--seed` | 0 | any `int64` | §14 above |
| `--max-depth` | 32 | — (must be >= 1) | JSON object/array nesting |
| `--max-line` | 1MB | 16MB | oversize lines skipped whole + counted, never truncated |
| `--max-query` | 8192 chars | 65536 chars | checked before any lexing — an over-length query is a fast, positioned `E-PARSE`, never a cost proportional to its own size |
| `--levels` | (built-in table) | — | comma-separated `name=NUM` pairs, §10 |

Every limit above rejects an out-of-range flag value itself as a usage
error (exit 1), before any I/O — consistent with `-f`/`-o` validation.

## 20. `--on-error` and the run-wide counter summary (§12.3)

`--on-error skip|warn|stop` (default `warn`) governs exactly two event
classes: a malformed line (decode failure) and an oversized line
(exceeds `--max-line`). Every other counter below is always just
counted, regardless of `--on-error`'s mode — the mode only changes
whether/how malformed and oversized lines specifically get treated:

- `skip`: counted internally, but the end-of-run summary line is
  suppressed entirely.
- `warn` (default): counted, and the summary line prints — the "count,
  continue" behavior §12.3 calls the default.
- `stop`: aborts on the very FIRST malformed or oversized line, exit 3.
  The triggering line's own decode error is printed (its "first offender
  printed" — see §18's `E-DATA` row). Everything matched *before* that
  point has already been emitted; nothing after it is read at all.

An unparseable `ts` candidate is **never** fatal, under any
`--on-error` mode — §12.3's own "time fields aren't errors."

The run-wide counters (`internal/summarize.Counters`), each folded into
one stderr line at the end of a batch-mode run:

| Counter | Meaning |
|---|---|
| `malformed` | decode failure (bad JSON, unterminated logfmt quote, nesting past `--max-depth`, ...) |
| `oversized` | a line beyond `--max-line`, skipped whole |
| `dup key(s)` | jsonl fields repeated on one line (last wins) |
| `dropped by --since/--until` | D-1: dropped by an explicit time-window bound |
| `ts unparsed` | a timestamp candidate field was present but failed to parse (§12.3) |
| `groups overflowed to (other)` | stats records collapsed into `(other)` past `--max-groups` (§8.3) — summed across all `-j` shards when parallel |

Watch mode accumulates the same counters internally (so `--on-error`'s
malformed/oversized handling behaves identically there) but doesn't
print the summary line — its own `SNAPSHOT`/rotation stderr messages
already serve that "what's happening" role, and a session with no
natural end has no natural "end of run" moment to print one at.

Not yet wired: the finer-grained `skipped_nonnumeric{fn,field}`
per-aggregate-function counters §8.4 also mentions — would need
threading through every aggregator wrapper in
`internal/pipeline/stats.go`; a real, separable piece of work, not
half-built silently.

## 21. Color (`-C`/`--no-color`, `NO_COLOR`)

Three ANSI SGR colors, applied only to stderr diagnostic *messages*
(never stdout's data output, on any `-o` format): red for the counter
summary when `malformed > 0`, yellow for `PARTIAL`/watch-stopped,
cyan for watch mode's `SNAPSHOT` marker. Resolved once per run
(`render.ShouldColor`), in this precedence order:

1. `-C`/`--no-color` — always wins outright.
2. [`NO_COLOR`](https://no-color.org) — presence disables color, any
   value at all, including empty; truthiness is never checked.
3. Otherwise, color is used only when stderr is an actual terminal
   (`os.ModeCharDevice` on the underlying `*os.File` — stdlib-only, no
   isatty binding). Redirected/piped stderr is never colored, regardless
   of the two flags above.
