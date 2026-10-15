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
	// after, when set, rewrites each composed answer (index from zero).
	after func(index int, composed []SynthesisInput, result *InvestigationResult)
}

func (s failingModel) Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
	*s.modelCalls++
	return InvestigationResult{}, &SynthesisFailure{Class: SynthesisFailureCallNotCompleted, Attempts: 2, Elapsed: 31 * time.Second, cause: errors.New("model call cancelled")}
}

func (s failingModel) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, _ *SynthesisFailure) (InvestigationResult, error) {
	*s.composed = append(*s.composed, input)
	result, err := s.inner.Synthesize(ctx, principal, input)
	result.Status = InvestigationDegraded
	if s.after != nil {
		s.after(len(*s.composed)-1, *s.composed, &result)
	}
	return result, err
}

type servedCut struct {
	result    InvestigationResult
	composed  []SynthesisInput
	telemetry *recordingTelemetry
	modelCall int
	kept      int
	declared  int
	cutIDs    map[string]bool
}

// servedCutOf runs the prod shape: a degraded first answer that overran the
// item budget over a project cohort with a kind_census_truncated detail and
// two relationship paths per member, the retry declined for want of a
// deadline reserve. It returns the answer served over the cut cohort.
func servedCutOf(t *testing.T) servedCut {
	t.Helper()
	const kind = SubjectProject
	declared := memberCutCallerCap
	served := memberCutCallerCap
	reason := KindCensusTruncatedReason(kind, declared, served)
	detail := CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: kind, Declared: &declared, Served: &served, Raw: reason,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	memberCutGraphDetails = []CoverageDetail{detail}
	anchor := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_platform", Label: "Platform"}
	paths := []RelationshipPath{}
	for index := 0; index < memberCutCallerCap; index++ {
		id := fmt.Sprintf("project_%03d", index)
		member := SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}
		ref := fmt.Sprintf("ref_path_%03d", index)
		paths = append(paths, RelationshipPath{
			PathID: fmt.Sprintf("path_member_%03d", index), Nodes: []SubjectRef{anchor, member},
			Edges: []RelationshipEdge{{
				Type: contractsv1.ContextFabricRelationshipRelatedTo, From: anchor, To: member,
				Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, EvidenceRefIDs: []string{ref},
			}},
			WhyRelevant: "The project is reached from the team that owns it.", EvidenceRefIDs: []string{ref},
		})
	}
	memberCutGraphPaths = paths
	var modelCalls int
	var composed []SynthesisInput
	memberCutWrapSynthesizer = func(inner AnswerSynthesizer) AnswerSynthesizer {
		return failingModel{inner: inner, modelCalls: &modelCalls, composed: &composed}
	}
	t.Cleanup(func() { memberCutGraphDetails, memberCutGraphPaths, memberCutWrapSynthesizer = nil, nil, nil })

	telemetry := &recordingTelemetry{}
	result, err := investigateMemberCut(t, memberCutEngineAtCeiling(t, memberCutGaps, nil, 0, 0, memberCutCallerCap, telemetry))
	if err != nil {
		t.Fatalf("err = %v, want the cut answer served", err)
	}
	cut := map[string]bool{}
	if len(composed) == 2 {
		kept := map[string]bool{}
		for _, member := range composed[1].Graph.Cohort.Members {
			kept[member.Subject.CanonicalID] = true
		}
		for _, member := range composed[0].Graph.Cohort.Members {
			if !kept[member.Subject.CanonicalID] {
				cut[member.Subject.CanonicalID] = true
			}
		}
	}
	return servedCut{result: result, composed: composed, telemetry: telemetry, modelCall: modelCalls, kept: memberCutKept, declared: memberCutCallerCap, cutIDs: cut}
}

// The answer is the degraded composer's second composition over the kept
// members: one failed model call, no model call for the cut, and the answer
// fits the item budget.
func TestADegradedItemsOverrunIsServedAsAnAnswerOverTheCutCohort(t *testing.T) {
	got := servedCutOf(t)
	if got.modelCall != 1 || len(got.composed) != 2 {
		t.Fatalf("model calls = %d, degraded compositions = %d, want the one failed call and a composition for each of the two passes", got.modelCall, len(got.composed))
	}
	if got.result.Cohort == nil || len(got.result.Cohort.Members) != got.kept || len(got.cutIDs) != got.declared-got.kept {
		t.Fatalf("served members = %d cut = %d, want %d served and %d cut", cohortSize(got.result.Cohort), len(got.cutIDs), got.kept, got.declared-got.kept)
	}
	if items := contractsv1.CountContextFabricResultItems(got.result).Budgeted(); items > 66 {
		t.Fatalf("served items = %d, want at most the 66-item plan ceiling", items)
	}
	if len(got.telemetry.memberCuts) != 1 {
		t.Fatalf("member-cut events = %d, want 1: %+v", len(got.telemetry.memberCuts), got.telemetry.memberCuts)
	}
	event := got.telemetry.memberCuts[0]
	if !event.Served || !event.Recomposed || event.Declined != MemberCutNotApplicable || event.Listed != got.declared || event.Kept != got.kept ||
		event.Basis != contractsv1.ContextFabricNarrowingBasisAttentionRank ||
		event.KeepBy != MemberCutKeepMeasured || event.Need != got.declared-got.kept {
		t.Fatalf("member-cut event = %+v, want served, recomposed, listed %d, kept %d, attention_rank, measured", event, got.declared, got.kept)
	}
}

// The top-attention member of the first pass is in the served cohort.
func TestTheServedCutKeepsTheTopAttentionMember(t *testing.T) {
	got := servedCutOf(t)
	top := topAttentionIndex(got.composed[0].Graph.Cohort)
	if top < 0 {
		t.Fatal("the first pass ranked no member")
	}
	id := got.composed[0].Graph.Cohort.Members[top].Subject.CanonicalID
	if got.cutIDs[id] {
		t.Fatalf("the top-attention member %s was cut", id)
	}
}

// The plan states the cut: an assembled_result step by the order it was taken
// in, with the counts before and after, and the answer's limitation states how
// many members are listed.
func TestTheServedCutIsLabelledWithItsBasisAndCounts(t *testing.T) {
	got := servedCutOf(t)
	var step *contractsv1.ContextFabricPlanNarrowing
	for index := range got.result.AnswerPlan.Narrowing {
		if got.result.AnswerPlan.Narrowing[index].Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			step = &got.result.AnswerPlan.Narrowing[index]
		}
	}
	if step == nil || step.Basis != contractsv1.ContextFabricNarrowingBasisAttentionRank || step.Before != got.declared || step.After != got.kept {
		t.Fatalf("narrowing = %+v, want an assembled_result step by attention_rank from %d to %d", got.result.AnswerPlan.Narrowing, got.declared, got.kept)
	}
	want := fmt.Sprintf("%d of %d members are listed", got.kept, got.declared)
	if !slices.ContainsFunc(got.result.Limitations, func(l string) bool { return strings.Contains(l, want) }) {
		t.Fatalf("limitations = %q, want the sentence stating %q", got.result.Limitations, want)
	}
}

func censusDetailsOf(result InvestigationResult) []CoverageDetail {
	details := []CoverageDetail{}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
			details = append(details, detail)
		}
	}
	return details
}

// The census detail's served count is the kept count, not the listed one.
func TestTheServedCutCensusServedCountIsTheKeptCount(t *testing.T) {
	got := servedCutOf(t)
	details := censusDetailsOf(got.result)
	if len(details) != 1 || details[0].Served == nil || *details[0].Served != got.kept || details[0].Declared == nil || *details[0].Declared != got.declared {
		t.Fatalf("census details = %+v, want one with served %d of declared %d", details, got.kept, got.declared)
	}
}

// The census detail's raw text and the degraded-reasons entry carry the kept
// count, and the pre-cut sentence is nowhere.
func TestTheServedCutCensusRawAndDegradedReasonsCarryTheKeptCount(t *testing.T) {
	got := servedCutOf(t)
	want := KindCensusTruncatedReason(SubjectProject, got.declared, got.kept)
	stale := KindCensusTruncatedReason(SubjectProject, got.declared, got.declared)
	details := censusDetailsOf(got.result)
	if len(details) != 1 || details[0].Raw != want {
		t.Fatalf("census details = %+v, want raw %q", details, want)
	}
	if !slices.Contains(got.result.Coverage.DegradedReasons, want) || slices.Contains(got.result.Coverage.DegradedReasons, stale) {
		t.Fatalf("degraded reasons = %q, want %q and not %q", got.result.Coverage.DegradedReasons, want, stale)
	}
}

// The census detail's displayed phrasing states the kept count.
func TestTheServedCutCensusLabelStatesTheKeptCount(t *testing.T) {
	got := servedCutOf(t)
	details := censusDetailsOf(got.result)
	if len(details) != 1 {
		t.Fatalf("census details = %+v, want one", details)
	}
	want := contractsv1.ComposeCoverageDetailLabel(details[0])
	if details[0].Label != want {
		t.Fatalf("label = %q, want %q", details[0].Label, want)
	}
	stale := details[0]
	served := got.declared
	stale.Served = &served
	if staleLabel := contractsv1.ComposeCoverageDetailLabel(stale); staleLabel == details[0].Label {
		t.Fatalf("label %q does not discriminate the kept count from the listed one", details[0].Label)
	}
}

// No path names a member the cut removed, and no evidence ref only a cut
// member's path carried is left on the answer.
func TestTheServedCutPathsNameNoCutMember(t *testing.T) {
	got := servedCutOf(t)
	if len(got.result.Paths) == 0 || len(got.cutIDs) == 0 {
		t.Fatalf("paths = %d cut = %d: the row does not discriminate", len(got.result.Paths), len(got.cutIDs))
	}
	if len(got.result.Paths) != got.kept {
		t.Fatalf("paths = %d, want one per kept member (%d)", len(got.result.Paths), got.kept)
	}
	for _, path := range got.result.Paths {
		for _, node := range path.Nodes {
			if got.cutIDs[node.CanonicalID] {
				t.Fatalf("path %s names the cut member %s", path.PathID, node.CanonicalID)
			}
		}
		for _, edge := range path.Edges {
			if got.cutIDs[edge.From.CanonicalID] || got.cutIDs[edge.To.CanonicalID] {
				t.Fatalf("path %s edge names a cut member", path.PathID)
			}
		}
	}
	for id := range got.cutIDs {
		index := strings.TrimPrefix(id, "project_")
		if slices.Contains(got.result.EvidenceRefIDs, "ref_path_"+index) {
			t.Fatalf("evidence refs still carry the cut member %s's path ref", id)
		}
	}
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

func cohortSize(cohort *Cohort) int {
	if cohort == nil {
		return 0
	}
	return len(cohort.Members)
}

// A second composition that names a member the cut removed is not served: it
// would describe a list the answer no longer has. The cut is refused and the
// event says why.
func TestAServedCutThatNamesACutMemberIsRefused(t *testing.T) {
	memberCutWrapSynthesizer = func(inner AnswerSynthesizer) AnswerSynthesizer {
		var modelCalls int
		var composed []SynthesisInput
		return failingModel{inner: inner, modelCalls: &modelCalls, composed: &composed,
			after: func(index int, composed []SynthesisInput, result *InvestigationResult) {
				if index != 1 {
					return
				}
				kept := map[string]bool{}
				for _, member := range composed[1].Graph.Cohort.Members {
					kept[member.Subject.CanonicalID] = true
				}
				for _, member := range composed[0].Graph.Cohort.Members {
					if !kept[member.Subject.CanonicalID] {
						result.CurrentState = "The review queue on " + member.Subject.CanonicalID + " is the one to watch."
						return
					}
				}
			}}
	}
	t.Cleanup(func() { memberCutWrapSynthesizer = nil })
	telemetry := &recordingTelemetry{}
	_, err := investigateMemberCut(t, memberCutEngineAtCeiling(t, memberCutGaps, nil, 0, 0, memberCutCallerCap, telemetry))
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want the budget refusal", err)
	}
	if len(telemetry.memberCuts) != 1 || telemetry.memberCuts[0].Declined != MemberCutNamesCutMember || telemetry.memberCuts[0].Served {
		t.Fatalf("member-cut events = %+v, want one declined names_cut_member", telemetry.memberCuts)
	}
}
