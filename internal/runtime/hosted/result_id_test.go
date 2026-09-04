package hosted

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestNewInvestigationResultID_IsUnguessable pins the property the
// chain-identity field rests on: a result id is becoming a BEARER
// REFERENCE (a caller who names a prior result id inherits that result's
// confirmed axes), so an id an attacker can PREDICT would be an in-org
// privilege-escalation primitive rather than merely an ugly identifier.
//
// WHY THIS IS A PIN AND NOT A RED-FIRST TEST, stated plainly because the
// distinction was the whole finding here. This function used to carry a
// clock-derived fallback, `result_fallback_<UnixNano>`, on the error branch
// of crypto/rand.Read. That fallback WAS guessable -- and it was also
// UNREACHABLE: since Go 1.24, crypto/rand.Read never returns a non-nil
// error. It calls fatal() and terminates the process (go.dev/issue/66821);
// the toolchain source ends that path with `panic("unreachable")`. go.mod
// requires go 1.27.0 and CI pins 1.27.0, so the branch was provably dead on
// every build that has ever run this code.
//
// So there was no live vulnerability to fix and there is no failing test to
// write for a branch that cannot execute -- attempting it crashes the test
// binary, which is itself the proof of unreachability. The fallback was
// deleted as dead code that misrepresented the contract (it implied a
// degraded-entropy mode exists), and what remains testable is the property
// itself, asserted here so a future edit cannot reintroduce a low-entropy
// id under a different spelling.
func TestNewInvestigationResultID_IsUnguessable(t *testing.T) {
	t.Parallel()

	const samples = 64
	seen := make(map[string]struct{}, samples)

	for i := 0; i < samples; i++ {
		id := newInvestigationResultID()

		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("id %q was generated twice in %d samples: the generator is not drawing fresh entropy", id, samples)
		}
		seen[id] = struct{}{}

		hexPart, ok := strings.CutPrefix(id, "result_")
		if !ok {
			t.Fatalf("id %q does not carry the result_ prefix", id)
		}
		// 16 bytes hex-encoded == 32 characters == 128 bits. This is the
		// number the bearer-reference model rests on, so it is asserted
		// rather than assumed.
		if len(hexPart) != 32 {
			t.Fatalf("id %q carries %d hex characters, want 32 (128 bits of entropy)", id, len(hexPart))
		}
		if _, err := hex.DecodeString(hexPart); err != nil {
			t.Fatalf("id %q is not hex after the prefix: %v", id, err)
		}
		for _, banned := range []string{"fallback", "time", "seq", "counter"} {
			if strings.Contains(strings.ToLower(id), banned) {
				t.Fatalf("id %q contains %q: no id may be synthesised from a predictable source", id, banned)
			}
		}
	}

	if len(seen) != samples {
		t.Fatalf("collected %d distinct ids from %d samples, want %d", len(seen), samples, samples)
	}
}
