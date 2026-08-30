// Package agg implements logq's stats-stage aggregation engine: group-key
// encoding and the aggregation functions themselves (count, sum, avg,
// min, max now; count_distinct, percentiles, top-K, and windowing in the
// commits that follow).
package agg

import (
	"sort"
	"strings"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// Group-key sentinel bytes and field separator (§8.2). MISSING and Null
// get distinct internal markers so `(missing)`, `(null)`, and any real
// value that happens to render as an empty string can never collide into
// the same group — three genuinely distinct groups, not two, which the
// spec calls out as a headline feature (tested T-31). These are control
// bytes essentially never present in real log field values, so an
// accidental collision with a legitimate value is a vanishingly rare,
// low-severity edge case, not worth elaborate escaping to rule out.
const (
	sentinelNull    = "\x00"
	sentinelMissing = "\x02"
	fieldSep        = "\x1f"
)

// GroupKey encodes the values resolved from a stats stage's "by" paths
// into one string key (§8.2), joining per-value encodings with fieldSep.
//
// Scalar kinds reuse eval.CellString's canonical rendering — the exact
// rules §11.5 says already "feed groups, table, csv." Array and Object
// get their own distinguishing encoding here instead of CellString's
// lossy "[array]"/"[object]" display placeholder: two DIFFERENT array or
// object values must never collide into the same group the way they
// reasonably can when merely being shown in a flat table cell.
func GroupKey(values []eval.Value) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = groupKeyPart(v)
	}
	return strings.Join(parts, fieldSep)
}

func groupKeyPart(v eval.Value) string {
	switch v.Kind {
	case eval.KindMissing:
		return sentinelMissing
	case eval.KindNull:
		return sentinelNull
	case eval.KindArray:
		parts := make([]string, len(v.Arr))
		for i, e := range v.Arr {
			parts[i] = groupKeyPart(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case eval.KindObject:
		if v.Obj == nil {
			return "{}"
		}
		// Sorted, not insertion order: eval.DeepEqual (commit 8) already
		// treats two records with the same fields in different insertion
		// order as equal — grouping should agree with that, not silently
		// split one semantic group into two based on field arrival order.
		keys := append([]string(nil), v.Obj.Keys()...)
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+groupKeyPart(v.Obj.Get(k)))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return eval.CellString(v)
	}
}
