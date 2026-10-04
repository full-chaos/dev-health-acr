package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// The reuse record names the validator rule that refused a stored candidate,
// "none" otherwise, and carries no caller text.
func TestWorkItemReuseRecordCarriesTheRejectReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reject bool
		want   string
	}{
		{"payload rejected", true, string(WorkItemRuleResultEvidenceOutsideMembers)},
		{"not rejected", false, WorkItemTupleRuleNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal, request, stored, _ := tupleReuseFixture(t)
			if tc.reject {
				stored.Result.EvidenceRefIDs = append(append([]string(nil), stored.Result.EvidenceRefIDs...), "foreign-secret-ref")
			}
			var output bytes.Buffer
			telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&output, nil)))
			engine := mustReuseTestEngine(t, EngineDependencies{Telemetry: telemetry, ReuseGate: tupleReuseGate{stored}})
			ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
			defer owner.Complete()
			_, _, _ = engine.tryReuseWorkItemTuple(ctx, principal, request, ResolvedGraphBinding{}, stored, WorkItemTupleClassification{Disposition: WorkItemTupleEligible})
			var line map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var l map[string]any
				if json.Unmarshal([]byte(raw), &l) == nil && l["msg"] == "context fabric work item reuse" {
					line = l
				}
			}
			if line == nil {
				t.Fatalf("no reuse record in %s", output.String())
			}
			if line["reject_reason"] != tc.want {
				t.Errorf("reject_reason = %v, want %q (record %v)", line["reject_reason"], tc.want, line)
			}
			if tc.reject && line["decision"] != "payload_rejected" {
				t.Errorf("decision = %v", line["decision"])
			}
			if strings.Contains(output.String(), "foreign-secret-ref") {
				t.Error("caller-derived text reached the record")
			}
		})
	}
}

// A value outside the closed vocabulary is reported as unrecognised, never verbatim.
func TestWorkItemReuseRejectReasonStaysInTheVocabulary(t *testing.T) {
	if got := workItemReuseRejectReason("free text\nfrom a caller"); got != continuationTelemetryUnrecognised {
		t.Fatalf("got %q", got)
	}
	if got := workItemReuseRejectReason(WorkItemTupleRuleUnclassified); got != WorkItemTupleRuleUnclassified {
		t.Fatalf("got %q", got)
	}
}
