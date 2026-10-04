package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemReuseRecordIsCertifiedWithItsRejectReason(t *testing.T) {
	for _, reason := range append(contextfabric.WorkItemTupleRejectReasonVocabulary(), "unrecognised") {
		t.Run(reason, func(t *testing.T) {
			var logBytes bytes.Buffer
			telemetry := contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&logBytes, nil)))
			telemetry.RecordWorkItemReuse(context.Background(), storage.Principal{OrgID: "org_8632"}, contextfabric.WorkItemReuseEvent{
				Decision:         "payload_rejected",
				SemanticRead:     contextfabric.SemanticStateReadAvailable,
				CensusRead:       "not_checked",
				RequestedTeamIDs: []string{},
				RejectReason:     reason,
			})
			parsed, err := certify.Parse(logBytes.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(parsed, certify.Assertion{
				Event: eventspec.WorkItemReuse,
				Want: map[string]any{
					"org_id":             "org_8632",
					"decision":           "payload_rejected",
					"reject_reason":      reason,
					"semantic_read":      "available",
					"census_read":        "not_checked",
					"requested_team_ids": []any{},
				},
			}); err != nil {
				t.Fatalf("certify: %v", err)
			}
		})
	}
}
