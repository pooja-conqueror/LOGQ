# STDLIB.md

Every place `logq` would normally reach for a third-party package, and what
it uses from the Go standard library instead. This ledger is built
incrementally — each row is added in the same commit that introduces the
substitution, not backfilled at the end, so the `git log` itself is the
evidence trail for the rationale below.

| Would-be dependency | logq uses instead | Rationale |
|---|---|---|
| `stretchr/testify` | `testing` + table-driven tests + `t.Run` subtests | Go ships a full test framework; a third-party assertion library buys nothing but an import, and zero test dependencies means nothing to disclose under the rules' dev-dependency carve-out either. |
| a hand-rolled duration-unit grammar | stdlib `time.ParseDuration` | The lexer tokenizes a duration literal permissively (digit run + letter-run suffix, e.g. `1h30m`, `-1h`) and keeps it as raw text in the AST; no custom unit-parsing logic exists anywhere. Validating/converting that text happens via `time.ParseDuration` once the evaluator lands (Phase 3+) — a subtraction from the original design, not an addition, and it gets compound-unit support (`1h30m`) for free instead of needing to hand-write it. |
| `agnivade/levenshtein` | Own ~25-line DP edit-distance function (`EditDistance`), collapsed to two rows | Powers "did you mean 'exists'?"-style diagnostics; a whole package for one well-known, easily-hand-written algorithm isn't worth the import. Radius capped at 2 and ties suppressed (per spec) so a wrong guess is never worse than no guess. |
| a hand-rolled regex matcher | stdlib `regexp` (RE2 class), compiled once per query at `Compile` time | `~`/`!~` need real regex matching, and stdlib already provides exactly the right safety property for untrusted log-derived patterns: RE2's automaton-based matching guarantees linear time in input size — no catastrophic backtracking, unlike a naive backtracking engine. Composing it correctly (compile once, reuse per record) is the craft here, not reinventing the matcher. |
| **`tidwall/gjson` — 10,420 known importers on pkg.go.dev, live-verified 2026-08-29** | `encoding/json` (`json.Decoder.Token()`) → a hand-written ordered map + `json.Number` int64/float64 discipline | gjson exists because `encoding/json`'s default `map[string]any` unmarshal is unordered and always decodes numbers to float64 — silently corrupting a large integer field (a snowflake ID, for instance) and discarding the source document's key order. Streaming through `Decoder.Token()` into our own ordered `Record`, with `json.Number` chosen deliberately over the float64 default, gets both properties back from stdlib alone: order preserved, big integers exact. This is the project's single clearest Package Killer case — gjson's core value proposition, replaced with ~150 lines built directly on the decoder gjson itself wraps. |
| the well-known `bufio.Scanner` 64KB `MaxScanTokenSize` trap | `bufio.Reader.ReadSlice`, hand-rolled line splitting | The naive fix (`scanner.Buffer(buf, largerMax)`) only raises the ceiling — a `Scanner` still just stops dead (`Scan()` returns `false`, `Err()` holds `ErrTooLong`) on anything past that new ceiling, silently ending the whole run unless the caller remembers to check `Err()`. Built on `bufio.Reader` directly instead, so one oversized line is skipped, counted, and reading resumes cleanly at the next line — never a silent early stop, never a full-line memory spike either (the oversized remainder is discarded via `ReadSlice`'s buffer-full signal, not first fully read then thrown away). |
| `spf13/cobra` + `pflag` | stdlib `flag` (`flag.NewFlagSet`), short/long aliases bound to the same variable | Two flags deep (`-f`/`--format`, `-o`/`--output`) plus help/version — cobra's full command tree and 30-transitive-dep footprint buys nothing here that `flag.NewFlagSet` with `ContinueOnError` doesn't already give directly: custom usage text, controlled exit codes, no framework-owned `os.Exit` calls to fight. |

## Disclosures

Any dev-only tooling that never ships in the built artifact is listed here
explicitly, per the rules' dev-dependency disclosure requirement. None yet.
