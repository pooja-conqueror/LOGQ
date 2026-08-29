package eval

// Order is the three-way (plus "uncomparable") result of Compare.
type Order int

const (
	Less    Order = -1
	Equal   Order = 0
	Greater Order = 1
	// Uncomparable covers cross-kind comparisons and Object/Array of any
	// kind (§1.4: "ordering comparisons return false [...] ordering ops on
	// Object/Array -> false"). The evaluator turns Uncomparable into a
	// plain `false` for every ordering operator (<, <=, >, >=) — no
	// implicit coercion happens here; the three sanctioned coercions
	// (string<->number, level ordinals, timestamp±duration) are applied by
	// the evaluator/coerce.go *before* calling Compare, not inside it.
	Uncomparable Order = 2
)

// Compare implements the spec's total order (§1.4): Number numeric, String
// byte-wise lexicographic (Go's native string < is already byte-wise for
// UTF-8, so no special-casing needed), Bool false<true, Timestamp
// chronological, Duration nanosecond-chronological. MISSING is
// deliberately not handled here — every operator involving MISSING is
// false unconditionally per M-2, decided by the evaluator before Compare
// is ever reached for that operand pair.
func Compare(a, b Value) Order {
	if a.Kind != b.Kind {
		return Uncomparable
	}
	switch a.Kind {
	case KindNumber:
		return compareNumber(a, b)
	case KindString:
		return orderFrom(a.S < b.S, a.S == b.S)
	case KindBool:
		return orderFrom(!a.B && b.B, a.B == b.B)
	case KindTimestamp:
		return orderFrom(a.Time.Before(b.Time), a.Time.Equal(b.Time))
	case KindDuration:
		return orderFrom(a.Dur < b.Dur, a.Dur == b.Dur)
	default: // Null, Array, Object, Missing: no ordering, per spec.
		return Uncomparable
	}
}

func compareNumber(a, b Value) Order {
	if a.IsInt && b.IsInt {
		return orderFrom(a.I < b.I, a.I == b.I)
	}
	af, bf := a.F, b.F
	if a.IsInt {
		af = float64(a.I)
	}
	if b.IsInt {
		bf = float64(b.I)
	}
	return orderFrom(af < bf, af == bf)
}

func orderFrom(less, equal bool) Order {
	switch {
	case equal:
		return Equal
	case less:
		return Less
	default:
		return Greater
	}
}

// DeepEqual implements ==/!= structural equality (§1.3, §5.2's truth
// table): two Nulls are equal, a Null is never equal to any other kind,
// Numbers compare numerically across the int/float split, Objects/Arrays
// compare by recursive structural equality regardless of field order.
func DeepEqual(a, b Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case KindNull:
		return true
	case KindMissing:
		// M-3: equality between two MISSING operands is false — but that
		// decision is made by the evaluator before any value comparison
		// happens at all (M-2/M-3 fire first). DeepEqual is never actually
		// invoked with a MISSING operand from eval's own code; this case
		// exists only so the switch is exhaustive.
		return false
	case KindBool:
		return a.B == b.B
	case KindNumber:
		return compareNumber(a, b) == Equal
	case KindString:
		return a.S == b.S
	case KindTimestamp:
		return a.Time.Equal(b.Time)
	case KindDuration:
		return a.Dur == b.Dur
	case KindArray:
		return arraysEqual(a.Arr, b.Arr)
	case KindObject:
		return recordsEqual(a.Obj, b.Obj)
	default:
		return false
	}
}

func arraysEqual(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func recordsEqual(x, y *Record) bool {
	if x == nil || y == nil {
		return x == y
	}
	if len(x.keys) != len(y.keys) {
		return false
	}
	for _, k := range x.keys {
		yv, ok := y.values[k]
		if !ok || !DeepEqual(x.values[k], yv) {
			return false
		}
	}
	return true
}
