package mcp

import "testing"

func TestReadFactsValidateTakesLongWindowsForInvestmentOnly(t *testing.T) {
	window := func(days int) *readFactsWindowInput { return &readFactsWindowInput{Mode: "trailing", Days: days} }
	cases := []struct {
		name  string
		kinds []string
		days  int
		ok    bool
	}{
		{"investment 180", []string{"investment"}, 180, true},
		{"investment 365", []string{"investment"}, 365, true},
		{"investment 366", []string{"investment"}, 366, false},
		{"health 61", []string{"health"}, 61, false},
		{"investment with health 180", []string{"investment", "health"}, 180, false},
	}
	for _, c := range cases {
		in := readFactsInput{Kinds: c.kinds, Subjects: []readFactsSubjectInput{{Kind: "team", CanonicalID: "t"}}, Window: window(c.days)}
		if err := in.validate(); (err == nil) != c.ok {
			t.Errorf("%s: err %v, ok want %v", c.name, err, c.ok)
		}
	}
}
