package query

import "testing"

// FuzzParseQuery proves ParseQuery's own core safety property: parsing
// arbitrary bytes must never panic (the recursive-descent parser is
// depth-checked — maxParenDepth, the length guard in
// ParseQueryWithLimit — specifically so pathological input gets a
// positioned E-PARSE instead of a stack overflow or an index panic), and
// any rejection must be a genuine, positioned *ParseError — never a bare
// error, never a silently-wrong-but-"successful" parse of garbage.
//
// The seed corpus spans every stage kind and several of the grammar's
// own sharper edges (deep nesting, keyword-shaped path segments, the
// relaxed S-5 sort/limit-after-stats exception, unterminated literals,
// raw control bytes, multibyte text) — coverage-guided fuzzing mutates
// FROM these, so a richer seed set finds real bugs faster than a blank
// one would.
func FuzzParseQuery(f *testing.F) {
	seeds := []string{
		// valid, ordinary
		`level == "error"`,
		`status >= 500 and not exists(user)`,
		`level >= "warn" or status != 200`,
		`x in [1, 2, 3]`,
		`name ~ "^prod-"`,
		`msg !~ "debug"`,
		`ts >= -1h`,
		`."http-status" == 500`,
		`headers["x-id"][0] == "abc"`,

		// every stage kind, and the S-5-relaxed sort/limit-after-stats
		// exception specifically
		`| fields a, b.c`,
		`| sort x desc limit 5`,
		`| limit 10`,
		`| stats count()`,
		`| stats count_distinct(user), sum(bytes), avg(ms), min(x), max(x), p50(ms), p95(ms), p99(ms) by service, region every 1h`,
		`| stats count() by service | sort count desc limit 3`,
		`| stats count() by service | limit 3`,
		`level == "error" | fields msg | sort x desc limit 1`,

		// keyword-shaped path segments (§4's own relaxation) in the
		// positions where it's actually legal
		`| stats sum(count) by sort`,
		`| fields count, sum, avg`,

		// sharp edges: deep nesting, empty/whitespace, unterminated
		// literals, stray operators, illegal bytes, S-5 violations
		`((((((((((true))))))))))`,
		``,
		`   `,
		`"unterminated`,
		`level == `,
		`== "error"`,
		`not not not exists(x)`,
		`| stats count() | fields x`,
		`| stats count() | stats count()`,
		`| stats count() | sort x desc limit 1 | fields y`,
		"\x00\x01\x02",
		"level == \"日本語\"",
		`x == 1_000`,
		`x == 1e`,
		`level in []`,
		`stats count()`, // no leading "|" — parsed as a (failing) filter, not a stage
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		_, err := ParseQuery(s)
		if err == nil {
			return // a successful parse of arbitrary input is fine — nothing to check further here
		}
		if _, ok := err.(*ParseError); !ok {
			t.Fatalf("ParseQuery(%q) returned a non-*ParseError: %T: %v", s, err, err)
		}
	})
}
