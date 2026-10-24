package eventspec

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

func TestProtectedLookingEventKeysAreTypedBenign(t *testing.T) {
	t.Parallel()
	for _, event := range All {
		for _, f := range event.Fields {
			if protected, _ := telemetryscrub.KeyClass(f.Key); !protected {
				continue
			}
			if got := telemetryscrub.BenignKind(f.Key); got == "" || got != string(f.Type) {
				t.Errorf("%s field %q (%s) is redacted in logs: add it to the scrubber typed benign keys with kind %q, or rename it", event.ID, f.Key, f.Type, f.Type)
			}
		}
	}
}

func TestAuthorizationReasonVocabularyScrubsClean(t *testing.T) {
	t.Parallel()
	for _, v := range contextfabric.StoredResultAuthorizationReasonVocabulary() {
		if got := telemetryscrub.ScrubText(string(v)); got != string(v) {
			t.Errorf("%q scrubs to %q", v, got)
		}
	}
}
