package eval

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// defaultLevelTable maps a recognized level token (case-insensitive) to its
// ordinal weight (§6.2). "warning" is an alias for "warn" — both 40 — so
// `level >= "warn"` and `level >= "warning"` behave identically.
var defaultLevelTable = map[string]int{
	"trace":   10,
	"debug":   20,
	"info":    30,
	"warn":    40,
	"warning": 40,
	"error":   50,
	"fatal":   60,
}

// levelFieldNames are the field names that trigger level-ordinal coercion
// when they appear on the left of a comparison (§6.2). Matching is exact
// and case-sensitive — these are literal JSON/logfmt keys, not a
// case-folded convention. --levels (CLI, Phase 8) extends the ordinal
// *table*, not this name list, per spec.
var levelFieldNames = map[string]bool{
	"level":    true,
	"severity": true,
	"lvl":      true,
	"loglevel": true,
}

// IsLevelFieldName reports whether name is one of the recognized level
// field names that gate ordinal coercion.
func IsLevelFieldName(name string) bool {
	return levelFieldNames[name]
}

// LevelOrdinalFromTable resolves a level value to its ordinal (coercion #2,
// §5.3/§6.2). table nil means the built-in defaultLevelTable. A String is
// looked up case-insensitively; a Number is used verbatim, per spec
// ("Numeric level values used verbatim"). ok is false when the value
// doesn't resolve to any known ordinal — per §6.2 the caller (the
// evaluator, commit 10) falls back to byte-wise string comparison in that
// case, never an error.
func LevelOrdinalFromTable(v Value, table map[string]int) (ord int, ok bool) {
	if table == nil {
		table = defaultLevelTable
	}
	switch v.Kind {
	case KindString:
		o, found := table[strings.ToLower(v.S)]
		return o, found
	case KindNumber:
		if v.IsInt {
			return int(v.I), true
		}
		return int(v.F), true
	default:
		return 0, false
	}
}

// CoerceNumeric attempts the sanctioned string<->number coercion (§5.3 rule
// 1): a Number coerces to itself; a numeric String is parsed via
// strconv.ParseFloat. This is deliberately stricter than raw ParseFloat in
// two ways the spec calls for ("strict, no spaces/underscores") that
// ParseFloat alone doesn't enforce:
//
//   - Underscore digit separators: since Go 1.13, ParseFloat accepts Go's
//     numeric-literal underscore syntax (e.g. "1_000.5" -> 1000.5). A log
//     field value containing a literal underscore (e.g. an opaque id that
//     happens to look numeric) must not silently coerce — rejected before
//     even calling ParseFloat.
//   - Non-finite results: ParseFloat also accepts "Inf"/"NaN" (any case)
//     as valid float literals. Letting a log value string "NaN" or "Inf"
//     participate in a numeric >= comparison would be surprising, not
//     useful — rejected after parsing.
//
// ok is false on any failure, which the evaluator turns into a comparison
// result of false plus a coerce_miss counter increment (Phase 10), never a
// hard error — coercion failure is data, not a bug.
func CoerceNumeric(v Value) (f float64, ok bool) {
	switch v.Kind {
	case KindNumber:
		if v.IsInt {
			return float64(v.I), true
		}
		return v.F, true
	case KindString:
		if strings.ContainsRune(v.S, '_') {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(v.S, 64)
		if err != nil {
			return 0, false
		}
		if math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// CoerceTimestampDuration implements §5.3 rule 3: when one operand is a
// Timestamp and the other a Duration, the Duration side becomes now+d
// before comparing — e.g. `ts >= -1h` becomes `ts >= (now - 1h)`.
//
// now is passed in explicitly rather than read from a package-level clock:
// batch mode freezes it once at process start (this is what makes
// deterministic, differential-testable output possible at all — §15);
// watch mode re-evaluates it on every poll tick instead, so a long-running
// `-w 'ts >= -1h'` behaves as an actual rolling window rather than freezing
// to whatever "now" was when the session started. Phase 6 (commit 22) and
// Phase 9 (commit 40) wire the actual policy; this function only applies
// whichever now it's given, and carries no state of its own.
//
// applied is false when neither operand is a Timestamp/Duration pairing —
// the caller should fall through to ordinary same-kind comparison (or
// Uncomparable) in that case, unchanged.
func CoerceTimestampDuration(a, b Value, now time.Time) (ra, rb Value, applied bool) {
	switch {
	case a.Kind == KindTimestamp && b.Kind == KindDuration:
		return a, Timestamp(now.Add(b.Dur)), true
	case a.Kind == KindDuration && b.Kind == KindTimestamp:
		return Timestamp(now.Add(a.Dur)), b, true
	default:
		return a, b, false
	}
}
