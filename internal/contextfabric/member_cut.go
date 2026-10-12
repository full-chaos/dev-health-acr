package contextfabric

import (
	"context"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The member cut: the model-free item lever for a flat cohort whose answer
// overran the item budget on members the answer does not name, when the cohort
// retry (the only other lever that removes members, and a model call) is
// unavailable.
//
// It must not patch the document. A cut produces a new cohort and everything
// derived from the cohort comes from ONE assembly over it; the seam for that is
// in place (the pass-1 draft kept on the pass's pending telemetry, assembled
// again by synthesizeAndAssemble with no model call). Serving the cut is
// deferred; what runs here is the assessment, recorded as one decision event.

// MemberCutDeclined is the CLOSED vocabulary of why the member cut did not
// serve an answer.
type MemberCutDeclined string

const (
	MemberCutNotApplicable MemberCutDeclined = ""
	// MemberCutNotItemsAxis: the answer did not overrun on items, or had no
	// item ceiling.
	MemberCutNotItemsAxis MemberCutDeclined = "not_items_axis"
	// MemberCutNoCohort: no flat cohort of two or more members (none, grouped,
	// a work-item census, or a single member).
	MemberCutNoCohort MemberCutDeclined = "no_cohort"
	// MemberCutNoDraft: the pass kept no draft to assemble again (a
	// client-written synthesis, or a pass that did not synthesize).
	MemberCutNoDraft MemberCutDeclined = "no_draft"
	// MemberCutNoStatedOrder: the list carries no order the plan can state
	// (neither attention rank nor canonical id order).
	MemberCutNoStatedOrder MemberCutDeclined = "no_stated_order"
	// MemberCutNothingCuttable: every member is named by the answer.
	MemberCutNothingCuttable MemberCutDeclined = "nothing_cuttable"
	// MemberCutServedCutDeferred: members could be left out (Uncited of Listed
	// are named by nothing in the answer) and the served cut is deferred until
	// every field a synthesizer derives from the graph input is restated over
	// a cut cohort.
	MemberCutServedCutDeferred MemberCutDeclined = "served_cut_deferred"
)

type memberCutAttempt struct {
	Declined MemberCutDeclined
	Measured MeasuredAttempt
	Listed   int
	Uncited  int
	Basis    contractsv1.ContextFabricNarrowingBasis
}

// MemberCutEvent is the operator record of ONE member-cut decision. Counts
// and closed values only.
type MemberCutEvent struct {
	Family      QuestionFamily
	Stage       contractsv1.ContextFabricPlanNarrowingStage
	Pass        int
	Overrun     contractsv1.ContextFabricBudgetOverrun
	MaxItems    int
	ItemsBefore int
	ItemsAfter  int
	Listed      int
	Uncited     int
	Basis       contractsv1.ContextFabricNarrowingBasis
	Declined    MemberCutDeclined
}

// memberCutBasis names the order the list is cut in, or false when the list
// carries no order the plan can state truthfully.
func memberCutBasis(cohort *Cohort) (contractsv1.ContextFabricNarrowingBasis, bool) {
	if cohort == nil || len(cohort.Groups) > 0 || contractsv1.ContextFabricCohortIsWalkList(cohort) {
		return "", false
	}
	if cohort.ScoreMeaning != "" {
		return contractsv1.ContextFabricNarrowingBasisAttentionRank, true
	}
	ids := make([]string, 0, len(cohort.Members))
	for _, member := range cohort.Members {
		ids = append(ids, member.Subject.CanonicalID)
	}
	if sort.StringsAreSorted(ids) {
		return contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, true
	}
	return "", false
}

// memberCutOrder returns the order members are cut in, worst first, over the
// members nothing in the answer names: for a ranked list the highest
// AttentionRank first (a member ranking did not run for counts as worst), as
// the Members array stays in pool order and says nothing of attention; for an
// unranked list sorted by canonical id, from the end of the list.
func memberCutOrder(result InvestigationResult, draft *InvestigationResult) (contractsv1.ContextFabricNarrowingBasis, []int, bool) {
	basis, ok := memberCutBasis(result.Cohort)
	if !ok {
		return "", nil, false
	}
	cited := walkListSubjectCited(result)
	prose := ""
	if draft != nil {
		prose = draftProse(*draft)
	}
	order := make([]int, 0, len(result.Cohort.Members))
	for index, member := range result.Cohort.Members {
		if !cited[member.Subject.CanonicalID] && !namedInProse(member.Subject, prose) {
			order = append(order, index)
		}
	}
	members := result.Cohort.Members
	sort.SliceStable(order, func(a, b int) bool {
		left, right := members[order[a]], members[order[b]]
		if basis == contractsv1.ContextFabricNarrowingBasisAttentionRank {
			if left.RankingComputed != right.RankingComputed {
				return !left.RankingComputed
			}
			if left.AttentionRank != right.AttentionRank {
				return left.AttentionRank > right.AttentionRank
			}
		}
		return order[a] > order[b]
	})
	return basis, order, true
}

// draftProse is every sentence the model wrote in the draft, lowercased, in one
// string: the judgment, the current state, the pressures, the driver and
// finding summaries, the limitations and the warnings. A member the prose names
// is not a member the answer can lose without that prose describing a list it
// no longer has.
func draftProse(draft InvestigationResult) string {
	parts := []string{draft.DirectJudgment, draft.CurrentState, draft.DeterministicAnswer}
	parts = append(parts, draft.StrongestPressures...)
	parts = append(parts, draft.Limitations...)
	parts = append(parts, draft.Warnings...)
	for _, driver := range draft.Drivers {
		parts = append(parts, driver.Title, driver.Summary, driver.Qualification)
	}
	for _, findings := range [][]Finding{draft.RemainingWork, draft.ReadinessGaps, draft.Conflicts} {
		for _, finding := range findings {
			parts = append(parts, finding.Kind, finding.Summary)
		}
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// namedInProse reports whether the member's canonical id or label appears in
// the prose. A name of three or more characters matches as a substring; a
// shorter one (the contract allows a one-character label) matches only as a
// whole word, so "AI" is found in "the AI team" and not in "said". A spurious
// match only keeps a member listed.
func namedInProse(subject SubjectRef, prose string) bool {
	for _, name := range []string{subject.CanonicalID, subject.Label} {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "":
		case len(name) >= 3:
			if strings.Contains(prose, name) {
				return true
			}
		default:
			if containsWord(prose, name) {
				return true
			}
		}
	}
	return false
}

// containsWord reports whether name appears in text with no letter or digit
// beside it.
func containsWord(text, name string) bool {
	for from := 0; ; {
		at := strings.Index(text[from:], name)
		if at < 0 {
			return false
		}
		start, end := from+at, from+at+len(name)
		beforeOK := start == 0 || !isWordByte(text[start-1])
		afterOK := end == len(text) || !isWordByte(text[end])
		if beforeOK && afterOK {
			return true
		}
		from = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || b >= 0x80
}

// planMemberCut assesses the member cut and does not serve it.
//
// A served cut produces a new cohort, and every field of the document that
// depends on the member list must come from one assembly over it. The seam for
// that exists (synthesizeAndAssemble takes the pass-1 draft, narrowedInputOver
// narrows the graph, copyResultDeep keeps the draft intact), and review found
// the fields the SYNTHESIZER derived from the graph input and left in the draft
// one at a time (a census served count, then its raw text and degraded reason):
// that class is not closed, so the served cut is deferred. What this does is the
// part that is already exact and useful: it says, on the trace, that an items
// overrun with the retry unavailable had members it could have left out, how
// many, and in which order.
func (e *Engine) planMemberCut(
	params synthesisAssemblyParams,
	firstPass assemblyTelemetry,
	result InvestigationResult,
	budget ResponseBudget,
	measured MeasuredAttempt,
) memberCutAttempt {
	attempt := memberCutAttempt{Measured: measured}
	if measured.Overrun != contractsv1.ContextFabricBudgetOverrunItems || budget.MaxItems <= 0 {
		attempt.Declined = MemberCutNotItemsAxis
		return attempt
	}
	graphCohort := params.Graph.Cohort
	if params.WorkItemCensus != nil || graphCohort == nil || len(graphCohort.Groups) > 0 || len(graphCohort.Members) < 2 ||
		result.Cohort == nil || len(result.Cohort.Members) != len(graphCohort.Members) {
		attempt.Declined = MemberCutNoCohort
		return attempt
	}
	if firstPass.Draft == nil || clientSynthesisRequested(params.Request) {
		attempt.Declined = MemberCutNoDraft
		return attempt
	}
	basis, order, ok := memberCutOrder(result, firstPass.Draft)
	if !ok {
		attempt.Declined = MemberCutNoStatedOrder
		return attempt
	}
	attempt.Listed, attempt.Basis, attempt.Uncited = len(graphCohort.Members), basis, len(order)
	switch {
	case len(order) == 0:
		attempt.Declined = MemberCutNothingCuttable
	default:
		attempt.Declined = MemberCutServedCutDeferred
	}
	return attempt
}

// recordMemberCut emits the one decision line of a member-cut assessment.
func (e *Engine) recordMemberCut(ctx context.Context, principal storage.Principal, plan *AnswerPlan, before MeasuredAttempt, attempt memberCutAttempt, budget ResponseBudget) {
	if e.telemetry == nil || attempt.Declined == MemberCutNotItemsAxis {
		return
	}
	e.telemetry.RecordMemberCut(ctx, principal, MemberCutEvent{
		Family:      plan.Family,
		Stage:       contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Pass:        answerPassFirst,
		Overrun:     before.Overrun,
		MaxItems:    budget.MaxItems,
		ItemsBefore: before.Measurement.Items.Budgeted(),
		Listed:      attempt.Listed,
		Uncited:     attempt.Uncited,
		Basis:       attempt.Basis,
		Declined:    attempt.Declined,
	})
}
