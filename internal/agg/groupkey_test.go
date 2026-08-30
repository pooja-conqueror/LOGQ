package agg

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func TestGroupKey_SingleValue(t *testing.T) {
	k1 := GroupKey([]eval.Value{eval.Str("error")})
	k2 := GroupKey([]eval.Value{eval.Str("error")})
	if k1 != k2 {
		t.Fatalf("identical single values must produce identical keys: %q != %q", k1, k2)
	}
}

func TestGroupKey_DifferentValuesProduceDifferentKeys(t *testing.T) {
	k1 := GroupKey([]eval.Value{eval.Str("error")})
	k2 := GroupKey([]eval.Value{eval.Str("info")})
	if k1 == k2 {
		t.Fatalf("different values must produce different keys, both got %q", k1)
	}
}

func TestGroupKey_MultiKeyJoin(t *testing.T) {
	k1 := GroupKey([]eval.Value{eval.Str("a"), eval.Str("b")})
	k2 := GroupKey([]eval.Value{eval.Str("a"), eval.Str("b")})
	if k1 != k2 {
		t.Fatalf("identical multi-value tuples must match: %q != %q", k1, k2)
	}
	// Order within the tuple matters — (a,b) != (b,a).
	k3 := GroupKey([]eval.Value{eval.Str("b"), eval.Str("a")})
	if k1 == k3 {
		t.Fatal("(a,b) and (b,a) must be different groups")
	}
}

func TestGroupKey_MissingNullRealValueThreeWayDistinction(t *testing.T) {
	// The headline feature (T-31): MISSING, Null, and a real value that
	// happens to render as the empty string must all form THREE distinct
	// groups, never collapsing into two or one.
	missing := GroupKey([]eval.Value{eval.Missing})
	null := GroupKey([]eval.Value{eval.Null})
	empty := GroupKey([]eval.Value{eval.Str("")})

	if missing == null {
		t.Fatalf("MISSING and Null must be distinct groups, both got %q", missing)
	}
	if missing == empty {
		t.Fatalf("MISSING and an empty string must be distinct groups, both got %q", missing)
	}
	if null == empty {
		t.Fatalf("Null and an empty string must be distinct groups, both got %q", null)
	}
}

func TestGroupKey_MissingNullDistinctWithinMultiKey(t *testing.T) {
	// The three-way distinction must hold even when MISSING/Null appear
	// alongside other, matching field values in a multi-key group-by.
	k1 := GroupKey([]eval.Value{eval.Str("svc"), eval.Missing})
	k2 := GroupKey([]eval.Value{eval.Str("svc"), eval.Null})
	k3 := GroupKey([]eval.Value{eval.Str("svc"), eval.Str("")})
	if k1 == k2 || k1 == k3 || k2 == k3 {
		t.Fatalf("expected three distinct keys, got %q, %q, %q", k1, k2, k3)
	}
}

func TestGroupKey_NumberFormattingMatchesCanonicalRules(t *testing.T) {
	// Int(5) and Float(5.0) must render identically as group-key text
	// (both "5"), matching eval.NumberString's own canonical rule —
	// otherwise the same logical value would split into two groups
	// depending on whether it happened to decode as an int or a float.
	k1 := GroupKey([]eval.Value{eval.Int(5)})
	k2 := GroupKey([]eval.Value{eval.Float(5.0)})
	if k1 != k2 {
		t.Fatalf("Int(5) and Float(5.0) should key identically: %q != %q", k1, k2)
	}
}

func TestGroupKey_ArraysWithDifferentContentsDiffer(t *testing.T) {
	k1 := GroupKey([]eval.Value{eval.Array([]eval.Value{eval.Int(1), eval.Int(2)})})
	k2 := GroupKey([]eval.Value{eval.Array([]eval.Value{eval.Int(1), eval.Int(3)})})
	if k1 == k2 {
		t.Fatal("arrays with different contents must produce different group keys")
	}
}

func TestGroupKey_ArraysWithSameContentsMatch(t *testing.T) {
	k1 := GroupKey([]eval.Value{eval.Array([]eval.Value{eval.Str("a"), eval.Str("b")})})
	k2 := GroupKey([]eval.Value{eval.Array([]eval.Value{eval.Str("a"), eval.Str("b")})})
	if k1 != k2 {
		t.Fatal("arrays with identical contents must produce the same group key")
	}
}

func TestGroupKey_ObjectsWithDifferentContentsDiffer(t *testing.T) {
	o1 := eval.NewRecord()
	o1.Set("x", eval.Int(1))
	o2 := eval.NewRecord()
	o2.Set("x", eval.Int(2))

	k1 := GroupKey([]eval.Value{eval.Object(o1)})
	k2 := GroupKey([]eval.Value{eval.Object(o2)})
	if k1 == k2 {
		t.Fatal("objects with different field values must produce different group keys")
	}
}

func TestGroupKey_ObjectsAreOrderIndependent(t *testing.T) {
	// Matches eval.DeepEqual's own order-independent equality (commit 8):
	// two records with the same fields set in different insertion order
	// must group together, not silently split into two groups.
	o1 := eval.NewRecord()
	o1.Set("a", eval.Int(1))
	o1.Set("b", eval.Int(2))

	o2 := eval.NewRecord()
	o2.Set("b", eval.Int(2))
	o2.Set("a", eval.Int(1))

	k1 := GroupKey([]eval.Value{eval.Object(o1)})
	k2 := GroupKey([]eval.Value{eval.Object(o2)})
	if k1 != k2 {
		t.Fatalf("objects with the same fields in different insertion order must key identically: %q != %q", k1, k2)
	}
}

func TestGroupKey_EmptyTuple(t *testing.T) {
	// A degenerate "group by nothing" call must not panic and should be
	// deterministic (used, in practice, only when a stats stage has no
	// "by" clause at all — a single global group).
	k1 := GroupKey(nil)
	k2 := GroupKey(nil)
	if k1 != k2 {
		t.Fatalf("GroupKey(nil) must be deterministic: %q != %q", k1, k2)
	}
}
