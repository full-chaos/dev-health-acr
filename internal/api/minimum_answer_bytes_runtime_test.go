package api

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The API runtime bundle carries its OWN lower-bound check on
// MaxSerializedBytes, independent of the service config loader's. Two
// independent guards on one quantity is exactly the shape that drifts, so each
// needs its own oracle -- a review found this one had none, and a guard with no
// test is a guard that can be deleted or inverted silently.
//
// It is a separate surface rather than a duplicate: config validation runs when
// the process reads its environment, and this runs when a caller composes the
// runtime bundle programmatically. A composed bundle never passes through the
// loader, so the loader's check does not cover it.
// boundsRejection is the EXACT error NewApp's limits guard returns
// (evidence_dependencies.go). Matching it exactly is the whole point: an
// earlier version of this test accepted any non-nil error and probed for the
// substrings "bounds" or "serialized", neither of which this message contains.
// It therefore passed with the guard reverted to the old floor -- proven by
// mutation, not argued -- because both calls fell through to the
// missing-capabilities error instead. A test that accepts any error from a
// constructor with many failure modes asserts nothing about the one it names.
const boundsRejection = "hosted read limits are invalid"

func TestNewAppRejectsSerializedBudgetBelowTheMinimumAnswerSize(t *testing.T) {
	t.Parallel()

	const oldFloor = 8192
	if oldFloor >= contractsv1.ContextFabricMinimumAnswerBytes {
		t.Fatalf("premise gone: %d is no longer below the minimum %d", oldFloor, contractsv1.ContextFabricMinimumAnswerBytes)
	}

	base := AppConfig{
		ServiceName:              "dev-health-acr",
		ServiceVersion:           "test",
		RequestTimeout:           time.Second,
		MaxRequestBodyBytes:      1 << 20,
		MaxEvidenceResponseBytes: 1 << 20,
		MaxItems:                 30,
		MaxOutputTokens:          4000,
	}

	below := base
	below.MaxSerializedBytes = oldFloor
	_, err := NewApp(below, Dependencies{}, testLogger(nil))
	if err == nil {
		t.Fatalf("NewApp accepted a %d-byte serialized budget: no answer can be serialized in that, so every investigation on the resulting app fails at the route blaming the caller's question", oldFloor)
	}
	if err.Error() != boundsRejection {
		t.Fatalf("NewApp rejected the sub-minimum budget with %q, not the limits guard's %q.\n"+
			"This test must fail when the GUARD is removed, and it can only do that by matching the guard's own error -- "+
			"any other message means it fell through to a different failure mode and proves nothing about the bound.",
			err.Error(), boundsRejection)
	}

	// The boundary is accepted, or the documented minimum is a lie. Dependencies{}
	// is empty, so this still errors -- but it must error for a DIFFERENT reason.
	// Asserting the message is not the bounds one is what makes this meaningful:
	// "some error occurred" would be true whether or not the boundary is accepted.
	at := base
	at.MaxSerializedBytes = contractsv1.ContextFabricMinimumAnswerBytes
	_, atErr := NewApp(at, Dependencies{}, testLogger(nil))
	if atErr != nil && atErr.Error() == boundsRejection {
		t.Fatalf("NewApp rejected exactly the documented minimum (%d) on limits grounds: the guard is off by one and the published minimum is unusable",
			contractsv1.ContextFabricMinimumAnswerBytes)
	}
}
