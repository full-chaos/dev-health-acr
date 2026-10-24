package eventspec

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
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

func TestAuthorizationReasonVocabularyIsTheClosedValueSetOfTheLogKey(t *testing.T) {
	t.Parallel()
	shape := regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	for _, v := range contextfabric.StoredResultAuthorizationReasonVocabulary() {
		if !shape.MatchString(string(v)) {
			t.Errorf("%q does not match the closed value code shape", v)
		}
	}
	for _, v := range contextfabric.StoredResultAuthorizationReasonVocabulary() {
		var buf bytes.Buffer
		slog.New(telemetryscrub.Handler(slog.NewJSONHandler(&buf, nil))).Info("m", "authorization_reason", string(v))
		var out map[string]any
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["authorization_reason"] != string(v) {
			t.Errorf("%q logs as %v", v, out["authorization_reason"])
		}
	}
	for _, v := range []string{"", "not_a_reason", "Bearer abcdefgh1", "pq: password authentication failed"} {
		var buf bytes.Buffer
		slog.New(telemetryscrub.Handler(slog.NewJSONHandler(&buf, nil))).Info("m", "authorization_reason", v)
		var out map[string]any
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["authorization_reason"] != telemetryscrub.Redacted {
			t.Errorf("%q logs as %v", v, out["authorization_reason"])
		}
	}
}
