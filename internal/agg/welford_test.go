package agg

import (
	"math"
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func TestWelford_MatchesNaiveAverageOnSmallInput(t *testing.T) {
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	var naive float64
	var a Avg
	for _, f := range values {
		naive += f
		a.Add(eval.Float(f))
	}
	naive /= float64(len(values))

	mean, any := a.Result()
	if !any {
		t.Fatal("Result() should report any=true")
	}
	if math.Abs(mean-naive) > 1e-9 {
		t.Fatalf("Welford mean = %v, naive mean = %v, want them to match closely", mean, naive)
	}
}

func TestWelford_SingleValue(t *testing.T) {
	var a Avg
	a.Add(eval.Int(42))
	mean, any := a.Result()
	if !any || mean != 42 {
		t.Fatalf("Result() = (%v, %v), want (42, true)", mean, any)
	}
}

func TestWelford_IncrementalCorrectness(t *testing.T) {
	// The running mean after each Add must match the mean of everything
	// added so far — proves the ONLINE property, not just the final
	// aggregate result.
	var a Avg
	var running []float64
	for _, f := range []float64{10, 20, 30, 5, 15} {
		running = append(running, f)
		a.Add(eval.Float(f))

		var sum float64
		for _, r := range running {
			sum += r
		}
		want := sum / float64(len(running))

		got, _ := a.Result()
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("after adding %v: mean = %v, want %v", running, got, want)
		}
	}
}
