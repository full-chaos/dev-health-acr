package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// failingModel fails the model call and composes the degraded answer from the
// wrapped fixture synthesizer, as RuntimeAnswerSynthesizer does with its own
// composer. modelCalls and composed count each path.
type failingModel struct {
	inner      AnswerSynthesizer
	modelCalls *int
	composed   *[]SynthesisInput
}

func (s failingModel) Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
	*s.modelCalls++
	return InvestigationResult{}, &SynthesisFailure{Class: SynthesisFailureCallNotCompleted, Attempts: 2, Elapsed: 31 * time.Second, cause: errors.New("model call cancelled")}
}

func (s failingModel) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, _ *SynthesisFailure) (InvestigationResult, error) {
	*s.composed = append(*s.composed, input)
	result, err := s.inner.Synthesize(ctx, principal, input)
	result.Status = InvestigationDegraded
	return result, err
}

// A degraded first answer that overran the item budget, with the cohort retry
// declined for want of a deadline reserve (the prod case), is served: the
// degraded composer runs again over the kept half of the cohort, no model
// call is made, and the answer says it lists fewer subjects than were found.
// Every field derived from the cohort describes the kept members.
func TestADegradedItemsOverrunIsServedAsAnAnswerOverTheCutCohort(t *testing.T) {
	const kind = SubjectProject
	declared := memberCutCallerCap
	detail := func(served int) CoverageDetail {
		reason := KindCensusTruncatedReason(kind, declared, served)
		d := CoverageDetail{
			DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
			Degrading: true, Kind: kind, Declared: &declared, Served: &served, Raw: reason,
		}
		d.Label = contractsv1.ComposeCoverageDetailLabel(d)
		return d
	}
	memberCutGraphDetails = []CoverageDetail{detail(memberCutCallerCap)}
	var modelCalls int
	var composed []SynthesisInput
	memberCutWrapSynthesizer = func(inner AnswerSynthesizer) AnswerSynthesizer {
		return failingModel{inner: inner, modelCalls: &modelCalls, composed: &composed}
	}
	t.Cleanup(func() { memberCutGraphDetails, memberCutWrapSynthesizer = nil, nil })

	telemetry := &recordingTelemetry{}
	result, err := investigateMemberCut(t, memberCutEngineAtCeiling(t, memberCutGaps, nil, 0, 0, memberCutCallerCap, telemetry))
	if err != nil {
		t.Fatalf("err = %v, want the cut answer served", err)
	}
	kept := memberCutCallerCap / 2
	if modelCalls != 1 || len(composed) != 2 {
		t.Fatalf("model calls = %d, degraded compositions = %d, want the one failed call and a composition for each of the two passes", modelCalls, len(composed))
	}
	if result.Cohort == nil || len(result.Cohort.Members) != kept {
		t.Fatalf("served members = %d, want %d", cohortSize(result.Cohort), kept)
	}
	if items := contractsv1.CountContextFabricResultItems(result).Budgeted(); items > 66 {
		t.Fatalf("served items = %d, want at most the 66-item plan ceiling", items)
	}
	if len(telemetry.memberCuts) != 1 {
		t.Fatalf("member-cut events = %d, want 1: %+v", len(telemetry.memberCuts), telemetry.memberCuts)
	}
	event := telemetry.memberCuts[0]
	if !event.Served || !event.Recomposed || event.Declined != MemberCutNotApplicable || event.Listed != memberCutCallerCap || event.Kept != kept ||
		event.Basis != contractsv1.ContextFabricNarrowingBasisAttentionRank {
		t.Fatalf("member-cut event = %+v, want served, recomposed, listed %d, kept %d, attention_rank", event, memberCutCallerCap, kept)
	}
	var step *contractsv1.ContextFabricPlanNarrowing
	for index := range result.AnswerPlan.Narrowing {
		if result.AnswerPlan.Narrowing[index].Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			step = &result.AnswerPlan.Narrowing[index]
		}
	}
	if step == nil || step.Basis != contractsv1.ContextFabricNarrowingBasisAttentionRank || step.Before != memberCutCallerCap || step.After != kept {
		t.Fatalf("narrowing = %+v, want an assembled_result step by attention_rank from %d to %d", result.AnswerPlan.Narrowing, memberCutCallerCap, kept)
	}
	if !slices.ContainsFunc(result.Limitations, func(l string) bool {
		return strings.Contains(l, fmt.Sprintf("%d of %d members are listed", kept, memberCutCallerCap))
	}) {
		t.Fatalf("limitations = %q, want the sentence stating %d of %d members are listed", result.Limitations, kept, memberCutCallerCap)
	}
	want := KindCensusTruncatedReason(kind, declared, kept)
	if len(result.Coverage.DegradedReasons) == 0 || !slices.Contains(result.Coverage.DegradedReasons, want) || slices.Contains(result.Coverage.DegradedReasons, KindCensusTruncatedReason(kind, declared, memberCutCallerCap)) {
		t.Fatalf("degraded reasons = %q, want the census reason over %d served members", result.Coverage.DegradedReasons, kept)
	}
	for _, d := range result.Coverage.Details {
		if d.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && (d.Served == nil || *d.Served != kept || d.Raw != want) {
			t.Fatalf("census detail = %+v, want served %d and raw %q", d, kept, want)
		}
	}
}

func cohortSize(cohort *Cohort) int {
	if cohort == nil {
		return 0
	}
	return len(cohort.Members)
}

// A member a finding cites is never cut, however the order ranks it
// (ruling 9324).
func TestACitedMemberSurvivesTheServedCut(t *testing.T) {
	var modelCalls int
	var composed []SynthesisInput
	memberCutWrapSynthesizer = func(inner AnswerSynthesizer) AnswerSynthesizer {
		return failingModel{inner: inner, modelCalls: &modelCalls, composed: &composed}
	}
	t.Cleanup(func() { memberCutWrapSynthesizer = nil })
	cited := []int{memberCutCallerCap - 1, memberCutCallerCap - 2, memberCutCallerCap - 3}
	telemetry := &recordingTelemetry{}
	result, err := investigateMemberCut(t, memberCutEngineAtCeiling(t, memberCutGaps, cited, 0, 0, memberCutCallerCap, telemetry))
	if err != nil {
		t.Fatalf("err = %v, want the cut answer served", err)
	}
	if len(composed) != 2 || len(telemetry.memberCuts) != 1 || !telemetry.memberCuts[0].Served {
		t.Fatalf("compositions = %d cuts = %+v, want a served cut", len(composed), telemetry.memberCuts)
	}
	first := composed[0].Graph.Cohort
	served := map[string]bool{}
	for _, member := range result.Cohort.Members {
		served[member.Subject.CanonicalID] = true
	}
	for _, index := range cited {
		if id := first.Members[index].Subject.CanonicalID; !served[id] {
			t.Fatalf("cited member %s was cut: served %v", id, served)
		}
	}
}
