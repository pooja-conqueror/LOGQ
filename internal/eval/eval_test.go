package eval

import (
	"testing"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/query"
)

func mustEval(t *testing.T, q string, rec *Record, now time.Time) bool {
	t.Helper()
	expr, err := query.ParseFilterExpr(q)
	if err != nil {
		t.Fatalf("parse(%q) error = %v", q, err)
	}
	cf, err := Compile(expr)
	if err != nil {
		t.Fatalf("compile(%q) error = %v", q, err)
	}
	return cf.Eval(rec, now)
}

// --- Generated truth-table matrix (§5.2), tested row by row -------------
//
// Legend matches the spec's own table: MISSING (field absent from the
// record), Null (field present with JSON null), and representative present
// values for the same-kind and cross-kind rows.

func TestTruthTable_MissingIsUniversallyFalse(t *testing.T) {
	// M-2: every binary operator except exists() returns false when either
	// operand is MISSING — never an error, regardless of the other side's
	// kind or which side is missing.
	ops := []string{"==", "!=", "<", "<=", ">", ">="}
	rec := NewRecord()
	rec.Set("present", Int(5))
	// "absent" is never Set — resolving it yields MISSING.

	for _, op := range ops {
		t.Run("missing_left_"+op, func(t *testing.T) {
			q := `absent ` + op + ` present`
			if got := mustEval(t, q, rec, time.Now()); got {
				t.Fatalf("%q on MISSING left = true, want false", q)
			}
		})
		t.Run("missing_right_"+op, func(t *testing.T) {
			q := `present ` + op + ` absent`
			if got := mustEval(t, q, rec, time.Now()); got {
				t.Fatalf("%q on MISSING right = true, want false", q)
			}
		})
		t.Run("missing_both_"+op, func(t *testing.T) {
			q := `absent ` + op + ` alsoabsent`
			if got := mustEval(t, q, rec, time.Now()); got {
				t.Fatalf("%q on MISSING both = true, want false", q)
			}
		})
	}
}

func TestTruthTable_MissingIsFalseUnderRegexAndIn(t *testing.T) {
	rec := NewRecord() // "absent" never set
	if mustEval(t, `absent ~ "x"`, rec, time.Now()) {
		t.Fatal("~ on MISSING = true, want false")
	}
	if mustEval(t, `absent !~ "x"`, rec, time.Now()) {
		t.Fatal("!~ on MISSING = true, want false (negation does not flip MISSING)")
	}
	if mustEval(t, `absent in [1, 2]`, rec, time.Now()) {
		t.Fatal("in on MISSING = true, want false")
	}
}

func TestTruthTable_ExistsIsTheOnlyMissingAwareOperator(t *testing.T) {
	rec := NewRecord()
	rec.Set("present", Int(1))
	if mustEval(t, `exists(present)`, rec, time.Now()) != true {
		t.Fatal("exists(present) = false, want true")
	}
	if mustEval(t, `exists(absent)`, rec, time.Now()) != false {
		t.Fatal("exists(absent) = true, want false")
	}
	// M-3: == between two MISSINGs is false; exists() is the real test.
	if mustEval(t, `absent == alsoabsent`, rec, time.Now()) {
		t.Fatal("MISSING == MISSING = true, want false (M-3)")
	}
}

func TestTruthTable_Null(t *testing.T) {
	rec := NewRecord()
	rec.Set("n", Null)
	rec.Set("n2", Null)
	rec.Set("v", Int(5))

	cases := []struct {
		q    string
		want bool
	}{
		{`n == n2`, true},  // Null, Null -> true
		{`n != n2`, false}, // Null, Null -> false
		{`n == v`, false},  // Null, V -> false
		{`n != v`, true},   // Null, V -> true
		{`v == n`, false},  // V, Null -> false
		{`v != n`, true},   // V, Null -> true
		{`n < v`, false},   // Null never orders
		{`n > v`, false},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			if got := mustEval(t, c.q, rec, time.Now()); got != c.want {
				t.Fatalf("%q = %v, want %v", c.q, got, c.want)
			}
		})
	}
}

func TestTruthTable_Numbers(t *testing.T) {
	rec := NewRecord()
	rec.Set("i", Int(5))
	rec.Set("j", Int(5))
	rec.Set("k", Int(9))

	cases := []struct {
		q    string
		want bool
	}{
		{`i == j`, true}, {`i != j`, false},
		{`i == k`, false}, {`i != k`, true},
		{`i < k`, true}, {`k < i`, false},
		{`i <= j`, true}, {`i >= j`, true},
		{`k > i`, true}, {`i > k`, false},
	}
	for _, c := range cases {
		if got := mustEval(t, c.q, rec, time.Now()); got != c.want {
			t.Fatalf("%q = %v, want %v", c.q, got, c.want)
		}
	}
}

func TestTruthTable_Strings(t *testing.T) {
	rec := NewRecord()
	rec.Set("s", Str("apple"))
	rec.Set("t", Str("apple"))
	rec.Set("u", Str("banana"))

	if !mustEval(t, `s == t`, rec, time.Now()) {
		t.Fatal("apple == apple should be true")
	}
	if mustEval(t, `s == u`, rec, time.Now()) {
		t.Fatal("apple == banana should be false")
	}
	if !mustEval(t, `s < u`, rec, time.Now()) {
		t.Fatal("apple < banana (byte-wise) should be true")
	}
}

func TestTruthTable_Bool(t *testing.T) {
	rec := NewRecord()
	rec.Set("f", Bool(false))
	rec.Set("tr", Bool(true))

	if !mustEval(t, `f < tr`, rec, time.Now()) {
		t.Fatal("false < true should be true")
	}
	if mustEval(t, `tr < f`, rec, time.Now()) {
		t.Fatal("true < false should be false")
	}
}

func TestTruthTable_ObjectArraySameKindDeepEqualNeverOrdered(t *testing.T) {
	rec := NewRecord()
	rec.Set("a1", Array([]Value{Int(1), Int(2)}))
	rec.Set("a2", Array([]Value{Int(1), Int(2)}))
	rec.Set("a3", Array([]Value{Int(9)}))

	if !mustEval(t, `a1 == a2`, rec, time.Now()) {
		t.Fatal("identical arrays should be == equal")
	}
	if mustEval(t, `a1 == a3`, rec, time.Now()) {
		t.Fatal("different arrays should not be == equal")
	}
	if mustEval(t, `a1 < a2`, rec, time.Now()) {
		t.Fatal("arrays must never order, even identical ones")
	}
}

func TestTruthTable_CrossKindWithoutCoercionIsFalse(t *testing.T) {
	rec := NewRecord()
	rec.Set("b", Bool(true))
	rec.Set("n", Int(1))

	if mustEval(t, `b == n`, rec, time.Now()) {
		t.Fatal("Bool == Number (no coercion applies) should be false")
	}
	if !mustEval(t, `b != n`, rec, time.Now()) {
		t.Fatal("Bool != Number (no coercion applies) should be true")
	}
	if mustEval(t, `b < n`, rec, time.Now()) {
		t.Fatal("Bool < Number should be false (Uncomparable)")
	}
}

// --- The three sanctioned coercions, wired end-to-end --------------------

func TestCoercionWiring_StringNumber(t *testing.T) {
	rec := NewRecord()
	rec.Set("status", Str("502"))

	if !mustEval(t, `status >= 500`, rec, time.Now()) {
		t.Fatal(`"502" >= 500 should coerce and be true`)
	}
	if !mustEval(t, `status == 502`, rec, time.Now()) {
		t.Fatal(`"502" == 502 should coerce and be true`)
	}
}

func TestCoercionWiring_LevelOrdinal(t *testing.T) {
	// The critical case: level field and literal are BOTH strings (same
	// Kind), so this only works if level-ordinal coercion is checked
	// before the same-kind byte-wise string fast path.
	rec := NewRecord()
	rec.Set("level", Str("error")) // ordinal 50

	if !mustEval(t, `level >= "warn"`, rec, time.Now()) {
		t.Fatal(`level "error" (50) >= "warn" (40) should be true ordinally`)
	}
	if mustEval(t, `level < "warn"`, rec, time.Now()) {
		t.Fatal(`level "error" (50) < "warn" (40) should be false ordinally`)
	}

	// Sanity: a byte-wise (non-ordinal) comparison of these same two
	// strings would go the OTHER way ("error" < "warn" alphabetically) —
	// confirming the ordinal path is actually engaged, not accidentally
	// falling back to string comparison.
	rec2 := NewRecord()
	rec2.Set("plainfield", Str("error"))
	if !mustEval(t, `plainfield < "warn"`, rec2, time.Now()) {
		t.Fatal(`a non-level field should compare byte-wise: "error" < "warn"`)
	}
}

func TestCoercionWiring_LevelOrdinalAliasAndNumeric(t *testing.T) {
	rec := NewRecord()
	rec.Set("severity", Str("warning")) // alias for warn, ordinal 40
	// == does NOT use ordinal coercion per design — "warning" and "warn"
	// are different strings, so plain equality is false here.
	if mustEval(t, `severity == "warn"`, rec, time.Now()) {
		t.Fatal(`"warning" == "warn" via plain string equality should be false`)
	}
	if !mustEval(t, `severity >= "warn"`, rec, time.Now()) {
		t.Fatal(`severity "warning" (40) >= "warn" (40) should be true ordinally`)
	}
}

func TestCoercionWiring_LevelOrdinalUnknownTokenFallsBackToStringCompare(t *testing.T) {
	rec := NewRecord()
	rec.Set("level", Str("critical")) // not in the built-in table
	// Falls back to byte-wise string compare per §6.2: "critical" < "warn"
	// byte-wise (c < w).
	if !mustEval(t, `level < "warn"`, rec, time.Now()) {
		t.Fatal(`unknown level token should fall back to byte-wise string compare`)
	}
}

func TestCoercionWiring_TimestampDuration(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	rec := NewRecord()
	// Phase 6 will populate this via the real ts-detection ladder; for now
	// exercise the wiring directly with a manually-set Timestamp value.
	rec.Set("ts", Timestamp(now.Add(-30*time.Minute)))

	if !mustEval(t, `ts >= -1h`, rec, now) {
		t.Fatal(`ts (now-30m) >= -1h (now-1h) should be true`)
	}
	if mustEval(t, `ts >= -10m`, rec, now) {
		t.Fatal(`ts (now-30m) >= -10m (now-10m) should be false`)
	}
}

func TestCoercionWiring_InWithNumericStringCoercion(t *testing.T) {
	rec := NewRecord()
	rec.Set("status", Str("502"))
	if !mustEval(t, `status in [500, 502, 503]`, rec, time.Now()) {
		t.Fatal(`"502" in [500, 502, 503] should coerce and match`)
	}
	rec.Set("status", Str("404"))
	if mustEval(t, `status in [500, 502, 503]`, rec, time.Now()) {
		t.Fatal(`"404" in [500, 502, 503] should not match`)
	}
}

// --- Logical combinators --------------------------------------------------

func TestEval_AndOrNotShortCircuitAndCombine(t *testing.T) {
	rec := NewRecord()
	rec.Set("a", Bool(true))
	rec.Set("b", Bool(false))

	if !mustEval(t, `a == true and not b == true`, rec, time.Now()) {
		t.Fatal("a and not b should be true")
	}
	if !mustEval(t, `a == true or b == true`, rec, time.Now()) {
		t.Fatal("a or b should be true")
	}
	if mustEval(t, `not a == true`, rec, time.Now()) {
		t.Fatal("not a should be false")
	}
}

func TestEval_NilFilterMatchesEverything(t *testing.T) {
	cf, err := Compile(nil)
	if err != nil {
		t.Fatalf("Compile(nil) error = %v", err)
	}
	if !cf.Eval(NewRecord(), time.Now()) {
		t.Fatal("a nil filter must match every record")
	}
}

// --- Regex -----------------------------------------------------------------

func TestEval_Regex(t *testing.T) {
	rec := NewRecord()
	rec.Set("msg", Str("auth failed for user bob"))
	rec.Set("code", Int(5))

	if !mustEval(t, `msg ~ "auth failed"`, rec, time.Now()) {
		t.Fatal("regex match should be true")
	}
	if mustEval(t, `msg !~ "auth failed"`, rec, time.Now()) {
		t.Fatal("negated regex match should be false")
	}
	if mustEval(t, `msg ~ "does not appear"`, rec, time.Now()) {
		t.Fatal("non-matching regex should be false")
	}
	// Matching a non-String operand is a type mismatch, not an error.
	if mustEval(t, `code ~ "5"`, rec, time.Now()) {
		t.Fatal("regex against a non-String value should be false")
	}
}

func TestEval_CompileError_InvalidRegex(t *testing.T) {
	expr, err := query.ParseFilterExpr(`msg ~ "("`) // unbalanced paren, invalid RE2
	if err != nil {
		t.Fatalf("parse should succeed (regex validity isn't the parser's job): %v", err)
	}
	_, err = Compile(expr)
	if err == nil {
		t.Fatal("Compile should reject an invalid regex pattern")
	}
	ce, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("error type = %T, want *CompileError", err)
	}
	if got := ce.Error(); got == "" {
		t.Fatal("CompileError.Error() must not be empty")
	}
}

// --- Nested field access, incl. MISSING-producing edge cases -------------

func TestEval_NestedPathAndArrayIndex(t *testing.T) {
	url := NewRecord()
	url.Set("path", Str("/api/x"))
	rec := NewRecord()
	rec.Set("url", Object(url))
	rec.Set("items", Array([]Value{Int(10), Int(20)}))

	if !mustEval(t, `url.path == "/api/x"`, rec, time.Now()) {
		t.Fatal("nested field access should resolve correctly")
	}
	if !mustEval(t, `items[1] == 20`, rec, time.Now()) {
		t.Fatal("array index access should resolve correctly")
	}
	if mustEval(t, `items[9] == 20`, rec, time.Now()) {
		t.Fatal("out-of-range index should resolve to MISSING (false)")
	}
}
