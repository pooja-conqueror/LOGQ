package eval

import (
	"testing"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/query"
)

func TestCompare_Number(t *testing.T) {
	cases := []struct {
		a, b Value
		want Order
	}{
		{Int(1), Int(2), Less},
		{Int(2), Int(1), Greater},
		{Int(5), Int(5), Equal},
		{Float(1.5), Float(2.5), Less},
		{Float(3.0), Float(3.0), Equal},
		{Int(5), Float(5.0), Equal},   // mixed int/float, numerically equal
		{Int(5), Float(5.5), Less},    // mixed, numeric compare
		{Float(6.0), Int(5), Greater}, // mixed, other direction
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Fatalf("Compare(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCompare_StringIsByteWise(t *testing.T) {
	// Go's native string < is already byte-wise lexicographic for UTF-8;
	// confirm Compare doesn't reinterpret it (e.g. via code-point
	// normalization or locale collation).
	if got := Compare(Str("apple"), Str("banana")); got != Less {
		t.Fatalf("Compare(apple, banana) = %v, want Less", got)
	}
	if got := Compare(Str("Z"), Str("a")); got != Less {
		// 'Z' (0x5A) sorts before 'a' (0x61) in byte-wise order.
		t.Fatalf("Compare(Z, a) = %v, want Less (byte-wise, not case-insensitive)", got)
	}
	if got := Compare(Str("café"), Str("cafe")); got != Greater {
		// 'é' is a multibyte UTF-8 sequence starting with a byte > 'e'.
		t.Fatalf("Compare(café, cafe) = %v, want Greater", got)
	}
}

func TestCompare_Bool(t *testing.T) {
	if Compare(Bool(false), Bool(true)) != Less {
		t.Fatal("false must be Less than true")
	}
	if Compare(Bool(true), Bool(false)) != Greater {
		t.Fatal("true must be Greater than false")
	}
	if Compare(Bool(true), Bool(true)) != Equal {
		t.Fatal("true must equal true")
	}
}

func TestCompare_Timestamp(t *testing.T) {
	t1 := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	if Compare(Timestamp(t1), Timestamp(t2)) != Less {
		t.Fatal("earlier timestamp must be Less")
	}
	if Compare(Timestamp(t2), Timestamp(t1)) != Greater {
		t.Fatal("later timestamp must be Greater")
	}
	if Compare(Timestamp(t1), Timestamp(t1)) != Equal {
		t.Fatal("identical timestamp must be Equal")
	}
}

func TestCompare_Duration(t *testing.T) {
	if Compare(DurationVal(time.Second), DurationVal(time.Minute)) != Less {
		t.Fatal("1s must be Less than 1m")
	}
}

func TestCompare_CrossKindIsUncomparable(t *testing.T) {
	cases := [][2]Value{
		{Int(1), Str("1")},
		{Bool(true), Int(1)},
		{Null, Int(0)},
		{Missing, Int(0)},
	}
	for _, c := range cases {
		if got := Compare(c[0], c[1]); got != Uncomparable {
			t.Fatalf("Compare(%+v, %+v) = %v, want Uncomparable", c[0], c[1], got)
		}
	}
}

func TestCompare_ObjectArrayNeverOrdered(t *testing.T) {
	// Same kind, but Object/Array still never order (§1.4).
	a := Array([]Value{Int(1)})
	b := Array([]Value{Int(2)})
	if got := Compare(a, b); got != Uncomparable {
		t.Fatalf("Compare(array, array) = %v, want Uncomparable", got)
	}
	oa, ob := Object(NewRecord()), Object(NewRecord())
	if got := Compare(oa, ob); got != Uncomparable {
		t.Fatalf("Compare(object, object) = %v, want Uncomparable", got)
	}
}

func TestDeepEqual_Null(t *testing.T) {
	if !DeepEqual(Null, Null) {
		t.Fatal("Null must equal Null")
	}
	if DeepEqual(Null, Int(0)) {
		t.Fatal("Null must not equal Int(0)")
	}
	if DeepEqual(Int(0), Null) {
		t.Fatal("Int(0) must not equal Null")
	}
}

func TestDeepEqual_NumberAcrossIntFloat(t *testing.T) {
	if !DeepEqual(Int(5), Float(5.0)) {
		t.Fatal("Int(5) must equal Float(5.0)")
	}
}

func TestDeepEqual_Array(t *testing.T) {
	a := Array([]Value{Int(1), Str("x")})
	b := Array([]Value{Int(1), Str("x")})
	c := Array([]Value{Int(1), Str("y")})
	if !DeepEqual(a, b) {
		t.Fatal("identical arrays must be equal")
	}
	if DeepEqual(a, c) {
		t.Fatal("arrays differing in one element must not be equal")
	}
	if DeepEqual(a, Array([]Value{Int(1)})) {
		t.Fatal("arrays of different length must not be equal")
	}
}

func TestDeepEqual_ObjectNestedAndOrderIndependent(t *testing.T) {
	r1 := NewRecord()
	r1.Set("a", Int(1))
	r1.Set("b", Array([]Value{Str("x"), Str("y")}))

	r2 := NewRecord()
	// Same fields, set in a different order — equality must not depend on
	// key order (only rendering does, later phases).
	r2.Set("b", Array([]Value{Str("x"), Str("y")}))
	r2.Set("a", Int(1))

	if !DeepEqual(Object(r1), Object(r2)) {
		t.Fatal("records with the same fields in different insertion order must be equal")
	}

	r3 := NewRecord()
	r3.Set("a", Int(1))
	r3.Set("b", Array([]Value{Str("x"), Str("z")}))
	if DeepEqual(Object(r1), Object(r3)) {
		t.Fatal("records with a differing nested value must not be equal")
	}
}

func TestRecord_SetGetKeys_OrderAndLastWins(t *testing.T) {
	r := NewRecord()
	r.Set("a", Int(1))
	r.Set("b", Int(2))
	r.Set("a", Int(99)) // overwrite; key order must not change

	if got := r.Get("a"); got.I != 99 {
		t.Fatalf("Get(a) = %+v, want overwritten value 99", got)
	}
	if got := r.Keys(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Keys() = %v, want [a b] (first-seen order preserved)", got)
	}
	if r.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", r.Len())
	}
}

func TestRecord_GetMissing(t *testing.T) {
	r := NewRecord()
	got := r.Get("nope")
	if got.Kind != KindMissing {
		t.Fatalf("Get(missing key) = %+v, want KindMissing", got)
	}
}

func mustParsePath(t *testing.T, src string) *query.PathRef {
	t.Helper()
	e, err := query.ParseFilterExpr(src + ` == 1`)
	if err != nil {
		t.Fatalf("failed to parse path %q: %v", src, err)
	}
	return e.(*query.Cmp).L.(*query.PathRef)
}

func TestResolve_SimpleField(t *testing.T) {
	r := NewRecord()
	r.Set("level", Str("error"))
	got := r.Resolve(mustParsePath(t, "level"))
	if got.Kind != KindString || got.S != "error" {
		t.Fatalf("Resolve(level) = %+v", got)
	}
}

func TestResolve_NestedField(t *testing.T) {
	inner := NewRecord()
	inner.Set("path", Str("/api/x"))
	outer := NewRecord()
	outer.Set("url", Object(inner))

	got := outer.Resolve(mustParsePath(t, "url.path"))
	if got.Kind != KindString || got.S != "/api/x" {
		t.Fatalf("Resolve(url.path) = %+v", got)
	}
}

func TestResolve_ArrayIndex(t *testing.T) {
	r := NewRecord()
	r.Set("items", Array([]Value{Str("a"), Str("b"), Str("c")}))

	got := r.Resolve(mustParsePath(t, "items[1]"))
	if got.Kind != KindString || got.S != "b" {
		t.Fatalf("Resolve(items[1]) = %+v", got)
	}
}

func TestResolve_MissingCases(t *testing.T) {
	inner := NewRecord()
	inner.Set("path", Str("/x"))
	r := NewRecord()
	r.Set("url", Object(inner))
	r.Set("items", Array([]Value{Str("a")}))
	r.Set("scalar", Int(5))

	cases := []string{
		"nope",       // field doesn't exist
		"url.nope",   // nested field doesn't exist
		"items[5]",   // index out of range
		"items[-1]",  // negative index
		"scalar.sub", // indexing into a non-object
		"scalar[0]",  // indexing into a non-array
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			got := r.Resolve(mustParsePath(t, src))
			if got.Kind != KindMissing {
				t.Fatalf("Resolve(%s) = %+v, want KindMissing", src, got)
			}
		})
	}
}
