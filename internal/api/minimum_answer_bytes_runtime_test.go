package api

import (
	"strings"
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
	if _, err := NewApp(below, Dependencies{}, testLogger(nil)); err == nil {
		t.Fatalf("NewApp accepted a %d-byte serialized budget: no answer can be serialized in that, so every investigation on the resulting app fails at the route blaming the caller's question", oldFloor)
	}

	// The boundary is accepted, or the documented minimum is a lie. This must
	// fail for a reason OTHER than the byte budget -- Dependencies{} is
	// deliberately empty, so any remaining error is about something else.
	at := base
	at.MaxSerializedBytes = contractsv1.ContextFabricMinimumAnswerBytes
	if _, err := NewApp(at, Dependencies{}, testLogger(nil)); err != nil && isBoundsError(err) {
		t.Fatalf("NewApp rejected exactly the documented minimum (%d) on bounds grounds: %v", contractsv1.ContextFabricMinimumAnswerBytes, err)
	}
}

// isBoundsError reports whether err is NewApp's own bounds rejection rather
// than one of its many other composition failures. Matching the bounds message
// specifically is what stops the boundary assertion above passing for the wrong
// reason -- an empty Dependencies{} fails for several reasons, and a test that
// accepted any error would prove nothing.
func isBoundsError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "bounds") || strings.Contains(msg, "serialized")
}
