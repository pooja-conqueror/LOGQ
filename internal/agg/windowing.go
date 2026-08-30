package agg

import "time"

// unixEpochUTC is the fixed, deterministic reference instant every window
// bucket is anchored to (directly or indirectly) — never "now" or "the
// first record's timestamp," so bucket boundaries never depend on when a
// query happens to run (§15 determinism).
var unixEpochUTC = time.Unix(0, 0).UTC()

// WindowBucket computes the event-time window bucket ts falls into for a
// window duration d, in timezone loc (§8.1, EC-24).
//
//   - d < 24h: bucket index = floor((ts−epoch)/d), anchored to the raw
//     Unix epoch INSTANT — timezone-invariant by construction (loc plays
//     no part at all), matching §8.1's "anchor = Unix epoch for D < 24h"
//     literally. A 1h bucket in Asia/Kolkata (UTC+5:30) therefore does
//     NOT align to local :00 wall-clock minutes — a deliberate, spec-
//     stated choice, not an oversight.
//   - d >= 24h: buckets align to LOCAL CIVIL-CALENDAR DAYS in loc, not to
//     raw 24h Duration multiples of an instant. This distinction is the
//     entire DST-safety property (EC-24, "DST spring-forward day, 1d
//     buckets, civil-day alignment holds"): naively anchoring at some
//     fixed instant and adding `idx*24h` as a Duration operates on
//     absolute time and does NOT track wall-clock midnight across a DST
//     transition — on a 23-hour spring-forward day, midnight+24h lands
//     at 1AM the day after next's neighbor, not at the following day's
//     actual local midnight, silently splitting or merging the
//     transition day's records into the wrong bucket. Deriving the
//     bucket's calendar date directly from ts.In(loc), counting whole
//     CALENDAR days (via a UTC-midnight difference, since UTC has no DST
//     and every calendar day is exactly 24h there) rather than elapsed
//     Duration, and then reconstructing the bucket's start via
//     time.Date(y, m, d, 0, 0, 0, 0, loc) — which Go resolves to the
//     correct absolute instant for that zone's offset on that specific
//     date — sidesteps the whole class of bug.
func WindowBucket(ts time.Time, d time.Duration, loc *time.Location) time.Time {
	if d < 24*time.Hour {
		return windowBucketSubDay(ts, d)
	}
	return windowBucketCivilDay(ts, d, loc)
}

func windowBucketSubDay(ts time.Time, d time.Duration) time.Time {
	deltaNS := ts.Sub(unixEpochUTC).Nanoseconds()
	dNS := d.Nanoseconds()
	idx := floorDiv(deltaNS, dNS)
	return unixEpochUTC.Add(time.Duration(idx * dNS))
}

// civilEpochUTC is the fixed reference CALENDAR DATE (not instant) that
// day-count windows are numbered from — January 1, 1970, expressed as a
// UTC midnight purely so date arithmetic (Sub, AddDate) stays exact and
// DST-free; it is never compared against as an absolute instant.
var civilEpochUTC = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

func windowBucketCivilDay(ts time.Time, d time.Duration, loc *time.Location) time.Time {
	dayCount := max(int64(d/(24*time.Hour)), 1)

	y, m, day := ts.In(loc).Date()
	tsDateUTC := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)

	// Both sides are UTC midnights, so this difference is always an exact
	// whole number of 24h days — no DST distortion is possible in UTC.
	daysSinceEpoch := int64(tsDateUTC.Sub(civilEpochUTC).Hours() / 24)
	bucketDayIndex := floorDiv(daysSinceEpoch, dayCount)

	bucketDateUTC := civilEpochUTC.AddDate(0, 0, int(bucketDayIndex*dayCount))
	by, bm, bd := bucketDateUTC.Date()

	// Reconstructing via time.Date in loc (not by adding a Duration) is
	// what makes this DST-safe: Go computes the correct absolute instant
	// for that zone's actual offset on that calendar date.
	return time.Date(by, bm, bd, 0, 0, 0, 0, loc)
}

// floorDiv is integer division that rounds toward negative infinity
// (Go's native `/` truncates toward zero) — needed for correct bucketing
// of timestamps before the reference epoch (EC-22: "pre-1970 timestamps,
// negative epochs fine").
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
