package eval

import (
	"testing"
	"time"
)

func TestCoerceNumeric_ValidStrings(t *testing.T) {
	cases := []struct {
		s    string
		want float64
	}{
		{"502", 502},
		{"3.14", 3.14},
		{"-5", -5},
		{"1e3", 1000},
		{"0", 0},
	}
	for _, c := range cases {
		t.Run(c.s, func(t *testing.T) {
			f, ok := CoerceNumeric(Str(c.s))
			if !ok || f != c.want {
				t.Fatalf("CoerceNumeric(%q) = (%v, %v), want (%v, true)", c.s, f, ok, c.want)
			}
		})
	}
}

func TestCoerceNumeric_NumberPassesThrough(t *testing.T) {
	if f, ok := CoerceNumeric(Int(42)); !ok || f != 42 {
		t.Fatalf("CoerceNumeric(Int(42)) = (%v, %v)", f, ok)
	}
	if f, ok := CoerceNumeric(Float(3.5)); !ok || f != 3.5 {
		t.Fatalf("CoerceNumeric(Float(3.5)) = (%v, %v)", f, ok)
	}
}

func TestCoerceNumeric_RejectsInvalidStrings(t *testing.T) {
	bad := []string{
		"abc",   // not numeric at all
		" 502",  // leading space
		"502 ",  // trailing space
		"1_000", // underscore digit separator — explicitly rejected
		"Inf",   // non-finite, ParseFloat would accept this
		"-Infinity",
		"NaN",
		"nan",
		"",
		"5.2.3",
	}
	for _, s := range bad {
		t.Run(s, func(t *testing.T) {
			if _, ok := CoerceNumeric(Str(s)); ok {
				t.Fatalf("CoerceNumeric(%q) = ok=true, want a rejected coercion", s)
			}
		})
	}
}

func TestCoerceNumeric_RejectsNonStringNonNumberKinds(t *testing.T) {
	for _, v := range []Value{Bool(true), Null, Missing, Array(nil), Object(NewRecord())} {
		if _, ok := CoerceNumeric(v); ok {
			t.Fatalf("CoerceNumeric(%+v) = ok=true, want false", v)
		}
	}
}

func TestLevelOrdinal_KnownTokens(t *testing.T) {
	cases := []struct {
		token string
		want  int
	}{
		{"trace", 10}, {"debug", 20}, {"info", 30},
		{"warn", 40}, {"warning", 40}, {"error", 50}, {"fatal", 60},
		{"ERROR", 50}, {"WaRn", 40}, // case-insensitive per §6.2
	}
	for _, c := range cases {
		t.Run(c.token, func(t *testing.T) {
			ord, ok := LevelOrdinalFromTable(Str(c.token), nil)
			if !ok || ord != c.want {
				t.Fatalf("LevelOrdinalFromTable(%q) = (%d, %v), want (%d, true)", c.token, ord, ok, c.want)
			}
		})
	}
}

func TestLevelOrdinal_NumericVerbatim(t *testing.T) {
	ord, ok := LevelOrdinalFromTable(Int(402), nil)
	if !ok || ord != 402 {
		t.Fatalf("LevelOrdinalFromTable(Int(402)) = (%d, %v), want (402, true)", ord, ok)
	}
}

func TestLevelOrdinal_UnknownTokenFails(t *testing.T) {
	_, ok := LevelOrdinalFromTable(Str("critical"), nil)
	if ok {
		t.Fatal("LevelOrdinalFromTable(critical) = ok=true, want false (unknown token, falls back to string compare in the evaluator)")
	}
}

func TestLevelOrdinal_CustomTableOverride(t *testing.T) {
	custom := map[string]int{"quiet": 5, "loud": 100}
	ord, ok := LevelOrdinalFromTable(Str("loud"), custom)
	if !ok || ord != 100 {
		t.Fatalf("custom table lookup = (%d, %v), want (100, true)", ord, ok)
	}
	// The built-in table's tokens are not implicitly merged into a custom
	// one passed by the caller — extension policy (merge vs. replace) is a
	// CLI-flag concern (--levels, Phase 8), not this function's.
	if _, ok := LevelOrdinalFromTable(Str("warn"), custom); ok {
		t.Fatal("custom table unexpectedly fell back to the built-in table")
	}
}

func TestLevelOrdinal_NonStringNonNumberFails(t *testing.T) {
	if _, ok := LevelOrdinalFromTable(Bool(true), nil); ok {
		t.Fatal("LevelOrdinalFromTable(Bool) = ok=true, want false")
	}
}

func TestIsLevelFieldName(t *testing.T) {
	for _, name := range []string{"level", "severity", "lvl", "loglevel"} {
		if !IsLevelFieldName(name) {
			t.Fatalf("IsLevelFieldName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Level", "LEVEL", "msg", "status", "levels"} {
		if IsLevelFieldName(name) {
			t.Fatalf("IsLevelFieldName(%q) = true, want false (exact, case-sensitive match only)", name)
		}
	}
}

func TestCoerceTimestampDuration_TimestampThenDuration(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ts := Timestamp(now.Add(-2 * time.Hour))
	dur := DurationVal(-1 * time.Hour) // `-1h`, as in `ts >= -1h`

	ra, rb, applied := CoerceTimestampDuration(ts, dur, now)
	if !applied {
		t.Fatal("applied = false, want true")
	}
	if ra.Kind != KindTimestamp || rb.Kind != KindTimestamp {
		t.Fatalf("ra/rb kinds = %v/%v, want both KindTimestamp", ra.Kind, rb.Kind)
	}
	want := now.Add(-1 * time.Hour)
	if !rb.Time.Equal(want) {
		t.Fatalf("rb.Time = %v, want %v (now - 1h)", rb.Time, want)
	}
	// The Timestamp side must be passed through unchanged.
	if !ra.Time.Equal(ts.Time) {
		t.Fatalf("ra.Time = %v, want unchanged %v", ra.Time, ts.Time)
	}
}

func TestCoerceTimestampDuration_DurationThenTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	dur := DurationVal(30 * time.Minute)
	ts := Timestamp(now)

	ra, rb, applied := CoerceTimestampDuration(dur, ts, now)
	if !applied {
		t.Fatal("applied = false, want true")
	}
	want := now.Add(30 * time.Minute)
	if !ra.Time.Equal(want) {
		t.Fatalf("ra.Time = %v, want %v", ra.Time, want)
	}
	if !rb.Time.Equal(now) {
		t.Fatalf("rb.Time = %v, want unchanged %v", rb.Time, now)
	}
}

func TestCoerceTimestampDuration_NotApplicable(t *testing.T) {
	now := time.Now()
	cases := [][2]Value{
		{Int(1), Int(2)},
		{Timestamp(now), Timestamp(now)},
		{DurationVal(time.Second), DurationVal(time.Minute)},
		{Str("x"), DurationVal(time.Second)},
	}
	for _, c := range cases {
		ra, rb, applied := CoerceTimestampDuration(c[0], c[1], now)
		if applied {
			t.Fatalf("CoerceTimestampDuration(%+v, %+v) applied = true, want false", c[0], c[1])
		}
		// Value contains a slice field (Arr), so it isn't Go-comparable
		// with == / != — DeepEqual (order.go) is the correct comparison.
		if !DeepEqual(ra, c[0]) || !DeepEqual(rb, c[1]) {
			t.Fatalf("operands must pass through unchanged when not applicable")
		}
	}
}

func TestMergeLevelTable_UnmentionedNamesKeepDefault(t *testing.T) {
	merged := MergeLevelTable(map[string]int{"critical": 55})
	if merged["info"] != defaultLevelTable["info"] {
		t.Fatalf("merged[\"info\"] = %d, want the untouched default %d", merged["info"], defaultLevelTable["info"])
	}
	if merged["critical"] != 55 {
		t.Fatalf("merged[\"critical\"] = %d, want 55", merged["critical"])
	}
}

func TestMergeLevelTable_OverrideReplacesDefault(t *testing.T) {
	merged := MergeLevelTable(map[string]int{"warn": 999})
	if merged["warn"] != 999 {
		t.Fatalf("merged[\"warn\"] = %d, want the override 999, not the default %d", merged["warn"], defaultLevelTable["warn"])
	}
}

func TestMergeLevelTable_OverrideKeysAreLowercased(t *testing.T) {
	merged := MergeLevelTable(map[string]int{"CRITICAL": 55})
	if merged["critical"] != 55 {
		t.Fatalf("merged[\"critical\"] = %d, want 55 — override keys must lowercase, matching LevelOrdinalFromTable's own lookup", merged["critical"])
	}
}

func TestMergeLevelTable_NilOverridesStillReturnsDefault(t *testing.T) {
	merged := MergeLevelTable(nil)
	if merged["error"] != defaultLevelTable["error"] {
		t.Fatalf("merged[\"error\"] = %d, want the default %d", merged["error"], defaultLevelTable["error"])
	}
}

func TestMergeLevelTable_DoesNotMutateTheDefaultTable(t *testing.T) {
	_ = MergeLevelTable(map[string]int{"warn": 999})
	if defaultLevelTable["warn"] != 40 {
		t.Fatalf("defaultLevelTable[\"warn\"] = %d, want the original 40 — MergeLevelTable must return a copy, never mutate the shared default", defaultLevelTable["warn"])
	}
}
