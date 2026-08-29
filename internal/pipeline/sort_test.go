package pipeline

import (
	"strconv"
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

func mustSort(t *testing.T, src string) *Sort {
	t.Helper()
	q, err := query.ParseQuery(src)
	if err != nil {
		t.Fatalf("ParseQuery(%q) error = %v", src, err)
	}
	return NewSort(q.Stages[0].(*query.SortStage))
}

func recWith(field string, v eval.Value) *eval.Record {
	rec := eval.NewRecord()
	rec.Set(field, v)
	return rec
}

func flushAll(s *Sort) []*eval.Record {
	var out []*eval.Record
	s.Flush(func(rec *eval.Record) { out = append(out, rec) })
	return out
}

func TestSort_ProcessNeverEmitsPerRecord(t *testing.T) {
	s := mustSort(t, `| sort n limit 10`)
	out, keep, done := s.Process(recWith("n", eval.Int(1)))
	if out != nil || keep || done {
		t.Fatalf("Process = (%v, %v, %v), want (nil, false, false) — sort only ever emits at Flush", out, keep, done)
	}
}

func TestSort_Ascending(t *testing.T) {
	s := mustSort(t, `| sort n asc limit 10`)
	for _, n := range []int64{3, 1, 4, 1, 5} {
		s.Process(recWith("n", eval.Int(n)))
	}
	got := flushAll(s)
	want := []int64{1, 1, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Get("n").I != w {
			t.Fatalf("got[%d].n = %d, want %d (full: %v)", i, got[i].Get("n").I, w, extractN(got))
		}
	}
}

func TestSort_Descending(t *testing.T) {
	s := mustSort(t, `| sort n desc limit 10`)
	for _, n := range []int64{3, 1, 4, 1, 5} {
		s.Process(recWith("n", eval.Int(n)))
	}
	got := extractN(flushAll(s))
	want := []int64{5, 4, 3, 1, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func extractN(recs []*eval.Record) []int64 {
	out := make([]int64, len(recs))
	for i, r := range recs {
		out[i] = r.Get("n").I
	}
	return out
}

func TestSort_BoundedTopK_EvictsWorseForBetterLateArrival(t *testing.T) {
	// Proves the bounded heap genuinely evicts — not just "keep the first
	// K seen." limit=3, descending: feed 1,2,3 first (fills the heap),
	// then 100 (must evict the current worst, which is 1).
	s := mustSort(t, `| sort n desc limit 3`)
	for _, n := range []int64{1, 2, 3, 100} {
		s.Process(recWith("n", eval.Int(n)))
	}
	got := extractN(flushAll(s))
	want := []int64{100, 3, 2}
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 records (bounded to limit)", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v — 1 should have been evicted for 100", got, want)
		}
	}
}

func TestSort_BoundedTopK_Ascending_EvictsWorseForBetterLateArrival(t *testing.T) {
	s := mustSort(t, `| sort n asc limit 3`)
	for _, n := range []int64{100, 50, 10, -5} {
		s.Process(recWith("n", eval.Int(n)))
	}
	got := extractN(flushAll(s))
	want := []int64{-5, 10, 50}
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 records", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v — 100 should have been evicted for -5", got, want)
		}
	}
}

func TestSort_FewerRecordsThanLimit(t *testing.T) {
	s := mustSort(t, `| sort n asc limit 100`)
	s.Process(recWith("n", eval.Int(2)))
	s.Process(recWith("n", eval.Int(1)))
	got := extractN(flushAll(s))
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("got %v, want [1 2]", got)
	}
}

func TestSort_MissingSortsLastAscending(t *testing.T) {
	s := mustSort(t, `| sort n asc limit 10`)
	s.Process(recWith("n", eval.Int(5)))
	s.Process(eval.NewRecord()) // "n" is missing entirely
	s.Process(recWith("n", eval.Int(1)))

	got := flushAll(s)
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	if got[0].Get("n").I != 1 || got[1].Get("n").I != 5 {
		t.Fatalf("present values out of order: %v", extractNSafe(got))
	}
	if got[2].Get("n").Kind != eval.KindMissing {
		t.Fatal("the record with a missing 'n' must sort last, not first or middle")
	}
}

func TestSort_MissingSortsLastDescendingToo(t *testing.T) {
	// The critical "in BOTH directions" case: naive direction-flipping
	// (just reverse the comparator) would put MISSING first in desc order
	// instead of last — this must not happen.
	s := mustSort(t, `| sort n desc limit 10`)
	s.Process(recWith("n", eval.Int(5)))
	s.Process(eval.NewRecord())
	s.Process(recWith("n", eval.Int(1)))

	got := flushAll(s)
	if got[0].Get("n").I != 5 || got[1].Get("n").I != 1 {
		t.Fatalf("present values out of order (desc): %v", extractNSafe(got))
	}
	if got[2].Get("n").Kind != eval.KindMissing {
		t.Fatal("MISSING must sort last in descending order too, not first")
	}
}

func extractNSafe(recs []*eval.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		v := r.Get("n")
		if v.Kind == eval.KindMissing {
			out[i] = "MISSING"
		} else {
			out[i] = "n=" + strconv.FormatInt(v.I, 10)
		}
	}
	return out
}

func TestSort_StableOnTiesPreservesArrivalOrder(t *testing.T) {
	// Three records tied at n=5, tagged a/b/c in arrival order; with a
	// limit exactly equal to the count, none should ever be evicted, and
	// output order must match arrival order exactly.
	s := mustSort(t, `| sort n asc limit 3`)
	for _, tag := range []string{"a", "b", "c"} {
		rec := eval.NewRecord()
		rec.Set("n", eval.Int(5))
		rec.Set("tag", eval.Str(tag))
		s.Process(rec)
	}
	got := flushAll(s)
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if got[i].Get("tag").S != w {
			t.Fatalf("got[%d].tag = %q, want %q (stability broken on a tie)", i, got[i].Get("tag").S, w)
		}
	}
}

func TestSort_StableUnderEvictionPressure_LaterTieNeverDisplacesEarlier(t *testing.T) {
	// limit=2, all three tied at n=5: a and b arrive first and fill the
	// heap; c arrives later, tied — c must NOT displace either a or b.
	s := mustSort(t, `| sort n asc limit 2`)
	for _, tag := range []string{"a", "b", "c"} {
		rec := eval.NewRecord()
		rec.Set("n", eval.Int(5))
		rec.Set("tag", eval.Str(tag))
		s.Process(rec)
	}
	got := flushAll(s)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Get("tag").S != "a" || got[1].Get("tag").S != "b" {
		t.Fatalf("got tags %q,%q — want a,b: a later-arriving tie (c) must never displace an earlier equal record",
			got[0].Get("tag").S, got[1].Get("tag").S)
	}
}

func TestSort_NestedPathKey(t *testing.T) {
	s := mustSort(t, `| sort url.path asc limit 10`)
	for _, p := range []string{"/z", "/a", "/m"} {
		inner := eval.NewRecord()
		inner.Set("path", eval.Str(p))
		rec := eval.NewRecord()
		rec.Set("url", eval.Object(inner))
		s.Process(rec)
	}
	got := flushAll(s)
	want := []string{"/a", "/m", "/z"}
	for i, w := range want {
		gotPath := got[i].Get("url").Obj.Get("path").S
		if gotPath != w {
			t.Fatalf("got[%d] path = %q, want %q", i, gotPath, w)
		}
	}
}

// --- Integration through the full Pipeline (Flush + downstream stages) ---

func TestPipeline_SortThenFieldsProjectsFlushedRecords(t *testing.T) {
	q, err := query.ParseQuery(`| sort n desc limit 2 | fields n`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	s := NewSort(q.Stages[0].(*query.SortStage))
	fs, err := NewFields(q.Stages[1].(*query.FieldsStage))
	if err != nil {
		t.Fatalf("NewFields error: %v", err)
	}
	p := New(s, fs)

	for _, n := range []int64{1, 5, 3} {
		rec := eval.NewRecord()
		rec.Set("n", eval.Int(n))
		rec.Set("extra", eval.Str("dropped by fields"))
		_, keep, _ := p.Process(rec)
		if keep {
			t.Fatal("sort must never emit per-record through Process")
		}
	}

	var out []*eval.Record
	p.Flush(func(rec *eval.Record) { out = append(out, rec) })

	if len(out) != 2 {
		t.Fatalf("got %d flushed records, want 2 (limit)", len(out))
	}
	if out[0].Get("n").I != 5 || out[1].Get("n").I != 3 {
		t.Fatalf("flushed order wrong: n=%d, n=%d, want 5 then 3", out[0].Get("n").I, out[1].Get("n").I)
	}
	// fields must have run on the flushed records — "extra" projected away.
	if out[0].Len() != 1 || out[0].Get("extra").Kind != eval.KindMissing {
		t.Fatalf("out[0] = %+v, want only 'n' — fields stage must apply to flushed sort output too", out[0])
	}
}
