package summarize

import "testing"

func TestCounters_Add(t *testing.T) {
	var total Counters
	total.Add(Counters{LinesRead: 10, Malformed: 2})
	total.Add(Counters{LinesRead: 5, Malformed: 1, DupKeys: 3})

	if total.LinesRead != 15 {
		t.Fatalf("LinesRead = %d, want 15", total.LinesRead)
	}
	if total.Malformed != 3 {
		t.Fatalf("Malformed = %d, want 3", total.Malformed)
	}
	if total.DupKeys != 3 {
		t.Fatalf("DupKeys = %d, want 3", total.DupKeys)
	}
}

func TestCounters_NoteworthyFalseOnCleanRun(t *testing.T) {
	c := Counters{LinesRead: 1000, EmptyLines: 5}
	if c.Noteworthy() {
		t.Fatal("Noteworthy() = true, want false — a plain line count and empty lines alone aren't noteworthy")
	}
}

func TestCounters_NoteworthyTrueForEachRealCounter(t *testing.T) {
	cases := []Counters{
		{Malformed: 1},
		{OversizedLines: 1},
		{DupKeys: 1},
		{DroppedByWindow: 1},
		{TSUnparsed: 1},
		{GroupsOverflowed: 1},
	}
	for _, c := range cases {
		if !c.Noteworthy() {
			t.Errorf("Noteworthy() = false for %+v, want true", c)
		}
	}
}

func TestCounters_StringOmitsZeroFields(t *testing.T) {
	c := Counters{LinesRead: 100}
	got := c.String()
	want := "100 line(s) read"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCounters_StringIncludesOnlyNonzeroFields(t *testing.T) {
	c := Counters{LinesRead: 100, Malformed: 3, TSUnparsed: 7}
	got := c.String()
	want := "100 line(s) read, 3 malformed, 7 ts unparsed"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCounters_StringAllFieldsNonzero(t *testing.T) {
	c := Counters{
		LinesRead:        1,
		Malformed:        1,
		OversizedLines:   1,
		DupKeys:          1,
		DroppedByWindow:  1,
		TSUnparsed:       1,
		GroupsOverflowed: 1,
	}
	got := c.String()
	want := "1 line(s) read, 1 malformed, 1 oversized, 1 dup key(s), 1 dropped by --since/--until, 1 ts unparsed, 1 groups overflowed to (other)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
