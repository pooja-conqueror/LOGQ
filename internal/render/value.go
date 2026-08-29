package render

import (
	"strconv"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// NumberString renders a Number value per §11.5's canonical rule: an int64
// renders as a plain decimal integer; a float64 renders via the shortest
// round-trip representation ('g' format, precision -1).
func NumberString(v eval.Value) string {
	if v.IsInt {
		return strconv.FormatInt(v.I, 10)
	}
	return strconv.FormatFloat(v.F, 'g', -1, 64)
}

// TimestampString renders a Timestamp value as RFC3339, per §11.5.
func TimestampString(v eval.Value) string {
	return v.Time.Format(time.RFC3339)
}

// DurationString renders a Duration value in Go's own compact,
// largest-units-first format (e.g. "1h30m0s") — exactly the "compact
// Go-style largest-units" form §11.5 calls for, which is simply
// time.Duration's own String() method; no reason to hand-roll it.
func DurationString(v eval.Value) string {
	return v.Dur.String()
}
