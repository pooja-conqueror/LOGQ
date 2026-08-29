package eval

import (
	"testing"
	"time"
)

func TestParseTimestamp_RFC3339Variants(t *testing.T) {
	cases := []struct {
		s    string
		want time.Time
	}{
		{"2026-08-29T12:00:00Z", time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)},
		{"2026-08-29T12:00:00.123456789Z", time.Date(2026, 8, 29, 12, 0, 0, 123456789, time.UTC)},
		{"2026-08-29T12:00:00+05:30", time.Date(2026, 8, 29, 12, 0, 0, 0, time.FixedZone("", 5*3600+30*60))},
	}
	for _, c := range cases {
		t.Run(c.s, func(t *testing.T) {
			got, ok := ParseTimestamp(c.s, nil)
			if !ok {
				t.Fatalf("ParseTimestamp(%q) failed to parse", c.s)
			}
			if !got.Equal(c.want) {
				t.Fatalf("ParseTimestamp(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestParseTimestamp_NaiveLayoutsUseGivenLocation(t *testing.T) {
	// A fixed-offset zone (not a named IANA zone — that needs tzdata,
	// which isn't wired until commit 22) proves the loc parameter is
	// actually respected, not silently ignored in favor of UTC.
	loc := time.FixedZone("TEST+0530", 5*3600+30*60)

	got, ok := ParseTimestamp("2026-08-29T12:00:00", loc)
	if !ok {
		t.Fatal("naive ISO timestamp failed to parse")
	}
	want := time.Date(2026, 8, 29, 12, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v (interpreted in the given location)", got, want)
	}

	// Same wall-clock text, nil location (defaults to UTC), must be a
	// DIFFERENT instant than the +05:30 interpretation above.
	utcGot, ok := ParseTimestamp("2026-08-29T12:00:00", nil)
	if !ok {
		t.Fatal("naive ISO timestamp (UTC default) failed to parse")
	}
	if utcGot.Equal(got) {
		t.Fatal("UTC-default parse and +05:30 parse must be different instants for the same wall-clock text")
	}
}

func TestParseTimestamp_OtherNaiveLayouts(t *testing.T) {
	cases := []string{
		"2026-08-29 12:00:00.5",
		"2026/08/29 12:00:00",
	}
	for _, s := range cases {
		if _, ok := ParseTimestamp(s, nil); !ok {
			t.Fatalf("ParseTimestamp(%q) failed to parse", s)
		}
	}
}

func TestParseTimestamp_RFC1123AndVariants(t *testing.T) {
	cases := []string{
		"Sat, 29 Aug 2026 12:00:00 UTC",   // RFC1123
		"Sat, 29 Aug 2026 12:00:00 +0000", // RFC1123Z
	}
	for _, s := range cases {
		if _, ok := ParseTimestamp(s, nil); !ok {
			t.Fatalf("ParseTimestamp(%q) failed to parse", s)
		}
	}
}

func TestParseTimestamp_NginxCombinedLogFormat(t *testing.T) {
	got, ok := ParseTimestamp("29/Aug/2026:12:00:00 +0000", nil)
	if !ok {
		t.Fatal("nginx/apache CLF timestamp failed to parse")
	}
	want := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_EpochHeuristic_Seconds(t *testing.T) {
	// EC-21: epoch seconds vs ms -> heuristic picks correctly both.
	got, ok := ParseTimestamp("1735500000", nil) // well under 1e11
	if !ok {
		t.Fatal("epoch-seconds string failed to parse")
	}
	want := time.Unix(1735500000, 0).UTC()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_EpochHeuristic_Milliseconds(t *testing.T) {
	got, ok := ParseTimestamp("1735500000123", nil) // between 1e11 and 1e14
	if !ok {
		t.Fatal("epoch-milliseconds string failed to parse")
	}
	want := time.UnixMilli(1735500000123).UTC()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_EpochHeuristic_TooLargeFails(t *testing.T) {
	if _, ok := ParseTimestamp("999999999999999999", nil); ok {
		t.Fatal("an epoch value >= 1e14 must not resolve")
	}
}

func TestParseTimestamp_EpochHeuristic_NegativePre1970(t *testing.T) {
	// EC-22: pre-1970 timestamps -> negative epochs fine.
	got, ok := ParseTimestamp("-86400", nil) // one day before the epoch
	if !ok {
		t.Fatal("negative epoch failed to parse")
	}
	want := time.Unix(-86400, 0).UTC()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseTimestamp_Unparseable(t *testing.T) {
	for _, s := range []string{"", "not a date", "2026-13-45", "yesterday"} {
		if _, ok := ParseTimestamp(s, nil); ok {
			t.Fatalf("ParseTimestamp(%q) unexpectedly succeeded", s)
		}
	}
}

func TestResolveTimestamp_NumberAsEpoch(t *testing.T) {
	got, ok := ResolveTimestamp(Int(1735500000), nil)
	if !ok {
		t.Fatal("integer epoch Number failed to resolve")
	}
	want := time.Unix(1735500000, 0).UTC()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveTimestamp_FractionalNumberNeverResolves(t *testing.T) {
	if _, ok := ResolveTimestamp(Float(1735500000.5), nil); ok {
		t.Fatal("a fractional Number must not resolve as an epoch timestamp")
	}
}

func TestResolveTimestamp_OtherKindsNeverResolve(t *testing.T) {
	for _, v := range []Value{Bool(true), Null, Missing, Array(nil), Object(NewRecord())} {
		if _, ok := ResolveTimestamp(v, nil); ok {
			t.Fatalf("ResolveTimestamp(%+v) unexpectedly resolved", v)
		}
	}
}

func TestResolveRecordTimestamp_PriorityOrder(t *testing.T) {
	rec := NewRecord()
	rec.Set("t", Str("2026-08-29T00:00:00Z"))         // lower priority
	rec.Set("timestamp", Str("2026-08-29T12:00:00Z")) // higher priority
	rec.Set("time", Str("2026-08-29T06:00:00Z"))      // higher priority still (but below "ts")

	got, name, ok := ResolveRecordTimestamp(rec, nil)
	if !ok {
		t.Fatal("resolution failed")
	}
	if name != "time" {
		t.Fatalf("resolved field = %q, want %q (highest-priority present field)", name, "time")
	}
	want := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveRecordTimestamp_FirstResolvableNotFirstPresent(t *testing.T) {
	// "ts" is present but garbage; resolution must fall through to the
	// next candidate ("time") rather than giving up entirely.
	rec := NewRecord()
	rec.Set("ts", Str("not a real timestamp"))
	rec.Set("time", Str("2026-08-29T12:00:00Z"))

	got, name, ok := ResolveRecordTimestamp(rec, nil)
	if !ok {
		t.Fatal("resolution failed — should have fallen through to 'time'")
	}
	if name != "time" {
		t.Fatalf("resolved field = %q, want %q", name, "time")
	}
	want := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveRecordTimestamp_AtSignField(t *testing.T) {
	rec := NewRecord()
	rec.Set("@timestamp", Str("2026-08-29T12:00:00Z"))
	_, name, ok := ResolveRecordTimestamp(rec, nil)
	if !ok || name != "@timestamp" {
		t.Fatalf("name=%q ok=%v, want @timestamp resolved", name, ok)
	}
}

func TestResolveRecordTimestamp_NoCandidatesPresent(t *testing.T) {
	rec := NewRecord()
	rec.Set("msg", Str("hello"))
	_, _, ok := ResolveRecordTimestamp(rec, nil)
	if ok {
		t.Fatal("resolution should fail when no candidate field is present at all")
	}
}

func TestResolveRecordTimestamp_AllCandidatesUnresolvable(t *testing.T) {
	rec := NewRecord()
	rec.Set("ts", Str("garbage"))
	rec.Set("eventTime", Bool(true)) // wrong kind entirely
	_, _, ok := ResolveRecordTimestamp(rec, nil)
	if ok {
		t.Fatal("resolution should fail when every present candidate is unresolvable")
	}
}
