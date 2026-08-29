package pipeline

import (
	"container/heap"
	"sort"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// Sort buffers at most Limit records (§7: "Materialize ≤ n post-filter
// records") — a bounded top-N via container/heap, not "collect the whole
// stream then truncate." It never emits per-record: Process always
// returns keep=false, done=false, because determining the true top-N
// requires seeing every remaining candidate — unlike Limit, which can
// stop after the first N arrivals, sort fundamentally cannot know it has
// "enough" until Flush.
type Sort struct {
	path  *query.PathRef
	order query.SortOrder
	limit int64

	items []sortItem
	seq   int64
}

// NewSort builds a Sort stage from the parsed SortStage AST. limit is
// already guaranteed >= 1 by the parser's POSINT grammar (commit 23), so
// this trusts that invariant rather than re-validating it.
func NewSort(ss *query.SortStage) *Sort {
	return &Sort{path: ss.Path, order: ss.Order, limit: ss.Limit}
}

type sortItem struct {
	rec *eval.Record
	key eval.Value
	seq int64
}

func (s *Sort) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	item := sortItem{rec: rec, key: rec.Resolve(s.path), seq: s.seq}
	s.seq++

	switch {
	case int64(len(s.items)) < s.limit:
		heap.Push(s, item)
	case itemLess(item, s.items[0], s.order):
		// Worse than (or tied with, and later than) the current worst
		// kept item — not good enough to displace it at capacity.
	default:
		s.items[0] = item
		heap.Fix(s, 0)
	}

	return nil, false, false
}

// Flush emits the buffered records in final sorted order — stable: ties
// resolve by original arrival order (seq), not by whatever order the
// heap's internal slice happens to be in after Push/Fix churn, which is
// NOT the same thing and would silently break stability if relied on
// directly.
func (s *Sort) Flush(emit func(*eval.Record)) {
	sort.SliceStable(s.items, func(i, j int) bool {
		a, b := s.items[i], s.items[j]
		if finalLess(a.key, b.key, s.order) {
			return true
		}
		if finalLess(b.key, a.key, s.order) {
			return false
		}
		return a.seq < b.seq
	})
	for _, item := range s.items {
		emit(item.rec)
	}
	s.items = nil
}

// finalLess reports whether a sorts strictly before b in the stage's
// final desired output order. MISSING always sorts last, in EITHER
// direction — checked before order is even consulted, so this holds
// regardless of asc/desc (§7: "MISSING sorts last in BOTH directions" —
// stated twice in the spec because it's the one rule everyone assumes is
// direction-dependent and gets wrong).
func finalLess(a, b eval.Value, order query.SortOrder) bool {
	aMissing := a.Kind == eval.KindMissing
	bMissing := b.Kind == eval.KindMissing
	switch {
	case aMissing && bMissing:
		return false
	case aMissing:
		return false
	case bMissing:
		return true
	}
	ord := eval.Compare(a, b)
	if ord == eval.Uncomparable {
		return false
	}
	if order == query.SortDesc {
		return ord == eval.Greater
	}
	return ord == eval.Less
}

// itemLess is the bounded-heap's eviction-priority ordering: true when a
// is "worse" (less desirable to keep) than b — meaning a sorts after b in
// the final order, or, on a tie, a arrived later than b. The item this
// considers worst sits at the heap's root, ready to be evicted first when
// a genuinely better candidate arrives. The tie rule matters for
// stability: without it, a new record merely EQUAL to an already-kept one
// could displace it, silently reordering equal-valued records away from
// their original arrival order.
func itemLess(a, b sortItem, order query.SortOrder) bool {
	if finalLess(b.key, a.key, order) {
		return true
	}
	if finalLess(a.key, b.key, order) {
		return false
	}
	return a.seq > b.seq
}

// heap.Interface implementation — Sort's own bounded item slice.
func (s *Sort) Len() int           { return len(s.items) }
func (s *Sort) Less(i, j int) bool { return itemLess(s.items[i], s.items[j], s.order) }
func (s *Sort) Swap(i, j int)      { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *Sort) Push(x any)         { s.items = append(s.items, x.(sortItem)) }
func (s *Sort) Pop() any {
	old := s.items
	n := len(old)
	item := old[n-1]
	s.items = old[:n-1]
	return item
}
