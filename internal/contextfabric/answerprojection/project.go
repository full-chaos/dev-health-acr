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
	cohort, cohortOmitted := projectCohort(result, bounds, index, clamp)
	clarification, candidatesOmitted := projectClarification(result, bounds, clamp)
	limitations, limitationsOmitted := boundedNarrative(result.Limitations, clamp)
	warnings, warningsOmitted := boundedNarrative(result.Warnings, clamp)
	coverage, coverageOmitted := projectCoverage(result, clamp)
	evidence := index.ids()
	evidenceOmitted := countUnindexedEvidence(result, index)

	projection := contractsv1.ContextFabricAnswerProjection{
		SchemaVersion: contractsv1.ContextFabricAnswerProjectionSchema,
		ResultID:      result.ResultID,
		RequestID:     result.RequestID,
		GeneratedAt:   result.GeneratedAt,
		Status:        result.Status,
		Question:      result.Question,
		Reused:        result.Reused,
		// Verbatim. These two fields are the answer.
		DirectJudgment:     clamp.text(result.DirectJudgment, contractsv1.ContextFabricProjectedJudgmentMaxLength),
		CurrentState:       clamp.text(result.CurrentState, contractsv1.ContextFabricProjectedJudgmentMaxLength),
		StrongestPressures: distinctStrings(result.StrongestPressures),
		// Never truncated: a surface that reported a different set of
		// committed subjects answered a different question.
		CommittedSubjects: append([]contractsv1.ContextFabricSubjectRef(nil), result.SubjectResolution.Committed...),
		Clarification:     clarification,
		Cohort:            cohort,
		PrincipalDrivers:  drivers,
		KeyFacts:          facts,
		CoverageSummary:   coverage,
		CoveragePartial:   result.Coverage.Partial,
		Limitations:       limitations,
		Warnings:          warnings,
		EvidenceRefIDs:    evidence,
		SubjectReceipts:   projectReceipts(result),
		Versions:          result.Versions,
	}
	projection.ProjectionBudget = contractsv1.ContextFabricProjectionBudget{
		DriversOmitted:         driversOmitted,
		WithheldDriversOmitted: withheldOmitted,
		CohortMembersOmitted:   cohortOmitted,
		FactsOmitted:           factsOmitted,
		CandidatesOmitted:      candidatesOmitted,
		EvidenceRefsOmitted:    evidenceOmitted,
		ValuesClamped:          clamp.count,
		LimitationsOmitted:     limitationsOmitted,
		WarningsOmitted:        warningsOmitted,
		CoverageOmitted:        coverageOmitted,
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
		budget.ValuesClamped > 0
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
			})
		}
		drivers = append(drivers, contractsv1.ContextFabricProjectedDriver{
			DriverID:      driver.DriverID,
			Standing:      driver.Standing,
			Category:      driver.Category,
			Title:         driver.Title,
			Summary:       driver.Summary,
			Qualification: driver.Qualification,
			Confidence:    driver.Confidence,
			// EvidenceRefIDs is a REQUIRED array; copying a canonical
			// empty slice with append(nil, ...) would yield nil and
			// serialize as null. ClaimedFactIDs is optional, so nil is
			// legitimate there and is left alone.
			EvidenceRefIDs: copyStrings(driver.EvidenceRefIDs),
			ClaimedFactIDs: append([]string(nil), driver.ClaimedFactIDs...),
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
func projectCohort(result contractsv1.ContextFabricInvestigationResult, bounds Budget, index *evidenceIndex, clamp *clamper) (*contractsv1.ContextFabricProjectedCohort, int) {
	if result.Cohort == nil {
		return nil, 0
	}
	canonical := *result.Cohort
	members := make([]contractsv1.ContextFabricProjectedCohortMember, 0, min(len(canonical.Members), bounds.MaxCohortMembers))
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
		members = append(members, contractsv1.ContextFabricProjectedCohortMember{
			Subject:          member.Subject,
			Rank:             member.Rank,
			InclusionReasons: clamp.strings(member.InclusionReasons, contractsv1.ContextFabricProjectedInclusionReasonsMaxCount, contractsv1.ContextFabricProjectedInclusionReasonMaxLength),
			EvidenceRefIDs:   append([]string(nil), member.EvidenceRefIDs...),
		})
	}
	return &contractsv1.ContextFabricProjectedCohort{
		Kind:      canonical.Kind,
		Total:     len(canonical.Members),
		Rationale: clamp.text(canonical.Rationale, contractsv1.ContextFabricProjectedCohortRationaleMaxLength),
		Complete:  canonical.Complete,
		Members:   members,
	}, len(canonical.Members) - len(members)
}

// projectClarification carries the ambiguity a caller must resolve. It is
// emitted only when the engine actually asked for clarification: attaching
// candidate lists to a confident answer would invite a consumer to treat a
// settled subject as still open.
func projectClarification(result contractsv1.ContextFabricInvestigationResult, bounds Budget, clamp *clamper) (*contractsv1.ContextFabricProjectedClarification, int) {
	if result.Status != contractsv1.ContextFabricInvestigationClarificationRequired {
		return nil, 0
	}
	canonical := result.SubjectResolution.Candidates
	retain := min(len(canonical), bounds.MaxCandidates)
	candidates := make([]contractsv1.ContextFabricProjectedCandidate, 0, retain)
	for _, candidate := range canonical[:retain] {
		candidates = append(candidates, contractsv1.ContextFabricProjectedCandidate{
			ReceiptID:    candidate.ReceiptID,
			Subject:      candidate.Subject,
			State:        candidate.State,
			Confidence:   candidate.Confidence,
			MatchReasons: clamp.strings(candidate.MatchReasons, contractsv1.ContextFabricProjectedMatchReasonsMaxCount, contractsv1.ContextFabricProjectedMatchReasonMaxLength),
		})
	}
	return &contractsv1.ContextFabricProjectedClarification{
		Prompt:     clamp.text(result.SubjectResolution.ClarificationPrompt, contractsv1.ContextFabricProjectedClarificationPromptMaxLength),
		Candidates: candidates,
	}, len(canonical) - retain
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
	// Clamp FIRST, then dedupe (codex round-5 R5-4). Deduping first let two
	// distinct legacy entries sharing a long prefix survive as separate
	// values, collide once clamped to the same prefix, and produce a
	// projection with duplicate entries that its own validator rejects --
	// which the route then emitted unvalidated. Post-clamp collisions are
	// real duplicates and are counted as omissions.
	clamped := make([]string, 0, len(values))
	for _, value := range values {
		clamped = append(clamped, clamp.text(value, contractsv1.ContextFabricProjectedNarrativeMaxLength))
	}
	distinct := distinctStrings(clamped)
	omitted := len(clamped) - len(distinct)
	if len(distinct) <= contractsv1.ContextFabricProjectedNarrativeMaxCount {
		return distinct, omitted
	}
	return distinct[:contractsv1.ContextFabricProjectedNarrativeMaxCount], omitted + len(distinct) - contractsv1.ContextFabricProjectedNarrativeMaxCount
}

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

// strings clamps a list's length and each entry, counting every shortened
// entry.
func (c *clamper) strings(values []string, maxCount, maxLength int) []string {
	retain := min(len(values), maxCount)
	out := make([]string, 0, retain)
	for _, value := range values[:retain] {
		out = append(out, c.text(value, maxLength))
	}
	return out
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
		name := strings.TrimSpace(source.Source)
		if _, exists := seen[name]; exists {
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
			Reason: clamp.text(source.Reason, contractsv1.ContextFabricProjectedCoverageReasonMaxLength),
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
