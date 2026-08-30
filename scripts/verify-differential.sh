#!/usr/bin/env bash
# verify-differential.sh — opt-in QA harness comparing logq's filter
# semantics against jq (github.com/jqlang/jq) as an independent oracle.
# Not part of the required test suite or the zero-dependency proof: jq
# is an external CLI tool, invoked as a subprocess, never imported into
# the Go module — disclosed in STDLIB.md's Disclosures section, and
# deliberately never referenced from `make build`, `make test`, or
# .github/workflows/ci.yml. Run it directly, or via `make
# verify-differential`.
#
# Scope is deliberately narrow: exists()/==/>= over a handful of small
# fixture records, cross-checked against the equivalent jq `select(...)`
# expression, canonicalized through `jq -cS .` on both sides so key
# ordering (which logq preserves and jq's -S sorts away) doesn't produce
# a false mismatch. This isn't meant to validate stats/percentiles/
# windowing — jq has no equivalent primitives for those, so there's no
# independent oracle to differential-test them against; the golden and
# chaos suites are what cover that ground.
set -euo pipefail

if ! command -v jq >/dev/null 2>&1; then
	echo "verify-differential: jq not found on PATH — skipping (optional local QA tooling, not part of the zero-dependency proof or required test suite)"
	exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/logq"
if [ ! -x "$BIN" ]; then
	echo "verify-differential: building logq..."
	(cd "$ROOT" && go build -o bin/logq ./cmd/logq)
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT
FIXTURE="$TMPDIR/fixture.jsonl"
cat >"$FIXTURE" <<'EOF'
{"name":"alice","status":200,"bytes":512}
{"name":"bob","status":404,"bytes":128}
{"name":"carol","status":500,"bytes":2048}
{"name":"dave","bytes":64}
{"name":"erin","status":200,"bytes":0}
EOF

# logq query | jq oracle expression — MISSING fields (dave has no
# "status") must behave as "false on every op except exists()" on the
# logq side; the jq side models that explicitly via `// -1` defaults
# rather than relying on jq's own (different) null-handling semantics.
CASES=(
	'exists(status)|select(has("status"))'
	'status == 200|select(.status == 200)'
	'status >= 400|select((.status // -1) >= 400)'
	'bytes > 100|select((.bytes // -1) > 100)'
)

fail=0
for c in "${CASES[@]}"; do
	logq_query="${c%%|*}"
	jq_expr="${c#*|}"

	logq_out="$("$BIN" -f jsonl -o jsonl "$logq_query" "$FIXTURE" | jq -cS .)"
	jq_out="$(jq -c "$jq_expr" "$FIXTURE" | jq -cS .)"

	if [ "$logq_out" != "$jq_out" ]; then
		echo "MISMATCH: logq query '$logq_query' vs jq '$jq_expr'"
		echo "  logq: $logq_out"
		echo "  jq:   $jq_out"
		fail=1
	else
		echo "OK: '$logq_query' agrees with jq '$jq_expr'"
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "verify-differential: FAILED — logq and jq disagreed on at least one case"
	exit 1
fi
echo "verify-differential: all cases agree with jq"
