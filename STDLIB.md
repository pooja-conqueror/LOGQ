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

## Disclosures

Any dev-only tooling that never ships in the built artifact is listed here
explicitly, per the rules' dev-dependency disclosure requirement. None yet.
