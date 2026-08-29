# STDLIB.md

Every place `logq` would normally reach for a third-party package, and what
it uses from the Go standard library instead. This ledger is built
incrementally — each row is added in the same commit that introduces the
substitution, not backfilled at the end, so the `git log` itself is the
evidence trail for the rationale below.

| Would-be dependency | logq uses instead | Rationale |
|---|---|---|
| _(entries land starting Phase 1 — first real substitution is `stretchr/testify` at the lexer's test suite)_ | | |

## Disclosures

Any dev-only tooling that never ships in the built artifact is listed here
explicitly, per the rules' dev-dependency disclosure requirement. None yet.
