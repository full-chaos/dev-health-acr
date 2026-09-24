package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The lever's decision graph must be rebuildable from its own lines: one
// fact-row truncation line per application (axis, bytes before/after, rows
// before/after), and on the refusal arm the existing refusal event, with the
// retry decline named.

func TestCHAOS6558ServedLeverEmitsOneTruncationLineAndOneDecision(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558ProdShape)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.factRowTruncations) != 1 {
		t.Fatalf("fact row truncation lines = %d, want exactly 1", len(telemetry.factRowTruncations))
	}
	line := telemetry.factRowTruncations[0]
	declared := chaos6558Facts * chaos6558RowsPerFact
	if !line.Served || line.Declined != FactRowTruncationNotApplicable || line.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("line = %+v, want served on the bytes axis", line)
	}
	if line.BytesBefore <= chaos6558MaxBytes || line.BytesAfter > chaos6558MaxBytes || line.MaxSerializedBytes != chaos6558MaxBytes {
		t.Fatalf("bytes before/after = %d/%d against %d, want an overrun cut to fit", line.BytesBefore, line.BytesAfter, line.MaxSerializedBytes)
	}
	if line.RowsBefore != declared || line.RowsAfter >= declared || line.PerTable < 1 || line.PerTable >= chaos6558RowsPerFact {
		t.Fatalf("rows before/after = %d/%d cap %d, want %d cut to a cap in [1,%d)", line.RowsBefore, line.RowsAfter, line.PerTable, declared, chaos6558RowsPerFact)
	}
	if line.RowsAfter != line.PerTable*chaos6558Facts || line.TablesTruncated != chaos6558Facts || !line.RowsDominate {
		t.Fatalf("line = %+v, want every table at the cap and rows dominating", line)
	}
	// The served document is the one the line describes.
	measured, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Bytes > line.MaxSerializedBytes {
		t.Fatalf("served %d bytes over the %d ceiling", measured.Bytes, line.MaxSerializedBytes)
	}
	// ONE assembled_result decision, no refusal planned, no retry.
	decisions := 0
	for _, event := range telemetry.planNarrowings {
		if event.Stage != contractsv1.ContextFabricPlanNarrowingAssembledResult {
			continue
		}
		decisions++
		if event.RefusalPlanned || event.RetryAttempted || event.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
			t.Fatalf("decision event = %+v, want the measured bytes overrun, served without retry", event)
		}
		if event.OutcomeCompletenessState != contractsv1.ContextFabricAnswerCompletenessPartial {
			t.Fatalf("decision completeness = %q, want partial", event.OutcomeCompletenessState)
		}
	}
	if decisions != 1 {
		t.Fatalf("assembled_result decisions = %d, want 1", decisions)
	}
	disclosed := false
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricFactRowTruncationLimitation(limitation) {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("no recognised fact-row truncation limitation in %q", result.Limitations)
	}
}

func TestCHAOS6558RefuseFastNamesTheDeclineAndKeepsTheRefusalEvent(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558Shape{rowPadding: 2000, maxBytes: 40000})
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request()); err == nil {
		t.Fatal("served an answer that cannot fit")
	}
	if len(telemetry.factRowTruncations) != 1 {
		t.Fatalf("fact row truncation lines = %d, want 1", len(telemetry.factRowTruncations))
	}
	line := telemetry.factRowTruncations[0]
	if line.Served || line.Declined != FactRowTruncationInsufficient || line.PerTable != 1 || !line.RowsDominate {
		t.Fatalf("line = %+v, want insufficient at one row per table with rows dominating", line)
	}
	if line.BytesAfter <= line.MaxSerializedBytes || line.BytesAfter >= line.BytesBefore {
		t.Fatalf("bytes after = %d, want the one-row document: below %d and still over %d", line.BytesAfter, line.BytesBefore, line.MaxSerializedBytes)
	}
	refusals := 0
	for _, event := range telemetry.planNarrowings {
		if !event.RefusalPlanned {
			continue
		}
		refusals++
		if event.RetryDeclined != RetryDeclinedCannotReduceAxis || event.RetryAttempted {
			t.Fatalf("refusal event = %+v, want retry declined cannot_reduce_axis", event)
		}
		if event.NarrowerContinuationAxis != NarrowingContinuationEvidenceWindow {
			t.Fatalf("refusal advice = %q, want evidence_window", event.NarrowerContinuationAxis)
		}
		if event.MeasuredBytes != line.BytesBefore {
			t.Fatalf("refusal measured %d bytes, want the assembled document's %d", event.MeasuredBytes, line.BytesBefore)
		}
	}
	if refusals != 1 {
		t.Fatalf("refusal events = %d, want 1", refusals)
	}
}

// Items overrun: the lever never applies and emits no line.
func TestCHAOS6558LeverIsSilentOffTheBytesAxis(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(8), 4, budgetStageOptions(12, 0), &calls, telemetry)
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if len(telemetry.factRowTruncations) != 0 {
		t.Fatalf("fact row truncation lines = %+v on an items overrun, want none", telemetry.factRowTruncations)
	}
}
