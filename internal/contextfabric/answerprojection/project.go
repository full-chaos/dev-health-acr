// Package answerprojection derives the bounded consumer projection of a
// Context Fabric investigation result (CHAOS-3746).
//
// This package is the SINGLE choke point through which every bounded
// consumer sees an answer. The hosted API and the MCP sidecar both call
// Project; neither builds its own summary. That is what makes API/MCP
// parity structural instead of a convention a future change could quietly
// break.
//
// PURITY CONSTRAINT (binding): this package must not import HTTP, MCP,
// storage, database, or any transport package. It depends on
// internal/contracts/v1 and the standard library, nothing else. The parity
// guarantee rests on both surfaces being able to call exactly this code, so
// anything that ties it to one transport breaks the guarantee. The
// constraint is enforced by TestPackageImportsStayPure in
// project_purity_test.go, not left to reviewer memory.
//
// PROJECTION RULE (binding): Project selects and drops. It never rewrites,
// re-ranks, re-judges, or re-words. Every drop is declared in the returned
// ProjectionBudget.
package answerprojection

import (
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Budget bounds one projection. A zero value means "use the defaults":
// every non-positive field is replaced by its DefaultBudget counterpart, so
// a caller can set only the fields it cares about.
type Budget struct {
	MaxDrivers       int
	MaxCohortMembers int
	MaxCandidates    int
	MaxFacts         int
	MaxEvidenceRefs  int
}

// DefaultBudget is the projection budget applied when a caller supplies
// none. It is deliberately smaller than the canonical contract maxima: the
// point of a projection is to be usable inside a bounded consumer, and a
// caller that wants everything should read the canonical result instead.
var DefaultBudget = Budget{
	MaxDrivers:       10,
	MaxCohortMembers: 25,
	MaxCandidates:    10,
	MaxFacts:         50,
	MaxEvidenceRefs:  100,
}

// withDefaults fills unset fields and clamps every field to the contract
// maximum for its projected array. Clamping here (rather than trusting the
// caller) means no caller can request a projection that would fail
// ContextFabricAnswerProjection.Validate on size alone.
func (b Budget) withDefaults() Budget {
	out := b
	if out.MaxDrivers <= 0 {
		out.MaxDrivers = DefaultBudget.MaxDrivers
	}
	if out.MaxCohortMembers <= 0 {
		out.MaxCohortMembers = DefaultBudget.MaxCohortMembers
	}
	if out.MaxCandidates <= 0 {
		out.MaxCandidates = DefaultBudget.MaxCandidates
	}
	if out.MaxFacts <= 0 {
		out.MaxFacts = DefaultBudget.MaxFacts
	}
	if out.MaxEvidenceRefs <= 0 {
		out.MaxEvidenceRefs = DefaultBudget.MaxEvidenceRefs
	}
	out.MaxDrivers = min(out.MaxDrivers, contractsv1.ContextFabricProjectedDriversMaxCount)
	out.MaxCohortMembers = min(out.MaxCohortMembers, contractsv1.ContextFabricProjectedCohortMaxCount)
	out.MaxCandidates = min(out.MaxCandidates, contractsv1.ContextFabricProjectedCandidatesMaxCount)
	out.MaxFacts = min(out.MaxFacts, contractsv1.ContextFabricProjectedFactsMaxCount)
	out.MaxEvidenceRefs = min(out.MaxEvidenceRefs, contractsv1.ContextFabricProjectedEvidenceMaxCount)
	return out
}

// standingRank orders driver standings by how much weight the engine put
// behind them. This is a selection order over the engine's OWN judgment
// field, not a ranking the projection invents: when a budget cannot carry
// every driver, the ones the engine called principal survive.
//
// Withheld is absent on purpose. A withheld driver is one the engine
// declined to stand behind, so the projection never presents it as part of
// the answer; it is counted in ProjectionBudget.WithheldDriversOmitted
// instead, because a consumer that could not see that some were withheld
// would read a filtered answer as a complete one.
func standingRank(standing contractsv1.ContextFabricDriverStanding) int {
	switch standing {
	case contractsv1.ContextFabricDriverPrincipal:
		return 0
	case contractsv1.ContextFabricDriverContributing:
		return 1
	case contractsv1.ContextFabricDriverSymptom:
		return 2
	case contractsv1.ContextFabricDriverContext:
		return 3
	default:
		return 4
	}
}

// Project derives the bounded consumer projection of result.
//
// The returned projection is a pure function of (result, budget). It
// contains no wording, ordering, or judgment that is not already present in
// result: every text field is copied verbatim, and the only freedom the
// projection exercises is which entries to keep.
func Project(result contractsv1.ContextFabricInvestigationResult, budget Budget) contractsv1.ContextFabricAnswerProjection {
	bounds := budget.withDefaults()

	// The evidence index is built AS content is admitted, not filtered
	// afterwards. Every retained driver or cohort member must have all of
	// its citations present in the index, so when a citation set does not
	// fit the remaining budget the CITING ITEM is dropped -- never one of
	// its references. Drivers are offered first because they carry the
	// judgment; a tight evidence budget should cost cohort tail, not the
	// reasons behind the answer.
	clamp := &clamper{}
	index := newEvidenceIndex(bounds.MaxEvidenceRefs)
	drivers, driversOmitted, withheldOmitted, facts, factsOmitted := projectDrivers(result, bounds, index, clamp)
	cohort, cohortOmitted, cohortReasonsOmitted := projectCohort(result, bounds, index, clamp)
	clarification, candidatesOmitted, candidateReasonsOmitted := projectClarification(result, bounds, clamp)
	limitations, limitationsOmitted := boundedLimitations(result.Limitations, clamp)
	// The engine's own displacement counts too (CHAOS-3746 round-16).
	// limitations_omitted means "limitations this investigation produced
	// that you are not reading", and a caveat the engine dropped at the
	// contract cap is exactly that. Leaving it out reported zero omissions
	// for an answer that had genuinely lost content -- and the projection
	// cannot rediscover it, because a displaced list and a list that had
	// room are indistinguishable here.
	limitationsOmitted += result.LimitationsDisplaced
	warnings, warningsOmitted := boundedNarrative(result.Warnings, clamp)
	coverage, coverageOmitted := projectCoverage(result, clamp)
	// CHAOS-4415: shapes are carried after the cohort and the facts they
	// cite have been cut, never before -- a shape is admitted only when
	// this projection still lets its reader check every number it plots.
	renderShapes, renderShapesOmitted := projectRenderShapes(result, cohort, facts)
	evidence := index.ids()
	evidenceOmitted := countUnindexedEvidence(result, index)

	projection := contractsv1.ContextFabricAnswerProjection{
		SchemaVersion: contractsv1.ContextFabricAnswerProjectionSchema,
		ResultID:      result.ResultID,
		RequestID:     result.RequestID,
		GeneratedAt:   result.GeneratedAt,
		Status:        result.Status,
		Question:      storedText(result.Question),
		Reused:        result.Reused,
		// Verbatim. These two fields are the answer.
		DirectJudgment:     clamp.text(storedText(result.DirectJudgment), contractsv1.ContextFabricProjectedJudgmentMaxLength),
		CurrentState:       clamp.text(storedText(result.CurrentState), contractsv1.ContextFabricProjectedJudgmentMaxLength),
		StrongestPressures: distinctStrings(result.StrongestPressures),
		// Never truncated: a surface that reported a different set of
		// committed subjects answered a different question.
		CommittedSubjects: append([]contractsv1.ContextFabricSubjectRef(nil), result.SubjectResolution.Committed...),
		Clarification:     clarification,
		Cohort:            cohort,
		PrincipalDrivers:  drivers,
		KeyFacts:          facts,
		CoverageSummary:   coverage,
		Temporal:          projectTemporal(result),
		CoveragePartial:   result.Coverage.Partial,
		Limitations:       limitations,
		Warnings:          warnings,
		EvidenceRefIDs:    evidence,
		SubjectReceipts:   projectReceipts(result),
		Versions:          result.Versions,
		// EffectiveEvidenceWindow, WindowClarification, StructureNeeds, and
		// ConfirmedStructure (CHAOS-3972 P3+W2) are copied verbatim, joining
		// the SAME never-dropped discipline Temporal/Limitations already
		// follow above -- the MCP investigate_question response is the
		// bounded consumer surface this whole disclosure mechanism exists to
		// reach, and a projection that dropped it would defeat the point.
		EffectiveEvidenceWindow: result.EffectiveEvidenceWindow,
		WindowClarification:     result.WindowClarification,
		StructureNeeds:          result.StructureNeeds,
		ConfirmedStructure:      append([]contractsv1.ContextFabricConfirmedStructureEntry(nil), result.ConfirmedStructure...),
		// PriorSubjectReceiptDispositions (CHAOS-3478/CHAOS-3813) joins the
		// SAME never-dropped discipline as ConfirmedStructure immediately
		// above -- see PriorSubjectReceiptDispositions' own doc comment
		// (context_fabric_answer_projection.go) for why omitting it here
		// would leave the default answer surface reproducing the exact
		// silent drop this field exists to close.
		PriorSubjectReceiptDispositions: append([]contractsv1.ContextFabricPriorSubjectReceiptEntry(nil), result.SubjectResolution.PriorSubjectReceiptDispositions...),
		RenderShapes:                    renderShapes,
	}
	projection.ProjectionBudget = contractsv1.ContextFabricProjectionBudget{
		DriversOmitted:         driversOmitted,
		WithheldDriversOmitted: withheldOmitted,
		CohortMembersOmitted:   cohortOmitted,
		FactsOmitted:           factsOmitted,
		CandidatesOmitted:      candidatesOmitted,
		EvidenceRefsOmitted:    evidenceOmitted,
		ReasonsOmitted:         cohortReasonsOmitted + candidateReasonsOmitted,
		ValuesClamped:          clamp.count,
		LimitationsOmitted:     limitationsOmitted,
		WarningsOmitted:        warningsOmitted,
		CoverageOmitted:        coverageOmitted,
		RenderShapesOmitted:    renderShapesOmitted,
	}
	projection.ProjectionBudget.Truncated = declaresDrop(projection.ProjectionBudget)
	if projection.CommittedSubjects == nil {
		projection.CommittedSubjects = []contractsv1.ContextFabricSubjectRef{}
	}
	return projection
}

// MarkFullResultOmitted records that a caller asked for the full canonical
// result alongside the projection and it did not fit the byte budget. It
// lives here so the "Truncated must match the declared drops" invariant
// stays in one place rather than being re-derived by each surface.
//
// The projection itself remains complete and valid. Only the requested
// extra copy of the canonical result was dropped, and the caller still
// holds ResultID to fetch it directly.
func MarkFullResultOmitted(projection *contractsv1.ContextFabricAnswerProjection) {
	if projection == nil {
		return
	}
	projection.ProjectionBudget.FullResultOmitted = true
	projection.ProjectionBudget.Truncated = true
}

func declaresDrop(budget contractsv1.ContextFabricProjectionBudget) bool {
	return budget.FullResultOmitted ||
		budget.DriversOmitted > 0 ||
		budget.WithheldDriversOmitted > 0 ||
		budget.CohortMembersOmitted > 0 ||
		budget.FactsOmitted > 0 ||
		budget.CandidatesOmitted > 0 ||
		budget.EvidenceRefsOmitted > 0 ||
		budget.LimitationsOmitted > 0 ||
		budget.WarningsOmitted > 0 ||
		budget.CoverageOmitted > 0 ||
		budget.ReasonsOmitted > 0 ||
		budget.ValuesClamped > 0 ||
		budget.RenderShapesOmitted > 0
}

// projectDrivers selects the drivers that survive the budget and the
// claimed facts they cite.
//
// Ordering: a stable sort by standing precedence only. Within one standing
// the canonical array order is preserved exactly, so the projection never
// invents a ranking the engine did not state.
//
// Fact coupling: a retained driver must never cite a claim the projection
// dropped, or the consumer that received it could not check the claim. So
// drivers are admitted one at a time, and a driver whose claims would push
// the fact set past the budget is dropped instead -- counted as an omitted
// driver, which is the honest description of what happened.
func projectDrivers(result contractsv1.ContextFabricInvestigationResult, bounds Budget, index *evidenceIndex, clamp *clamper) (drivers []contractsv1.ContextFabricProjectedDriver, driversOmitted, withheldOmitted int, facts []contractsv1.ContextFabricProjectedFact, factsOmitted int) {
	claims := make(map[string]contractsv1.ContextFabricClaimedFact, len(result.ClaimedFacts))
	for _, fact := range result.ClaimedFacts {
		claims[fact.ClaimID] = fact
	}

	ordered := make([]contractsv1.ContextFabricDriverJudgment, 0, len(result.Drivers))
	for _, driver := range result.Drivers {
		if driver.Standing == contractsv1.ContextFabricDriverWithheld {
			withheldOmitted++
			continue
		}
		ordered = append(ordered, driver)
	}
	// Standing first, then DriverID as a total tie-break. Without the
	// second key, two drivers of equal standing kept their canonical array
	// order, so the SAME answer arriving with its drivers in a different
	// order produced a different retained set under a limiting budget --
	// which makes the differential parity check unfalsifiable in exactly
	// the case it exists to police (codex round-1 F7).
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := standingRank(ordered[i].Standing), standingRank(ordered[j].Standing)
		if left != right {
			return left < right
		}
		return ordered[i].DriverID < ordered[j].DriverID
	})

	drivers = make([]contractsv1.ContextFabricProjectedDriver, 0, min(len(ordered), bounds.MaxDrivers))
	facts = make([]contractsv1.ContextFabricProjectedFact, 0, bounds.MaxFacts)
	retainedClaims := make(map[string]struct{}, bounds.MaxFacts)
	for _, driver := range ordered {
		if len(drivers) >= bounds.MaxDrivers {
			driversOmitted++
			continue
		}
		additional := make([]contractsv1.ContextFabricClaimedFact, 0, len(driver.ClaimedFactIDs))
		for _, claimID := range driver.ClaimedFactIDs {
			if _, retained := retainedClaims[claimID]; retained {
				continue
			}
			fact, known := claims[claimID]
			if !known {
				// The canonical result already proved every driver
				// claim resolves, so this cannot happen for a valid
				// result. Skip rather than fabricate a fact.
				continue
			}
			additional = append(additional, fact)
		}
		if len(facts)+len(additional) > bounds.MaxFacts {
			driversOmitted++
			continue
		}
		// The evidence index must be able to carry every reference this
		// driver cites. If it cannot, the DRIVER goes, not a reference:
		// a retained driver citing an ID the caller cannot find in the
		// index would be unverifiable, which is worse than a shorter
		// answer that is honest about being shorter.
		if !index.admit(driver.EvidenceRefIDs) {
			driversOmitted++
			continue
		}
		for _, fact := range additional {
			retainedClaims[fact.ClaimID] = struct{}{}
			facts = append(facts, contractsv1.ContextFabricProjectedFact{
				ClaimID: fact.ClaimID,
				Kind:    fact.Kind,
				Subject: fact.Subject,
				Field:   fact.Field,
				Value:   fact.Value,
				// Rows (CHAOS-4347) is carried through unchanged, same as
				// Value -- it is copied evidence, not something this
				// projection step computes or narrows.
				Rows: fact.Rows,
			})
		}
		drivers = append(drivers, contractsv1.ContextFabricProjectedDriver{
			DriverID:      driver.DriverID,
			Standing:      driver.Standing,
			Category:      driver.Category,
			Title:         storedText(driver.Title),
			Summary:       storedText(driver.Summary),
			Qualification: storedText(driver.Qualification),
			Confidence:    driver.Confidence,
			// EvidenceRefIDs is a REQUIRED array; copying a canonical
			// empty slice with append(nil, ...) would yield nil and
			// serialize as null. ClaimedFactIDs is optional, so nil is
			// legitimate there and is left alone.
			EvidenceRefIDs: copyStrings(driver.EvidenceRefIDs),
			ClaimedFactIDs: append([]string(nil), driver.ClaimedFactIDs...),
			// AffectedSubjects (CHAOS-4398 PR3, design doc §4a) copied
			// verbatim -- ties a cohort-answer driver back to which
			// team(s) it explains.
			AffectedSubjects: append([]contractsv1.ContextFabricSubjectRef(nil), driver.AffectedSubjects...),
		})
	}
	// Claimed facts the canonical result carried but no retained driver
	// cites are not "omitted" in the sense that matters -- they were never
	// part of what this projection asserts. Only facts a dropped driver
	// would have brought are counted, which is the count a caller can act
	// on.
	factsOmitted = countUncitedClaims(result, retainedClaims)
	return drivers, driversOmitted, withheldOmitted, facts, factsOmitted
}

// countUncitedClaims counts claimed facts cited by at least one non-withheld
// canonical driver that the projection did not retain.
func countUncitedClaims(result contractsv1.ContextFabricInvestigationResult, retained map[string]struct{}) int {
	cited := make(map[string]struct{})
	for _, driver := range result.Drivers {
		if driver.Standing == contractsv1.ContextFabricDriverWithheld {
			continue
		}
		for _, claimID := range driver.ClaimedFactIDs {
			cited[claimID] = struct{}{}
		}
	}
	omitted := 0
	for claimID := range cited {
		if _, ok := retained[claimID]; !ok {
			omitted++
		}
	}
	return omitted
}

// projectCohort narrows a cohort to its leading members. Total always
// reports the canonical member count, so a caller sees the true size even
// when the member list is cut.
//
// Complete is copied verbatim: it is the engine's statement about the
// cohort it discovered, not a statement about this projection. Projection
// truncation shows up in Total versus len(Members) and in the declared
// budget, never by silently flipping the engine's own claim.
func projectCohort(result contractsv1.ContextFabricInvestigationResult, bounds Budget, index *evidenceIndex, clamp *clamper) (*contractsv1.ContextFabricProjectedCohort, int, int) {
	if result.Cohort == nil {
		return nil, 0, 0
	}
	canonical := *result.Cohort
	reasonsOmitted := 0
	members := make([]contractsv1.ContextFabricProjectedCohortMember, 0, min(len(canonical.Members), bounds.MaxCohortMembers))
	// retained mirrors members 1:1 (same order, same cut) but keeps the
	// CANONICAL member -- including Drivers, which the projected member
	// type does not carry -- so buildRankingTable below can read
	// per-family driver evidence without re-deriving it. Built from
	// retained, not canonical.Members, so RankingTable never rows a team
	// the budget/evidence cut above already dropped from Members.
	retained := make([]contractsv1.ContextFabricCohortMember, 0, min(len(canonical.Members), bounds.MaxCohortMembers))
	for _, member := range canonical.Members {
		if len(members) >= bounds.MaxCohortMembers {
			break
		}
		// Same rule as drivers: a member whose citations do not fit the
		// evidence index is dropped whole rather than kept with dangling
		// references. Ranks stay strictly increasing because members are
		// only ever dropped from consideration, never reordered.
		if !index.admit(member.EvidenceRefIDs) {
			break
		}
		reasons, reasonsDropped := clamp.strings(member.InclusionReasons, contractsv1.ContextFabricProjectedInclusionReasonsMaxCount, contractsv1.ContextFabricProjectedInclusionReasonMaxLength)
		reasonsOmitted += reasonsDropped
		members = append(members, contractsv1.ContextFabricProjectedCohortMember{
			Subject:          member.Subject,
			Rank:             member.Rank,
			InclusionReasons: reasons,
			EvidenceRefIDs:   append([]string(nil), member.EvidenceRefIDs...),
			// RankingComputed/AttentionRank/Score/RankingBasis/DataCompleteness/
			// Outcome/MissingSignals (CHAOS-4398 PR3, design doc §4a/§8) are
			// copied verbatim -- see ContextFabricProjectedCohortMember's own
			// doc comment.
			RankingComputed:  member.RankingComputed,
			AttentionRank:    member.AttentionRank,
			Score:            member.Score,
			RankingBasis:     append([]string(nil), member.RankingBasis...),
			DataCompleteness: member.DataCompleteness,
			Outcome:          member.Outcome,
			MissingSignals:   append([]string(nil), member.MissingSignals...),
		})
		retained = append(retained, member)
	}
	return &contractsv1.ContextFabricProjectedCohort{
		Kind:         canonical.Kind,
		Total:        len(canonical.Members),
		Rationale:    clamp.text(storedText(canonical.Rationale), contractsv1.ContextFabricProjectedCohortRationaleMaxLength),
		Complete:     canonical.Complete,
		Members:      members,
		RankingTable: buildRankingTable(retained),
	}, len(canonical.Members) - len(members), reasonsOmitted
}

// projectClarification carries the ambiguity a caller must resolve. It is
// emitted only when the engine actually asked for clarification: attaching
// candidate lists to a confident answer would invite a consumer to treat a
// settled subject as still open.
func projectClarification(result contractsv1.ContextFabricInvestigationResult, bounds Budget, clamp *clamper) (*contractsv1.ContextFabricProjectedClarification, int, int) {
	if result.Status != contractsv1.ContextFabricInvestigationClarificationRequired {
		return nil, 0, 0
	}
	canonical := result.SubjectResolution.Candidates
	retain := min(len(canonical), bounds.MaxCandidates)
	candidates := make([]contractsv1.ContextFabricProjectedCandidate, 0, retain)
	reasonsOmitted := 0
	for _, candidate := range canonical[:retain] {
		reasons, reasonsDropped := clamp.strings(candidate.MatchReasons, contractsv1.ContextFabricProjectedMatchReasonsMaxCount, contractsv1.ContextFabricProjectedMatchReasonMaxLength)
		reasonsOmitted += reasonsDropped
		candidates = append(candidates, contractsv1.ContextFabricProjectedCandidate{
			ReceiptID:    candidate.ReceiptID,
			Subject:      candidate.Subject,
			State:        candidate.State,
			Confidence:   candidate.Confidence,
			MatchReasons: reasons,
		})
	}
	return &contractsv1.ContextFabricProjectedClarification{
		Prompt:     clamp.text(storedText(result.SubjectResolution.ClarificationPrompt), contractsv1.ContextFabricProjectedClarificationPromptMaxLength),
		Candidates: candidates,
	}, len(canonical) - retain, reasonsOmitted
}

// evidenceIndex accumulates the evidence references retained content
// cites, under a hard cap.
//
// It exists so the "index contains every reference retained content cites"
// invariant is enforced by CONSTRUCTION rather than checked afterwards.
// The previous shape selected content first and truncated the index second,
// which could leave a retained driver citing an ID the caller could not
// resolve (codex round-1 F6).
type evidenceIndex struct {
	limit int
	seen  map[string]struct{}
	order []string
}

func newEvidenceIndex(limit int) *evidenceIndex {
	return &evidenceIndex{limit: limit, seen: make(map[string]struct{}, limit), order: make([]string, 0, limit)}
}

// admit adds every reference in ids, or none of them. It reports whether
// the citing item may be retained: all-or-nothing is the point, since a
// partially indexed citation set is exactly the dangling-reference state
// this type prevents. Already-indexed references cost nothing, so an item
// citing only references a previous item already brought always fits.
func (e *evidenceIndex) admit(ids []string) bool {
	additional := 0
	for _, id := range ids {
		if _, exists := e.seen[id]; exists {
			continue
		}
		additional++
	}
	if len(e.order)+additional > e.limit {
		return false
	}
	for _, id := range ids {
		if _, exists := e.seen[id]; exists {
			continue
		}
		e.seen[id] = struct{}{}
		e.order = append(e.order, id)
	}
	return true
}

// ids returns the indexed references, always as a non-nil slice.
//
// A nil slice marshals to JSON null, and evidence_ref_ids is a REQUIRED
// array in both the projection schema and its validator. So a result whose
// retained content cited nothing produced an internal error on the MCP path
// and a schema-violating null body on the API path (codex round-2 F1) --
// for the entirely ordinary case of an answer with no citations.
func (e *evidenceIndex) ids() []string {
	out := make([]string, len(e.order))
	copy(out, e.order)
	return out
}

// countUnindexedEvidence reports how many distinct references the canonical
// result's non-withheld drivers and cohort members cite that the index does
// not carry. That is the count a caller can act on: it names what the
// evidence budget cost, not merely how many references existed.
func countUnindexedEvidence(result contractsv1.ContextFabricInvestigationResult, index *evidenceIndex) int {
	cited := make(map[string]struct{})
	for _, driver := range result.Drivers {
		if driver.Standing == contractsv1.ContextFabricDriverWithheld {
			continue
		}
		for _, id := range driver.EvidenceRefIDs {
			cited[id] = struct{}{}
		}
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			for _, id := range member.EvidenceRefIDs {
				cited[id] = struct{}{}
			}
		}
	}
	omitted := 0
	for id := range cited {
		if _, ok := index.seen[id]; !ok {
			omitted++
		}
	}
	return omitted
}

// boundedNarrative bounds a free-text array (limitations, warnings) to the
// projection's own contract maximum and reports how many entries were
// dropped.
//
// The canonical result allows 250 of these while the projection allows 100,
// so copying them wholesale turned a valid result into an invalid
// projection (codex round-1 F4). Truncating silently would have been worse
// than the crash: a shortened limitations list reads as a more confident
// answer than the investigation actually gave.
func boundedNarrative(values []string, clamp *clamper) ([]string, int) {
	return boundedNarrativeRetaining(values, clamp, nil)
}

// boundedLimitations is boundedNarrative with a RETENTION PRIORITY: if the
// list must be cut, the retrieval-degradation disclosure is kept.
//
// Without it the disclosure is precisely the entry that gets dropped. The
// engine appends it last, and the cut keeps a prefix -- so on a legacy
// stored row written when the canonical cap was 250, a bounded consumer
// receives 100 model caveats and no statement that retrieval was degraded,
// which reads as a cleaner answer than the investigation gave. The
// canonical result still carries it, so nothing looks wrong from the API's
// canonical view; only the bounded consumer is misled.
//
// The displacement is counted in limitations_omitted like every other drop,
// because the entry it displaces is genuinely gone.
func boundedLimitations(values []string, clamp *clamper) ([]string, int) {
	return boundedNarrativeRetaining(values, clamp, contractsv1.IsContextFabricRetrievalDegradedLimitation)
}

// boundedNarrativeRetaining is the shared implementation. retain, when
// non-nil, names entries that must survive the count cap.
func boundedNarrativeRetaining(values []string, clamp *clamper, retain func(string) bool) ([]string, int) {
	// Clamp FIRST, then dedupe (codex round-5 R5-4). Deduping first let two
	// distinct legacy entries sharing a long prefix survive as separate
	// values, collide once clamped to the same prefix, and produce a
	// projection with duplicate entries that its own validator rejects --
	// which the route then emitted unvalidated. Post-clamp collisions are
	// real duplicates and are counted as omissions.
	//
	// Shortening is recorded per entry and counted only for SURVIVORS
	// (codex round-10 F1). Counting during the clamp -- through clamper.text,
	// which increments on every input -- counted entries that dedup and the
	// count cap then discarded: 101 distinct oversized limitations put 100
	// values on the wire and reported values_clamped: 101. ValuesClamped
	// describes what the consumer RECEIVED in shortened form, so an entry it
	// never received cannot appear in it. Same mechanics as the round-9 F4
	// fix on clamper.strings; this path was simply not covered by it.
	clamped := make([]clampedNarrative, 0, len(values))
	for _, value := range values {
		cut := truncateRunes(value, contractsv1.ContextFabricProjectedNarrativeMaxLength)
		clamped = append(clamped, clampedNarrative{value: cut, shortened: cut != value})
	}
	seen := make(map[string]struct{}, len(clamped))
	// Allocated, never nil: these feed required array members.
	distinct := make([]clampedNarrative, 0, len(clamped))
	for _, entry := range clamped {
		if _, exists := seen[entry.value]; exists {
			continue
		}
		seen[entry.value] = struct{}{}
		distinct = append(distinct, entry)
	}
	omitted := len(clamped) - len(distinct)
	if len(distinct) > contractsv1.ContextFabricProjectedNarrativeMaxCount {
		omitted += len(distinct) - contractsv1.ContextFabricProjectedNarrativeMaxCount
		kept := distinct[:contractsv1.ContextFabricProjectedNarrativeMaxCount]
		// A retained entry sitting past the cut is moved into the kept
		// set, displacing the last entry there. Deliberately checked
		// against the CLAMPED value: a disclosure long enough to be
		// shortened is no longer the constant, and would be missed by a
		// check against the original -- the same trim-before-compare
		// discipline the stored-read path already follows.
		if retain != nil {
			kept = retainPastTheCut(kept, distinct[contractsv1.ContextFabricProjectedNarrativeMaxCount:], retain)
		}
		distinct = kept
	}
	survivors := make([]string, 0, len(distinct))
	for _, entry := range distinct {
		if entry.shortened {
			clamp.count++
		}
		survivors = append(survivors, entry.value)
	}
	return survivors, omitted
}

// storedText normalizes a bounded text value read from storage.
//
// Canonical STORED reads measure bounded text on the trimmed value, because
// padded rows were legally writable before the bound was enforced raw and
// those rows are immutable (CHAOS-3746 round 13, world (b)). The projection
// contract measures the SAME fields raw, so copying a legacy value verbatim
// produced a projection the projection's own validator rejected: a row that
// reads back perfectly well became unservable, 500 from the hosted route and
// an internal error from the MCP tool (codex round-14 F1).
//
// Trimming here is NORMALIZATION of legal legacy data, not repair of model
// output. Whitespace padding carries no content, so removing it changes
// nothing a reader can observe except the length -- unlike truncation, which
// would cut real characters off a value that was never too long. That is also
// why trimming must happen BEFORE clamping: clamp.text would otherwise count
// a padded-but-not-oversized value as shortened, telling a consumer content
// was lost when only whitespace was.
func storedText(value string) string { return strings.TrimSpace(value) }

// clamper shortens oversize values and COUNTS how many it shortened.
//
// Clamping used to be silent (codex round-5 R5-3): a legacy judgment twice
// the projection's length came back cut with truncated=false and no count,
// so a consumer had no way to know it was reading a shortened value.
// Shortening is a form of omission and is disclosed like one.
type clamper struct{ count int }

// text cuts a string to maxLength RUNES, never bytes: cutting mid-rune
// would emit invalid UTF-8, a worse failure than the length violation it
// prevents.
func (c *clamper) text(value string, maxLength int) string {
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	c.count++
	return string(runes[:maxLength])
}

// strings clamps a list's entries and its length, deduping AFTER the
// clamp, and reports how many entries were dropped.
//
// Post-clamp dedup is not optional (codex round-6 F2): two distinct legacy
// entries sharing a long prefix become identical once clamped, and the
// projection validator rejects duplicates -- so a valid stored row produced
// an invalid projection. Clamping first and deduping after makes the
// collision visible as what it is, a dropped entry.
//
// Dropping entries is disclosed too. A 50-entry legacy list silently
// becoming 32 left ProjectionBudget untruncated, so a consumer could not
// tell the list was cut.
func (c *clamper) strings(values []string, maxCount, maxLength int) ([]string, int) {
	// Canonical order: shorten, then dedupe, then truncate (codex round-8
	// F6). Truncating first threw away a later entry that would have
	// survived deduplication, and clamping before truncation counted
	// shortened values the caller never received. Counting must describe
	// what reached the wire, not what the algorithm touched on the way.
	//
	// Each entry carries whether IT was shortened, so the count survives
	// dedup and truncation (codex round-9 F4). The previous version indexed
	// the ORIGINAL slice by SURVIVOR position; once dedup dropped an entry
	// every later survivor was compared against an unrelated original, and a
	// survivor that had been shortened went uncounted. ValuesClamped
	// under-reported, and a consumer reads that field to decide whether what
	// it received is verbatim -- so under-reporting is the one direction
	// this count may never fail in.
	type clampedValue struct {
		value     string
		shortened bool
	}
	shortened := make([]clampedValue, 0, len(values))
	for _, value := range values {
		cut := truncateRunes(value, maxLength)
		shortened = append(shortened, clampedValue{value: cut, shortened: cut != value})
	}
	seen := make(map[string]struct{}, len(shortened))
	// Allocated, never nil: these feed required array members.
	deduped := make([]clampedValue, 0, len(shortened))
	for _, entry := range shortened {
		if _, exists := seen[entry.value]; exists {
			continue
		}
		seen[entry.value] = struct{}{}
		deduped = append(deduped, entry)
	}
	dropped := len(shortened) - len(deduped)
	if len(deduped) > maxCount {
		dropped += len(deduped) - maxCount
		deduped = deduped[:maxCount]
	}
	survivors := make([]string, 0, len(deduped))
	for _, entry := range deduped {
		// Count only survivors that were actually shortened: entries cut by
		// maxCount never reached the wire, and an entry that fit is verbatim.
		if entry.shortened {
			c.count++
		}
		survivors = append(survivors, entry.value)
	}
	return survivors, dropped
}

// truncateRunes cuts to maxLength RUNES, never bytes: a mid-rune cut emits
// invalid UTF-8, a worse failure than the length violation it prevents.
func truncateRunes(value string, maxLength int) string {
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength])
}

// projectCoverage reports each source's state once, bounded by the
// projection's own contract maximum.
//
// Coverage is never narrowed by the caller's budget -- a bounded consumer
// must always be able to see which sources were missing when it judges an
// answer -- but the canonical result allows 250 sources against the
// projection's 100, so the overflow is truncated and DECLARED rather than
// dropped in silence (codex round-1 F5).
func projectCoverage(result contractsv1.ContextFabricInvestigationResult, clamp *clamper) ([]contractsv1.ContextFabricProjectedCoverage, int) {
	seen := make(map[string]struct{}, len(result.Coverage.Sources))
	entries := make([]contractsv1.ContextFabricProjectedCoverage, 0, len(result.Coverage.Sources))
	omitted := 0
	for _, source := range result.Coverage.Sources {
		// Trimming can collapse two stored entries onto one name:
		// canonical uniqueness is checked BEFORE trimming, so a legacy
		// row can legitimately hold both " work_items " and "work_items"
		// with different states. Collapsing drops one source's state and
		// reason, so it is counted as an omission rather than vanishing
		// (codex round-6 F3).
		name := storedText(source.Source)
		if _, exists := seen[name]; exists {
			omitted++
			continue
		}
		seen[name] = struct{}{}
		if len(entries) >= contractsv1.ContextFabricProjectedCoverageMaxCount {
			omitted++
			continue
		}
		entries = append(entries, contractsv1.ContextFabricProjectedCoverage{
			Source: clamp.text(name, contractsv1.ContextFabricProjectedCoverageSourceMaxLength),
			State:  source.State,
			Reason: clamp.text(storedText(source.Reason), contractsv1.ContextFabricProjectedCoverageReasonMaxLength),
		})
	}
	return entries, omitted
}

// projectReceipts emits a continuation handle for every subject this result
// actually stood behind or proposed. An ambiguous or unresolved candidate
// gets no receipt: binding a follow-up turn to a subject the engine could
// not resolve would carry the ambiguity forward silently.
func projectReceipts(result contractsv1.ContextFabricInvestigationResult) []contractsv1.ContextFabricBoundSubjectReceipt {
	receipts := make([]contractsv1.ContextFabricBoundSubjectReceipt, 0, len(result.SubjectResolution.Candidates))
	seen := make(map[string]struct{}, len(result.SubjectResolution.Candidates))
	for _, candidate := range result.SubjectResolution.Candidates {
		if candidate.State != contractsv1.ContextFabricResolutionCommitted && candidate.State != contractsv1.ContextFabricResolutionProposed {
			continue
		}
		if _, exists := seen[candidate.ReceiptID]; exists {
			continue
		}
		if len(receipts) >= contractsv1.ContextFabricProjectedReceiptsMaxCount {
			break
		}
		seen[candidate.ReceiptID] = struct{}{}
		receipts = append(receipts, contractsv1.ContextFabricBoundSubjectReceipt{
			ResultID:  result.ResultID,
			ReceiptID: candidate.ReceiptID,
		})
	}
	return receipts
}

// copyStrings copies values as a NON-NIL slice. Required array members must
// never marshal to null, and append([]string(nil), empty...) returns nil.
func copyStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

// distinctStrings copies values, dropping exact repeats. Removing a
// duplicate loses no information, so it is not a declared drop.
func distinctStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	// Allocated, never nil: these feed required array members.
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// projectTemporal copies CHAOS-3781's temporal label, and copies it whole.
//
// It is never narrowed, clamped, or budgeted. What time an answer speaks
// for is not detail a consumer can trade away for a shorter response: an
// answer about March read as an answer about today is wrong, not merely
// abbreviated, so there is no budget under which dropping this is the
// right call.
//
// The pointer is deep-copied rather than shared because Project must not
// alias the caller's result -- see TestProjectDoesNotMutateTheCanonicalResult.
// ContextFabricTemporalLabel holds only value fields and time pointers, and
// the time pointers are themselves replaced here, so a shallow struct copy
// plus fresh instants is a complete one.
func projectTemporal(result contractsv1.ContextFabricInvestigationResult) *contractsv1.ContextFabricTemporalLabel {
	if result.Temporal == nil {
		return nil
	}
	label := *result.Temporal
	label.Requested = copyTimeContext(result.Temporal.Requested)
	label.Effective = copyTimeContext(result.Temporal.Effective)
	return &label
}

func copyTimeContext(source contractsv1.ContextFabricTimeContext) contractsv1.ContextFabricTimeContext {
	copied := contractsv1.ContextFabricTimeContext{Axis: source.Axis}
	if source.AsOf != nil {
		instant := *source.AsOf
		copied.AsOf = &instant
	}
	if source.Start != nil {
		instant := *source.Start
		copied.Start = &instant
	}
	if source.End != nil {
		instant := *source.End
		copied.End = &instant
	}
	return copied
}

// clampedNarrative is one narrative entry after clamping, carrying whether
// the clamp actually shortened it. Package-level rather than local to
// boundedNarrativeRetaining so retainPastTheCut can be a plain function
// over it: an earlier attempt used a generic helper reaching the value
// through an interface the type did not implement, which compiled and then
// silently retained nothing.
type clampedNarrative struct {
	value     string
	shortened bool
}

// retainPastTheCut moves the first dropped entry matching retain into kept,
// displacing kept's LAST entry, and returns the result. It is a no-op when
// nothing dropped matches or when a matching entry already survived.
//
// Only one entry is ever rescued, because only one is ever needed: the
// caller's predicate names a single disclosure and the list is already
// deduplicated by the time this runs.
func retainPastTheCut(kept, dropped []clampedNarrative, retain func(string) bool) []clampedNarrative {
	for _, entry := range kept {
		if retain(entry.value) {
			return kept
		}
	}
	for _, entry := range dropped {
		if !retain(entry.value) {
			continue
		}
		rescued := append([]clampedNarrative(nil), kept[:len(kept)-1]...)
		return append(rescued, entry)
	}
	return kept
}
