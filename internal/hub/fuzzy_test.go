package hub

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"testing"
)

// searchCases is testdata/search_cases.json, generated from
// web/src/lib/fuzzy.ts by hack/search-cases.ts. The web tests read the same
// file, so the two rankings cannot drift.
type searchCases struct {
	FieldCases []struct {
		Term    string `json:"term"`
		Text    string `json:"text"`
		Primary bool   `json:"primary"`
		Match   *struct {
			Score  float64      `json:"score"`
			Ranges []fuzzyRange `json:"ranges"`
		} `json:"match"`
	} `json:"fieldCases"`
	ItemCases []struct {
		Query     string      `json:"query"`
		Primary   string      `json:"primary"`
		Secondary []string    `json:"secondary"`
		Match     *fuzzyMatch `json:"match"`
	} `json:"itemCases"`
	RankItems []struct {
		ID        string   `json:"id"`
		Primary   string   `json:"primary"`
		Secondary []string `json:"secondary"`
		Order     int      `json:"order"`
	} `json:"rankItems"`
	RankCases []struct {
		Query    string   `json:"query"`
		Limit    int      `json:"limit"`
		Expected []string `json:"expected"`
	} `json:"rankCases"`
}

func loadSearchCases(t *testing.T) searchCases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/search_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c searchCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.FieldCases) == 0 || len(c.ItemCases) == 0 || len(c.RankCases) == 0 {
		t.Fatal("search_cases.json has no cases")
	}
	return c
}

func sameScore(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestFuzzyFieldCases(t *testing.T) {
	for _, c := range loadSearchCases(t).FieldCases {
		got, ok := matchField(lowerASCII(c.Term), lowerASCII(c.Text), c.Primary, true)
		if c.Term == "" {
			continue
		}
		switch {
		case c.Match == nil && ok:
			t.Errorf("matchField(%q, %q, %v) = %v, want no match", c.Term, c.Text, c.Primary, got)
		case c.Match != nil && !ok:
			t.Errorf("matchField(%q, %q, %v): no match, want %v", c.Term, c.Text, c.Primary, *c.Match)
		case c.Match != nil && (!sameScore(got.score, c.Match.Score) || !reflect.DeepEqual(nonNil(got.ranges), nonNil(c.Match.Ranges))):
			t.Errorf("matchField(%q, %q, %v) = %v %v, want %v %v", c.Term, c.Text, c.Primary, got.score, got.ranges, c.Match.Score, c.Match.Ranges)
		}
	}
}

func nonNil(r []fuzzyRange) []fuzzyRange {
	if r == nil {
		return []fuzzyRange{}
	}
	return r
}

func TestFuzzyItemCases(t *testing.T) {
	for _, c := range loadSearchCases(t).ItemCases {
		got, ok := matchItem(c.Query, c.Primary, c.Secondary)
		switch {
		case c.Match == nil && ok:
			t.Errorf("matchItem(%q) = %+v, want no match", c.Query, got)
		case c.Match != nil && !ok:
			t.Errorf("matchItem(%q): no match, want %+v", c.Query, *c.Match)
		case c.Match != nil:
			if !sameScore(got.Score, c.Match.Score) || !reflect.DeepEqual(got.Primary, nonNil(c.Match.Primary)) {
				t.Errorf("matchItem(%q) = %v %v, want %v %v", c.Query, got.Score, got.Primary, c.Match.Score, c.Match.Primary)
			}
			for i := range c.Match.Secondary {
				if !reflect.DeepEqual(got.Secondary[i], nonNil(c.Match.Secondary[i])) {
					t.Errorf("matchItem(%q) secondary[%d] = %v, want %v", c.Query, i, got.Secondary[i], c.Match.Secondary[i])
				}
			}
		}
	}
}

// TestFuzzyRankCases ranks the fixture's items as fuzzy.ts's rank does and
// as Search orders hits (compareRanked with the tiebreak order).
func TestFuzzyRankCases(t *testing.T) {
	c := loadSearchCases(t)
	for _, rc := range c.RankCases {
		type scored struct {
			id    string
			score float64
			order int
		}
		var hits []scored
		for _, it := range c.RankItems {
			if m, ok := matchItem(rc.Query, it.Primary, it.Secondary); ok {
				hits = append(hits, scored{it.ID, m.Score, it.Order})
			}
		}
		slices.SortStableFunc(hits, func(a, b scored) int { return compareRanked(a.score, b.score, a.order-b.order) })
		got := []string{}
		for _, h := range hits[:min(len(hits), rc.Limit)] {
			got = append(got, h.id)
		}
		if !slices.Equal(got, rc.Expected) {
			t.Errorf("rank(%q, limit %d) = %v, want %v", rc.Query, rc.Limit, got, rc.Expected)
		}
	}
}

func TestAbbrOf(t *testing.T) {
	for kind, want := range map[string]string{
		"HelmRelease": "HR", "ConfigMap": "CM", "ClusterRoleBinding": "CRB", "customresourcedefinition": "CUS",
		"EC2NodeClass": "ENC", "VeryLongKindNameWithManyWords": "VLKN",
	} {
		if got := abbrOf(kind); got != want {
			t.Errorf("abbrOf(%q) = %q, want %q", kind, got, want)
		}
	}
}
