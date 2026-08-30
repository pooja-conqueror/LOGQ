package agg

import (
	"testing"
	"time"

	// Blank-imported here (test-only) so America/New_York's real DST
	// transition rules resolve even on a host with no system tzdata
	// installed — this dev machine included (§ commit 22's own
	// rationale for the same blank import in cmd/logq/main.go). The
	// shipped binary already gets this via main.go; this import never
	// reaches it, since tests aren't compiled into the release artifact.
	_ "time/tzdata"
)

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q) failed: %v", name, err)
	}
	return loc
}

func TestWindowBucket_SubDayGroupsWithinSameBucket(t *testing.T) {
	d := time.Hour
	a := time.Date(2026, 3, 5, 14, 10, 0, 0, time.UTC)
	b := time.Date(2026, 3, 5, 14, 50, 0, 0, time.UTC)
	ba, bb := WindowBucket(a, d, time.UTC), WindowBucket(b, d, time.UTC)
	if !ba.Equal(bb) {
		t.Fatalf("two timestamps in the same clock hour landed in different buckets: %v vs %v", ba, bb)
	}
	want := time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC)
	if !ba.Equal(want) {
		t.Fatalf("bucket start = %v, want %v", ba, want)
	}
}

func TestWindowBucket_SubDayCrossesBoundary(t *testing.T) {
	d := time.Hour
	a := time.Date(2026, 3, 5, 14, 59, 59, 0, time.UTC)
	b := time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC)
	ba, bb := WindowBucket(a, d, time.UTC), WindowBucket(b, d, time.UTC)
	if ba.Equal(bb) {
		t.Fatal("timestamps either side of the hour boundary must land in different buckets")
	}
}

func TestWindowBucket_SubDayIgnoresTimezone(t *testing.T) {
	// §8.1: sub-day buckets anchor to the raw Unix epoch instant — the
	// timezone parameter must have zero effect on the result.
	ts := time.Date(2026, 3, 5, 14, 10, 0, 0, time.UTC)
	kolkata := mustLoadLocation(t, "Asia/Kolkata") // UTC+5:30, a non-whole-hour offset
	inUTC := WindowBucket(ts, time.Hour, time.UTC)
	inKolkata := WindowBucket(ts, time.Hour, kolkata)
	if !inUTC.Equal(inKolkata) {
		t.Fatalf("sub-day bucket depended on timezone: %v (UTC) vs %v (Kolkata)", inUTC, inKolkata)
	}
}

func TestWindowBucket_SubDayHandlesPreEpochTimestamps(t *testing.T) {
	// EC-22: pre-1970 timestamps must bucket correctly (floor, not
	// truncate-toward-zero) rather than silently misplacing negative
	// offsets into the wrong hour.
	d := time.Hour
	// 1969-12-31T23:30:00Z sits 30 minutes before the epoch, within the
	// [-1h, 0) bucket, whose correct start is 1969-12-31T23:00:00Z.
	ts := time.Date(1969, 12, 31, 23, 30, 0, 0, time.UTC)
	got := WindowBucket(ts, d, time.UTC)
	want := time.Date(1969, 12, 31, 23, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("bucket = %v, want %v", got, want)
	}
}

func TestWindowBucket_CivilDayGroupsWithinSameLocalDay(t *testing.T) {
	loc := mustLoadLocation(t, "America/New_York")
	d := 24 * time.Hour
	morning := time.Date(2026, 6, 15, 6, 0, 0, 0, loc)
	night := time.Date(2026, 6, 15, 23, 30, 0, 0, loc)
	bm, bn := WindowBucket(morning, d, loc), WindowBucket(night, d, loc)
	if !bm.Equal(bn) {
		t.Fatalf("same local calendar day landed in different buckets: %v vs %v", bm, bn)
	}
	wantY, wantM, wantD := 2026, time.June, 15
	gy, gm, gd := bm.Date()
	if gy != wantY || gm != wantM || gd != wantD {
		t.Fatalf("bucket date = %d-%02d-%02d, want %d-%02d-%02d", gy, gm, gd, wantY, wantM, wantD)
	}
	if bm.Hour() != 0 || bm.Minute() != 0 {
		t.Fatalf("civil-day bucket must start at local midnight, got %v", bm)
	}
}

func TestWindowBucket_CivilDayDSTSpringForwardAlignmentHolds(t *testing.T) {
	// EC-24: 2026-03-08 is America/New_York's real spring-forward DST
	// transition day (2AM -> 3AM), making it a 23-hour civil day. A
	// naive "anchor + idx*24h" scheme would place a timestamp taken 24
	// real hours after that day's midnight one hour INTO March 9th,
	// still inside what should already be the March 9th bucket — the
	// exact failure mode civil-day (calendar-date-based) bucketing
	// exists to prevent.
	loc := mustLoadLocation(t, "America/New_York")
	d := 24 * time.Hour

	dstDayMidnight := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	nextDayMidnight := time.Date(2026, 3, 9, 0, 0, 0, 0, loc)
	if got := nextDayMidnight.Sub(dstDayMidnight); got != 23*time.Hour {
		t.Fatalf("test setup assumption wrong: America/New_York 2026-03-08 is not a 23h day (got %v) — recheck the transition date", got)
	}

	// A timestamp late on the transition day itself must still bucket to
	// March 8th, not spill into March 9th's bucket.
	lateOnDSTDay := time.Date(2026, 3, 8, 22, 0, 0, 0, loc)
	b := WindowBucket(lateOnDSTDay, d, loc)
	if !b.Equal(dstDayMidnight) {
		t.Fatalf("late-on-transition-day timestamp bucketed to %v, want the DST day's own midnight %v", b, dstDayMidnight)
	}

	// A timestamp exactly 24 real (absolute) hours after the DST day's
	// midnight is already 1AM on March 9th (since March 8th only had 23
	// hours) — it must bucket to March 9th, not March 8th.
	exactly24hLater := dstDayMidnight.Add(24 * time.Hour)
	b2 := WindowBucket(exactly24hLater, d, loc)
	if !b2.Equal(nextDayMidnight) {
		t.Fatalf("24-real-hours-after-midnight timestamp (%v) bucketed to %v, want March 9th's midnight %v", exactly24hLater, b2, nextDayMidnight)
	}
}

func TestWindowBucket_CivilDayCrossesLocalDateBoundary(t *testing.T) {
	loc := mustLoadLocation(t, "America/New_York")
	d := 24 * time.Hour
	justBefore := time.Date(2026, 6, 15, 23, 59, 59, 0, loc)
	justAfter := time.Date(2026, 6, 16, 0, 0, 1, 0, loc)
	b1, b2 := WindowBucket(justBefore, d, loc), WindowBucket(justAfter, d, loc)
	if b1.Equal(b2) {
		t.Fatal("timestamps either side of local midnight must land in different civil-day buckets")
	}
}

func TestWindowBucket_MultiDayWindow(t *testing.T) {
	// 7-day buckets are numbered from the fixed 1970-01-01 anchor, not
	// from either test date — so which two dates share a bucket is a
	// fact about that anchor, not a free choice. 2026-06-11..2026-06-17
	// is one such anchor-aligned 7-day span (verified independently);
	// 2026-06-15 and 2026-06-18 straddle its boundary and must NOT share
	// a bucket, which this test also checks, to pin the exact semantics
	// rather than a convenient assumption.
	loc := mustLoadLocation(t, "America/New_York")
	d := 7 * 24 * time.Hour

	withinSpan1 := time.Date(2026, 6, 12, 6, 0, 0, 0, loc)
	withinSpan2 := time.Date(2026, 6, 16, 23, 0, 0, 0, loc)
	b1, b2 := WindowBucket(withinSpan1, d, loc), WindowBucket(withinSpan2, d, loc)
	if !b1.Equal(b2) {
		t.Fatalf("two dates within the same anchor-aligned 7-day span should share a bucket: %v vs %v", b1, b2)
	}
	wantStart := time.Date(2026, 6, 11, 0, 0, 0, 0, loc)
	if !b1.Equal(wantStart) {
		t.Fatalf("bucket start = %v, want %v", b1, wantStart)
	}
	if b1.Hour() != 0 || b1.Minute() != 0 {
		t.Fatalf("multi-day bucket must still start at local midnight, got %v", b1)
	}

	acrossBoundary := time.Date(2026, 6, 18, 6, 0, 0, 0, loc)
	b3 := WindowBucket(acrossBoundary, d, loc)
	if b3.Equal(b1) {
		t.Fatal("a date past the anchor-aligned 7-day span's end must land in the NEXT bucket, not the same one")
	}
}

func TestFloorDiv_NegativeRoundsTowardNegativeInfinity(t *testing.T) {
	cases := []struct{ a, b, want int64 }{
		{7, 2, 3},
		{-7, 2, -4},
		{7, -2, -4},
		{-7, -2, 3},
		{0, 5, 0},
		{-1, 5, -1},
	}
	for _, c := range cases {
		if got := floorDiv(c.a, c.b); got != c.want {
			t.Errorf("floorDiv(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
