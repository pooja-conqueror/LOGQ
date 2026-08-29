# STDLIB.md

Every place `logq` would normally reach for a third-party package, and what
it uses from the Go standard library instead. This ledger is built
incrementally — each row is added in the same commit that introduces the
substitution, not backfilled at the end, so the `git log` itself is the
evidence trail for the rationale below.

| Would-be dependency | logq uses instead | Rationale |
|---|---|---|
| `stretchr/testify` | `testing` + table-driven tests + `t.Run` subtests | Go ships a full test framework; a third-party assertion library buys nothing but an import, and zero test dependencies means nothing to disclose under the rules' dev-dependency carve-out either. |

## Disclosures

Any dev-only tooling that never ships in the built artifact is listed here
explicitly, per the rules' dev-dependency disclosure requirement. None yet.
