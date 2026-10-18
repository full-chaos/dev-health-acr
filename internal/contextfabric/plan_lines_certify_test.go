package contextfabric_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestPlanNarrowingAndRetryLinesCertifyWhatTheEngineProduces(t *testing.T) {
	t.Parallel()
	events := contextfabric.PlanEventsFromEngineForTest(t)
	if len(events.Narrowings) < 6 || len(events.Retries) == 0 || len(events.Disclosures) == 0 {
		t.Fatalf("the engine scenarios produced %d plan narrowing, %d retry selection and %d disclosure events, want every kind", len(events.Narrowings), len(events.Retries), len(events.Disclosures))
	}
	stages := map[string]bool{}
	for _, event := range events.Narrowings {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordPlanNarrowing(ctx, principalOrg1, event)
		line := certifyRequestLine(t, buf.Bytes(), eventspec.PlanNarrowing, map[string]any{"org_id": "org_1"})
		stages[line["stage"].(string)] = true
	}
	for _, event := range events.Retries {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordSynthesisRetrySelection(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.SynthesisRetrySelection, map[string]any{"org_id": "org_1"})
	}
	for _, stage := range []string{"cardinality", "assembled_result"} {
		if !stages[stage] {
			t.Errorf("the engine scenarios never wrote a plan narrowing line at stage %q: %v", stage, keysOf(stages))
		}
	}
}

func TestPlanNarrowingLineCertifiesEveryValueOfEveryClosedKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key    string
		values []string
		set    func(*contextfabric.PlanNarrowingEvent, string)
	}{
		{"family", contextfabric.QuestionFamilyLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) { e.Family = contextfabric.QuestionFamily(v) }},
		{"family_version", contextfabric.QuestionFamilyTableVersionVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) { e.FamilyVersion = v }},
		{"stage", contextfabric.PlanNarrowingStageVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.Stage = contractsv1.ContextFabricPlanNarrowingStage(v)
		}},
		{"basis", contextfabric.NarrowingBasisLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.Basis = contractsv1.ContextFabricNarrowingBasis(v)
		}},
		{"overrun", contextfabric.BudgetOverrunLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.Overrun = contractsv1.ContextFabricBudgetOverrun(v)
		}},
		{"facts_only_declined", contextfabric.FactsOnlyDeclinedLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.FactsOnlyDeclined = contextfabric.FactsOnlyDeclined(v)
		}},
		{"retry_declined", contextfabric.RetryDeclinedReasonVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.RetryDeclined = contextfabric.RetryDeclinedReason(v)
		}},
		{"narrower_continuation_axis", contextfabric.NarrowingContinuationAxisLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.NarrowerContinuationAxis = contextfabric.NarrowingContinuationAxis(v)
		}},
		{"outcome_completeness_state", contextfabric.AnswerCompletenessStateLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.OutcomeCompletenessState = contractsv1.ContextFabricAnswerCompletenessState(v)
		}},
		{"outcome_reduction_declined", contextfabric.OutcomeReductionDeclinedLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.OutcomeReductionDeclined = contextfabric.OutcomeReductionDeclined(v)
		}},
		{"ledger_status", contextfabric.LedgerStatusLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.LedgerStatus = contractsv1.ContextFabricLedgerStatus(v)
		}},
		{"quota_availability", contextfabric.ItemQuotaAvailabilityLogVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) {
			e.QuotaAvailability = contextfabric.ItemQuotaAvailability(v)
		}},
		{"ceiling_fit", contextfabric.CeilingFitStepVocabulary(), func(e *contextfabric.PlanNarrowingEvent, v string) { e.CeilingFit = contextfabric.CeilingFitStep(v) }},
	}
	for _, tc := range cases {
		seen := map[string]bool{}
		for _, value := range tc.values {
			event := contextfabric.PlanNarrowingEvent{Family: contextfabric.QuestionFamilyUnclassified, FamilyVersion: contextfabric.QuestionFamilyTableVersion, Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Overrun: contractsv1.ContextFabricBudgetFits, LedgerStatus: contractsv1.ContextFabricLedgerReconciled, QuotaAvailability: contextfabric.ItemQuotaBounded}
			tc.set(&event, value)
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			sink.RecordPlanNarrowing(ctx, principalOrg1, event)
			line := certifyRequestLine(t, buf.Bytes(), eventspec.PlanNarrowing, nil)
			seen[line[tc.key].(string)] = true
		}
		sameMembers(t, tc.key, keysOf(seen), tc.values)
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordPlanNarrowing(ctx, principalOrg1, contextfabric.PlanNarrowingEvent{
		Family: contextfabric.QuestionFamilyUnclassified, FamilyVersion: contextfabric.QuestionFamilyTableVersion, Stage: contractsv1.ContextFabricPlanNarrowingCardinality,
		Overrun: "future_overrun", LedgerStatus: "future_status",
	})
	certifyRequestLine(t, buf.Bytes(), eventspec.PlanNarrowing, map[string]any{"overrun": "unclassified", "ledger_status": "unclassified", "quota_availability": "unclassified"})
	buf.Reset()
	sink.RecordPlanNarrowing(ctx, principalOrg1, contextfabric.PlanNarrowingEvent{Family: contextfabric.QuestionFamilyUnclassified, Stage: "future_stage", FamilyVersion: contextfabric.QuestionFamilyTableVersion})
	certifyRefused(t, buf.Bytes(), eventspec.PlanNarrowing, "an unknown stage")
	buf.Reset()
	sink.RecordSynthesisRetrySelection(ctx, principalOrg1, contextfabric.PlanNarrowingEvent{Family: "future_family", FamilyVersion: contextfabric.QuestionFamilyTableVersion, Stage: contractsv1.ContextFabricPlanNarrowingCardinality})
	certifyRefused(t, buf.Bytes(), eventspec.SynthesisRetrySelection, "an unknown family")
}

func TestCohortNarrowingDisclosureLineCertifiesWhatTheEngineProduces(t *testing.T) {
	t.Parallel()
	events := contextfabric.PlanEventsFromEngineForTest(t)
	outcomes := map[string]bool{"chain_unreconciled": true, "work_item_census": true} // written by the work item census and the unreconciled chain, not by these scenarios
	for _, event := range events.Disclosures {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordCohortNarrowingDisclosure(ctx, principalOrg1, event)
		line := certifyRequestLine(t, buf.Bytes(), eventspec.CohortNarrowingDisclosure, map[string]any{"org_id": "org_1", "declared": event.Declared, "served": event.Served})
		outcomes[line["outcome"].(string)] = true
	}
	for _, outcome := range contextfabric.CohortNarrowingDisclosureOutcomeVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordCohortNarrowingDisclosure(ctx, principalOrg1, contextfabric.CohortNarrowingDisclosureEvent{
			Family: contextfabric.QuestionFamilyUnclassified, Kind: contextfabric.SubjectProject, Outcome: contextfabric.CohortNarrowingDisclosureOutcome(outcome), Declared: 11, Served: 10, Steps: 1,
		})
		certifyRequestLine(t, buf.Bytes(), eventspec.CohortNarrowingDisclosure, map[string]any{"outcome": outcome})
	}
	sameMembers(t, "disclosure outcomes", keysOf(outcomes), contextfabric.CohortNarrowingDisclosureOutcomeVocabulary())
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordCohortNarrowingDisclosure(ctx, principalOrg1, contextfabric.CohortNarrowingDisclosureEvent{Family: contextfabric.QuestionFamilyUnclassified, Kind: "future_kind", Outcome: contextfabric.CohortNarrowingDisclosed})
	certifyRefused(t, buf.Bytes(), eventspec.CohortNarrowingDisclosure, "an unknown member kind")
}

func TestGroupedCohortCompletenessLineCertifiesEveryGroupingOutcomeTheEngineWrites(t *testing.T) {
	t.Parallel()
	refusals := map[string]bool{}
	for _, event := range contextfabric.GroupedCohortEventsForTest(t) {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordGroupedCohortCompleteness(ctx, principalOrg1, event)
		line := certifyRequestLine(t, buf.Bytes(), eventspec.GroupedCohortCompleteness, map[string]any{"org_id": "org_1"})
		refusal, _ := line["grouping_refusal"].(string)
		refusals[refusal] = true
		if _, refused := line["grouping_refusal"]; refused != (event.Refusal != contextfabric.CohortGroupingRefusalNone) {
			t.Errorf("grouping_refusal presence = %v for refusal %q", refused, event.Refusal)
		}
	}
	for _, want := range []string{"", "group_kind_source_mismatch", "no_member_placed"} {
		if !refusals[want] {
			t.Errorf("the grouped scenarios never wrote the grouping outcome %q: %v", want, keysOf(refusals))
		}
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordGroupedCohortCompleteness(ctx, principalOrg1, contextfabric.GroupedCohortCompletenessEvent{Family: contextfabric.QuestionFamilyGroupedCohortStatus, Refusal: "future_refusal", PlannedGroupKind: contextfabric.SubjectTeam})
	if !strings.Contains(buf.String(), `"grouping_refusal":"unclassified"`) {
		t.Fatalf("an unknown grouping refusal was not logged as the class: %s", buf.String())
	}
	certifyRequestLine(t, buf.Bytes(), eventspec.GroupedCohortCompleteness, map[string]any{"grouping_refusal": "unclassified"})
	buf.Reset()
	sink.RecordGroupedCohortCompleteness(ctx, principalOrg1, contextfabric.GroupedCohortCompletenessEvent{Family: "future_family"})
	certifyRefused(t, buf.Bytes(), eventspec.GroupedCohortCompleteness, "an unknown family")
}

func TestAtMostOneRetrySelectionDisclosureAndGroupedLinePerRequest(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	retry := contextfabric.PlanNarrowingEvent{Family: contextfabric.QuestionFamilyUnclassified, FamilyVersion: contextfabric.QuestionFamilyTableVersion, Stage: contractsv1.ContextFabricPlanNarrowingAssembledResult, Overrun: contractsv1.ContextFabricBudgetFits, LedgerStatus: contractsv1.ContextFabricLedgerReconciled, QuotaAvailability: contextfabric.ItemQuotaBounded}
	disclosure := contextfabric.CohortNarrowingDisclosureEvent{Family: contextfabric.QuestionFamilyUnclassified, Kind: contextfabric.SubjectProject, Outcome: contextfabric.CohortNarrowingDisclosed, Declared: 11, Served: 10, Steps: 1}
	grouped := contextfabric.GroupedCohortCompletenessEvent{Family: contextfabric.QuestionFamilyGroupedCohortStatus, GroupCount: 2}
	sink.RecordSynthesisRetrySelection(ctx, principalOrg1, retry)
	sink.RecordCohortNarrowingDisclosure(ctx, principalOrg1, disclosure)
	sink.RecordGroupedCohortCompleteness(ctx, principalOrg1, grouped)
	certifyRequestLine(t, buf.Bytes(), eventspec.SynthesisRetrySelection, nil)
	certifyRequestLine(t, buf.Bytes(), eventspec.CohortNarrowingDisclosure, nil)
	certifyRequestLine(t, buf.Bytes(), eventspec.GroupedCohortCompleteness, nil)
	sink.RecordSynthesisRetrySelection(ctx, principalOrg1, retry)
	sink.RecordCohortNarrowingDisclosure(ctx, principalOrg1, disclosure)
	sink.RecordGroupedCohortCompleteness(ctx, principalOrg1, grouped)
	certifyRefused(t, buf.Bytes(), eventspec.SynthesisRetrySelection, "a second retry selection line in one request")
	certifyRefused(t, buf.Bytes(), eventspec.CohortNarrowingDisclosure, "a second cohort narrowing disclosure line in one request")
	certifyRefused(t, buf.Bytes(), eventspec.GroupedCohortCompleteness, "a second grouped cohort completeness line in one request")
}
