// Package pipeline chains post-filter query stages (§7: "records -> Filter
// -> Stage1 -> ... -> Renderer" — the Filter itself lives in
// eval.CompiledFilter, already wired in cmd/logq; this package is
// everything after it). Kept as its own package specifically so
// cmd/logq/main.go can stay "flag parsing, wiring, exit-code mapping
// ONLY," per the project's own architecture — stage orchestration logic
// doesn't belong bolted onto main().
package pipeline

import "github.com/pooja-conqueror/LOGQ/internal/eval"

// Stage is one executable pipeline stage: given one input record, it
// returns the (possibly transformed) output record and whether to keep
// it. done signals the whole pipeline is finished — no more input needs
// to be read at all (e.g. limit's count reached) — a cheap way for the
// caller to stop decoding the rest of a multi-GB source once nothing more
// could pass.
//
// done and keep are independent: a stage can signal "this is the last
// record I'll ever accept" (done=true) while still keeping THIS record
// (keep=true) — done only cuts off FUTURE records, never retroactively
// discards the one that triggered it.
type Stage interface {
	Process(rec *eval.Record) (out *eval.Record, keep bool, done bool)
}

// Pipeline chains zero or more Stages left to right.
type Pipeline struct {
	stages []Stage
}

// New builds a Pipeline from an ordered list of stages.
func New(stages ...Stage) *Pipeline {
	return &Pipeline{stages: stages}
}

// Process runs rec through every stage in order. keep is false if any
// stage dropped the record — in which case later stages never see it at
// all. done is true once any stage has signaled the pipeline is finished;
// processing of THIS record still continues through the remaining stages
// (a later stage might still transform or drop it), but the caller should
// stop feeding the pipeline any further records once done is true.
func (p *Pipeline) Process(rec *eval.Record) (out *eval.Record, keep bool, done bool) {
	cur := rec
	for _, stage := range p.stages {
		stageOut, stageKeep, stageDone := stage.Process(cur)
		if !stageKeep {
			return nil, false, stageDone
		}
		cur = stageOut
		if stageDone {
			done = true
		}
	}
	return cur, true, done
}
