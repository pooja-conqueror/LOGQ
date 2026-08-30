package pipeline

import (
	"fmt"
	"sort"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/agg"
	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// DefaultMaxGroups is the cardinality guard's default cap (§8.3) — wired
// to a --max-groups flag in a later commit; NewStats uses this until then.
const DefaultMaxGroups = 10000

// windowColumn is the fixed output column name for a windowed stats
// query's bucket label (§8.1). Not user-controllable — there's no
// grammar for renaming it, unlike "by"/stat-function columns, which take
// their names from the query text itself.
const windowColumn = "window"

// otherLabel is the display text used in every "by" column, and the
// window column, of the single collapsed cardinality-overflow row
// (§8.3). count_distinct's own placeholder inside that row is the
// distinct, spec-mandated "∅" (emptySetMarker) — deliberately NOT reused
// here, since "(other)" describes the row's IDENTITY (which original
// groups it stands in for) while "∅" describes that ONE aggregate's
// result (an abandoned, not merely absent, computation).
const otherLabel = "(other)"

// noTsLabel is the window column's text for the synthetic row holding
// records whose timestamp never resolved (§8.1's "(no-ts)" row).
const noTsLabel = "(no-ts)"

const emptySetMarker = "∅"
const noneMarker = "(none)"

// Stats is the terminal aggregation stage (§7/§8, at-most-one, enforced
// at parse time by S-5 — never checked here). It never streams: every
// input record only updates internal group state; Flush is the only
// place output rows are ever produced, since the true group set — and
// therefore the true sorted output order — can't be known until the
// input is exhausted.
type Stats struct {
	fns   []query.StatFn
	by    []*query.PathRef
	every time.Duration // 0 means no "every" clause
	loc   *time.Location

	maxGroups int
	maxSample int
	seed      int64

	groups     map[string]*statGroup
	other      *statGroup
	overflowed int64
}

// NewStats builds a Stats stage from the parsed StatsStage AST, using the
// package's default cardinality cap and agg's own default percentile
// reservoir cap and seed. loc is the run's resolved --tz location —
// required unconditionally for API simplicity even though it's only
// actually consulted when ss.Every != "" (§8.1's civil-day bucket
// alignment).
func NewStats(ss *query.StatsStage, loc *time.Location) (*Stats, error) {
	return NewStatsWithLimits(ss, loc, DefaultMaxGroups, 0, agg.DefaultReservoirSeed)
}

// NewStatsWithLimits is NewStats with explicit overrides — maxSample <= 0
// means "use agg's own default" — exported so a --max-groups/--max-sample/
// --seed flag can override the defaults without duplicating this
// constructor's logic.
func NewStatsWithLimits(ss *query.StatsStage, loc *time.Location, maxGroups, maxSample int, seed int64) (*Stats, error) {
	var every time.Duration
	if ss.Every != "" {
		d, err := time.ParseDuration(ss.Every)
		if err != nil {
			// Unreachable in practice: the parser (commit 28, S-1) already
			// validated this exact text at compile time. Guarded anyway
			// rather than trusting a cross-package invariant silently.
			return nil, fmt.Errorf("internal error: invalid every duration %q survived parsing: %w", ss.Every, err)
		}
		every = d
	}

	s := &Stats{
		fns:       ss.Fns,
		by:        ss.By,
		every:     every,
		loc:       loc,
		maxGroups: maxGroups,
		maxSample: maxSample,
		seed:      seed,
		groups:    make(map[string]*statGroup),
	}

	// EC-38: a query with no grouping dimension at all (no "by", no
	// "every") must still emit exactly one row — every() cells starved,
	// count 0 — even over zero matching input records, matching ordinary
	// SQL aggregate-without-GROUP-BY semantics. Pre-seeding the single
	// global group here, unconditionally, means Flush needs no special
	// case at all: it just emits whatever's in s.groups, and this one is
	// already there even if Process is never called.
	if len(s.by) == 0 && s.every == 0 {
		s.groups[agg.GroupKey(nil)] = newStatGroup(nil, s.fns, false, s.maxSample, s.seed)
	}

	return s, nil
}

// groupKey computes the group-key string rec resolves to under this
// Stats config (window bucket, if any, then the "by" tuple), plus the
// pieces buildRecord/Process each separately need — factored out so
// GroupKeyFor (exported, for a parallel dispatcher) and Process share
// exactly one implementation of this logic.
func (s *Stats) groupKey(rec *eval.Record) (key string, byValues []eval.Value, hasBucket bool, bucketStart time.Time) {
	byValues = make([]eval.Value, len(s.by))
	for i, p := range s.by {
		byValues[i] = rec.Resolve(p)
	}

	keyValues := make([]eval.Value, 0, len(byValues)+1)
	if s.every > 0 {
		if rec.HasTime {
			bucketStart = agg.WindowBucket(rec.Time, s.every, s.loc)
			hasBucket = true
			keyValues = append(keyValues, eval.Timestamp(bucketStart))
		} else {
			// eval.Missing's group-key sentinel (\x02) sorts before every
			// real value's CellString (RFC3339 timestamps included, which
			// always start with a printable digit) — this single choice is
			// what makes "(no-ts) sorts first" (§15) fall out of an
			// ordinary byte-wise key sort, with no separate special case.
			keyValues = append(keyValues, eval.Missing)
		}
	}
	keyValues = append(keyValues, byValues...)
	return agg.GroupKey(keyValues), byValues, hasBucket, bucketStart
}

// GroupKeyFor computes the group key rec would resolve to under this
// Stats config, without touching any aggregator state — exposed so a
// parallel dispatcher (ParallelStats) can pick a deterministic shard for
// rec before routing it to a worker, using the exact same key logic
// Process itself uses internally, so records for the same group always
// land on the same shard regardless of which shard instance is asked.
func (s *Stats) GroupKeyFor(rec *eval.Record) string {
	key, _, _, _ := s.groupKey(rec)
	return key
}

// Process updates the group (or the (other)/(no-ts) partition) rec's key
// resolves to. It never emits — see the type doc comment.
func (s *Stats) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	key, byValues, hasBucket, bucketStart := s.groupKey(rec)

	g, exists := s.groups[key]
	if !exists {
		if int64(len(s.groups)) >= int64(s.maxGroups) {
			s.overflowed++
			if s.other == nil {
				s.other = newStatGroup(nil, s.fns, true, s.maxSample, s.seed)
			}
			g = s.other
		} else {
			g = newStatGroup(byValues, s.fns, false, s.maxSample, s.seed)
			g.hasBucket = hasBucket
			g.bucketStart = bucketStart
			s.groups[key] = g
		}
	}
	for _, a := range g.aggs {
		a.AddRecord(rec)
	}
	return nil, false, false
}

// statsRow pairs a group's sort key with its rendered output row — used
// both by Stats' own Flush and by ParallelStats, which needs each
// shard's rows re-sorted together as one global sequence rather than
// emitted shard-by-shard. other is true only for the single collapsed
// cardinality-overflow row, which never participates in the byte-wise
// key sort (§15: always last, unconditionally).
type statsRow struct {
	key   string
	other bool
	rec   *eval.Record
}

// snapshotRows computes s's current §15-ordered row sequence WITHOUT
// touching any state: real groups sorted byte-wise ascending by group
// key (which, thanks to eval.Timestamp's RFC3339 CellString encoding
// and eval.Missing's sentinel byte both already being part of that key
// when windowing is active, also yields "(no-ts) first, then buckets
// chronologically" for free — no separate sort pass needed), then the
// collapsed (other) row last, if the cardinality guard ever triggered.
// Safe to call repeatedly and get a consistent, growing view each time —
// every aggregator's own Result() method is a pure read (§8.4's
// algorithms table), so re-rendering the same group twice is never
// observably different from rendering it once.
func (s *Stats) snapshotRows() []statsRow {
	keys := make([]string, 0, len(s.groups))
	for k := range s.groups {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Go's string < is already byte-wise — matches §15 directly

	rows := make([]statsRow, 0, len(keys)+1)
	for _, k := range keys {
		rows = append(rows, statsRow{key: k, rec: s.buildRecord(s.groups[k])})
	}
	if s.other != nil {
		rows = append(rows, statsRow{other: true, rec: s.buildRecord(s.other)})
	}
	return rows
}

// sortedRows is snapshotRows plus Flush's own one-shot contract: s is
// left empty afterward, so a second call returns nothing.
func (s *Stats) sortedRows() []statsRow {
	rows := s.snapshotRows()
	s.groups = nil
	s.other = nil
	return rows
}

// Flush emits one output record per tracked group, in sortedRows' order,
// then clears all state — the batch-mode, end-of-stream contract.
func (s *Stats) Flush(emit func(*eval.Record)) {
	for _, row := range s.sortedRows() {
		emit(row.rec)
	}
}

// Snapshot returns the current rows in §15 order WITHOUT clearing any
// state — for watch mode's repeated SNAPSHOT re-emission every poll
// interval (§14), where the same accumulating Stats keeps being asked
// to render its current, still-growing view again and again, unlike
// Flush's one-shot "this is the final answer" contract.
func (s *Stats) Snapshot() []*eval.Record {
	rows := s.snapshotRows()
	out := make([]*eval.Record, len(rows))
	for i, row := range rows {
		out[i] = row.rec
	}
	return out
}

// OverflowedGroups reports how many records were routed into (other) for
// having introduced a group key beyond the cardinality cap (§8.3:
// "Summary reports groups_overflowed: K") — a later commit's stderr
// summary line is the actual consumer; Stats only needs to count it.
func (s *Stats) OverflowedGroups() int64 { return s.overflowed }

// buildRecord renders one group's output row: the window column (if
// windowing is active), then the "by" columns in query order, then each
// stat function's result column, also in query order.
func (s *Stats) buildRecord(g *statGroup) *eval.Record {
	out := eval.NewRecord()
	if s.every > 0 {
		out.Set(windowColumn, eval.Str(s.windowLabel(g)))
	}
	for i, p := range s.by {
		if g.isOther {
			out.Set(pathKey(p), eval.Str(otherLabel))
			continue
		}
		// Matches Fields.Process's own convention: a MISSING value is
		// never stored in a Record at all, only ever omitted — §11.5's
		// jsonl contract ("MISSING -> omitted key") depends on producers
		// upholding this, not on the renderer special-casing a Missing
		// value it finds already sitting in a real key. Every record in
		// this group necessarily resolved this exact path to MISSING (by
		// construction of the group key), so this is never a
		// per-record inconsistency within the group.
		if g.byValues[i].Kind == eval.KindMissing {
			continue
		}
		out.Set(pathKey(p), g.byValues[i])
	}
	for i, fn := range s.fns {
		out.Set(statColumnName(fn), g.aggs[i].Result())
	}
	return out
}

// statColumnName derives a stat function's output column name: the bare
// function keyword ("count"), or that keyword plus an underscore-joined
// flattening of its target path ("sum_duration_ms", "count_distinct_url").
// Deliberately NOT the fuller "sum(duration_ms)" call-syntax form (S-6's
// own duplicate-detection text, in query.statFnKey): a stats output
// column must be usable as an ordinary `sort <col>` target (§8.5), and
// Path grammar has no syntax for parentheses at all — this has to be a
// plain identifier. S-6 already guarantees no two StatFns in one query
// share (kind, path), so this flattening can't collide either, with no
// second uniqueness check needed here.
func statColumnName(fn query.StatFn) string {
	name := fn.Kind.String()
	if fn.Path == nil {
		return name
	}
	for _, seg := range fn.Path.Segs {
		name += "_"
		if seg.IsIndex {
			name += fmt.Sprintf("%d", seg.Index)
		} else {
			name += seg.Ident
		}
	}
	return name
}

func (s *Stats) windowLabel(g *statGroup) string {
	switch {
	case g.isOther:
		return otherLabel
	case g.hasBucket:
		return g.bucketStart.In(s.loc).Format(time.RFC3339)
	default:
		return noTsLabel
	}
}

// statGroup holds one group's per-StatFn running aggregate state.
// byValues is the resolved "by" tuple that created this group — captured
// once, since every record sharing this group's key resolved the exact
// same tuple by construction. isOther groups never populate byValues at
// all; buildRecord renders otherLabel for every "by" column instead.
type statGroup struct {
	byValues    []eval.Value
	isOther     bool
	hasBucket   bool
	bucketStart time.Time
	aggs        []statAggregator
}

func newStatGroup(byValues []eval.Value, fns []query.StatFn, isOther bool, maxSample int, seed int64) *statGroup {
	aggs := make([]statAggregator, len(fns))
	for i, fn := range fns {
		aggs[i] = newStatAggregator(fn, isOther, maxSample, seed)
	}
	return &statGroup{byValues: byValues, isOther: isOther, aggs: aggs}
}

// statAggregator normalizes every internal/agg aggregator type — whose
// Add signatures differ (Count takes nothing; the rest take a resolved
// eval.Value) — behind one interface keyed on the raw record, and folds
// each type's own "starved" placeholder rendering (§8.4's "Output when
// starved" column) into a single Value-producing Result.
type statAggregator interface {
	AddRecord(rec *eval.Record)
	Result() eval.Value
}

func newStatAggregator(fn query.StatFn, isOther bool, maxSample int, seed int64) statAggregator {
	// §8.3: "(other) group holding counts only ... count_distinct inside
	// (other) reports ∅" — merging count_distinct sets from many
	// different original groups would misrepresent each one's actual
	// per-group cardinality, so (other) never tracks it at all; every
	// other function still aggregates over (other)'s merged numerics
	// normally.
	if isOther && fn.Kind == query.StatCountDistinct {
		return countDistinctOtherAgg{}
	}
	switch fn.Kind {
	case query.StatCount:
		return &countStatAgg{}
	case query.StatCountDistinct:
		return &countDistinctStatAgg{path: fn.Path, cd: agg.NewCountDistinct()}
	case query.StatSum:
		return &sumStatAgg{path: fn.Path}
	case query.StatAvg:
		return &avgStatAgg{path: fn.Path}
	case query.StatMin:
		return &minMaxStatAgg{path: fn.Path, mm: agg.NewMin()}
	case query.StatMax:
		return &minMaxStatAgg{path: fn.Path, mm: agg.NewMax()}
	case query.StatP50:
		return newPercentileStatAgg(fn.Path, 0.5, maxSample, seed)
	case query.StatP95:
		return newPercentileStatAgg(fn.Path, 0.95, maxSample, seed)
	case query.StatP99:
		return newPercentileStatAgg(fn.Path, 0.99, maxSample, seed)
	default:
		panic(fmt.Sprintf("internal error: unhandled StatFnKind %v survived parsing", fn.Kind))
	}
}

// countStatAgg: §8.4 starved output is 0, which agg.Count.Result already
// returns for an untouched value — no special-casing needed.
type countStatAgg struct{ c agg.Count }

func (a *countStatAgg) AddRecord(*eval.Record) { a.c.Add() }
func (a *countStatAgg) Result() eval.Value     { return eval.Int(a.c.Result()) }

// sumStatAgg: §8.4 starved output is 0 (unlike min/max/avg/percentiles,
// which use "(none)" — sum has a real, meaningful zero identity).
type sumStatAgg struct {
	path *query.PathRef
	s    agg.Sum
}

func (a *sumStatAgg) AddRecord(rec *eval.Record) { a.s.Add(rec.Resolve(a.path)) }
func (a *sumStatAgg) Result() eval.Value {
	v, any := a.s.Result()
	if !any {
		return eval.Int(0)
	}
	return v
}

type avgStatAgg struct {
	path *query.PathRef
	a    agg.Avg
}

func (a *avgStatAgg) AddRecord(rec *eval.Record) { a.a.Add(rec.Resolve(a.path)) }
func (a *avgStatAgg) Result() eval.Value {
	mean, any := a.a.Result()
	if !any {
		return eval.Str(noneMarker)
	}
	return eval.Float(mean)
}

type minMaxStatAgg struct {
	path *query.PathRef
	mm   *agg.MinMax
}

func (a *minMaxStatAgg) AddRecord(rec *eval.Record) { a.mm.Add(rec.Resolve(a.path)) }
func (a *minMaxStatAgg) Result() eval.Value {
	v, ok := a.mm.Result()
	if !ok {
		return eval.Str(noneMarker)
	}
	return v
}

// countDistinctStatAgg: §8.4 starved output is 0, which agg.CountDistinct
// already returns for an untouched value. The only case needing explicit
// handling is the cap itself (§8.4: "on cap: freeze, report >=65536").
type countDistinctStatAgg struct {
	path *query.PathRef
	cd   *agg.CountDistinct
}

func (a *countDistinctStatAgg) AddRecord(rec *eval.Record) { a.cd.Add(rec.Resolve(a.path)) }
func (a *countDistinctStatAgg) Result() eval.Value {
	count, approx := a.cd.Result()
	if approx {
		return eval.Str(fmt.Sprintf(">=%d", count))
	}
	return eval.Int(count)
}

// countDistinctOtherAgg is the stub count_distinct's aggregator slot in
// the (other) row uses — it never tracks anything, always reporting the
// spec-mandated "∅" (§8.3), never a real (and misleadingly merged) count.
type countDistinctOtherAgg struct{}

func (countDistinctOtherAgg) AddRecord(*eval.Record) {}
func (countDistinctOtherAgg) Result() eval.Value     { return eval.Str(emptySetMarker) }

// percentileStatAgg: §8.4 starved output is "(none)"; once approximate
// (the reservoir cap was exceeded), the result is rendered as text with a
// trailing "*" (§8.4: "column header gains *") rather than kept as a
// numeric Value — approximateness is a genuinely PER-GROUP property here
// (each group has its own independently-capped reservoir), so marking it
// inline on the value itself is more honest than a blanket column-wide
// flag would be, while still satisfying the spec's "starred" requirement
// literally.
type percentileStatAgg struct {
	path *query.PathRef
	p    *agg.Percentile
}

func newPercentileStatAgg(path *query.PathRef, q float64, maxSample int, seed int64) *percentileStatAgg {
	if maxSample <= 0 {
		maxSample = agg.DefaultMaxSample
	}
	return &percentileStatAgg{path: path, p: agg.NewPercentileWithSeed(q, maxSample, seed)}
}

func (a *percentileStatAgg) AddRecord(rec *eval.Record) { a.p.Add(rec.Resolve(a.path)) }
func (a *percentileStatAgg) Result() eval.Value {
	v, approx, ok := a.p.Result()
	if !ok {
		return eval.Str(noneMarker)
	}
	if approx {
		return eval.Str(eval.NumberString(v) + "*")
	}
	return v
}
