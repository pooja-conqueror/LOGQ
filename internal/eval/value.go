// Package eval implements logq's value model (records, MISSING-vs-Null
// semantics, total order) and, from Phase 3 commit 10 onward, the
// three-valued filter evaluator itself.
package eval

import (
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// Kind identifies a Value's runtime type. Timestamp and Duration aren't
// among the raw kinds a log format can decode directly (JSON has no native
// timestamp type) — they arise as derived values: a record's resolved `ts`
// field, and duration literals in the query language. Both still need a
// real, comparable Kind so the total order in order.go has something to
// dispatch on for the spec's Timestamp±Duration coercion.
type Kind int

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
	KindTimestamp
	KindDuration
	// KindMissing is the result of a path resolution that found nothing —
	// M-1: distinct from KindNull. It is never present as a value actually
	// stored inside a Record; it only ever appears as Resolve's return
	// value or an evaluator operand.
	KindMissing
)

// Value is a tagged union over Kind. Using explicit typed fields (rather
// than interface{}) avoids per-value heap allocation/boxing on the hot
// filter-evaluation path, which matters for the project's constant-memory,
// multi-GB streaming goal.
type Value struct {
	Kind  Kind
	B     bool
	I     int64
	F     float64
	IsInt bool // valid when Kind == KindNumber: true selects I, false selects F
	S     string
	Time  time.Time
	Dur   time.Duration
	Arr   []Value
	Obj   *Record
}

var (
	Null    = Value{Kind: KindNull}
	Missing = Value{Kind: KindMissing}
)

func Bool(b bool) Value                 { return Value{Kind: KindBool, B: b} }
func Int(i int64) Value                 { return Value{Kind: KindNumber, I: i, IsInt: true} }
func Float(f float64) Value             { return Value{Kind: KindNumber, F: f} }
func Str(s string) Value                { return Value{Kind: KindString, S: s} }
func Timestamp(t time.Time) Value       { return Value{Kind: KindTimestamp, Time: t} }
func DurationVal(d time.Duration) Value { return Value{Kind: KindDuration, Dur: d} }
func Array(vs []Value) Value            { return Value{Kind: KindArray, Arr: vs} }
func Object(r *Record) Value            { return Value{Kind: KindObject, Obj: r} }

// Record is an ordered map field->Value, preserving the source document's
// key order (§1.1). Ts/HasTs and LevelOrd/HasLevel are derived properties
// resolved in Phase 6 (commit 21) — left unpopulated (HasTs/HasLevel false)
// until then.
type Record struct {
	keys   []string
	values map[string]Value

	Time     time.Time
	HasTime  bool
	LevelOrd int
	HasLevel bool
}

// NewRecord returns an empty, ready-to-populate Record.
func NewRecord() *Record {
	return &Record{values: make(map[string]Value)}
}

// Set adds or overwrites a field. First-seen key order is preserved even
// when a later Set overwrites an existing key's value — this is exactly
// the "duplicate keys -> last-wins, but key order unchanged" behavior the
// JSON decoder (Phase 4) needs.
func (r *Record) Set(key string, v Value) {
	if _, exists := r.values[key]; !exists {
		r.keys = append(r.keys, key)
	}
	r.values[key] = v
}

// Get returns the value for key, or Missing if the key is absent.
func (r *Record) Get(key string) Value {
	if v, ok := r.values[key]; ok {
		return v
	}
	return Missing
}

// Len reports the number of fields.
func (r *Record) Len() int { return len(r.keys) }

// Keys returns field names in first-seen (insertion) order. The returned
// slice must not be mutated by callers.
func (r *Record) Keys() []string { return r.keys }

// Resolve walks path against r, per the query language's Path grammar
// (dotted field access and bracket indexing). Any failure along the way —
// a missing field, an out-of-range or negative array index, or indexing
// into a value that isn't an object/array — resolves to Missing rather
// than an error, per M-1/M-2: path resolution never fails, it only ever
// returns Missing.
//
// The single-segment path "ts" is virtual (§6.1): it always means the
// record's RESOLVED timestamp, populated by ResolveRecordTimestamp's
// candidate-priority ladder — never a literal raw field that merely
// happens to be named "ts". That's the whole point of the candidate-name
// system: `ts >= -1h` behaves identically whether the source JSON's real
// field was called "ts", "timestamp", or "@timestamp".
func (r *Record) Resolve(path *query.PathRef) Value {
	if len(path.Segs) == 1 && !path.Segs[0].IsIndex && path.Segs[0].Ident == "ts" {
		if r.HasTime {
			return Timestamp(r.Time)
		}
		return Missing
	}

	cur := Object(r)
	for _, seg := range path.Segs {
		if seg.IsIndex {
			if cur.Kind != KindArray {
				return Missing
			}
			if seg.Index < 0 || seg.Index >= int64(len(cur.Arr)) {
				return Missing
			}
			cur = cur.Arr[seg.Index]
			continue
		}
		if cur.Kind != KindObject || cur.Obj == nil {
			return Missing
		}
		cur = cur.Obj.Get(seg.Ident)
	}
	return cur
}
