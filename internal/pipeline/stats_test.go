package pipeline

import (
	"fmt"
	"testing"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// mustStatsStage parses src and returns its (necessarily terminal, or
// followed only by sort/limit) StatsStage — the first *query.StatsStage
// found in q.Stages.
func mustStatsStage(t *testing.T, src string) *query.StatsStage {
	t.Helper()
	q, err := query.ParseQuery(src)
	if err != nil {
		t.Fatalf("ParseQuery(%q) error = %v", src, err)
	}
	for _, st := range q.Stages {
		if ss, ok := st.(*query.StatsStage); ok {
			return ss
		}
	}
	t.Fatalf("ParseQuery(%q) produced no StatsStage", src)
	return nil
}

func recWithFields(fields map[string]eval.Value) *eval.Record {
	r := eval.NewRecord()
	for k, v := range fields {
		r.Set(k, v)
	}
	return r
}

func flushStats(t *testing.T, s *Stats) []*eval.Record {
	t.Helper()
	var out []*eval.Record
	s.Flush(func(rec *eval.Record) { out = append(out, rec) })
	return out
}

func TestStats_SimpleCountNoGrouping(t *testing.T) {
	ss := mustStatsStage(t, `| stats count()`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	for range 3 {
		s.Process(recWithFields(nil))
	}
	rows := flushStats(t, s)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1 (no grouping dimension)", len(rows))
	}
	if got := rows[0].Get("count"); got.I != 3 {
		t.Fatalf("count = %+v, want Int(3)", got)
	}
}

func TestStats_EmptyInputStillEmitsOneRow(t *testing.T) {
	// EC-38: zero matching records, no "by"/"every" — one row, starved
	// cells, not zero rows.
	ss := mustStatsStage(t, `| stats count(), avg(x)`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	rows := flushStats(t, s)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if got := rows[0].Get("count"); got.I != 0 {
		t.Fatalf("count = %+v, want Int(0)", got)
	}
	if got := rows[0].Get("avg_x"); got.Kind != eval.KindString || got.S != noneMarker {
		t.Fatalf("avg_x = %+v, want the %q marker", got, noneMarker)
	}
}

func TestStats_ByClauseWithRecordsProducesGroupsWithNoSpecialCase(t *testing.T) {
	// With a "by" clause and zero matching records, no groups exist at
	// all — nothing to synthesize a group FROM — so zero rows, unlike the
	// no-"by" EC-38 case above.
	ss := mustStatsStage(t, `| stats count() by service`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	rows := flushStats(t, s)
	if len(rows) != 0 {
		t.Fatalf("len(rows) = %d, want 0 (nothing to group with a \"by\" clause and no input)", len(rows))
	}
}

func TestStats_GroupByProducesSortedRows(t *testing.T) {
	ss := mustStatsStage(t, `| stats count() by service`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	for _, svc := range []string{"b", "a", "a", "c", "b"} {
		s.Process(recWithFields(map[string]eval.Value{"service": eval.Str(svc)}))
	}
	rows := flushStats(t, s)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	wantOrder := []string{"a", "b", "c"}
	wantCount := []int64{2, 2, 1}
	for i, want := range wantOrder {
		if got := rows[i].Get("service"); got.S != want {
			t.Fatalf("rows[%d].service = %q, want %q (§15: byte-wise ascending)", i, got.S, want)
		}
		if got := rows[i].Get("count"); got.I != wantCount[i] {
			t.Fatalf("rows[%d].count = %v, want %d", i, got, wantCount[i])
		}
	}
}

func TestStats_ByValuePreservesOriginalKind(t *testing.T) {
	ss := mustStatsStage(t, `| stats count() by code`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	s.Process(recWithFields(map[string]eval.Value{"code": eval.Int(200)}))
	rows := flushStats(t, s)
	if got := rows[0].Get("code"); got.Kind != eval.KindNumber || !got.IsInt || got.I != 200 {
		t.Fatalf("code = %+v, want the original Int(200) unchanged", got)
	}
}

func TestStats_MissingByValueOmittedFromOutputRecord(t *testing.T) {
	// Regression: a group whose "by" path resolved to MISSING for every
	// contributing record must not store a Missing-kind Value under that
	// key at all — only ever omit it — matching Fields.Process's own
	// convention. Storing it would make render.JSONL serialize it as
	// literal "null", conflating MISSING with an actual Null value.
	ss := mustStatsStage(t, `| stats count() by service`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	s.Process(recWithFields(nil)) // "service" never set at all
	rows := flushStats(t, s)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if got := rows[0].Get("service"); got.Kind != eval.KindMissing {
		t.Fatalf("service = %+v, want Get to report Missing (key genuinely absent from the record), not a stored value", got)
	}
	found := false
	for _, k := range rows[0].Keys() {
		if k == "service" {
			found = true
		}
	}
	if found {
		t.Fatal("\"service\" key must not be present in the output record at all when its value is MISSING")
	}
}

func TestStats_MissingNullEmptyAreThreeDistinctGroups(t *testing.T) {
	ss := mustStatsStage(t, `| stats count() by x`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	s.Process(recWithFields(nil))                                      // x missing
	s.Process(recWithFields(map[string]eval.Value{"x": eval.Null}))    // x null
	s.Process(recWithFields(map[string]eval.Value{"x": eval.Str("")})) // x = ""
	rows := flushStats(t, s)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3 distinct groups (missing/null/empty-string)", len(rows))
	}
}

func TestStats_SumStarvedIsZeroNotNoneMarker(t *testing.T) {
	ss := mustStatsStage(t, `| stats sum(x)`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	s.Process(recWithFields(map[string]eval.Value{"x": eval.Str("not numeric")}))
	rows := flushStats(t, s)
	got := rows[0].Get("sum_x")
	if got.Kind != eval.KindNumber || !got.IsInt || got.I != 0 {
		t.Fatalf("sum_x = %+v, want Int(0) (§8.4: sum's starved output is 0, unlike min/max/avg)", got)
	}
}

func TestStats_MinMaxAvgStarvedAreNoneMarker(t *testing.T) {
	ss := mustStatsStage(t, `| stats min(x), max(x), avg(x)`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	s.Process(recWithFields(nil)) // x always missing
	rows := flushStats(t, s)
	for _, col := range []string{"min_x", "max_x", "avg_x"} {
		got := rows[0].Get(col)
		if got.Kind != eval.KindString || got.S != noneMarker {
			t.Fatalf("%s = %+v, want the %q marker", col, got, noneMarker)
		}
	}
}

func TestStats_CountDistinctBasic(t *testing.T) {
	ss := mustStatsStage(t, `| stats count_distinct(user) by service`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	users := map[string][]string{
		"a": {"u1", "u2", "u1"},
		"b": {"u1", "u3"},
	}
	for svc, us := range users {
		for _, u := range us {
			s.Process(recWithFields(map[string]eval.Value{"service": eval.Str(svc), "user": eval.Str(u)}))
		}
	}
	rows := flushStats(t, s)
	want := map[string]int64{"a": 2, "b": 2}
	for _, row := range rows {
		svc := row.Get("service").S
		got := row.Get("count_distinct_user")
		if got.Kind != eval.KindNumber || got.I != want[svc] {
			t.Fatalf("service=%s count_distinct_user = %+v, want Int(%d)", svc, got, want[svc])
		}
	}
}

func TestStats_CountDistinctCapReportsApproxMarker(t *testing.T) {
	ss := mustStatsStage(t, `| stats count_distinct(x)`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	for i := range 65600 {
		s.Process(recWithFields(map[string]eval.Value{"x": eval.Int(int64(i))}))
	}
	rows := flushStats(t, s)
	got := rows[0].Get("count_distinct_x")
	if got.Kind != eval.KindString || got.S != ">=65536" {
		t.Fatalf("count_distinct_x = %+v, want the string \">=65536\"", got)
	}
}

func TestStats_WindowingBucketsAndSortsChronologically(t *testing.T) {
	ss := mustStatsStage(t, `| stats count() every 1h`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	times := []time.Time{
		time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC),
		time.Date(2026, 6, 1, 12, 10, 0, 0, time.UTC),
		time.Date(2026, 6, 1, 14, 45, 0, 0, time.UTC), // same hour as the first
	}
	for _, ts := range times {
		rec := recWithFields(nil)
		rec.Time, rec.HasTime = ts, true
		s.Process(rec)
	}
	rows := flushStats(t, s)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 buckets", len(rows))
	}
	if got := rows[0].Get("window").S; got != "2026-06-01T12:00:00Z" {
		t.Fatalf("rows[0].window = %q, want the earlier bucket first (chronological)", got)
	}
	if got := rows[1].Get("window").S; got != "2026-06-01T14:00:00Z" {
		t.Fatalf("rows[1].window = %q, want the later bucket", got)
	}
	if got := rows[1].Get("count").I; got != 2 {
		t.Fatalf("rows[1].count = %d, want 2 (two records shared the 14:00 bucket)", got)
	}
}

func TestStats_WindowingNoTsSortsFirst(t *testing.T) {
	ss := mustStatsStage(t, `| stats count() every 1h`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	withTs := recWithFields(nil)
	withTs.Time, withTs.HasTime = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), true
	s.Process(withTs)
	s.Process(recWithFields(nil)) // no timestamp at all

	rows := flushStats(t, s)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if got := rows[0].Get("window").S; got != noTsLabel {
		t.Fatalf("rows[0].window = %q, want %q sorted first", got, noTsLabel)
	}
	if got := rows[1].Get("window").S; got == noTsLabel {
		t.Fatal("rows[1] should be the real bucket, not another (no-ts) row")
	}
}

func TestStats_MaxGroupsOverflowsToOther(t *testing.T) {
	ss := mustStatsStage(t, `| stats count() by service`)
	s, err := NewStatsWithLimits(ss, time.UTC, 2, 0)
	if err != nil {
		t.Fatalf("NewStatsWithLimits error = %v", err)
	}
	for _, svc := range []string{"a", "b", "c", "d", "a"} {
		s.Process(recWithFields(map[string]eval.Value{"service": eval.Str(svc)}))
	}
	rows := flushStats(t, s)
	if len(rows) != 3 { // a, b (first two distinct keys), plus one collapsed (other)
		t.Fatalf("len(rows) = %d, want 3 (2 real groups + 1 other)", len(rows))
	}
	last := rows[len(rows)-1]
	if got := last.Get("service"); got.S != otherLabel {
		t.Fatalf("last row's service = %q, want %q sorted last", got.S, otherLabel)
	}
	// c and d overflowed (2 records); a and b's SECOND arrival (the second
	// "a") did not, since a was already a tracked real group before the
	// cap was hit.
	if got := last.Get("count").I; got != 2 {
		t.Fatalf("(other) count = %d, want 2 (c and d's single records each)", got)
	}
	if s.OverflowedGroups() != 2 {
		t.Fatalf("OverflowedGroups() = %d, want 2", s.OverflowedGroups())
	}
}

func TestStats_CountDistinctInOtherReportsEmptySetMarker(t *testing.T) {
	ss := mustStatsStage(t, `| stats count_distinct(user) by service`)
	s, err := NewStatsWithLimits(ss, time.UTC, 1, 0)
	if err != nil {
		t.Fatalf("NewStatsWithLimits error = %v", err)
	}
	s.Process(recWithFields(map[string]eval.Value{"service": eval.Str("a"), "user": eval.Str("u1")}))
	s.Process(recWithFields(map[string]eval.Value{"service": eval.Str("b"), "user": eval.Str("u2")})) // overflows to (other)
	rows := flushStats(t, s)
	last := rows[len(rows)-1]
	if last.Get("service").S != otherLabel {
		t.Fatalf("expected the last row to be (other), got service=%q", last.Get("service").S)
	}
	got := last.Get("count_distinct_user")
	if got.Kind != eval.KindString || got.S != emptySetMarker {
		t.Fatalf("count_distinct_user in (other) = %+v, want the %q marker, never a real merged count", got, emptySetMarker)
	}
}

func TestStats_PercentileApproxGetsStarMarker(t *testing.T) {
	ss := mustStatsStage(t, `| stats p50(x)`)
	s, err := NewStatsWithLimits(ss, time.UTC, DefaultMaxGroups, 5) // tiny reservoir cap
	if err != nil {
		t.Fatalf("NewStatsWithLimits error = %v", err)
	}
	for i := range 100 {
		s.Process(recWithFields(map[string]eval.Value{"x": eval.Int(int64(i))}))
	}
	rows := flushStats(t, s)
	got := rows[0].Get("p50_x")
	if got.Kind != eval.KindString {
		t.Fatalf("p50_x = %+v, want an approximate string cell once the reservoir cap is exceeded", got)
	}
	if len(got.S) == 0 || got.S[len(got.S)-1] != '*' {
		t.Fatalf("p50_x = %q, want a trailing '*' marker", got.S)
	}
}

func TestStats_PercentileExactHasNoMarker(t *testing.T) {
	ss := mustStatsStage(t, `| stats p50(x)`)
	s, err := NewStats(ss, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	for _, n := range []int64{1, 2, 3, 4, 5} {
		s.Process(recWithFields(map[string]eval.Value{"x": eval.Int(n)}))
	}
	rows := flushStats(t, s)
	got := rows[0].Get("p50_x")
	if got.Kind != eval.KindNumber || !got.IsInt || got.I != 3 {
		t.Fatalf("p50_x = %+v, want the exact Int(3), no marker, well under cap", got)
	}
}

func TestStats_SortLimitOverStatsOutputBoundedTopK(t *testing.T) {
	// The proof this commit exists to deliver: §8.5's "sort <aggcol> desc
	// limit K after stats" reuses commit 25's Sort/Limit stages UNCHANGED
	// over the Stats stage's own output rows, and correctly bounds memory
	// to O(K) regardless of how many groups actually existed.
	q, err := query.ParseQuery(`| stats count() by service | sort count desc limit 3`)
	if err != nil {
		t.Fatalf("ParseQuery error = %v", err)
	}
	if len(q.Stages) != 2 {
		t.Fatalf("Stages = %#v, want 2 (stats, sort)", q.Stages)
	}
	statsAST := q.Stages[0].(*query.StatsStage)
	sortAST := q.Stages[1].(*query.SortStage)

	stats, err := NewStats(statsAST, time.UTC)
	if err != nil {
		t.Fatalf("NewStats error = %v", err)
	}
	sortStage := NewSort(sortAST)
	pl := New(stats, sortStage)

	// 50 distinct services, with service N getting N+1 records — the
	// top-3 by count should be svc49 (50), svc48 (49), svc47 (48).
	for n := range 50 {
		svc := fmt.Sprintf("svc%02d", n)
		for range n + 1 {
			pl.Process(recWithFields(map[string]eval.Value{"service": eval.Str(svc)}))
		}
	}

	var out []*eval.Record
	pl.Flush(func(rec *eval.Record) { out = append(out, rec) })

	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want exactly 3 (bounded by limit 3, regardless of 50 groups)", len(out))
	}
	wantCounts := []int64{50, 49, 48}
	for i, want := range wantCounts {
		if got := out[i].Get("count").I; got != want {
			t.Fatalf("out[%d].count = %d, want %d (descending top-3)", i, got, want)
		}
	}
}
