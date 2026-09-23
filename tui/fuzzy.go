package tui

import (
	"regexp"
	"sort"
	"strings"
)

// Port of src/fuzzy.ts: subsequence fuzzy matching with a quality score
// (lower is better) and token-based filtering.

// FuzzyMatch is a fuzzy match outcome.
type FuzzyMatch struct {
	Matches bool
	Score   float64
}

// MatchFuzzy matches query characters in order within text. Upstream names
// both the type and the function fuzzyMatch/FuzzyMatch; Go cannot, so the
// function is MatchFuzzy (divergence D66).
func MatchFuzzy(query string, text string) FuzzyMatch {
	queryLower := strings.ToLower(query)
	textLower := strings.ToLower(text)

	matchQuery := func(normalizedQuery string) FuzzyMatch {
		if len(normalizedQuery) == 0 {
			return FuzzyMatch{Matches: true, Score: 0}
		}
		if len([]rune(normalizedQuery)) > len([]rune(textLower)) {
			return FuzzyMatch{Matches: false, Score: 0}
		}

		// The scan runs over runes so multi-byte queries match consistently.
		queryRunes := []rune(normalizedQuery)
		textRunes := []rune(textLower)

		queryIndex := 0
		score := 0.0
		lastMatchIndex := -1
		consecutiveMatches := 0

		for queryIndex < len(queryRunes) {
			i := indexRuneFrom(textRunes, queryRunes[queryIndex], lastMatchIndex+1)
			if i == -1 {
				break
			}

			isWordBoundary := i == 0 || isFuzzyBoundary(textRunes[i-1])

			// Reward consecutive matches.
			if lastMatchIndex == i-1 {
				consecutiveMatches++
				score -= float64(consecutiveMatches) * 5
			} else {
				consecutiveMatches = 0
				// Penalize gaps.
				if lastMatchIndex >= 0 {
					score += float64(i-lastMatchIndex-1) * 2
				}
			}

			// Reward word-boundary matches.
			if isWordBoundary {
				score -= 10
			}

			// Slight penalty for later matches.
			score += unfusedProduct(float64(i), 0.1)

			lastMatchIndex = i
			queryIndex++
		}

		if queryIndex < len(queryRunes) {
			return FuzzyMatch{Matches: false, Score: 0}
		}
		if normalizedQuery == textLower {
			score -= 100
		}
		return FuzzyMatch{Matches: true, Score: score}
	}

	primaryMatch := matchQuery(queryLower)
	if primaryMatch.Matches {
		return primaryMatch
	}

	swappedQuery := swappedAlphaNumericQuery(queryLower)
	if swappedQuery == "" {
		return primaryMatch
	}

	swappedMatch := matchQuery(swappedQuery)
	if !swappedMatch.Matches {
		return primaryMatch
	}
	return FuzzyMatch{Matches: true, Score: swappedMatch.Score + 5}
}

// unfusedProduct returns a*b with the multiplication rounded before it is
// accumulated.
//
// Go's arm64 and ppc64 backends contract `acc += a*b` into a fused
// multiply-add, which rounds once, where V8 rounds the product and the sum
// separately. The ULP that fusion saves is enough to drift from upstream's
// golden scores, so the product is forced through a call boundary — //go:noinline
// keeps the intermediate rounding and the port bit-identical to V8.
//
//go:noinline
func unfusedProduct(a, b float64) float64 { return a * b }

var (
	alphaNumericQueryRegex = regexp.MustCompile(`^([a-z]+)([0-9]+)$`)
	numericAlphaQueryRegex = regexp.MustCompile(`^([0-9]+)([a-z]+)$`)
)

// swappedAlphaNumericQuery swaps a leading letter/digit run with a trailing
// one (upstream's alphaNumericMatch/numericAlphaMatch).
func swappedAlphaNumericQuery(query string) string {
	if match := alphaNumericQueryRegex.FindStringSubmatch(query); match != nil {
		return match[2] + match[1]
	}
	if match := numericAlphaQueryRegex.FindStringSubmatch(query); match != nil {
		return match[2] + match[1]
	}
	return ""
}

func isFuzzyBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '-', '_', '.', '/', ':':
		return true
	}
	return false
}

// indexRuneFrom returns the index of needle at or after start.
func indexRuneFrom(haystack []rune, needle rune, start int) int {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(haystack); i++ {
		if haystack[i] == needle {
			return i
		}
	}
	return -1
}

// FuzzyFilter filters and sorts items by fuzzy match quality (best first).
// Whitespace- and slash-separated tokens must all match.
func FuzzyFilter[T any](items []T, query string, getText func(item T) string) []T {
	if strings.TrimSpace(query) == "" {
		return items
	}

	tokens := splitFuzzyTokens(strings.TrimSpace(query))
	if len(tokens) == 0 {
		return items
	}

	type result struct {
		item  T
		score float64
	}
	var results []result
	for _, item := range items {
		text := getText(item)
		totalScore := 0.0
		allMatch := true
		for _, token := range tokens {
			match := MatchFuzzy(token, text)
			if match.Matches {
				totalScore += match.Score
			} else {
				allMatch = false
				break
			}
		}
		if allMatch {
			results = append(results, result{item: item, score: totalScore})
		}
	}

	sort.SliceStable(results, func(a, b int) bool { return results[a].score < results[b].score })
	out := make([]T, 0, len(results))
	for _, entry := range results {
		out = append(out, entry.item)
	}
	return out
}

// splitFuzzyTokens splits on whitespace and slashes.
func splitFuzzyTokens(query string) []string {
	return strings.FieldsFunc(query, func(r rune) bool {
		return isWhitespaceRune(r) || r == '/'
	})
}
