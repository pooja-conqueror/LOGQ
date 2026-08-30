package eval

import (
	"strconv"
	"time"
)

// TimestampFieldNames are the candidate field names tried, in priority
// order, when resolving a record's timestamp (§6.1). First *resolvable*
// wins — a candidate being merely present isn't enough if its value
// doesn't actually parse; resolution moves on to the next name rather
// than giving up.
var TimestampFieldNames = []string{"ts", "time", "timestamp", "@timestamp", "t", "eventTime"}

// timestampLayout pairs a time.Parse layout with whether it carries its
// own zone info. naive=true layouts have no zone offset in the source
// text at all and are interpreted in the caller-supplied location; the
// rest carry an explicit offset or zone name and are parsed as-is via
// time.Parse. This is an explicit table rather than auto-detected from
// the layout string — easier to verify correct by inspection than trying
// to pattern-match Go's reference-time zone tokens.
type timestampLayout struct {
	layout string
	naive  bool
}

// timestampLayouts is the §6.1 resolution ladder, tried in order.
var timestampLayouts = []timestampLayout{
	{time.RFC3339Nano, false},
	{time.RFC3339, false},
	{"2006-01-02T15:04:05", true},
	{"2006-01-02 15:04:05.999999999", true},
	{"2006/01/02 15:04:05", true},
	{time.RFC1123, false},
	{time.RFC1123Z, false},
	{"02/Jan/2006:15:04:05 -0700", false}, // nginx/apache combined log format
}

// ParseTimestamp implements the §6.1 ladder for a single string: each
// layout is tried in order, first success wins. Naive layouts are
// interpreted in loc (nil means UTC). Falls back to the numeric epoch
// heuristic if no layout matches. ok is false if nothing works.
func ParseTimestamp(s string, loc *time.Location) (t time.Time, ok bool) {
	if loc == nil {
		loc = time.UTC
	}
	for _, tl := range timestampLayouts {
		var parsed time.Time
		var err error
		if tl.naive {
			parsed, err = time.ParseInLocation(tl.layout, s, loc)
		} else {
			parsed, err = time.Parse(tl.layout, s)
		}
		if err == nil {
			return parsed, true
		}
	}
	return parseEpochHeuristic(s)
}

// epochFromInt implements §6.1's numeric epoch heuristic: |n| < 1e11 is
// Unix seconds, < 1e14 is Unix milliseconds, anything larger fails.
// Negative values (pre-1970 timestamps, EC-22) work directly — epoch time
// has no zone ambiguity, so the result is always UTC regardless of loc.
func epochFromInt(n int64) (time.Time, bool) {
	abs := n
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs < 1e11:
		return time.Unix(n, 0).UTC(), true
	case abs < 1e14:
		return time.UnixMilli(n).UTC(), true
	default:
		return time.Time{}, false
	}
}

func parseEpochHeuristic(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return epochFromInt(n)
}

// ResolveTimestamp attempts to interpret v as a timestamp: a String goes
// through the full ParseTimestamp ladder; a Number is treated directly as
// an epoch value via the same seconds/milliseconds heuristic — real-world
// logs commonly store timestamps as raw JSON numbers, not quoted strings.
// A fractional (non-integer) Number never resolves — the spec's epoch
// heuristic is integer-only.
func ResolveTimestamp(v Value, loc *time.Location) (time.Time, bool) {
	switch v.Kind {
	case KindString:
		return ParseTimestamp(v.S, loc)
	case KindNumber:
		if v.IsInt {
			return epochFromInt(v.I)
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// ResolveRecordTimestamp implements the full field-priority + parse-ladder
// resolution (§6.1): each candidate name in TimestampFieldNames is tried
// in order, and the first one whose value actually resolves wins — not
// just the first one present. fieldName reports which candidate matched.
//
// attempted distinguishes two very different "ok=false" cases for the
// caller's ts_unparsed counter (§12.3: "ts unparsed | count (time fields
// aren't errors)") — attempted=false means no candidate field was even
// present at all (the ordinary, unremarkable case for most log lines,
// not worth counting as anything); attempted=true means at least one
// candidate WAS present but every one of them failed to parse as a
// timestamp, which is the case §12.3 actually means by "unparsed."
func ResolveRecordTimestamp(rec *Record, loc *time.Location) (t time.Time, fieldName string, ok bool, attempted bool) {
	for _, name := range TimestampFieldNames {
		v := rec.Get(name)
		if v.Kind == KindMissing {
			continue
		}
		attempted = true
		if parsed, resolved := ResolveTimestamp(v, loc); resolved {
			return parsed, name, true, true
		}
	}
	return time.Time{}, "", false, attempted
}
