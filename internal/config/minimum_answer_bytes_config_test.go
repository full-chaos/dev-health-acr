package config

import (
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RED B, surface 1 of 2: a deployment configured below the minimum must FAIL TO
// START.
//
// The governing promise says a configuration below the minimum "fails to
// start", and the reason it must be startup rather than request time is that a
// budget too small to hold any answer is MISCONFIGURATION, not a caller's
// mistake. Refusing at the boundary is what makes the request-time refusal
// unreachable for a correctly deployed service.
//
// RED on origin/main: 8192 is accepted, and every investigation on that
// deployment then fails at the route with a caller-facing error blaming the
// question.
func TestConfigBelowTheMinimumAnswerSizeFailsToStart(t *testing.T) {
	t.Parallel()

	// The OLD floor, which was the contract's own minimum until this change
	// and is provably too small to serialize any answer.
	// The probe value is one byte below the static floor, not the old 8192.
	// 8192 is now LEGAL against this constant, and that is not an oversight:
	// the static floor is the request-INDEPENDENT one, and a service at 8192
	// can serve a small question perfectly well. What 8192 cannot serve is a
	// LARGE question -- and catching that is the per-request runtime check's
	// job, not this one's. Testing the boundary directly is also the stronger
	// assertion: an off-by-one in the guard fails here and would not fail
	// against a value four times away from it.
	const belowFloor = contractsv1.ContextFabricMinimumAnswerBytes - 1

	// Driven through load() with the real environment variable, so this
	// exercises the actual startup path rather than a hand-built struct.
	_, err := load(mapLookup(map[string]string{"ACR_MAX_SERIALIZED_BYTES": strconv.Itoa(belowFloor)}))
	if err == nil {
		t.Fatalf("a deployment configured at %d bytes started successfully, one below the floor of %d: not even the irreducible answer fits, so every investigation on it fails",
			belowFloor, contractsv1.ContextFabricMinimumAnswerBytes)
	}
	// The refusal must NAME the bound and the minimum. An operator who has
	// just been refused a boot needs the number to set, not "invalid config".
	msg := err.Error()
	// Derived from the constant, never restated: a hardcoded number here
	// would have to be edited every time the measurement moves, and the
	// edit is exactly the moment someone stops checking whether the message
	// still names the right value.
	for _, want := range []string{"ACR_MAX_SERIALIZED_BYTES", strconv.Itoa(contractsv1.ContextFabricMinimumAnswerBytes)} {
		if !strings.Contains(msg, want) {
			t.Errorf("startup refusal %q does not mention %q: an operator cannot act on it", msg, want)
		}
	}
}

// The boundary itself: exactly the minimum must be ACCEPTED. A guard that
// refuses its own documented minimum is an off-by-one that would make the
// documented value a lie.
func TestConfigExactlyAtTheMinimumAnswerSizeStarts(t *testing.T) {
	t.Parallel()
	if _, err := load(mapLookup(map[string]string{"ACR_MAX_SERIALIZED_BYTES": strconv.Itoa(contractsv1.ContextFabricMinimumAnswerBytes)})); err != nil {
		t.Fatalf("a deployment configured at exactly the documented minimum (%d) was refused: %v", contractsv1.ContextFabricMinimumAnswerBytes, err)
	}
}
