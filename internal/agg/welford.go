package agg

import "github.com/pooja-conqueror/LOGQ/internal/eval"

// Avg computes a running mean via Welford's online algorithm (B. P.
// Welford, 1962) — numerically stable, and unlike a naive running-sum/n
// approach, immune to running-sum overflow entirely: it never accumulates
// an unbounded total at all, only a running mean updated incrementally
// per value (mean += (x - mean) / n), so there's no int64/float64 range
// concern regardless of how long the stream runs. Non-numeric values are
// skipped (Add returns false), never an error, same discipline as Sum.
type Avg struct {
	n    int64
	mean float64
}

func (a *Avg) Add(v eval.Value) bool {
	f, ok := numericValue(v)
	if !ok {
		return false
	}
	a.n++
	delta := f - a.mean
	a.mean += delta / float64(a.n)
	return true
}

// Result returns the running mean, and whether any numeric value has ever
// been added — false means "empty," the §8.4 `(none)` case.
func (a *Avg) Result() (mean float64, any bool) {
	return a.mean, a.n > 0
}
