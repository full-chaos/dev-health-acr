package contextfabric

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// An input that needs more than the old pass limit of reductions is reduced
// until it fits, not widened and not refused.
func TestAnInputNeedingManyPassesKeepsReducing(t *testing.T) {
	facts := make([]CanonicalFact, 100)
	for index := range facts {
		facts[index] = CanonicalFact{Kind: FactStatus, Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item_%03d", index)}}
	}
	input := SynthesisInput{}
	input.Facts.Facts = facts
	calls := 0
	encode := func(_ string, given SynthesisInput, max int) ([]byte, error) {
		calls++
		if len(given.Facts.Facts) <= 1 {
			return []byte(`{}`), nil
		}
		return nil, &ModelInputOverflow{Bytes: max + 1, MaxBytes: max}
	}
	given, encoded, bound, err := RuntimeAnswerSynthesizer{}.encodeWithinInputBound(context.Background(), storage.Principal{}, input, encode, 1000, 1<<20)
	if err != nil || string(encoded) != `{}` || bound == nil || len(given.Facts.Facts) != 1 {
		t.Fatalf("encoded = %q, err = %v, bound nil = %v, facts = %d, want one fact fitted", encoded, err, bound == nil, len(given.Facts.Facts))
	}
	if calls <= maxSynthesisInputBoundPasses+1 {
		t.Fatalf("encode calls = %d, want more than the old pass limit", calls)
	}
}

// An input no fact removal can shrink is served whole when it fits the widened bound.
func TestAnInputNothingCanShrinkIsWidened(t *testing.T) {
	input := SynthesisInput{}
	encode := func(_ string, _ SynthesisInput, max int) ([]byte, error) {
		if max < 5000 {
			return nil, &ModelInputOverflow{Bytes: 5000, MaxBytes: max}
		}
		return []byte(`{}`), nil
	}
	_, encoded, _, err := RuntimeAnswerSynthesizer{}.encodeWithinInputBound(context.Background(), storage.Principal{}, input, encode, 1000, 8000)
	if err != nil || string(encoded) != `{}` {
		t.Fatalf("encoded = %q, err = %v, want the whole input", encoded, err)
	}
}
