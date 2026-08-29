package pipeline

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func TestLimit_KeepsExactlyN(t *testing.T) {
	l := NewLimit(3)
	kept := 0
	var lastDone bool
	for range 5 {
		_, keep, done := l.Process(eval.NewRecord())
		if keep {
			kept++
		}
		lastDone = done
	}
	if kept != 3 {
		t.Fatalf("kept = %d, want 3", kept)
	}
	if !lastDone {
		t.Fatal("done must be true once the limit is exhausted")
	}
}

func TestLimit_DoneSignaledOnTheNthRecordItself(t *testing.T) {
	l := NewLimit(2)
	_, keep1, done1 := l.Process(eval.NewRecord())
	if !keep1 || done1 {
		t.Fatalf("record 1: keep=%v done=%v, want true,false", keep1, done1)
	}
	_, keep2, done2 := l.Process(eval.NewRecord())
	if !keep2 || !done2 {
		t.Fatalf("record 2 (the Nth): keep=%v done=%v, want true,true", keep2, done2)
	}
}

func TestLimit_One(t *testing.T) {
	l := NewLimit(1)
	_, keep, done := l.Process(eval.NewRecord())
	if !keep || !done {
		t.Fatalf("keep=%v done=%v, want true,true (limit 1 is done on the first record)", keep, done)
	}
}

func TestLimit_RecordsAfterLimitAreDropped(t *testing.T) {
	l := NewLimit(1)
	l.Process(eval.NewRecord()) // consumes the one allowed slot

	out, keep, done := l.Process(eval.NewRecord())
	if keep {
		t.Fatal("a record past the limit must be dropped")
	}
	if !done {
		t.Fatal("done must remain true for records past the limit")
	}
	if out != nil {
		t.Fatal("a dropped record's output must be nil")
	}
}

func TestLimit_IdentityPreserved(t *testing.T) {
	rec := eval.NewRecord()
	rec.Set("x", eval.Int(42))
	l := NewLimit(5)
	out, keep, _ := l.Process(rec)
	if !keep {
		t.Fatal("expected keep=true")
	}
	if out != rec {
		t.Fatal("Limit must pass the record through unchanged, same pointer, not a copy")
	}
}
