package pipeline

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// passThrough is a trivial test-only Stage that keeps every record and
// never signals done — used to isolate chaining behavior from any real
// stage's own logic.
type passThrough struct{ calls int }

func (p *passThrough) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	p.calls++
	return rec, true, false
}

// dropAll drops every record it sees.
type dropAll struct{ calls int }

func (d *dropAll) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	d.calls++
	return nil, false, false
}

func TestPipeline_EmptyChainPassesThroughUnchanged(t *testing.T) {
	p := New()
	rec := eval.NewRecord()
	rec.Set("a", eval.Int(1))

	out, keep, done := p.Process(rec)
	if !keep || done {
		t.Fatalf("keep=%v done=%v, want keep=true done=false", keep, done)
	}
	if out != rec {
		t.Fatal("empty pipeline must return the same record unchanged")
	}
}

func TestPipeline_SingleStageChains(t *testing.T) {
	pt := &passThrough{}
	p := New(pt)
	_, keep, _ := p.Process(eval.NewRecord())
	if !keep || pt.calls != 1 {
		t.Fatalf("keep=%v calls=%d", keep, pt.calls)
	}
}

func TestPipeline_DropStopsChainEarly(t *testing.T) {
	drop := &dropAll{}
	pt := &passThrough{}
	// drop comes first — pt must never be called for a dropped record.
	p := New(drop, pt)

	_, keep, _ := p.Process(eval.NewRecord())
	if keep {
		t.Fatal("keep should be false — the record was dropped")
	}
	if pt.calls != 0 {
		t.Fatalf("pt.calls = %d, want 0 — later stages must not see a dropped record", pt.calls)
	}
}

func TestPipeline_DoneStillLetsLaterStagesProcessTheSameRecord(t *testing.T) {
	// The core semantic this test locks in: done cuts off FUTURE records,
	// it does not retroactively discard the record that triggered it.
	limit := NewLimit(1)
	pt := &passThrough{}
	p := New(limit, pt)

	out, keep, done := p.Process(eval.NewRecord())
	if !keep {
		t.Fatal("the record that triggers done must still be kept")
	}
	if !done {
		t.Fatal("done must be true once limit's count is reached")
	}
	if pt.calls != 1 {
		t.Fatalf("pt.calls = %d, want 1 — later stages must still process the record that triggered done", pt.calls)
	}
	if out == nil {
		t.Fatal("out must not be nil for a kept record")
	}
}

func TestPipeline_DoneFromAnEarlierStagePropagatesEvenIfLaterStageWouldKeep(t *testing.T) {
	limit := NewLimit(1)
	pt := &passThrough{}
	p := New(pt, limit) // passThrough first this time, limit second

	_, keep1, done1 := p.Process(eval.NewRecord())
	if !keep1 || !done1 {
		t.Fatalf("first record: keep=%v done=%v, want true,true", keep1, done1)
	}
	_, keep2, done2 := p.Process(eval.NewRecord())
	if keep2 {
		t.Fatal("second record should be dropped — limit's count is exhausted")
	}
	if !done2 {
		t.Fatal("done must still be true on the second call")
	}
}

func TestPipeline_MultipleRealStagesChainCorrectly(t *testing.T) {
	rec := eval.NewRecord()
	rec.Set("a", eval.Int(1))
	rec.Set("b", eval.Int(2))
	rec.Set("c", eval.Int(3))

	// Build a real Fields stage via query parsing, matching how the CLI
	// will actually construct one (commit 27) — not a hand-built AST.
	q, err := query.ParseQuery(`| fields a, c`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	fs, err := NewFields(q.Stages[0].(*query.FieldsStage))
	if err != nil {
		t.Fatalf("NewFields error: %v", err)
	}

	p := New(fs, NewLimit(1))
	out, keep, done := p.Process(rec)
	if !keep || !done {
		t.Fatalf("keep=%v done=%v", keep, done)
	}
	if out.Len() != 2 || out.Get("a").I != 1 || out.Get("c").I != 3 {
		t.Fatalf("out = %+v, want fields a and c only", out)
	}
	if out.Get("b").Kind != eval.KindMissing {
		t.Fatal("field b must have been projected away")
	}
}
