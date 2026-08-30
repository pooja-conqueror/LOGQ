package agg

import "github.com/pooja-conqueror/LOGQ/internal/eval"

// FNV-64a constants — the standard, public offset basis and prime.
// Hand-implemented here rather than using stdlib's hash/fnv: keying this
// hash means starting from a NON-standard offset basis (distinctSalt
// XORed into the public one), and hash/fnv's public API doesn't expose a
// way to set that. The algorithm itself is five lines and public domain,
// so implementing it directly is simpler and more honest than working
// around stdlib's API to fake the same effect via some other trick (e.g.
// hashing the salt as a prefix, which is a different, weaker scheme).
const (
	fnvOffsetBasis64 uint64 = 0xcbf29ce484222325
	fnvPrime64       uint64 = 0x100000001b3
)

// distinctSalt is a fixed, documented 64-bit constant XORed into FNV-64a's
// offset basis before hashing anything — see CountDistinct's doc comment
// for the full rationale. Arbitrary (golden-ratio-derived, a common
// source of "looks random, is fixed" constants) but permanent: changing
// it in a later version would silently change which distinct values
// happen to collide, breaking any reproducibility comparison against an
// older binary's output on the same input.
const distinctSalt uint64 = 0x9E3779B97F4A7C15

// saltedFNV64 hashes data with FNV-1a, starting from distinctSalt XORed
// into the standard offset basis instead of the bare public constant.
func saltedFNV64(data []byte) uint64 {
	h := fnvOffsetBasis64 ^ distinctSalt
	for _, b := range data {
		h ^= uint64(b)
		h *= fnvPrime64
	}
	return h
}

// maxDistinctCap is the default cap on count_distinct's tracked set size
// (§8.4). Beyond it, counting freezes and Result reports the ">=" form —
// memory stays O(min(distinct, cap)), never O(distinct).
const maxDistinctCap = 65536

// CountDistinct estimates the number of distinct canonical values seen,
// via a capped hash set (§8.4), hashed with FNV-1a keyed by a FIXED,
// documented salt — never the public FNV-64a constant, and deliberately
// NOT a per-process random seed. Go's own hash/maphash (the stdlib
// SipHash-class answer to exactly this problem) was seriously considered
// and rejected here specifically:
//
//   - Log field values are attacker-influenceable input — exactly the
//     class hash-flooding attacks target (the same bug class that forced
//     Node.js/PHP/Python/Java toward randomized hash seeds after the
//     2011-12 disclosure wave). An unkeyed, public-constant FNV-64a would
//     let an attacker replay an off-the-shelf FNV-64a collision generator
//     against logq specifically, degrading inserts and skewing the count.
//   - A per-process RANDOM seed closes that hole, but at a real cost
//     here: it would let two runs of logq over IDENTICAL input diverge on
//     the astronomically rare collision-boundary case at full cap
//     saturation — breaking this project's own batch-mode determinism
//     guarantee (§15), where a run's output is supposed to be a pure
//     function of (input bytes, query, flags, version), full stop.
//   - A FIXED salt gets most of the real-world benefit of keying — an
//     attacker can no longer just reuse a generic, public FNV-64a
//     collision set against logq — while staying fully deterministic
//     across every run. "Keyed, not secret": stated plainly here, not
//     oversold as a cryptographic guarantee it isn't.
//
// Collision math at full 65536-item cap saturation (birthday bound,
// n²/(2·2⁶⁴) for n=65536): ≈1.16×10⁻¹⁰, roughly 1 in 8.6 billion.
// Collisions are actively detected and counted (CollisionCount), not
// silently absorbed into the reported distinct count.
type CountDistinct struct {
	seen           map[uint64]string // hash -> the first canonical string that produced it
	collisionCount int64
	overflowed     bool // true once a genuinely NEW value was dropped for being over cap
}

func NewCountDistinct() *CountDistinct {
	return &CountDistinct{seen: make(map[uint64]string)}
}

// Add folds v's canonical encoding into the tracked set. MISSING is
// skipped — an absent field isn't "a distinct absence" to count, it's
// simply not there, matching Track B's own three-valued-logic ethos.
//
// The cap only ever blocks a genuinely NEW value from being recorded — a
// repeat of an already-tracked value (or a collision landing on an
// already-occupied bucket) is still classified correctly even once the
// set is full, so a run that lands EXACTLY on the cap with no further
// new values arriving still reports an exact count, not an approximate
// one (see overflowed).
func (c *CountDistinct) Add(v eval.Value) {
	if v.Kind == eval.KindMissing {
		return
	}
	key := groupKeyPart(v) // reuses the same canonical-encoding logic GroupKey uses
	h := saltedFNV64([]byte(key))
	isNew, collision := classifyDistinct(c.seen, h, key)
	if collision {
		c.collisionCount++
		return
	}
	if !isNew {
		return // an ordinary repeat of an already-seen value
	}
	if len(c.seen) >= maxDistinctCap {
		c.overflowed = true // frozen — Result reports the ">=" form from here on
		return
	}
	c.seen[h] = key
}

// classifyDistinct is Add's core classification logic, factored out (and
// kept non-mutating) so it can be unit-tested directly against a
// hand-constructed map and synthetic hash values — finding a genuine
// FNV-64a collision to exercise this path through Add's real public API
// would be computationally impractical in a test. isNew is true the
// first time hash is seen at all; isCollision is true when hash was
// already present but under a DIFFERENT key (a real collision, not a
// repeat of the same value).
func classifyDistinct(seen map[uint64]string, hash uint64, key string) (isNew, isCollision bool) {
	existing, ok := seen[hash]
	if !ok {
		return true, false
	}
	if existing == key {
		return false, false
	}
	return false, true
}

// Result returns the tracked distinct count and whether the cap turned
// away at least one genuinely new value (§8.4: "on cap: freeze, report
// >=65536" — approx=true is that case; Count is then the cap itself, and
// the true count is >= it). Landing exactly on the cap with nothing left
// over still reports an exact count.
func (c *CountDistinct) Result() (count int64, approx bool) {
	return int64(len(c.seen)), c.overflowed
}

// CollisionCount reports how many Add calls hit a hash bucket already
// occupied by a DIFFERENT canonical value — expected to be astronomically
// rare (see the type doc comment) but counted, per the spec's own "FNV
// collisions counted" requirement, rather than silently ignored.
func (c *CountDistinct) CollisionCount() int64 {
	return c.collisionCount
}
