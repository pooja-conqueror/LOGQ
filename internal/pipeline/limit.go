package pipeline

import "github.com/pooja-conqueror/LOGQ/internal/eval"

// Limit passes through only the first N records that reach it, then keeps
// signaling done so the caller stops feeding it — and, upstream, can stop
// reading/decoding the rest of a multi-GB source entirely once nothing
// more could pass.
type Limit struct {
	N     int64
	count int64
}

// NewLimit builds a Limit stage. n must be >= 1 — already enforced by the
// parser's POSINT grammar for LimitStage (commit 23), so this constructor
// trusts that invariant rather than re-validating it.
func NewLimit(n int64) *Limit {
	return &Limit{N: n}
}

func (l *Limit) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	if l.count >= l.N {
		return nil, false, true // already at the limit; this and all further records are dropped
	}
	l.count++
	return rec, true, l.count >= l.N // keep this one; done exactly when it was the last one accepted
}
