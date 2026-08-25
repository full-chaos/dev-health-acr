package hosted

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// TestIsNilRuntime_DetectsTypedNilPointer is RED-FIRST evidence for a codex
// review finding (chaos4171pr2-codex-r1): a non-nil interface value
// wrapping a nil concrete pointer must be detected as nil, or the
// composition site would build a contextfabric.RuntimeOfferPhraser around
// a receiver whose first field read panics.
func TestIsNilRuntime_DetectsTypedNilPointer(t *testing.T) {
	t.Parallel()
	var typedNil *genkitruntime.Runtime
	if !isNilRuntime(typedNil) {
		t.Fatal("isNilRuntime(typed-nil *genkitruntime.Runtime) = false, want true")
	}
}

func TestIsNilRuntime_FalseForANonNilValue(t *testing.T) {
	t.Parallel()
	if isNilRuntime(&genkitruntime.Runtime{}) {
		t.Fatal("isNilRuntime(&genkitruntime.Runtime{}) = true, want false")
	}
}

// TestIsNilRuntime_FalseForANonPointerKind proves the switch's default
// branch: a struct or other non-nilable kind can never BE a typed nil, so
// it must never be misreported as one.
func TestIsNilRuntime_FalseForANonPointerKind(t *testing.T) {
	t.Parallel()
	if isNilRuntime(struct{}{}) {
		t.Fatal("isNilRuntime(struct{}{}) = true, want false")
	}
}
