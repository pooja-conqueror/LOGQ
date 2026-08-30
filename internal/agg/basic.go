package agg

import (
	"math/big"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// Count is a running count of records seen (§8.4: "count: ++, O(1)").
// count() takes no path argument in the grammar, so there's nothing to
// inspect per record — every Add call increments by one unconditionally.
type Count struct{ n int64 }

func (c *Count) Add()          { c.n++ }
func (c *Count) Result() int64 { return c.n }

// numericValue extracts v's numeric value as float64, or ok=false if v
// isn't a Number at all. Shared by Sum's float-mode path and Avg — both
// need "the number, regardless of whether it's stored as int or float,"
// unlike Sum's int64 fast path, which needs the raw integer specifically.
func numericValue(v eval.Value) (f float64, ok bool) {
	if v.Kind != eval.KindNumber {
		return 0, false
	}
	if v.IsInt {
		return float64(v.I), true
	}
	return v.F, true
}

// Sum accumulates a running sum with an int64 fast path and a
// math/big.Int fallback on integer overflow (§8.4: "int128-emulated via
// big.Int fallback after int64 overflow check"). The moment a non-integer
// (float) value is added, the running total switches to float64
// permanently — big.Int overflow protection is specifically an
// integer-domain concern; ordinary float64 range/precision rules apply
// once any float is involved, same as any other numeric summation.
// Non-numeric values are skipped (Add returns false), never an error —
// the caller is responsible for a skipped_nonnumeric counter (Phase 10).
type Sum struct {
	haveAny  bool
	isFloat  bool
	floatSum float64
	intSum   int64
	overflow bool
	bigSum   big.Int
}

func (s *Sum) Add(v eval.Value) bool {
	if v.Kind != eval.KindNumber {
		return false
	}
	s.haveAny = true

	if s.isFloat {
		f, _ := numericValue(v)
		s.floatSum += f
		return true
	}
	if !v.IsInt {
		// First float seen: fold whatever the int-domain total currently
		// is into floatSum and switch modes for the rest of this Sum's
		// lifetime.
		s.isFloat = true
		if s.overflow {
			f, _ := new(big.Float).SetInt(&s.bigSum).Float64()
			s.floatSum = f
		} else {
			s.floatSum = float64(s.intSum)
		}
		s.floatSum += v.F
		return true
	}

	if s.overflow {
		s.bigSum.Add(&s.bigSum, big.NewInt(v.I))
		return true
	}
	next := s.intSum + v.I
	// Overflow check: if the result's sign doesn't match what unbounded
	// addition would produce, int64 wrapped around.
	if (v.I > 0 && next < s.intSum) || (v.I < 0 && next > s.intSum) {
		s.overflow = true
		s.bigSum.SetInt64(s.intSum)
		s.bigSum.Add(&s.bigSum, big.NewInt(v.I))
		return true
	}
	s.intSum = next
	return true
}

// Result returns the running sum as a Value (Int while everything summed
// stayed integer and in range, Float once a float was involved or int64
// overflowed and even big.Int no longer fits back into one), and whether
// any numeric value has ever been added at all — false means "empty," the
// §8.4 `(none)`/0 case the caller decides how to render, not this type.
func (s *Sum) Result() (value eval.Value, any bool) {
	if !s.haveAny {
		return eval.Value{}, false
	}
	if s.isFloat {
		return eval.Float(s.floatSum), true
	}
	if s.overflow {
		if s.bigSum.IsInt64() {
			return eval.Int(s.bigSum.Int64()), true
		}
		// A genuinely huge sum beyond int64 range even via big.Int is an
		// extreme edge case; fall back to float64, a documented, honest
		// precision-loss point rather than growing an unbounded-precision
		// output type for it.
		f, _ := new(big.Float).SetInt(&s.bigSum).Float64()
		return eval.Float(f), true
	}
	return eval.Int(s.intSum), true
}

// isOrderable reports whether Kind can ever participate in the total
// order (order.go) at all — mirrors Compare's own switch exactly. Used to
// keep MinMax from ever "locking in" on a value whose kind can never be
// meaningfully compared (Missing, Null, Array, Object) as its initial
// current value, which would otherwise let that permanently-Uncomparable
// value block every later, genuinely orderable value from ever replacing
// it.
func isOrderable(k eval.Kind) bool {
	switch k {
	case eval.KindNumber, eval.KindString, eval.KindBool, eval.KindTimestamp, eval.KindDuration:
		return true
	default:
		return false
	}
}

// MinMax tracks a running minimum or maximum by the total order
// (order.go, commit 8), per §8.4. "Starved" (Add never called with an
// orderable value) reports ok=false at Result, which the caller renders
// as `(none)` — this type doesn't decide the rendering itself.
type MinMax struct {
	want eval.Order // eval.Less for min, eval.Greater for max
	cur  eval.Value
	have bool
}

func NewMin() *MinMax { return &MinMax{want: eval.Less} }
func NewMax() *MinMax { return &MinMax{want: eval.Greater} }

func (m *MinMax) Add(v eval.Value) {
	if !isOrderable(v.Kind) {
		return
	}
	if !m.have {
		m.cur, m.have = v, true
		return
	}
	if eval.Compare(v, m.cur) == m.want {
		m.cur = v
	}
}

func (m *MinMax) Result() (eval.Value, bool) {
	return m.cur, m.have
}
