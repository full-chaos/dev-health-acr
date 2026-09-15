package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestSynthesisRetrySelectionUsesConfiguredInfoLoggerAndDeclaredMeasurement(t *testing.T) {
	var configured bytes.Buffer
	var defaultBuffer bytes.Buffer
	previousDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&defaultBuffer, nil)))
	defer slog.SetDefault(previousDefault)

	requestID := "req_57550000000000000000000000000000"
	ctx := observability.WithRequestID(context.Background(), requestID)
	event := contextfabric.PlanNarrowingEvent{
		Family:                   contextfabric.QuestionFamilySubjectInvestigation,
		FamilyVersion:            contextfabric.QuestionFamilyTableVersion,
		Stage:                    contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Basis:                    contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
		BasisObserved:            true,
		Before:                   24,
		After:                    8,
		Groups:                   false,
		Overrun:                  contractsv1.ContextFabricBudgetOverrunItems,
		MeasuredItems:            24,
		MeasuredBytes:            4096,
		MaxItems:                 12,
		MaxSerializedBytes:       8192,
		PredictedItems:           20,
		Attribution:              contractsv1.ContextFabricItemAttribution{Global: 4, Member: 12, Group: 6, MultiGroup: 2},
		RetryAttempted:           false,
		RetryFit:                 false,
		RetryFailed:              false,
		RefusalPlanned:           false,
		DeadlineReserved:         true,
		LedgerStatus:             contractsv1.ContextFabricLedgerReconciled,
		QuotaAvailability:        contextfabric.ItemQuotaBounded,
		QuotaGroupAllowance:      4,
		QuotaGroupsGranted:       3,
		QuotaGroupsMeasured:      3,
		QuotaGroupsOverAllowance: 1,
	}

	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&configured, nil))).
		RecordSynthesisRetrySelection(ctx, storage.Principal{OrgID: "org_5755"}, event)
	if configured.Len() == 0 {
		t.Fatal("configured logger received no retry-selection line")
	}
	if defaultBuffer.Len() != 0 {
		t.Fatalf("retry-selection line went to slog.Default instead of the configured logger: %s", defaultBuffer.Bytes())
	}

	parsed, err := certify.Parse(configured.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on configured slog.JSONHandler output: %v", err)
	}
	want := map[string]any{
		"request_id":                  requestID,
		"org_id":                      "org_5755",
		"family":                      string(contextfabric.QuestionFamilySubjectInvestigation),
		"family_version":              contextfabric.QuestionFamilyTableVersion,
		"stage":                       "assembled_result",
		"basis":                       "canonical_id_lexical",
		"basis_observed":              true,
		"before":                      24,
		"after":                       8,
		"groups":                      false,
		"overrun":                     "items",
		"measured_items":              24,
		"measured_bytes":              4096,
		"max_items":                   12,
		"max_serialized_bytes":        8192,
		"predicted_items":             20,
		"attribution_global":          4,
		"attribution_member":          12,
		"attribution_group":           6,
		"attribution_multi_group":     2,
		"retry_attempted":             false,
		"retry_fit":                   false,
		"retry_failed":                false,
		"deadline_reserved":           true,
		"ledger_status":               "reconciled",
		"quota_availability":          "bounded",
		"quota_group_allowance":       4,
		"quota_groups_granted":        3,
		"quota_groups_measured":       3,
		"quota_groups_over_allowance": 1,
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.SynthesisRetrySelection, Want: want}); err != nil {
		t.Fatalf("certify retry-selection Info line: %v", err)
	}
	if eventspec.SynthesisRetrySelection.Msg != contextfabric.SynthesisRetrySelectionLogMessage {
		t.Fatalf("declared retry-selection message %q differs from the production constant %q", eventspec.SynthesisRetrySelection.Msg, contextfabric.SynthesisRetrySelectionLogMessage)
	}

	lines := parsed.LinesWithMsg(eventspec.SynthesisRetrySelection.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d retry-selection lines, want one", len(lines))
	}
	declared := make(map[string]bool, len(eventspec.SynthesisRetrySelection.Fields))
	for _, field := range eventspec.SynthesisRetrySelection.Fields {
		declared[field.Key] = true
	}
	for key := range lines[0] {
		switch key {
		case "time", "level", "msg":
			continue
		}
		if !declared[key] {
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.SynthesisRetrySelection.ID)
		}
	}
}

func TestSynthesisRetrySelectionOmitsOptionalRequestIDWithoutRequestContext(t *testing.T) {
	var output bytes.Buffer
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&output, nil))).
		RecordSynthesisRetrySelection(context.Background(), storage.Principal{OrgID: "org_5755"}, contextfabric.PlanNarrowingEvent{})

	parsed, err := certify.Parse(output.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on request-id-less configured output: %v", err)
	}
	lines := parsed.LinesWithMsg(eventspec.SynthesisRetrySelection.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d retry-selection lines, want one", len(lines))
	}
	if _, present := lines[0]["request_id"]; present {
		t.Fatalf("request_id is present without a request context: %v", lines[0]["request_id"])
	}
	for _, field := range eventspec.SynthesisRetrySelection.Fields {
		if field.Key == "request_id" && field.Presence != eventspec.PresenceConditional {
			t.Fatalf("request_id presence = %s, want conditional to match requestIDLogAttrs", field.Presence)
		}
	}
}
