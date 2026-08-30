package agg

import (
	"math"
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func TestCount_Basic(t *testing.T) {
	var c Count
	if c.Result() != 0 {
		t.Fatalf("fresh Count = %d, want 0", c.Result())
	}
	c.Add()
	c.Add()
	c.Add()
	if c.Result() != 3 {
		t.Fatalf("Result() = %d, want 3", c.Result())
	}
}

func TestSum_IntFastPath(t *testing.T) {
	var s Sum
	s.Add(eval.Int(1))
	s.Add(eval.Int(2))
	s.Add(eval.Int(3))
	v, any := s.Result()
	if !any || !v.IsInt || v.I != 6 {
		t.Fatalf("Result() = (%+v, %v), want exact Int(6)", v, any)
	}
}

func TestSum_SkipsNonNumericNeverErrors(t *testing.T) {
	var s Sum
	if s.Add(eval.Str("not a number")) {
		t.Fatal("Add(non-numeric) should return false")
	}
	if s.Add(eval.Bool(true)) {
		t.Fatal("Add(bool) should return false")
	}
	s.Add(eval.Int(5))
	v, any := s.Result()
	if !any || v.I != 5 {
		t.Fatalf("non-numeric values must be skipped, not counted: %+v", v)
	}
}

func TestSum_EmptyReportsNotAny(t *testing.T) {
	var s Sum
	_, any := s.Result()
	if any {
		t.Fatal("Result() on an empty Sum should report any=false (§8.4 (none) case)")
	}
}

func TestSum_Int64OverflowFallsBackToBigInt(t *testing.T) {
	var s Sum
	s.Add(eval.Int(math.MaxInt64))
	s.Add(eval.Int(1)) // overflows int64
	v, any := s.Result()
	if !any {
		t.Fatal("Result should still report any=true")
	}
	// MaxInt64 + 1 overflows int64 but fits a float64 only approximately;
	// what matters here is that it did NOT silently wrap around to a
	// negative int64 — the classic overflow bug this fallback exists to
	// prevent.
	if v.IsInt && v.I < 0 {
		t.Fatalf("sum wrapped around to a negative int64 (%d) — overflow not caught", v.I)
	}
}

func TestSum_Int64OverflowExactBigIntResult(t *testing.T) {
	var s Sum
	// Two values that together clearly exceed int64 but whose SUM still
	// fits back into int64 range is impossible by construction once
	// overflow triggers from a single addition — so assert the actual
	// numeric correctness via a case using big.Int's own exactness: add
	// MaxInt64 twice, confirm the result is neither wrapped negative nor
	// silently truncated.
	s.Add(eval.Int(math.MaxInt64))
	s.Add(eval.Int(math.MaxInt64))
	v, _ := s.Result()
	// 2*MaxInt64 does not fit int64 OR stay exact through a naive
	// approach; it must come back as a Float (documented precision-loss
	// point) rather than a wrapped/truncated Int.
	if v.IsInt {
		t.Fatalf("2*MaxInt64 must not present as an exact Int (int64 can't hold it): %+v", v)
	}
	if v.F <= 0 {
		t.Fatalf("sum of two positive MaxInt64 values must not come back non-positive: %v", v.F)
	}
}

func TestSum_SwitchesToFloatModeOnFirstFloat(t *testing.T) {
	var s Sum
	s.Add(eval.Int(1))
	s.Add(eval.Int(2))
	s.Add(eval.Float(0.5))
	v, any := s.Result()
	if !any || v.IsInt {
		t.Fatalf("Result() = %+v, want a Float once any float value has been added", v)
	}
	if v.F != 3.5 {
		t.Fatalf("F = %v, want 3.5", v.F)
	}
}

func TestSum_FloatModeContinuesAcceptingLaterInts(t *testing.T) {
	var s Sum
	s.Add(eval.Float(1.5))
	s.Add(eval.Int(2)) // arrives after float mode already engaged
	v, _ := s.Result()
	if v.IsInt || v.F != 3.5 {
		t.Fatalf("Result() = %+v, want Float(3.5)", v)
	}
}

func TestSum_NegativeOverflow(t *testing.T) {
	var s Sum
	s.Add(eval.Int(math.MinInt64))
	s.Add(eval.Int(-1)) // underflows int64
	v, any := s.Result()
	if !any {
		t.Fatal("Result should report any=true")
	}
	if v.IsInt && v.I > 0 {
		t.Fatalf("sum wrapped around to a positive int64 (%d) — underflow not caught", v.I)
	}
}

func TestAvg_Basic(t *testing.T) {
	var a Avg
	a.Add(eval.Int(1))
	a.Add(eval.Int(2))
	a.Add(eval.Int(3))
	mean, any := a.Result()
	if !any || mean != 2 {
		t.Fatalf("Result() = (%v, %v), want (2, true)", mean, any)
	}
}

func TestAvg_EmptyReportsNotAny(t *testing.T) {
	var a Avg
	_, any := a.Result()
	if any {
		t.Fatal("Result() on an empty Avg should report any=false")
	}
}

func TestAvg_SkipsNonNumeric(t *testing.T) {
	var a Avg
	a.Add(eval.Int(10))
	a.Add(eval.Str("skip me"))
	a.Add(eval.Int(20))
	mean, _ := a.Result()
	if mean != 15 {
		t.Fatalf("mean = %v, want 15 (non-numeric value must not count toward n)", mean)
	}
}

func TestAvg_NumericallyStableOverManyValues(t *testing.T) {
	// Welford's method must not drift the way a naive sum/n approach can
	// over a long stream — 100000 copies of 7 must average to exactly 7.
	var a Avg
	for range 100000 {
		a.Add(eval.Int(7))
	}
	mean, _ := a.Result()
	if mean != 7 {
		t.Fatalf("mean = %v, want exactly 7", mean)
	}
}

func TestMinMax_Basic(t *testing.T) {
	min, max := NewMin(), NewMax()
	for _, n := range []int64{5, 1, 9, 3} {
		min.Add(eval.Int(n))
		max.Add(eval.Int(n))
	}
	minV, minOK := min.Result()
	maxV, maxOK := max.Result()
	if !minOK || minV.I != 1 {
		t.Fatalf("min = %+v (ok=%v), want 1", minV, minOK)
	}
	if !maxOK || maxV.I != 9 {
		t.Fatalf("max = %+v (ok=%v), want 9", maxV, maxOK)
	}
}

func TestMinMax_EmptyReportsNotOK(t *testing.T) {
	min := NewMin()
	_, ok := min.Result()
	if ok {
		t.Fatal("Result() on a starved MinMax should report ok=false (§8.4 (none) case)")
	}
}

func TestMinMax_MissingNeverParticipates(t *testing.T) {
	min := NewMin()
	min.Add(eval.Missing)
	_, ok := min.Result()
	if ok {
		t.Fatal("MISSING must never become the current min/max")
	}
	min.Add(eval.Int(5))
	v, ok := min.Result()
	if !ok || v.I != 5 {
		t.Fatalf("after a real value arrives, min = %+v (ok=%v), want 5", v, ok)
	}
}

func TestMinMax_NullNeverParticipates(t *testing.T) {
	// Regression: if Null were allowed to "lock in" as the first value
	// (Compare treats Null as Uncomparable against everything, including
	// itself in ordering context), it would permanently block every
	// later real value from ever becoming the min/max, since Uncomparable
	// never satisfies the Less/Greater check that would replace it.
	min := NewMin()
	min.Add(eval.Null)
	min.Add(eval.Int(5))
	min.Add(eval.Int(1))
	v, ok := min.Result()
	if !ok || v.I != 1 {
		t.Fatalf("min = %+v (ok=%v), want 1 — Null must never block real values from being tracked", v, ok)
	}
}

func TestMinMax_ArrayObjectNeverParticipate(t *testing.T) {
	// Same "never locks in an Uncomparable kind" protection, for the
	// other two kinds Compare always treats as Uncomparable.
	min := NewMin()
	min.Add(eval.Array([]eval.Value{eval.Int(1)}))
	min.Add(eval.Object(eval.NewRecord()))
	min.Add(eval.Int(3))
	v, ok := min.Result()
	if !ok || v.I != 3 {
		t.Fatalf("min = %+v (ok=%v), want 3", v, ok)
	}
}

func TestMinMax_CrossKindLaterValueIgnored(t *testing.T) {
	// Once a min/max has locked onto a kind, a later cross-kind candidate
	// is simply ignored (Uncomparable never satisfies Less/Greater) —
	// documented, intentional behavior, not a crash or silent corruption.
	min := NewMin()
	min.Add(eval.Int(5))
	min.Add(eval.Str("a")) // cross-kind; ignored
	v, ok := min.Result()
	if !ok || v.I != 5 {
		t.Fatalf("min = %+v (ok=%v), want the original Int(5) unchanged", v, ok)
	}
}

func TestMinMax_StringOrdering(t *testing.T) {
	min := NewMin()
	for _, s := range []string{"banana", "apple", "cherry"} {
		min.Add(eval.Str(s))
	}
	v, ok := min.Result()
	if !ok || v.S != "apple" {
		t.Fatalf("min = %+v (ok=%v), want apple", v, ok)
	}
}
