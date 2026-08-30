package agg

import (
	"math"
	"math/rand"
	"sort"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// DefaultReservoirSeed is the fixed seed Reservoir uses unless a --seed
// flag overrides it (internal/pipeline/stats.go, wired to cmd/logq).
// Batch-mode determinism (§15) requires percentile output to be a pure
// function of (input, query, flags) — the seed must never come from
// process or wall-clock entropy, whatever value it's set to.
const DefaultReservoirSeed = 0

// DefaultMaxSample is the reservoir cap p50/p95/p99 use unless a
// --max-sample flag overrides it — chosen large enough that real-world
// grouped percentile queries stay exact in practice, while still bounding
// memory to O(cap) regardless of how many records actually flow through a
// group.
const DefaultMaxSample = 100000

// Reservoir implements Vitter's Algorithm L (J.S. Vitter, "Random
// Sampling with a Reservoir," ACM TOMS 11(1), 1985) — deliberately NOT
// "Algorithm R," the older, simpler Waterman/Knuth reservoir algorithm
// that draws one random number per incoming item (O(N) draws total).
// Algorithm L instead computes a skip-gap directly — how many incoming
// items to pass over before the next replacement — needing only
// O(k·(1+log(N/k))) random draws for the whole stream. The asymptotic
// win is real at logq's scale: files bigger than RAM, N in the billions,
// k in the tens of thousands.
//
// While the total item count stays at or under k, the reservoir holds
// EVERY item seen — the result is then exact, not a sample at all. Only
// once more than k items have arrived does it become a genuine uniform
// random sample of size k. The algorithm is inherently streaming (it
// only ever needs the NEXT item, never random access into the past),
// which is exactly the shape logq's per-record pipeline already offers.
type Reservoir struct {
	k     int
	rng   *rand.Rand
	items []eval.Value
	n     int64 // total items offered so far

	w        float64
	nextRepl int64
}

// NewReservoir creates a reservoir capped at k items (k must be
// positive), seeded deterministically.
func NewReservoir(k int, seed int64) *Reservoir {
	return &Reservoir{
		k:     k,
		rng:   rand.New(rand.NewSource(seed)),
		items: make([]eval.Value, 0, k),
	}
}

// Add offers v to the reservoir. Reservoir is a generic sampling
// primitive with no opinion on value kind — filtering to numeric-only
// values is Percentile.Add's job, one layer up.
func (r *Reservoir) Add(v eval.Value) {
	r.n++
	if len(r.items) < r.k {
		r.items = append(r.items, v)
		if len(r.items) == r.k {
			// Reservoir just became full — this is the point Vitter's
			// pseudocode initializes W and schedules the first future
			// replacement index.
			r.w = math.Exp(math.Log(r.rng.Float64()) / float64(r.k))
			r.scheduleNext()
		}
		return
	}
	if r.n == r.nextRepl {
		r.items[r.rng.Intn(r.k)] = v
		r.w *= math.Exp(math.Log(r.rng.Float64()) / float64(r.k))
		r.scheduleNext()
	}
}

// scheduleNext computes the next item index (in r.n's 1-based counting)
// at which a replacement should happen, per Algorithm L's skip-gap
// formula: i := i + floor(log(random())/log(1-W)) + 1.
func (r *Reservoir) scheduleNext() {
	skip := int64(math.Floor(math.Log(r.rng.Float64())/math.Log(1-r.w))) + 1
	r.nextRepl = r.n + skip
}

// Items returns the current sample. Callers that need to mutate or hold
// onto it beyond the next Add should copy it first.
func (r *Reservoir) Items() []eval.Value {
	return r.items
}

// Exact reports whether the reservoir holds every item ever offered
// (true) or a k-sized sample of a larger stream (false).
func (r *Reservoir) Exact() bool {
	return r.n <= int64(r.k)
}

// Percentile computes a single quantile (0.5, 0.95, 0.99, ...) over a
// numeric field via nearest-rank selection on a Reservoir sample: sort,
// then take the value at rank ceil(q·n), clamped to [1,n], 1-indexed.
//
// Nearest-rank is chosen deliberately over any of the several
// linear-interpolation variants: it always returns an ACTUAL value that
// was observed, never a synthetic average of two neighbors — so an
// integer-valued field (duration_ms, status) reports an exact integer
// percentile, and the choice needs no judgment call about which
// interpolation method (R's own docs list nine) to pick.
type Percentile struct {
	q         float64
	reservoir *Reservoir
}

// NewPercentile creates a percentile aggregator at quantile q (e.g. 0.95
// for p95), using the default reservoir cap.
func NewPercentile(q float64) *Percentile {
	return NewPercentileWithCap(q, DefaultMaxSample)
}

// NewPercentileWithCap is NewPercentile with an explicit reservoir cap —
// exported mainly so tests can exercise the approximate path without
// pushing DefaultMaxSample+1 values through in every case.
func NewPercentileWithCap(q float64, k int) *Percentile {
	return NewPercentileWithSeed(q, k, DefaultReservoirSeed)
}

// NewPercentileWithSeed is NewPercentileWithCap with an explicit reservoir
// seed too — the full constructor everything else delegates to, exported
// for a --seed flag override (§8.4: "fixed default seed 0 (--seed
// exposed)").
func NewPercentileWithSeed(q float64, k int, seed int64) *Percentile {
	return &Percentile{q: q, reservoir: NewReservoir(k, seed)}
}

// Add feeds v in if it's numeric (Int or Float). Anything else —
// including MISSING and Null — is skipped, matching Sum/Avg's own
// never-error, skip-non-numeric convention (§8.4).
func (p *Percentile) Add(v eval.Value) {
	if _, ok := numericValue(v); ok {
		p.reservoir.Add(v)
	}
}

// Result returns the selected value and whether it's exact (the
// reservoir held every numeric value ever seen) or approximate (drawn
// from a capped random sample) — approx should drive a "*"-marked column
// header at render time (§8.4). ok is false only when nothing numeric
// was ever added.
func (p *Percentile) Result() (v eval.Value, approx bool, ok bool) {
	items := p.reservoir.Items()
	if len(items) == 0 {
		return eval.Value{}, false, false
	}
	sorted := append([]eval.Value(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		return eval.Compare(sorted[i], sorted[j]) == eval.Less
	})

	rank := int(math.Ceil(p.q * float64(len(sorted))))
	rank = max(rank, 1)
	rank = min(rank, len(sorted))
	return sorted[rank-1], !p.reservoir.Exact(), true
}
