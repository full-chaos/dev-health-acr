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
	// MemberCutCohortKind: the cohort is not a list of teams, repositories or
	// projects. The cut orders by attention or by id; it is never applied to
	// any other kind of list (no ranking of people exists).
	MemberCutCohortKind MemberCutDeclined = "cohort_kind"
	// MemberCutNoStatedOrder: the list carries no order the plan can state
	// (neither attention rank nor canonical id order).
	MemberCutNoStatedOrder MemberCutDeclined = "no_stated_order"
	// MemberCutNothingCuttable: every member the cut would remove is named by
	// the answer, or is the top-ranked member.
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

// MemberCutKeepBasis is the CLOSED vocabulary of how the number of kept
// members was computed.
type MemberCutKeepBasis string

const (
	// MemberCutKeepMeasured: from the first pass's measured items per member
	// and its overrun, plus a safety margin.
	MemberCutKeepMeasured MemberCutKeepBasis = "measured"
	// MemberCutKeepHalf: items per member could not be measured, so the
	// retry's own half target.
	MemberCutKeepHalf MemberCutKeepBasis = "half"
)

type memberCutAttempt struct {
	Declined MemberCutDeclined
	Need     int
	KeepBy   MemberCutKeepBasis
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
	Need        int
	KeepBy      MemberCutKeepBasis
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
	top := -1
	if basis == contractsv1.ContextFabricNarrowingBasisAttentionRank {
		top = topAttentionIndex(cohort)
	}
	order := make([]int, 0, len(cohort.Members))
	for index, member := range cohort.Members {
		if index != top && !cited[member.Subject.CanonicalID] && !namedInProse(member.Subject, prose) {
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

// topAttentionIndex is the member ranking put first (lowest AttentionRank among
// those it ran for), or -1. The cut keeps the highest-attention members, so
// this one is never cut.
func topAttentionIndex(cohort *Cohort) int {
	top := -1
	for index, member := range cohort.Members {
		if !member.RankingComputed {
			continue
		}
		if top < 0 || member.AttentionRank < cohort.Members[top].AttentionRank {
			top = index
		}
	}
	return top
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

// memberCutKeep is how many members the cut keeps and how that was decided.
// From the first pass's measurement: items per member is its items over its
// members, the overrun divided by that is the fewest members whose removal
// fits MaxItems, and a margin of a tenth of the list (at least one) covers the
// per-member share being an average. When items per member cannot be measured
// the retry's own half target applies. Never fewer than one member or than
// every member the answer names. A fixed allocation, not a search: the cut is
// synthesized once.
func memberCutKeep(listed, protected, items, maxItems int) (keep, need int, by MemberCutKeepBasis) {
	if items > maxItems && maxItems > 0 && listed > 0 {
		need = ((items-maxItems)*listed + items - 1) / items
		margin := listed / 10
		if margin < 1 {
			margin = 1
		}
		need += margin
		keep, by = listed-need, MemberCutKeepMeasured
	} else {
		keep, by = listed/2, MemberCutKeepHalf
		need = listed - keep
	}
	if keep < 1 {
		keep = 1
	}
	if protected > keep {
		keep = protected
	}
	return keep, need, by
}

// memberCutKind reports whether the cut applies to a cohort of kind: teams,
// repositories and projects only.
func memberCutKind(kind SubjectKind) bool {
	switch kind {
	case SubjectTeam, SubjectRepository, SubjectProject:
		return true
	}
	return false
}

// planMemberCut decides whether the member cut runs and builds its input.
// answer is the first pass's assembled answer, the one that overran: what it
// cites and names is kept.
func (e *Engine) planMemberCut(ctx context.Context, params synthesisAssemblyParams, firstPass assemblyTelemetry, answer InvestigationResult, items, maxItems int) memberCutAttempt {
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
	if !memberCutKind(graphCohort.Kind) {
		attempt.Declined = MemberCutCohortKind
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
	keep, need, by := memberCutKeep(listed, listed-len(order), items, maxItems)
	attempt.Listed, attempt.Uncited, attempt.Basis, attempt.Kept, attempt.Need, attempt.KeepBy = listed, len(order), basis, keep, need, by
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
		Need:        attempt.Need,
		KeepBy:      attempt.KeepBy,
		Basis:       attempt.Basis,
		Recomposed:  attempt.Recompose != nil,
		Served:      attempt.Declined == MemberCutNotApplicable,
		Declined:    attempt.Declined,
	})
}
