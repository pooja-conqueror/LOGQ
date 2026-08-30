package eval

import (
	"strconv"
	"time"
)

// This file holds §11.5's canonical value-rendering rules — deliberately
// placed in eval rather than render, because the spec states directly
// that this rendering "feeds groups, table, csv": internal/agg's
// group-key encoding needs the exact same rules internal/render's
// table/csv/jsonl renderers do. Living here, in the shared lower layer
// both packages already depend on, is what lets both consume it without
// agg having to import render (the wrong direction — render is the outer,
// presentation-facing layer) or duplicating this logic a second time.

// NumberString renders a Number value per §11.5's canonical rule: an int64
// renders as a plain decimal integer; a float64 renders via the shortest
// round-trip representation ('g' format, precision -1).
func NumberString(v Value) string {
	if v.IsInt {
		return strconv.FormatInt(v.I, 10)
	}
	return strconv.FormatFloat(v.F, 'g', -1, 64)
}

// TimestampString renders a Timestamp value as RFC3339, per §11.5.
func TimestampString(v Value) string {
	return v.Time.Format(time.RFC3339)
}

// DurationString renders a Duration value in Go's own compact,
// largest-units-first format (e.g. "1h30m0s") — exactly the "compact
// Go-style largest-units" form §11.5 calls for, which is simply
// time.Duration's own String() method; no reason to hand-roll it.
func DurationString(v Value) string {
	return v.Dur.String()
}

// CellString renders v as plain text for a table/csv cell or (for the
// scalar kinds) a group-key component — the shared "how do I show this
// value as text" logic (§11.5). MISSING renders "(missing)" here (a
// human-facing table wants it visible, distinct from Null); render/csv.go
// overrides this one case to an empty cell instead, per §11.3's own
// distinct rule, and internal/agg's group-key encoding overrides the
// Array/Object cases instead of using this function's lossy
// "[array]"/"[object]" display placeholders, since two DIFFERENT
// array/object values must never collide into the same group the way
// they reasonably can when merely being displayed in a flat table cell.
func CellString(v Value) string {
	switch v.Kind {
	case KindMissing:
		return "(missing)"
	case KindNull:
		return "null"
	case KindBool:
		if v.B {
			return "true"
		}
		return "false"
	case KindNumber:
		return NumberString(v)
	case KindString:
		return v.S
	case KindTimestamp:
		return TimestampString(v)
	case KindDuration:
		return DurationString(v)
	case KindArray:
		return "[array]"
	case KindObject:
		return "[object]"
	default:
		return ""
	}
}
