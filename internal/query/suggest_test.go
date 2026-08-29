package query

import "testing"

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"count", "count", 0},
		{"cont", "count", 1},     // one deletion (missing 'u')
		{"coutn", "count", 2},    // transposition = 2 single-char edits
		{"levle", "level", 2},    // transposition = 2 single-char edits
		{"kitten", "sitting", 3}, // the textbook example
		{"exsits", "exists", 2},
	}
	for _, c := range cases {
		t.Run(c.a+"/"+c.b, func(t *testing.T) {
			got := EditDistance(c.a, c.b)
			if got != c.want {
				t.Fatalf("EditDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestEditDistance_Symmetric(t *testing.T) {
	pairs := [][2]string{{"count", "cont"}, {"level", "levle"}, {"", "abc"}}
	for _, p := range pairs {
		if EditDistance(p[0], p[1]) != EditDistance(p[1], p[0]) {
			t.Fatalf("EditDistance(%q,%q) != EditDistance(%q,%q)", p[0], p[1], p[1], p[0])
		}
	}
}

func TestSuggest_WithinRadius(t *testing.T) {
	candidates := []string{"count", "sum", "avg"}
	cases := []struct{ got, want string }{
		{"cont", "count"},  // distance 1
		{"coutn", "count"}, // distance 2
		{"avq", "avg"},     // distance 1
	}
	for _, c := range cases {
		t.Run(c.got, func(t *testing.T) {
			got := Suggest(c.got, candidates)
			if got != c.want {
				t.Fatalf("Suggest(%q, ...) = %q, want %q", c.got, got, c.want)
			}
		})
	}
}

func TestSuggest_BeyondRadiusReturnsEmpty(t *testing.T) {
	got := Suggest("xyzzy", []string{"count", "sum", "avg"})
	if got != "" {
		t.Fatalf("Suggest(%q, ...) = %q, want no suggestion", "xyzzy", got)
	}
}

func TestSuggest_TieIsSuppressed(t *testing.T) {
	// "an" is distance 1 from both "and" and "or"... actually distance
	// from "or" is 2 ('a'->'o', insert 'n') so pick a genuine tie instead:
	// "sun" is distance 1 from both "sum" and "sub".
	got := Suggest("sun", []string{"sum", "sub"})
	if got != "" {
		t.Fatalf("Suggest(%q, [sum, sub]) = %q, want \"\" (tie suppressed)", "sun", got)
	}
}

func TestSuggest_ExactMatchWins(t *testing.T) {
	got := Suggest("count", []string{"count", "count_distinct"})
	if got != "count" {
		t.Fatalf("Suggest exact match = %q, want %q", got, "count")
	}
}

func TestSuggest_EmptyCandidates(t *testing.T) {
	if got := Suggest("anything", nil); got != "" {
		t.Fatalf("Suggest with no candidates = %q, want \"\"", got)
	}
}

// End-to-end: the parser wires Suggest into real error sites (see
// parser.go's topLevelConnectors/existsCandidates usage); these confirm the
// wiring, not just the algorithm in isolation.
func TestParse_SuggestsOnTypoedConnector(t *testing.T) {
	_, err := ParseFilterExpr(`a == 1 adn b == 2`)
	pe := err.(*ParseError)
	if pe.Suggest != "and" {
		t.Fatalf("Suggest = %q, want %q (full error: %s)", pe.Suggest, "and", pe.Error())
	}
}

func TestParse_SuggestsOnTypoedExists(t *testing.T) {
	_, err := ParseFilterExpr(`exsits(url.path)`)
	pe := err.(*ParseError)
	if pe.Suggest != "exists" {
		t.Fatalf("Suggest = %q, want %q (full error: %s)", pe.Suggest, "exists", pe.Error())
	}
}

func TestParse_NoSuggestionWhenNothingClose(t *testing.T) {
	_, err := ParseFilterExpr(`a == 1 zzzzzzz b == 2`)
	pe := err.(*ParseError)
	if pe.Suggest != "" {
		t.Fatalf("Suggest = %q, want no suggestion", pe.Suggest)
	}
	if pe.Error() == "" {
		t.Fatal("Error() must still be non-empty without a suggestion")
	}
}
