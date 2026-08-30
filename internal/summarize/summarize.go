// Package summarize aggregates every non-fatal event counter a run
// accumulates (§12.3's error model, plus §8.3's cardinality guard) into
// the single end-of-run stderr summary line — nothing is ever silently
// dropped without a trace, but nothing noteworthy is buried in per-line
// noise either.
package summarize

import (
	"fmt"
	"strings"
)

// Counters holds every counter seeded through the build. Fields are
// exported so callers across cmd/logq can increment them directly — the
// same low-ceremony style the ad-hoc counters this package replaces
// already used, just consolidated into one type instead of scattered
// local variables.
//
// Not every counter mentioned elsewhere in the codebase lives here yet:
// the per-(function,field) skipped_nonnumeric counters §8.4 mentions
// would need threading through every aggregator wrapper in
// internal/pipeline/stats.go — real, separable work, deliberately out of
// scope for this commit and called out in README's Honest Limits rather
// than half-wired silently.
type Counters struct {
	LinesRead        int64
	Malformed        int64 // decode failure (bad JSON, unterminated logfmt quote, etc.)
	OversizedLines   int64
	EmptyLines       int64
	DupKeys          int64 // jsonl fields that appeared more than once on one line (last wins)
	DroppedByWindow  int64 // dropped by an explicit --since/--until bound (D-1)
	TSUnparsed       int64 // a timestamp candidate field was present but failed to parse (§12.3: "time fields aren't errors" — never fatal, even under --on-error stop)
	GroupsOverflowed int64 // stats records routed into (other) past --max-groups (§8.3)
}

// Add merges o's counts into c — folding one source's (or, for
// GroupsOverflowed, one stats stage's) counters into the run-wide total.
func (c *Counters) Add(o Counters) {
	c.LinesRead += o.LinesRead
	c.Malformed += o.Malformed
	c.OversizedLines += o.OversizedLines
	c.EmptyLines += o.EmptyLines
	c.DupKeys += o.DupKeys
	c.DroppedByWindow += o.DroppedByWindow
	c.TSUnparsed += o.TSUnparsed
	c.GroupsOverflowed += o.GroupsOverflowed
}

// Noteworthy reports whether anything worth a human's attention was ever
// counted — LinesRead and EmptyLines are deliberately excluded: a plain
// line count isn't a problem signal, and empty lines are routine, not
// noteworthy.
func (c Counters) Noteworthy() bool {
	return c.Malformed > 0 || c.OversizedLines > 0 || c.DupKeys > 0 ||
		c.DroppedByWindow > 0 || c.TSUnparsed > 0 || c.GroupsOverflowed > 0
}

// String renders the one-line stderr summary (§12.3) — always includes
// the line count for context, then only the counters that are actually
// nonzero, so a clean run's summary doesn't pad out a wall of "0"s.
func (c Counters) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d line(s) read", c.LinesRead)
	type field struct {
		n     int64
		label string
	}
	for _, f := range []field{
		{c.Malformed, "malformed"},
		{c.OversizedLines, "oversized"},
		{c.DupKeys, "dup key(s)"},
		{c.DroppedByWindow, "dropped by --since/--until"},
		{c.TSUnparsed, "ts unparsed"},
		{c.GroupsOverflowed, "groups overflowed to (other)"},
	} {
		if f.n > 0 {
			fmt.Fprintf(&sb, ", %d %s", f.n, f.label)
		}
	}
	return sb.String()
}
