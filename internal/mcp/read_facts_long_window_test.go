package mcp

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestReadFactsValidateBoundsWindowAtTheWidestKindMaximum(t *testing.T) {
	for days, ok := range map[int]bool{61: true, 365: true, 366: false} {
		in := readFactsInput{Kinds: []string{"investment"}, Subjects: []readFactsSubjectInput{{Kind: "team", CanonicalID: "t"}}, Window: &readFactsWindowInput{Mode: "trailing", Days: days}}
		if err := in.validate(); (err == nil) != ok {
			t.Errorf("days %d: err %v, ok want %v", days, err, ok)
		}
	}
}

func TestReadFactsLocalBoundEqualsTheWidestKindMaximum(t *testing.T) {
	if readFactsMaxRangeDays != contractsv1.WidestRangeDays() {
		t.Fatalf("local bound %d, table %d", readFactsMaxRangeDays, contractsv1.WidestRangeDays())
	}
}
