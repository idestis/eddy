package hub

import (
	"slices"
	"strings"
)

// This file is a port of web/src/lib/fuzzy.ts, the command palette's match
// ranking, so that server-side search (GET /api/v1/search) ranks exactly
// like the browser. testdata/search_cases.json holds cases both
// implementations must pass; change the two together.
//
// Indices are byte offsets. Kubernetes names, namespaces and kinds are
// ASCII, where they equal the UTF-16 offsets fuzzy.ts returns.

// Match tiers. Higher is better; tiers are 100 apart.
const (
	tierExact              = 900
	tierPrefix             = 800
	tierWord               = 700
	tierSubstring          = 600
	tierSecondaryExact     = 500
	tierSecondaryPrefix    = 400
	tierSecondaryWord      = 300
	tierSecondarySubstring = 200
	tierSubsequence        = 100
)

// fuzzyRange is a half-open [start, end) range of matched bytes. It
// marshals as a two-element array, like fuzzy.ts's Ranges.
type fuzzyRange [2]int

type fieldMatch struct {
	score  float64
	ranges []fuzzyRange
}

// fuzzyMatch is how a whole query matched an item.
type fuzzyMatch struct {
	Score float64 `json:"score"`
	// Primary are the matched ranges in the primary text.
	Primary []fuzzyRange `json:"primary"`
	// Secondary are the matched ranges per secondary text, by index.
	Secondary [][]fuzzyRange `json:"secondary"`
}

// isWordBreak mirrors /[\s\-/_.:@]/ for ASCII.
func isWordBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r', '-', '/', '_', '.', ':', '@':
		return true
	}
	return false
}

func isWordStart(t string, i int) bool { return i == 0 || isWordBreak(t[i-1]) }

func indexFrom(t, q string, from int) int {
	if from > len(t) {
		return -1
	}
	i := strings.Index(t[from:], q)
	if i < 0 {
		return -1
	}
	return from + i
}

func indexByteFrom(t string, c byte, from int) int {
	if from >= len(t) {
		return -1
	}
	i := strings.IndexByte(t[from:], c)
	if i < 0 {
		return -1
	}
	return from + i
}

// lowerASCII lowercases s, allocating only when it has an upper-case letter.
func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' || c >= 0x80 {
			return strings.ToLower(s)
		}
	}
	return s
}

// matchField scores one (already lower-cased) term against one field
// (lower-cased too), or reports false when it does not match well enough.
// It is fuzzy.ts's matchField; subsequence matches only when allowSub.
func matchField(q, t string, primary, allowSub bool) (fieldMatch, bool) {
	if q == "" {
		return fieldMatch{score: 1}, true
	}
	if t == "" {
		return fieldMatch{}, false
	}
	// Shorter targets win ties inside a tier; the in-tier bonus stays below 100.
	tidy := func(tier, at int) float64 {
		return float64(tier) + 40*(float64(len(q))/float64(len(t))) + (40-float64(min(at, 40)))*0.5
	}
	whole := func(tier, at int) (fieldMatch, bool) {
		return fieldMatch{score: tidy(tier, at), ranges: []fuzzyRange{{at, at + len(q)}}}, true
	}
	pick := func(p, s int) int {
		if primary {
			return p
		}
		return s
	}
	if t == q {
		return whole(pick(tierExact, tierSecondaryExact), 0)
	}
	// One search serves the prefix, word-boundary and substring tiers
	// (fuzzy.ts's wordBoundaryIndex starts from the same first hit).
	if s := strings.Index(t, q); s == 0 {
		return whole(pick(tierPrefix, tierSecondaryPrefix), 0)
	} else if s > 0 {
		w := s
		for w > 0 && !isWordStart(t, w) {
			w = indexFrom(t, q, w+1)
		}
		if w >= 0 {
			return whole(pick(tierWord, tierSecondaryWord), w)
		}
		return whole(pick(tierSubstring, tierSecondarySubstring), s)
	}
	if !primary || !allowSub {
		return fieldMatch{}, false
	}
	return subsequence(q, t)
}

// subsequence matches every character of q in order, preferring word
// starts; most characters must be contiguous or start a word.
func subsequence(q, t string) (fieldMatch, bool) {
	var ranges []fuzzyRange
	from, last, good := 0, -2, 0
	for i := 0; i < len(q); i++ {
		ch := q[i]
		j := indexByteFrom(t, ch, from)
		if j < 0 {
			return fieldMatch{}, false
		}
		if j != last+1 {
			for k := j; k >= 0 && k < len(t); k = indexByteFrom(t, ch, k+1) {
				if isWordStart(t, k) {
					j = k
					break
				}
			}
		}
		if j == last+1 || isWordStart(t, j) {
			good++
		}
		if n := len(ranges); n > 0 && ranges[n-1][1] == j {
			ranges[n-1][1] = j + 1
		} else {
			ranges = append(ranges, fuzzyRange{j, j + 1})
		}
		last, from = j, j+1
	}
	if float64(good)/float64(len(q)) < 0.6 {
		return fieldMatch{}, false
	}
	span := ranges[len(ranges)-1][1] - ranges[0][0]
	bonus := float64(good*4-len(ranges)*3) - float64(span)*0.2
	return fieldMatch{score: tierSubsequence + max(0, min(99, 50+bonus)), ranges: ranges}, true
}

// queryTerms splits a query into lower-cased whitespace-separated terms.
func queryTerms(query string) []string {
	f := strings.Fields(query)
	for i := range f {
		f[i] = lowerASCII(f[i])
	}
	return f
}

// matchTerms is fuzzy.ts's matchItem over pre-split, lower-cased terms and
// lower-cased fields. ranges controls whether matched ranges are kept.
func matchTerms(terms []string, primary string, secondary []string, allowSub, ranges bool) (fuzzyMatch, bool) {
	out := fuzzyMatch{}
	if ranges {
		out.Primary = []fuzzyRange{}
		out.Secondary = make([][]fuzzyRange, len(secondary))
		for i := range out.Secondary {
			out.Secondary[i] = []fuzzyRange{}
		}
	}
	if len(terms) == 0 {
		out.Score = 1
		return out, true
	}
	for _, term := range terms {
		best, ok := matchField(term, primary, true, allowSub)
		where := -1
		for i, text := range secondary {
			m, mok := matchField(term, text, false, false)
			// Earlier secondary fields win ties.
			if mok && (!ok || m.score-float64(i) > best.score) {
				best, ok, where = m, true, i
			}
		}
		if !ok {
			return fuzzyMatch{}, false
		}
		out.Score += best.score
		if ranges {
			if where < 0 {
				out.Primary = append(out.Primary, best.ranges...)
			} else {
				out.Secondary[where] = append(out.Secondary[where], best.ranges...)
			}
		}
	}
	out.Score /= float64(len(terms))
	if ranges {
		out.Primary = mergeRanges(out.Primary)
		for i := range out.Secondary {
			out.Secondary[i] = mergeRanges(out.Secondary[i])
		}
	}
	return out, true
}

// matchItem is fuzzy.ts's matchItem: the AND of the query's terms over a
// primary text and secondary texts in priority order.
func matchItem(query, primary string, secondary []string) (fuzzyMatch, bool) {
	sec := make([]string, len(secondary))
	for i, s := range secondary {
		sec[i] = lowerASCII(s)
	}
	return matchTerms(queryTerms(query), lowerASCII(primary), sec, true, true)
}

func mergeRanges(rs []fuzzyRange) []fuzzyRange {
	sorted := slices.Clone(rs)
	slices.SortStableFunc(sorted, func(a, b fuzzyRange) int { return a[0] - b[0] })
	out := []fuzzyRange{}
	for _, r := range sorted {
		if n := len(out); n > 0 && r[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

// matchTier is the tier of a match score (equal tier = equal quality).
func matchTier(score float64) int { return int(score / 100) }

// compareRanked orders two matches like fuzzy.ts's rank: higher tier
// first, then the tiebreak, then the finer score.
func compareRanked(aScore, bScore float64, tiebreak int) int {
	if d := matchTier(bScore) - matchTier(aScore); d != 0 {
		return d
	}
	if tiebreak != 0 {
		return tiebreak
	}
	switch {
	case bScore > aScore:
		return 1
	case bScore < aScore:
		return -1
	}
	return 0
}

// kindAbbr is the short kind label of web/src/lib/kinds.ts, which the
// palette matches as a secondary field.
var kindAbbr = map[string]string{
	"Kustomization": "KS", "HelmRelease": "HR", "GitRepository": "GIT", "OCIRepository": "OCI",
	"HelmRepository": "HELM", "HelmChart": "CHRT", "Bucket": "BKT", "Deployment": "DEP",
	"StatefulSet": "STS", "DaemonSet": "DS", "Job": "JOB", "CronJob": "CJ", "Pod": "POD",
	"HorizontalPodAutoscaler": "HPA", "Service": "SVC", "Ingress": "ING", "NetworkPolicy": "NP",
	"PersistentVolumeClaim": "PVC", "StorageClass": "SC", "Namespace": "NS", "ServiceAccount": "SA",
	"PodDisruptionBudget": "PDB", "NodePool": "NPL", "NodeClaim": "NCL", "EC2NodeClass": "ENC",
	"ExternalSecret": "ES", "ClusterExternalSecret": "CES", "SecretStore": "SS",
	"ClusterSecretStore": "CSS", "PushSecret": "PS",
}

// abbrOf mirrors kindInfo(kind).abbr: the table, else the kind's
// non-lower-case letters (at most 4), else its first three letters upper-cased.
func abbrOf(kind string) string {
	if a, ok := kindAbbr[kind]; ok {
		return a
	}
	var b strings.Builder
	for i := 0; i < len(kind) && b.Len() < 4; i++ {
		if c := kind[i]; c < 'a' || c > 'z' {
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	return strings.ToUpper(kind[:min(3, len(kind))])
}
