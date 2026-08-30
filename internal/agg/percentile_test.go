package agg

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func TestReservoir_UnderCapKeepsEverythingInOrder(t *testing.T) {
	r := NewReservoir(10, defaultReservoirSeed)
	for i := range int64(5) {
		r.Add(eval.Int(i))
	}
	if !r.Exact() {
		t.Fatal("Exact() should be true when total offered <= cap")
	}
	items := r.Items()
	if len(items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(items))
	}
	for i, v := range items {
		if v.I != int64(i) {
			t.Fatalf("items[%d] = %v, want %d — under cap, no replacement should ever happen", i, v.I, i)
		}
	}
}

func TestReservoir_OverCapStaysAtCapSize(t *testing.T) {
	r := NewReservoir(50, defaultReservoirSeed)
	for i := range int64(1000) {
		r.Add(eval.Int(i))
	}
	if r.Exact() {
		t.Fatal("Exact() should be false once more than cap items have been offered")
	}
	if len(r.Items()) != 50 {
		t.Fatalf("len(items) = %d, want exactly the cap (50)", len(r.Items()))
	}
}

func TestReservoir_OverCapValuesAreDistinctSubsetOfInput(t *testing.T) {
	// Each input value (0..999) is unique; Algorithm L never duplicates one
	// input item into two reservoir slots, so every sampled value must be
	// in range and pairwise distinct.
	r := NewReservoir(50, defaultReservoirSeed)
	for i := range int64(1000) {
		r.Add(eval.Int(i))
	}
	seen := map[int64]bool{}
	for _, v := range r.Items() {
		if v.I < 0 || v.I >= 1000 {
			t.Fatalf("sampled value %d out of the input's range [0,1000)", v.I)
		}
		if seen[v.I] {
			t.Fatalf("value %d sampled twice — Algorithm L must never duplicate one input item into two slots", v.I)
		}
		seen[v.I] = true
	}
}

func TestReservoir_DeterministicAcrossInstances(t *testing.T) {
	build := func() []eval.Value {
		r := NewReservoir(20, defaultReservoirSeed)
		for i := range int64(500) {
			r.Add(eval.Int(i))
		}
		return r.Items()
	}
	a, b := build(), build()
	if len(a) != len(b) {
		t.Fatalf("len(a)=%d != len(b)=%d — same seed, same input must give the same reservoir", len(a), len(b))
	}
	for i := range a {
		if a[i].I != b[i].I {
			t.Fatalf("a[%d]=%d != b[%d]=%d — fixed seed must make the sample fully reproducible", i, a[i].I, i, b[i].I)
		}
	}
}

func TestPercentile_ExactMedianSmallSet(t *testing.T) {
	p := NewPercentileWithCap(0.5, 100)
	for _, n := range []int64{1, 2, 3, 4, 5} {
		p.Add(eval.Int(n))
	}
	v, approx, ok := p.Result()
	if !ok {
		t.Fatal("Result() ok = false, want true")
	}
	if approx {
		t.Fatal("approx = true, want false — well under cap")
	}
	if v.I != 3 {
		t.Fatalf("p50 = %d, want 3 (nearest-rank ceil(0.5*5)=3rd smallest)", v.I)
	}
}

func TestPercentile_ExactP95AndP99SmallSet(t *testing.T) {
	values := []int64{1, 2, 3, 4, 5}
	p95 := NewPercentileWithCap(0.95, 100)
	p99 := NewPercentileWithCap(0.99, 100)
	for _, n := range values {
		p95.Add(eval.Int(n))
		p99.Add(eval.Int(n))
	}
	v95, _, _ := p95.Result()
	v99, _, _ := p99.Result()
	if v95.I != 5 {
		t.Fatalf("p95 = %d, want 5 (ceil(0.95*5)=5th smallest)", v95.I)
	}
	if v99.I != 5 {
		t.Fatalf("p99 = %d, want 5 (ceil(0.99*5)=5th smallest)", v99.I)
	}
}

func TestPercentile_PreservesIntKindExactly(t *testing.T) {
	p := NewPercentileWithCap(0.5, 100)
	p.Add(eval.Int(10))
	p.Add(eval.Int(20))
	p.Add(eval.Int(30))
	v, _, _ := p.Result()
	if !v.IsInt {
		t.Fatalf("nearest-rank must return the ORIGINAL value unchanged, got a Float: %+v", v)
	}
}

func TestPercentile_ApproxMarkedOnceOverCap(t *testing.T) {
	p := NewPercentileWithCap(0.5, 10)
	for i := range int64(1000) {
		p.Add(eval.Int(i))
	}
	_, approx, ok := p.Result()
	if !ok {
		t.Fatal("Result() ok = false, want true")
	}
	if !approx {
		t.Fatal("approx = false, want true once total offered exceeds the reservoir cap")
	}
}

func TestPercentile_SkipsNonNumeric(t *testing.T) {
	p := NewPercentileWithCap(0.5, 100)
	p.Add(eval.Str("not a number"))
	p.Add(eval.Missing)
	p.Add(eval.Null)
	p.Add(eval.Int(42))
	v, _, ok := p.Result()
	if !ok || v.I != 42 {
		t.Fatalf("Result() = (%+v, ok=%v), want (42, true) — non-numeric values must be skipped", v, ok)
	}
}

func TestPercentile_EmptyReportsNotOK(t *testing.T) {
	p := NewPercentileWithCap(0.5, 100)
	_, _, ok := p.Result()
	if ok {
		t.Fatal("Result() ok = true on an empty Percentile, want false")
	}
}

func TestPercentile_DeterministicAcrossInstances(t *testing.T) {
	build := func() eval.Value {
		p := NewPercentileWithCap(0.95, 25)
		for i := range int64(300) {
			p.Add(eval.Int(i))
		}
		v, _, _ := p.Result()
		return v
	}
	a, b := build(), build()
	if a.I != b.I {
		t.Fatalf("two identical runs diverged: p95 = %d vs %d — fixed seed must make this reproducible", a.I, b.I)
	}
}
