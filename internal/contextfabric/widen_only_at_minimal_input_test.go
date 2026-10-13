package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Widening past the preferred share is for an input no fact removal can shrink.
// An input the pass limit stopped while facts were still being removed has a
// smaller form, so it is not served whole.
func TestAnInputThePassLimitStoppedIsNotWidened(t *testing.T) {
	facts := make([]CanonicalFact, 100)
	for index := range facts {
		facts[index] = CanonicalFact{Kind: FactStatus, Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item_%03d", index)}}
	}
	input := SynthesisInput{}
	input.Facts.Facts = facts
	encode := func(_ string, _ SynthesisInput, max int) ([]byte, error) {
		if max >= 1<<20 {
			return []byte(`{}`), nil
		}
		return nil, &ModelInputOverflow{Bytes: max + max/20, MaxBytes: max}
	}
	_, _, _, err := RuntimeAnswerSynthesizer{}.encodeWithinInputBound(context.Background(), storage.Principal{}, input, encode, 1000, 1<<20)
	var overflow *ModelInputOverflow
	if !errors.As(err, &overflow) {
		t.Fatalf("err = %v, want the overflow the pass limit ended on", err)
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
