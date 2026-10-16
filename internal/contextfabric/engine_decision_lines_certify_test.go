package contextfabric_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	renderShapeSummaryLegacyKeys = []string{
		"time", "level", "msg", "org_id", "question_shape", "render_shapes_selected", "render_shape_rules_skipped",
		"render_shape_members_truncated", "render_shape_trends_omitted", "render_shape_accounting", "selected_shapes", "request_id",
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

func certifyRefused(t *testing.T, raw []byte, event eventspec.Event, what string, extra ...map[string]any) {
	t.Helper()
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	want := map[string]any{"request_id": factScopeCertifyRequestID}
	for _, more := range extra {
		for k, v := range more {
			want[k] = v
		}
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: event, Want: want}); err == nil {
		t.Errorf("%s was certified, want it refused: %s", what, raw)
	}
}

// TestRenderShapeAndStatusLinesCertifyEveryProducerValueAndRefuseAnyOther
// drives every value of the closed enums the engine lines carry, each read from
// the producer's own vocabulary, through the real sink, and requires a value
// outside the vocabulary to be refused.
func TestRenderShapeAndStatusLinesCertifyEveryProducerValueAndRefuseAnyOther(t *testing.T) {
	t.Parallel()
	rules := contextfabric.RenderShapeRuleLineVocabulary()
	reason := contextfabric.RenderShapeSkipReason(contextfabric.RenderShapeSkipReasonLineVocabulary()[0])
	record := func(shape, rule string) []byte {
		var buf bytes.Buffer
		telemetry, ctx := cohortTelemetry(&buf)
		telemetry.RecordRenderShapeSelection(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.RenderShapeSelectionEvent{
			Shape:   contractsv1.ContextFabricInvestigationShape(shape),
			Skipped: []contextfabric.RenderShapeSkip{{Rule: contractsv1.ContextFabricRenderShapeRule(rule), Reason: reason}},
		})
		return buf.Bytes()
	}
	for _, shape := range contractsv1.ContextFabricInvestigationShapeVocabulary() {
		for _, rule := range rules {
			raw := record(string(shape), rule)
			certifyRequestLine(t, raw, eventspec.RenderShapeSelectionSummary, map[string]any{"question_shape": string(shape)})
			certifyRequestLine(t, raw, eventspec.RenderShapeRuleSkipped, map[string]any{"question_shape": string(shape), "render_shape_rule": rule})
		}
	}
	certifyRefused(t, record("future_shape", rules[0]), eventspec.RenderShapeSelectionSummary, "an unknown question shape on the summary line")
	certifyRefused(t, record(string(contractsv1.ContextFabricShapeOpen), "future_rule"), eventspec.RenderShapeRuleSkipped, "an unknown render shape rule", map[string]any{"render_shape_rule": "future_rule"})

	statuses := []string{"complete", "partial", "degraded", "clarification_required", "no_match"}
	shadow := func(model, server string, basis contextfabric.ServerStatusBasis) []byte {
		var buf bytes.Buffer
		telemetry, ctx := cohortTelemetry(&buf)
		telemetry.RecordServerStatusShadow(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.ServerStatusShadow{
			ModelStatus: contextfabric.InvestigationStatus(model), ServerStatus: contextfabric.InvestigationStatus(server), Basis: basis, Version: "shadow-v1",
		})
		return buf.Bytes()
	}
	for _, status := range statuses {
		for _, basis := range contextfabric.ServerStatusBasisVocabulary() {
			certifyRequestLine(t, shadow(status, status, basis), eventspec.ServerStatusShadowLine, map[string]any{"model_status": status, "server_status": status, "basis": string(basis)})
		}
	}
	certifyRequestLine(t, shadow("complete", "", contextfabric.ServerStatusBasisNoPlan), eventspec.ServerStatusShadowLine, map[string]any{"server_status": ""})
	certifyRefused(t, shadow("future_status", "complete", contextfabric.ServerStatusBasisServed), eventspec.ServerStatusShadowLine, "an unknown model status")
	certifyRefused(t, shadow("complete", "future_status", contextfabric.ServerStatusBasisServed), eventspec.ServerStatusShadowLine, "an unknown server status")
	certifyRefused(t, shadow("complete", "complete", contextfabric.ServerStatusBasis("future_basis")), eventspec.ServerStatusShadowLine, "an unknown basis")
}

func selectedShapesRecord(selections []contextfabric.RenderShapeSelection) []byte {
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordRenderShapeSelection(ctx, storage.Principal{OrgID: "org_1"}, contextfabric.RenderShapeSelectionEvent{
		Shape: contractsv1.ContextFabricShapeOpen, Selected: selections,
	})
	return buf.Bytes()
}

// TestRenderShapeSelectionIsOneRecordWithTheSelectedShapesAsABoundedList pins
// the one-record decision: the shapes ride on the summary line as a list whose
// values are asserted (not merely present), every kind, presentation and rule
// the producers define certifies, the list is bounded by the producer's own
// shape budget, and a value outside any vocabulary is refused.
func TestRenderShapeSelectionIsOneRecordWithTheSelectedShapesAsABoundedList(t *testing.T) {
	t.Parallel()
	rules := contextfabric.RenderShapeRuleLineVocabulary()
	asWant := func(selections []contextfabric.RenderShapeSelection) []any {
		out := make([]any, 0, len(selections))
		for _, s := range selections {
			out = append(out, map[string]any{
				"render_shape_kind": string(s.Kind), "render_shape_presentation": string(s.Presentation), "render_shape_rule": string(s.Rule),
				"render_shape_series": s.SeriesCount, "render_shape_points": s.PointCount,
			})
		}
		return out
	}
	// Every kind crossed with every presentation, spread over the rules, in chunks within the budget.
	var all []contextfabric.RenderShapeSelection
	for i, kind := range contractsv1.ContextFabricRenderKindVocabulary() {
		for j, presentation := range contractsv1.ContextFabricRenderPresentationVocabulary() {
			all = append(all, contextfabric.RenderShapeSelection{
				Kind: kind, Presentation: presentation, Rule: contractsv1.ContextFabricRenderShapeRule(rules[(i+j)%len(rules)]),
				SeriesCount: 1 + i, PointCount: 10 + j,
			})
		}
	}
	for start := 0; start < len(all); start += contractsv1.ContextFabricRenderShapesMaxCount {
		end := start + contractsv1.ContextFabricRenderShapesMaxCount
		if end > len(all) {
			end = len(all)
		}
		chunk := all[start:end]
		raw := selectedShapesRecord(chunk)
		if lines := bytes.Count(raw, []byte("\n")); lines != 1 {
			t.Fatalf("the selector's decision wrote %d lines, want exactly one record: %s", lines, raw)
		}
		certifyRequestLine(t, raw, eventspec.RenderShapeSelectionSummary, map[string]any{
			"org_id": "org_1", "render_shapes_selected": len(chunk), "render_shape_rules_skipped": 0, "selected_shapes": asWant(chunk),
		})
	}
	// The measured zero is an empty list, never null and never absent.
	empty := selectedShapesRecord(nil)
	line := certifyRequestLine(t, empty, eventspec.RenderShapeSelectionSummary, map[string]any{"render_shapes_selected": 0, "selected_shapes": []any{}})
	if list, ok := line["selected_shapes"].([]any); !ok || len(list) != 0 {
		t.Fatalf("an empty selection must write selected_shapes as an empty list, got %#v", line["selected_shapes"])
	}
	// Over the producer's own budget is refused.
	over := make([]contextfabric.RenderShapeSelection, contractsv1.ContextFabricRenderShapesMaxCount+1)
	for i := range over {
		over[i] = all[0]
	}
	certifyRefused(t, selectedShapesRecord(over), eventspec.RenderShapeSelectionSummary, "a list longer than the shape budget")
	// A value outside a vocabulary is refused.
	bad := all[0]
	bad.Kind = contractsv1.ContextFabricRenderKind("future_kind")
	certifyRefused(t, selectedShapesRecord([]contextfabric.RenderShapeSelection{bad}), eventspec.RenderShapeSelectionSummary, "an unknown render kind")
	bad = all[0]
	bad.Presentation = contractsv1.ContextFabricRenderPresentation("future_presentation")
	certifyRefused(t, selectedShapesRecord([]contextfabric.RenderShapeSelection{bad}), eventspec.RenderShapeSelectionSummary, "an unknown presentation")
	bad = all[0]
	bad.Rule = contractsv1.ContextFabricRenderShapeRule("future_rule")
	certifyRefused(t, selectedShapesRecord([]contextfabric.RenderShapeSelection{bad}), eventspec.RenderShapeSelectionSummary, "an unknown rule")
}

// TestAnUndeclaredMemberInASelectedShapeRowIsRefused pins the object_slice
// row guard: a key the declaration does not list inside a list row fails, the
// same as an undeclared member of a single object.
func TestAnUndeclaredMemberInASelectedShapeRowIsRefused(t *testing.T) {
	t.Parallel()
	raw := selectedShapesRecord([]contextfabric.RenderShapeSelection{{
		Kind: contractsv1.ContextFabricRenderKindSeries, Presentation: contractsv1.ContextFabricRenderPresentationBars,
		Rule: contractsv1.ContextFabricRenderRuleCohortAttentionScore, SeriesCount: 1, PointCount: 2,
	}})
	certifyRequestLine(t, raw, eventspec.RenderShapeSelectionSummary, nil)
	mutated := bytes.Replace(raw, []byte(`"render_shape_points":2`), []byte(`"render_shape_points":2,"surprise_member":"x"`), 1)
	if bytes.Equal(raw, mutated) {
		t.Fatalf("the fixture did not contain the member to extend: %s", raw)
	}
	certifyRefused(t, mutated, eventspec.RenderShapeSelectionSummary, "a list row carrying an undeclared member")
}
