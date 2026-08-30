package pipeline

import (
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// NewParallelStats builds a stats stage sharded across workers goroutines
// (`-j N`), each running its own independent, lock-free *Stats instance
// over a disjoint slice of the group-key space — the explicit, honest
// scope this parallelizes: the default pipeline stays single-threaded
// end to end (record decoding/filtering never runs concurrently), only
// stats' own per-group aggregation math (FNV hashing for count_distinct,
// big.Int arithmetic, Welford updates, Algorithm L reservoir sampling)
// gets distributed once a group's key is known.
//
// Sharding is keyed on the record's OWN group key (via Stats.GroupKeyFor,
// hashed to pick a shard), not round-robin — so every record for a given
// group always lands on the same shard's Stats instance, and different
// shards end up holding entirely disjoint sets of groups. That is what
// keeps the merge at Flush trivial: collect each shard's already-sorted
// rows and merge them into one global order (§15), never needing to
// combine two shards' PARTIAL aggregate state for the SAME group — no
// general "merge two Reservoirs/CountDistinct sets/Welford accumulators"
// algorithm exists anywhere in this codebase, deliberately, since the
// sharding scheme never requires one.
//
// Determinism (§15) survives sharding: the input is still read and
// dispatched by one goroutine, in original file order; each shard's
// buffered channel preserves FIFO order for exactly the records routed
// to it, so a given group's own Stats instance sees its records in the
// SAME relative order sequential (-j 1) processing would have — Welford
// and Algorithm L are both streaming, order-sensitive algorithms, and
// this is what keeps their results byte-identical to -j 1's, group by
// group. The one honest exception: the cardinality guard (§8.3) is
// PER-SHARD here, not global — if the total group count is large enough
// to overflow --max-groups, -j N>1 can emit up to N separate (other)
// rows instead of sequential mode's single one. Documented in README,
// not silently different.
//
// A query with no grouping dimension at all (no "by", no "every") has
// exactly one possible group — nothing to shard by — so this falls back
// to plain NewStatsWithLimits (workers=1 in effect) rather than paying
// channel/goroutine overhead for zero parallelism benefit, and rather
// than risking N-1 idle shards each pre-seeding their own spurious
// duplicate empty-group row (see NewStatsWithLimits' own EC-38 handling)
// for a group that only ever really exists once.
func NewParallelStats(ss *query.StatsStage, loc *time.Location, maxGroups, maxSample int, seed int64, workers int) (stage Stage, err error) {
	if workers < 2 || singleGlobalGroup(ss) {
		return NewStatsWithLimits(ss, loc, maxGroups, maxSample, seed)
	}

	shards := make([]*Stats, workers)
	for i := range shards {
		s, err := NewStatsWithLimits(ss, loc, maxGroups, maxSample, seed)
		if err != nil {
			return nil, err
		}
		shards[i] = s
	}

	ps := &ParallelStats{shards: shards, chans: make([]chan *eval.Record, workers)}
	ps.wg.Add(workers)
	for i := range shards {
		// Buffered so a fast dispatcher doesn't stall waiting on a
		// momentarily-busy shard — bounded, not unbounded, so a
		// pathologically skewed shard distribution still applies real
		// backpressure onto the (single-threaded) reader instead of
		// growing memory without limit.
		ps.chans[i] = make(chan *eval.Record, 256)
		go func(i int) {
			defer ps.wg.Done()
			for rec := range ps.chans[i] {
				shards[i].Process(rec)
			}
		}(i)
	}
	return ps, nil
}

// singleGlobalGroup mirrors NewStatsWithLimits' own EC-38 pre-seed
// condition exactly — the one case where sharding by group key would
// route every record to the same shard anyway.
func singleGlobalGroup(ss *query.StatsStage) bool {
	return len(ss.By) == 0 && ss.Every == ""
}

// ParallelStats is the real (workers >= 2, multi-group) case NewParallelStats
// builds. It implements pipeline.Stage and Flusher exactly like *Stats
// does, so the rest of the pipeline (and cmd/logq's buildPipeline) never
// needs to know parallelism is involved at all.
type ParallelStats struct {
	shards []*Stats
	chans  []chan *eval.Record
	wg     sync.WaitGroup
}

// Process routes rec to the shard its group key hashes to. It never
// blocks the caller beyond an ordinary (possibly backpressured) channel
// send, and — matching Stats.Process's own contract — never emits.
func (ps *ParallelStats) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	idx := shardIndex(ps.shards[0].GroupKeyFor(rec), len(ps.shards))
	ps.chans[idx] <- rec
	return nil, false, false
}

// Flush closes every shard's input channel, waits for all workers to
// finish draining (no more Process calls arrive after this point — the
// pipeline contract already guarantees Flush is end-of-stream), then
// merges every shard's own already-sorted rows into one final §15 order:
// non-(other) rows byte-wise ascending by key, any (other) rows last.
// Since shards hold disjoint group-key sets by construction, this merge
// never needs to resolve a tie between two shards' rows for the SAME
// group — the sort is purely for correctly interleaving genuinely
// different shards' groups, not for combining anything.
func (ps *ParallelStats) Flush(emit func(*eval.Record)) {
	for _, ch := range ps.chans {
		close(ch)
	}
	ps.wg.Wait()

	var all []statsRow
	for _, s := range ps.shards {
		all = append(all, s.sortedRows()...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].other != all[j].other {
			return !all[i].other // any (other) row sorts after every non-(other) row
		}
		return all[i].key < all[j].key
	})
	for _, row := range all {
		emit(row.rec)
	}
}

// shardIndex maps key to a worker index via a plain (unsalted) FNV-1a
// hash mod n — this is pure load-balancing, not a security boundary the
// way count_distinct's hash-flooding defense is (internal/agg/distinct.go):
// the input here is the query's OWN group keys, not attacker-controlled
// log field values chosen to defeat this specific hash, so stdlib
// hash/fnv is the right, unadorned tool, with no salting rationale that
// would apply here at all.
func shardIndex(key string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}
