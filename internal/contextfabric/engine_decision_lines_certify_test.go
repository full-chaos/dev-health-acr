package contextfabric_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	renderShapeSummaryLegacyKeys = []string{
		"time", "level", "msg", "org_id", "question_shape", "render_shapes_selected", "render_shape_rules_skipped",
		"render_shape_members_truncated", "render_shape_trends_omitted", "render_shape_accounting", "request_id",
	}
	renderShapeSkippedLegacyKeys = []string{
		"time", "level", "msg", "org_id", "question_shape", "render_shape_rule", "render_shape_skip_reason", "request_id",
	}
	ensembleLegacyKeys = []string{
		"time", "level", "msg", "org_id", "requested", "primary_succeeded", "fallback_served", "failed", "quorum_met", "request_id",
	}
	serverStatusShadowLegacyKeys = []string{
		"time", "level", "msg", "org_id", "model_status", "server_status", "basis", "derived", "disagreed", "version", "request_id",
	}
)

func pinKeys(t *testing.T, buf *bytes.Buffer, want []string, msgLines int) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != msgLines {
		t.Fatalf("got %d lines, want %d: %s", len(lines), msgLines, buf.String())
	}
	if got := jsonKeys(t, lines[len(lines)-1]); !reflect.DeepEqual(got, want) {
		t.Fatalf("key sequence = %v, want %v", got, want)
	}
}

func TestRenderShapeSummaryAndSkippedLinesCertify(t *testing.T) {
	t.Parallel()
	rules := []contractsv1.ContextFabricRenderShapeRule{
		contractsv1.ContextFabricRenderRuleCohortAttentionScore, contractsv1.ContextFabricRenderRuleCohortDriverContribution,
		contractsv1.ContextFabricRenderRuleDatedFactTrend,
	}
	reasons := contextfabric.RenderShapeSkipReasonLineVocabulary()
	ok := contextfabric.RenderShapeSelectionEvent{Shape: contractsv1.ContextFabricShapeOpen, MembersTruncated: 2, TrendsOmitted: 3}
	for i, rule := range rules {
		ok.Skipped = append(ok.Skipped, contextfabric.RenderShapeSkip{Rule: rule, Reason: contextfabric.RenderShapeSkipReason(reasons[i])})
	}
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordRenderShapeSelection(ctx, storage.Principal{OrgID: "org_1"}, ok)
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("a selection of three skipped rules writes the summary and three skip lines, got %d: %s", len(lines), buf.String())
	}
	if got := jsonKeys(t, lines[0]); !reflect.DeepEqual(got, renderShapeSummaryLegacyKeys) {
		t.Fatalf("summary key sequence = %v, want %v", got, renderShapeSummaryLegacyKeys)
	}
	if got := jsonKeys(t, lines[1]); !reflect.DeepEqual(got, renderShapeSkippedLegacyKeys) {
		t.Fatalf("skipped key sequence = %v, want %v", got, renderShapeSkippedLegacyKeys)
	}
	certifyRequestLine(t, buf.Bytes(), eventspec.RenderShapeSelectionSummary, map[string]any{
		"org_id": "org_1", "question_shape": string(contractsv1.ContextFabricShapeOpen), "render_shapes_selected": 0,
		"render_shape_rules_skipped": 3, "render_shape_members_truncated": 2, "render_shape_trends_omitted": 3, "render_shape_accounting": "ok",
	})
	for i, rule := range rules {
		certifyRequestLine(t, buf.Bytes(), eventspec.RenderShapeRuleSkipped, map[string]any{
			"org_id": "org_1", "question_shape": string(contractsv1.ContextFabricShapeOpen), "render_shape_rule": string(rule),
			"render_shape_skip_reason": reasons[i],
		})
	}
	// Every closed skip reason certifies, so a reason added to the producer's
	// constants but not to the published set is caught where it is next driven.
	for _, reason := range reasons {
		var one bytes.Buffer
		telemetry, ctx := cohortTelemetry(&one)
		telemetry.RecordRenderShapeSelection(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.RenderShapeSelectionEvent{
			Shape:   contractsv1.ContextFabricShapeOpen,
			Skipped: []contextfabric.RenderShapeSkip{{Rule: rules[0], Reason: contextfabric.RenderShapeSkipReason(reason)}},
		})
		certifyRequestLine(t, one.Bytes(), eventspec.RenderShapeRuleSkipped, map[string]any{
			"org_id": "org_1", "question_shape": string(contractsv1.ContextFabricShapeOpen), "render_shape_rule": string(rules[0]),
			"render_shape_skip_reason": reason,
		})
	}

	var violated bytes.Buffer
	telemetry, ctx = cohortTelemetry(&violated)
	telemetry.RecordRenderShapeSelection(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.RenderShapeSelectionEvent{Shape: contractsv1.ContextFabricShapeOpen})
	line := certifyRequestLine(t, violated.Bytes(), eventspec.RenderShapeSelectionSummary, map[string]any{"render_shape_accounting": "violated"})
	if line["render_shapes_selected"] != float64(0) {
		t.Fatalf("an empty selection must write a measured zero, got %v", line["render_shapes_selected"])
	}
}

func TestInterpretationEnsembleLinesCertifyAtTheirLevels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		met    bool
		event  eventspec.Event
		failed int
	}{
		{"quorum met is Info", true, eventspec.InterpretationEnsembleResolved, 0},
		{"quorum missed is Warn", false, eventspec.InterpretationEnsembleGap, 2},
	} {
		var buf bytes.Buffer
		telemetry, ctx := cohortTelemetry(&buf)
		telemetry.RecordInterpretationEnsemble(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.InterpretationEnsembleEvent{
			Requested: 3, PrimarySucceeded: 1, FallbackServed: 4, Failed: tc.failed, QuorumMet: tc.met,
		})
		pinKeys(t, &buf, ensembleLegacyKeys, 1)
		certifyRequestLine(t, buf.Bytes(), tc.event, map[string]any{
			"org_id": "org_1", "requested": 3, "primary_succeeded": 1, "fallback_served": 4, "failed": tc.failed, "quorum_met": tc.met,
		})
	}
}

func TestServerStatusShadowLineCertifies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordServerStatusShadow(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.ServerStatusShadow{
		ModelStatus: contextfabric.InvestigationStatus("complete"), ServerStatus: contextfabric.InvestigationStatus("partial"),
		Basis: contextfabric.ServerStatusBasisCoverageDegraded, Derived: true, Disagreed: true, Version: "shadow-v1",
	})
	pinKeys(t, &buf, serverStatusShadowLegacyKeys, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.ServerStatusShadowLine, map[string]any{
		"org_id": "org_1", "model_status": "complete", "server_status": "partial",
		"basis": string(contextfabric.ServerStatusBasisCoverageDegraded), "derived": true, "disagreed": true, "version": "shadow-v1",
	})
}
