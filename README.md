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

_Filled in as flags and the query language land._

```
logq --version
logq --help
```

## Query Language

_EBNF, semantics, and examples land in `GRAMMAR.md` once the parser and
evaluator are built (Phases 2–3 of the build plan)._

## Honest Limits

_Updated as features are cut or scoped down. Nothing hidden — if a
capability described in an earlier draft of this README doesn't ship, it
will be listed here instead of silently disappearing._

## License

MIT — see `LICENSE`.
