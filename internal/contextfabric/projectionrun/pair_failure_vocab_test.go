package projectionrun

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
)

// The pair failure counter's closed label set must be exactly the classes the
// classifier can return; a class added to failureClasses without the counter
// vocabulary would be recorded as "other".
func TestPairFailureCounterVocabularyMatchesTheClassifier(t *testing.T) {
	if len(failureClasses) == 0 {
		t.Fatal("failureClasses is empty; this test would be vacuous")
	}
	want := []string{failureClassUnclassified}
	for _, entry := range failureClasses {
		want = append(want, entry.class)
	}
	slices.Sort(want)
	want = slices.Compact(want)
	got := hostedmetrics.ProjectionFailureClassVocabulary()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("hostedmetrics failure class vocabulary %v, classifier classes %v", got, want)
	}
}
