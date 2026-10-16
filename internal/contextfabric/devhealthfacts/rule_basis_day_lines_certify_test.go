package devhealthfacts_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestRuleBasisDayLinesCertifyThroughTheDeficiencyProducer reads a fired
// compounding-risk rule row through the real provider, once with no day in the
// rationale and once with an impossible calendar day, and certifies the line
// each read writes against its declared event.
func TestRuleBasisDayLinesCertifyThroughTheDeficiencyProducer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rationale string
		event     eventspec.Event
		want      map[string]any
	}{
		{"no day named", ruleBasisRationale, eventspec.DeficiencyRuleBasisDayAbsent,
			map[string]any{"org_id": "org-1", "rule_id": "compounding-risk"}},
		{"impossible day", "Compounding Risk score is 0.739 (severity: high). Score as of 2026-02-31.", eventspec.DeficiencyRuleBasisDayInvalid,
			map[string]any{"org_id": "org-1", "rule_id": "compounding-risk", "day": "2026-02-31"}},
	} {
		client := &fakeClient{tables: []fakeTable{
			{match: "WHERE rn = 1 AND fired = 1", rows: [][]any{{
				"team-1", "compounding-risk", "v1", "warning", "Code risk is compounding where change pressure is highest.",
				tc.rationale, "criterion", "2026-10-03", "2026-10-15", "2026-10-15 06:00:00",
			}}},
		}}
		var buf bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		_, err := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactOperationalDeficiencies).ReadFacts(
			context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: contextfabric.FactOperationalDeficiencies, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
			})
		slog.SetDefault(previous)
		if err != nil {
			t.Fatalf("%s: ReadFacts: %v", tc.name, err)
		}
		var line []byte
		for _, candidate := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			if bytes.Contains(candidate, []byte(`"msg":"`+tc.event.Msg+`"`)) {
				line = candidate
			}
		}
		if line == nil {
			t.Fatalf("%s: no %s line was written: %s", tc.name, tc.event.Msg, buf.String())
		}
		log, err := certify.Parse(line)
		if err != nil {
			t.Fatalf("%s: certify.Parse: %v", tc.name, err)
		}
		if _, err := certify.Certify(log, certify.Assertion{Event: tc.event, Want: tc.want}); err != nil {
			t.Fatalf("%s: the line failed certification: %v\n%s", tc.name, err, line)
		}
	}
}
