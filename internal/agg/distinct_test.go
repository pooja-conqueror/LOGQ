package agg

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func TestCountDistinct_Basic(t *testing.T) {
	cd := NewCountDistinct()
	for _, s := range []string{"a", "b", "a", "c", "b", "a"} {
		cd.Add(eval.Str(s))
	}
	count, approx := cd.Result()
	if approx {
		t.Fatal("Result() should not report approx=true well under the cap")
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3 distinct values", count)
	}
}

func TestCountDistinct_MissingSkipped(t *testing.T) {
	cd := NewCountDistinct()
	cd.Add(eval.Missing)
	cd.Add(eval.Missing)
	cd.Add(eval.Str("x"))
	count, _ := cd.Result()
	if count != 1 {
		t.Fatalf("count = %d, want 1 — MISSING must never count as a distinct value", count)
	}
}

func TestCountDistinct_NullAndEmptyStringAreDistinct(t *testing.T) {
	cd := NewCountDistinct()
	cd.Add(eval.Null)
	cd.Add(eval.Str(""))
	count, _ := cd.Result()
	if count != 2 {
		t.Fatalf("count = %d, want 2 — Null and empty string must be distinct values", count)
	}
}

func TestCountDistinct_NumberCanonicalization(t *testing.T) {
	// Int(5) and Float(5.0) must count as the SAME distinct value — matches
	// GroupKey's own canonical-number rule (reused here via groupKeyPart),
	// otherwise the same logical value would split into two depending on
	// whether it happened to decode as an int or a float.
	cd := NewCountDistinct()
	cd.Add(eval.Int(5))
	cd.Add(eval.Float(5.0))
	count, _ := cd.Result()
	if count != 1 {
		t.Fatalf("count = %d, want 1 — Int(5) and Float(5.0) must canonicalize to the same value", count)
	}
}

func TestCountDistinct_CapFreezesAndReportsApprox(t *testing.T) {
	cd := NewCountDistinct()
	for i := range maxDistinctCap + 100 {
		cd.Add(eval.Int(int64(i)))
	}
	count, approx := cd.Result()
	if !approx {
		t.Fatal("Result() should report approx=true once the cap is exceeded")
	}
	if count != maxDistinctCap {
		t.Fatalf("count = %d, want exactly the cap (%d) once frozen", count, maxDistinctCap)
	}
}

func TestCountDistinct_CapExactBoundaryNotApprox(t *testing.T) {
	cd := NewCountDistinct()
	for i := range maxDistinctCap {
		cd.Add(eval.Int(int64(i)))
	}
	count, approx := cd.Result()
	if approx {
		t.Fatal("Result() should not report approx=true when the count lands EXACTLY on the cap with nothing overflowing it")
	}
	if count != maxDistinctCap {
		t.Fatalf("count = %d, want %d", count, maxDistinctCap)
	}
}

func TestCountDistinct_DeterministicAcrossInstances(t *testing.T) {
	// The whole point of a FIXED salt over a random one: two independent
	// instances processing the identical sequence must land on the exact
	// same result, every time — no per-process randomness anywhere in the
	// hash path.
	input := []string{"alpha", "beta", "gamma", "alpha", "delta", "beta", "epsilon"}

	cd1 := NewCountDistinct()
	cd2 := NewCountDistinct()
	for _, s := range input {
		cd1.Add(eval.Str(s))
		cd2.Add(eval.Str(s))
	}

	c1, a1 := cd1.Result()
	c2, a2 := cd2.Result()
	if c1 != c2 || a1 != a2 {
		t.Fatalf("two instances over identical input diverged: (%d,%v) != (%d,%v)", c1, a1, c2, a2)
	}
}

func TestSaltedFNV64_Deterministic(t *testing.T) {
	data := []byte("some canonical value text")
	first := saltedFNV64(data)
	second := saltedFNV64(data)
	if first != second {
		t.Fatal("saltedFNV64 must be a pure function — same input, same output, always")
	}
}

func TestSaltedFNV64_SaltActuallyChangesTheDigest(t *testing.T) {
	// Confirms the salt isn't a no-op: hashing with the salt folded in
	// must differ from plain, unsalted FNV-1a starting from the bare
	// public offset basis.
	data := []byte("attacker-controlled field value")

	unsalted := fnvOffsetBasis64
	for _, b := range data {
		unsalted ^= uint64(b)
		unsalted *= fnvPrime64
	}

	if saltedFNV64(data) == unsalted {
		t.Fatal("salted hash must differ from the unsalted public-constant FNV-1a digest")
	}
}

func TestClassifyDistinct_NewKey(t *testing.T) {
	seen := map[uint64]string{}
	isNew, collision := classifyDistinct(seen, 42, "foo")
	if !isNew || collision {
		t.Fatalf("classifyDistinct(new) = (%v, %v), want (true, false)", isNew, collision)
	}
}

func TestClassifyDistinct_RepeatOfSameKey(t *testing.T) {
	seen := map[uint64]string{42: "foo"}
	isNew, collision := classifyDistinct(seen, 42, "foo")
	if isNew || collision {
		t.Fatalf("classifyDistinct(repeat) = (%v, %v), want (false, false)", isNew, collision)
	}
}

func TestClassifyDistinct_GenuineCollision(t *testing.T) {
	// Same hash, DIFFERENT underlying value — a real collision, hand
	// constructed here since finding one via saltedFNV64 itself would be
	// computationally impractical in a unit test.
	seen := map[uint64]string{42: "foo"}
	isNew, collision := classifyDistinct(seen, 42, "bar")
	if isNew || !collision {
		t.Fatalf("classifyDistinct(collision) = (%v, %v), want (false, true)", isNew, collision)
	}
}

func TestCountDistinct_CollisionCountStartsZero(t *testing.T) {
	cd := NewCountDistinct()
	cd.Add(eval.Str("a"))
	cd.Add(eval.Str("b"))
	if cd.CollisionCount() != 0 {
		t.Fatalf("CollisionCount() = %d, want 0 for ordinary, non-colliding input", cd.CollisionCount())
	}
}
