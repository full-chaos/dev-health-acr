package contextfabric

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The member cut: the item lever for a flat cohort whose answer overran the
// item budget when the cohort retry did not run. It never patches the
// document. It builds a cut cohort (narrowedInputOver, as the retry does) and
// synthesizes the answer again over it, ONCE, through the path the first pass
// took: the degraded composer when the first pass was served a degraded
// answer (no model call), the model when the model wrote it and the retry
// gate admits a second call. Every field a synthesizer derives from its input
// therefore describes the cut cohort.

// MemberCutDeclined is the CLOSED vocabulary of why the member cut did not
// serve an answer. Empty means it served.
type MemberCutDeclined string

const (
	MemberCutNotApplicable MemberCutDeclined = ""
	// MemberCutNotItemsAxis: the answer did not overrun on items, or had no
	// item ceiling.
	MemberCutNotItemsAxis MemberCutDeclined = "not_items_axis"
	// MemberCutNoCohort: no flat cohort of two or more members (none, grouped,
	// a work-item census, or a single member).
	MemberCutNoCohort MemberCutDeclined = "no_cohort"
	// MemberCutClientSynthesis: the client wrote the synthesis, so the
	// service has no synthesizer path to run again.
	MemberCutClientSynthesis MemberCutDeclined = "client_synthesis"
	// MemberCutNoStatedOrder: the list carries no order the plan can state
	// (neither attention rank nor canonical id order).
	MemberCutNoStatedOrder MemberCutDeclined = "no_stated_order"
	// MemberCutNothingCuttable: every member the half target would remove is
	// named by the answer.
	MemberCutNothingCuttable MemberCutDeclined = "nothing_cuttable"
	// MemberCutNoSynthesisTime: the model wrote the first answer and the
	// retry gate (deadline reserve, remaining deadline) admits no second
	// model call.
	MemberCutNoSynthesisTime MemberCutDeclined = "no_synthesis_time"
	// MemberCutStillOver: the answer synthesized over the cut cohort still
	// overran after every lever, and was refused.
	MemberCutStillOver MemberCutDeclined = "still_over"
	// MemberCutSynthesisFailed: the synthesis over the cut cohort failed.
	MemberCutSynthesisFailed MemberCutDeclined = "synthesis_failed"
)

type memberCutAttempt struct {
	Declined MemberCutDeclined
	Listed   int
	Uncited  int
	Kept     int
	Basis    contractsv1.ContextFabricNarrowingBasis
	// Input is the cut cohort's synthesis input, set when the cut runs.
	Input narrowedInput
	// Recompose is the first pass's model failure when its answer was the
	// degraded composition: the cut is composed the same way.
	Recompose *SynthesisFailure
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
	Listed      int
	Uncited     int
	Kept        int
	Basis       contractsv1.ContextFabricNarrowingBasis
	Recomposed  bool
	Served      bool
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

// memberCutOrder returns the members of cohort the answer does not name, worst
// first: for a ranked list the highest AttentionRank first (a member ranking
// did not run for counts as worst), as the Members array stays in pool order
// and says nothing of attention; for an unranked list sorted by canonical id,
// from the end of the list. A member the answer cites or its prose names is
// never in the order (ruling 9324).
func memberCutOrder(cohort *Cohort, basis contractsv1.ContextFabricNarrowingBasis, answer InvestigationResult) []int {
	cited := walkListSubjectCited(answer)
	prose := answerProse(answer)
	order := make([]int, 0, len(cohort.Members))
	for index, member := range cohort.Members {
		if !cited[member.Subject.CanonicalID] && !namedInProse(member.Subject, prose) {
			order = append(order, index)
		}
	}
	members := cohort.Members
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
	return order
}

// answerProse is every sentence of the answer in one lowercased string: the
// judgment, the current state, the pressures, the driver and finding
// summaries, the limitations, the warnings and the coverage phrasing. A member
// the prose names is kept.
func answerProse(answer InvestigationResult) string {
	parts := []string{answer.DirectJudgment, answer.CurrentState, answer.DeterministicAnswer}
	parts = append(parts, answer.StrongestPressures...)
	parts = append(parts, answer.Limitations...)
	parts = append(parts, answer.Warnings...)
	for _, driver := range answer.Drivers {
		parts = append(parts, driver.Title, driver.Summary, driver.Qualification)
	}
	for _, findings := range [][]Finding{answer.RemainingWork, answer.ReadinessGaps, answer.Conflicts} {
		for _, finding := range findings {
			parts = append(parts, finding.Kind, finding.Summary)
		}
	}
	for _, detail := range answer.Coverage.Details {
		parts = append(parts, detail.Phrasing)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// namedInProse reports whether the member's canonical id or label appears in
// the prose. A name of three or more characters matches as a substring; a
// shorter one (the contract allows a one-character label) matches only as a
// whole word, so "AI" is found in "the AI team" and not in "said". Length is
// counted in characters, not bytes. A spurious match only keeps a member.
func namedInProse(subject SubjectRef, prose string) bool {
	for _, name := range []string{subject.CanonicalID, subject.Label} {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "":
		case utf8.RuneCountInString(name) >= 3:
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

// memberCutKeep is how many members the cut keeps: the retry's own half
// target (at least one), raised to every member the answer names. A fixed
// allocation, not a search: the cut is synthesized once.
func memberCutKeep(listed, protected int) int {
	keep := listed / 2
	if keep < 1 {
		keep = 1
	}
	if protected > keep {
		keep = protected
	}
	return keep
}

// planMemberCut decides whether the member cut runs and builds its input.
// answer is the first pass's assembled answer, the one that overran: what it
// cites and names is kept.
func (e *Engine) planMemberCut(ctx context.Context, params synthesisAssemblyParams, firstPass assemblyTelemetry, answer InvestigationResult) memberCutAttempt {
	var attempt memberCutAttempt
	graphCohort := params.Graph.Cohort
	if params.WorkItemCensus != nil || graphCohort == nil || len(graphCohort.Groups) > 0 || len(graphCohort.Members) < 2 {
		attempt.Declined = MemberCutNoCohort
		return attempt
	}
	if clientSynthesisRequested(params.Request) {
		attempt.Declined = MemberCutClientSynthesis
		return attempt
	}
	basis, ok := memberCutBasis(graphCohort)
	if !ok {
		attempt.Declined = MemberCutNoStatedOrder
		return attempt
	}
	cohort := copyCohortForRetry(graphCohort)
	order := memberCutOrder(cohort, basis, answer)
	listed := len(cohort.Members)
	keep := memberCutKeep(listed, listed-len(order))
	attempt.Listed, attempt.Uncited, attempt.Basis, attempt.Kept = listed, len(order), basis, keep
	if keep >= listed {
		attempt.Declined = MemberCutNothingCuttable
		return attempt
	}
	if firstPass.DegradedFailure != nil {
		attempt.Recompose = firstPass.DegradedFailure
	} else if e.synthesisDeadlineReserve <= 0 || !e.retryDeadlineAvailable(ctx) {
		attempt.Declined = MemberCutNoSynthesisTime
		return attempt
	}
	cut := make(map[int]bool, listed-keep)
	for _, index := range order[:listed-keep] {
		cut[index] = true
	}
	kept := make([]CohortMember, 0, keep)
	for index, member := range cohort.Members {
		if !cut[index] {
			kept = append(kept, member)
		}
	}
	attempt.Input = narrowedInputOver(params, params.Graph, params.Facts, cohort, kept, listed, basis)
	return attempt
}

// recordMemberCut emits the one decision line of a member-cut decision.
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
		Kept:        attempt.Kept,
		Basis:       attempt.Basis,
		Recomposed:  attempt.Recompose != nil,
		Served:      attempt.Declined == MemberCutNotApplicable,
		Declined:    attempt.Declined,
	})
}
