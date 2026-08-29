package query

// maxSuggestRadius is the maximum edit distance a candidate may be from the
// unrecognized text and still be offered as a "did you mean" suggestion.
const maxSuggestRadius = 2

// EditDistance computes the Levenshtein edit distance between a and b with
// insert = delete = substitute = cost 1 (classic unweighted Levenshtein),
// via a dynamic-programming table collapsed to two rows. No third-party
// string-distance package anywhere in this codebase.
func EditDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n, m := len(ra), len(rb)

	prev := make([]int, m+1)
	curr := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		curr[0] = i
		for j := 1; j <= m; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = minInt(del, minInt(ins, sub))
		}
		prev, curr = curr, prev
	}
	return prev[m]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Suggest finds the closest candidate to got by edit distance, for "did you
// mean 'X'?" diagnostics. It returns "" (no suggestion) when nothing is
// within maxSuggestRadius, or when the best distance is tied between two or
// more candidates — a tie is genuinely ambiguous, so it's suppressed rather
// than guessed at, per the spec ("ties suppressed").
func Suggest(got string, candidates []string) string {
	best := -1
	bestCandidate := ""
	tie := false

	for _, c := range candidates {
		d := EditDistance(got, c)
		if d > maxSuggestRadius {
			continue
		}
		switch {
		case best == -1 || d < best:
			best, bestCandidate, tie = d, c, false
		case d == best:
			tie = true
		}
	}

	if best == -1 || tie {
		return ""
	}
	return bestCandidate
}
