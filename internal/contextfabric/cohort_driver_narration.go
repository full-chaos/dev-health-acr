package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is CHAOS-4398 PR3b's own §5a narration: "narrate the cohort's
// ContextFabricDriverJudgment entries from the already-computed per-member
// ContextFabricCohortMemberDriver primitive"
// (docs/design/context-fabric-subject-model-and-cohort-answers.md §5a).
// cohortDriverNarrationBudget/selectMembersForDriverNarration decide WHICH
// members get narrated and how many drivers each; narrateCohortDriverJudgments
// builds the actual ContextFabricDriverJudgment entries, resolving each
// driver's ClaimedFactIDs closure from the SourceClaimedFactIDs RankCohort
// already minted (R4-style ruling: "the narration cites, it never mints" --
// see ContextFabricCohortMemberDriver.SourceClaimedFactIDs' own doc
// comment) -- never re-deriving or re-minting a citation here.

// cohortSignalDriverCategory maps each RankCohort signal-family name to its
// ContextFabricDriverCategory -- the SAME category
// ContextFabricDriverCategoryRequiresClaimedFact keys its FactKind
// requirement by, so a narrated judgment's ClaimedFactIDs closure (this
// file resolves from SourceClaimedFactIDs) is checked against the identical
// FactKind RankCohort minted the citation for. Mirrored here (this package
// cannot depend on contracts/v1 test-only helpers) the same cross-package
// discipline contextFabricCohortMemberDriverWeights already documents on
// the contracts/v1 side.
var cohortSignalDriverCategory = map[string]contractsv1.ContextFabricDriverCategory{
	RankingSignalInvestmentMix:      contractsv1.ContextFabricDriverCategoryInvestment,
	RankingSignalHealthRisk:         contractsv1.ContextFabricDriverCategoryHealth,
	RankingSignalDeficiencySeverity: contractsv1.ContextFabricDriverCategoryDeficiency,
	RankingSignalReadinessGap:       contractsv1.ContextFabricDriverCategoryReadiness,
	RankingSignalWorkloadPressure:   contractsv1.ContextFabricDriverCategoryWorkload,
}

// cohortSignalDisplayName is narration-prose ONLY (Title/Summary wording),
// never a wire value or a closed-vocabulary field -- a cosmetic label, not
// a citation.
var cohortSignalDisplayName = map[string]string{
	RankingSignalInvestmentMix:      "investment mix",
	RankingSignalHealthRisk:         "health risk",
	RankingSignalDeficiencySeverity: "operational deficiencies",
	RankingSignalReadinessGap:       "readiness gap",
	RankingSignalWorkloadPressure:   "workload pressure",
}

// narrateCohortDriverJudgments builds the §5a narrated ContextFabricDriverJudgment
// entries for a RankCohort-ranked cohort, given how many driver judgments
// synthesis already produced for everything else in the answer
// (synthesisDriverCount) and the signalCitation map RankCohort computed
// but did not mint (cohortMemberSignalCitations). Call AFTER
// SynthesizeAnswer returns and BEFORE the commit-affirmation gate
// (engine.go's own ordering comment at the call site explains why).
//
// Team-lead ruling (CHAOS-4398 PR3b): "minting follows citation, not
// ranking" -- a ClaimedFact is minted HERE, and ONLY for a driver this
// composer actually decides to narrate (top floor(available/3) members by
// AttentionRank, top-3 drivers per member by WeightContributed). A driver
// RankCohort ranked but this composer never narrates keeps
// SourceClaimedFactIDs empty, exactly as PR2 shipped it -- see
// cohortMemberSignalCitations' own doc comment for the byte-budget reason
// (unconditional minting measured ~708 KB worst-case for a 250-member
// fully-qualified cohort; citation-gated minting caps at
// <=16 members * 3 drivers = 48 claims regardless of cohort size).
//
// Every entry cites (team, signal) and introduces NO number absent from
// the member's own ContextFabricCohortMemberDriver: Title/Summary quote
// only Value/Weight/WeightContributed/Score, already on the driver/member;
// ClaimedFactIDs is exactly the ONE ClaimID minted for this citation;
// AffectedSubjects is exactly {member.Subject}.
//
// A member with no EvidenceRefIDs of its own is skipped (never narrated
// with a fabricated evidence reference) -- CHAOS-4398 PR3b judgment call,
// same "disclose the bound, do not silently drop, never emit invalid"
// pattern used throughout this design. Likewise a driver with no citation
// available (a producer-bug case; see scoreMember's own comment) is
// skipped, never fabricated. The highest-AttentionRank narrated member's
// single top driver (by WeightContributed) is Standing=principal; every
// other narrated driver is Standing=contributing -- design doc §5a's own
// recommendation for surviving a small projection-time render budget that
// truncates by Standing, not by cohort rank.
//
// The returned CohortDriverNarrationEvent is the standing-order telemetry
// for this emission (team-lead ruling): a closed outcome plus counts, no
// team name or score value -- the same content-safety discipline
// CohortRankedEvent already applies to RankCohort's own telemetry.
//
// Citation eligibility (team-lead ruling, refined by codex R1): a citation
// is a legitimate narration input ONLY when it names a field that exists
// on a REAL CanonicalFact this ranking pass actually read (citeFactField's
// own contract -- it reads fact.Fields[field] and returns nil if the field
// is absent, never invents one). deficiencySeveritySignal's available-zero
// exception is the one signal that can be available=true (a real ranking
// value) with NO real fact to cite at all -- OperationalDeficienciesProvider
// only ever emits a row for an actually-fired rule, so zero fired rules is
// zero rows, not a row with a zero-valued count field. That branch
// therefore returns a nil citation (see its own doc comment), and this
// composer's `if citation == nil { continue }` check below is what makes
// "ranks but is never cited" the enforced behavior, not just a comment.
// validateMintedClaimsGrounded (cohort_ranking.go) is the second,
// structural half of this same guarantee: every claim this composer DOES
// mint must also re-verify against the real fact bundle before
// engine.go appends it, so a future citation-construction bug (a signal
// that builds a signalCitation some way OTHER than citeFactField) cannot
// silently reintroduce an ungrounded claim the way the fired_rules_count
// case did.
//
// Grounding kind (codex R3, CHAOS-4448): every judgment this composer
// emits is produced by the RankCohort ranking heuristic, so it is an
// INFERENCE, never a canonical observation -- Derivation=rule_inferred
// and EpistemicStatus=inferred are ONE decision, not two independent
// fields. docs/design/context-fabric-result-semantics.md §1 exists to stop
// exactly this blur (an inference presented as an observed measurement),
// and genkitruntime/prompts.go instructs the model itself never to commit
// it; a server-side composer is held to the same bar. A future narration
// path deriving a judgment some OTHER way must set both fields to match
// that derivation, never inherit this pair.
//
// Null-vs-zero citation eligibility (team-lead ruling): distinct from the
// no-real-fact case above, a citation whose Value is an explicit
// Value.Null ("no evaluation happened at all") would NOT be a legitimate
// narration input at any Standing -- it could describe only a withheld
// judgment, never Principal/Contributing, because there is no real
// observation behind it to stand on. This composer does not implement
// that second branch today: no signal function in this package ever
// constructs a null-valued signalCitation (see signalCitation's own
// documentation) -- this is a documented, deliberate gap, not a silent
// one: a FUTURE signal path that introduces a null citation must also add
// the Standing=withheld routing this comment describes, not assume
// narrateCohortDriverJudgments already handles it.
func narrateCohortDriverJudgments(cohort *Cohort, synthesisDrivers []DriverJudgment, synthesisClaimedFactCount int, citations cohortMemberSignalCitations, allocation ItemAllocation) ([]DriverJudgment, []ClaimedFact, CohortDriverNarrationEvent) {
	if cohort == nil || !cohortHasAnyRankedDriver(cohort) {
		return nil, nil, CohortDriverNarrationEvent{Outcome: CohortDriverNarrationNoDrivers}
	}
	// CHAOS-5008. The contract caps below are a CEILING, never the budget:
	// they bound what the result document may legally carry, and satisfying
	// them says nothing about whether the ITEM budget can afford it. This
	// composer used to consult them alone, and would authorise 16 members x
	// 3 drivers = 48 judgments plus 48 minted claims -- 96 items, or 68 in
	// the measured fixture -- against a plan ceiling of 30, because 50 and
	// 250 were the only numbers it looked at.
	//
	// The allocator is now the authority and the caps are the second, weaker
	// bound. Both are applied and the SMALLER wins, so neither the item
	// budget nor the document contract can be overrun.
	membersToNarrate, driversPerMember := cohortDriverNarrationBudget(
		contractsv1.ContextFabricDriversMaxCount, len(synthesisDrivers),
		contractsv1.ContextFabricClaimedFactsMaxCount, synthesisClaimedFactCount,
	)
	allocator := CohortDriverNarrationAllocatorStaticCaps
	if allocation.MaxItems > 0 {
		allocator = CohortDriverNarrationAllocatorPlanBudget
		if allocated := allocation.NarrationDriverAllowance(driversPerMember); allocated < membersToNarrate {
			membersToNarrate = allocated
		}
	}
	if membersToNarrate == 0 {
		return nil, nil, CohortDriverNarrationEvent{
			Outcome:        CohortDriverNarrationBudgetExhausted,
			Allocator:      allocator,
			AllocatedItems: allocation.NarrationBudget,
		}
	}
	// selectMembersForDriverNarration returns copies in AttentionRank
	// order -- used here only to fix the NARRATION ORDER (so Standing=
	// principal lands on the truly highest-ranked qualifying member, not
	// just the first one pool order happens to reach); indexByCanonicalID
	// below is what lets this function MUTATE the real
	// cohort.Members[i].Drivers[j] entries the copies point back to.
	selected := selectMembersForDriverNarration(cohort.Members, membersToNarrate)
	indexByCanonicalID := make(map[string]int, len(cohort.Members))
	for i, member := range cohort.Members {
		indexByCanonicalID[member.Subject.CanonicalID] = i
	}

	// takenDriverIDs starts as every DriverID synthesis already produced,
	// then grows with each id this composer assigns -- validateDrivers
	// enforces uniqueness across the COMBINED result.Drivers array, so a
	// generated id must clear both sets, not just its own.
	takenDriverIDs := make(map[string]struct{}, len(synthesisDrivers)+membersToNarrate*driversPerMember)
	for _, driver := range synthesisDrivers {
		takenDriverIDs[driver.DriverID] = struct{}{}
	}

	var judgments []DriverJudgment
	var mintedClaims []ClaimedFact
	narratedMembers := 0
	skippedNoEvidence := 0
	driversSkipped := map[string]int{}
	principalAssigned := false
	for _, memberCopy := range selected {
		if len(memberCopy.EvidenceRefIDs) == 0 {
			skippedNoEvidence++
			continue
		}
		memberIndex := indexByCanonicalID[memberCopy.Subject.CanonicalID]
		member := &cohort.Members[memberIndex]
		driverIndices := topDriverIndicesByWeightContributed(member.Drivers, driversPerMember)
		memberJudgmentCount := 0
		for position, driverIndex := range driverIndices {
			driver := &member.Drivers[driverIndex]
			category, hasCategory := cohortSignalDriverCategory[driver.Signal]
			if !hasCategory {
				// defensively unreachable: every RankCohort signal has a category entry above
				driversSkipped[string(CohortDriverSkipUnknownSignal)]++
				continue
			}
			citation := citations[member.Subject.CanonicalID][driver.Signal]
			if citation == nil {
				// no citation to mint from -- never narrate without one
				// (the deficiency available-zero case, and scoreMember's
				// own producer-bug case). Behaviour unchanged; the
				// elimination is now on the record.
				driversSkipped[string(CohortDriverSkipNoCitation)]++
				continue
			}
			claimID := cohortDriverClaimID(member.Subject, driver.Signal, driver.Window)
			driver.SourceClaimedFactIDs = []string{claimID}
			mintedClaims = append(mintedClaims, ClaimedFact{
				ClaimID: claimID,
				Kind:    citation.kind,
				Subject: member.Subject,
				Field:   citation.field,
				Value:   convertFactValueScalar(citation.value),
			})

			standing := DriverContributing
			if !principalAssigned {
				standing = DriverPrincipal
				principalAssigned = true
			}
			driverID := deconflictCohortDriverJudgmentID(cohortDriverJudgmentID(*member, position), takenDriverIDs)
			takenDriverIDs[driverID] = struct{}{}
			judgments = append(judgments, DriverJudgment{
				DriverID:         driverID,
				Standing:         standing,
				Category:         string(category),
				Title:            cohortDriverJudgmentTitle(*member, *driver),
				Summary:          cohortDriverJudgmentSummary(*member, *driver),
				AffectedSubjects: []SubjectRef{member.Subject},
				EvidenceRefIDs:   member.EvidenceRefIDs,
				ClaimedFactIDs:   []string{claimID},
				Derivation:       DerivationRuleInferred,
				EpistemicStatus:  EpistemicInferred,
				Confidence:       cohortDriverJudgmentConfidence(*member),
				Current:          true,
			})
			memberJudgmentCount++
		}
		if memberJudgmentCount > 0 {
			narratedMembers++
		}
	}
	return judgments, mintedClaims, CohortDriverNarrationEvent{
		Outcome:                  CohortDriverNarrationEmitted,
		JudgmentsEmitted:         len(judgments),
		FactsMinted:              len(mintedClaims),
		MembersNarrated:          narratedMembers,
		MembersSkippedNoEvidence: skippedNoEvidence,
		DriversSkipped:           driversSkipped,
		Allocator:                allocator,
		AllocatedItems:           allocation.NarrationBudget,
	}
}

// cohortHasAnyRankedDriver reports whether ranking produced at least one
// member with at least one Driver -- distinguishes CohortDriverNarrationNoDrivers
// (nothing to narrate regardless of budget: every member was
// insufficient_evidence/not_applicable, or RankCohort never ran) from
// CohortDriverNarrationBudgetExhausted (real drivers exist, but synthesis
// alone used the whole shared budget).
func cohortHasAnyRankedDriver(cohort *Cohort) bool {
	for _, member := range cohort.Members {
		if len(member.Drivers) > 0 {
			return true
		}
	}
	return false
}

// CohortDriverNarrationOutcome is the closed vocabulary
// narrateCohortDriverJudgments' own telemetry event reports -- CHAOS-4398
// PR3b's standing-order requirement that this emission carry the SAME
// decision-basis-in-the-same-change discipline every other outcome-
// affecting branch in this codebase does (root AGENTS.md).
type CohortDriverNarrationOutcome string

const (
	// CohortDriverNarrationEmitted means at least an attempt was made with
	// both budget and real drivers available -- JudgmentsEmitted may still
	// be 0 if every candidate member lacked EvidenceRefIDs (MembersSkippedNoEvidence
	// names that count separately, never silently folded into "no drivers").
	CohortDriverNarrationEmitted CohortDriverNarrationOutcome = "emitted"
	// CohortDriverNarrationBudgetExhausted means real ranked drivers existed,
	// but synthesis alone already reached ContextFabricDriversMaxCount --
	// see cohortDriverNarrationBudget's own doc comment.
	CohortDriverNarrationBudgetExhausted CohortDriverNarrationOutcome = "budget_exhausted"
	// CohortDriverNarrationNoDrivers means RankCohort ranked zero members
	// with any Driver at all (every member insufficient_evidence/
	// not_applicable, or cohort was nil/never ranked) -- independent of
	// the synthesis budget.
	CohortDriverNarrationNoDrivers CohortDriverNarrationOutcome = "no_drivers"
)

// CohortDriverNarrationAllocator is the CLOSED vocabulary naming which budget
// bounded a narration.
//
// WHY IT EXISTS AT ALL. Before CHAOS-5008, narration consulted only the static
// contract caps (50 drivers / 250 claims) and would authorise 68 items against
// a 30-item ceiling. After the fix it takes the SMALLER of that ceiling and the
// allocator's item budget. Both paths can emit identical counts on a small
// cohort, so without this field a reader cannot tell a fixed narration from an
// unfixed one, and a regression would be invisible in the artifacts -- which is
// exactly the diagnosability bar the standing telemetry order sets.
type CohortDriverNarrationAllocator string

const (
	// CohortDriverNarrationAllocatorPlanBudget means the ONE allocator's
	// item budget bounded this narration. The post-CHAOS-5008 steady state.
	CohortDriverNarrationAllocatorPlanBudget CohortDriverNarrationAllocator = "plan_budget"
	// CohortDriverNarrationAllocatorStaticCaps means the static contract
	// caps bounded it, because no item budget was in force (an unbounded
	// plan). Legitimate, and distinct from the defect it used to hide.
	CohortDriverNarrationAllocatorStaticCaps CohortDriverNarrationAllocator = "static_caps"
)

var cohortDriverNarrationAllocators = [2]CohortDriverNarrationAllocator{
	CohortDriverNarrationAllocatorPlanBudget,
	CohortDriverNarrationAllocatorStaticCaps,
}

// CohortDriverNarrationAllocatorCount is the closed vocabulary's size.
const CohortDriverNarrationAllocatorCount = len(cohortDriverNarrationAllocators)

// CohortDriverNarrationAllocatorVocabulary returns it in published order.
func CohortDriverNarrationAllocatorVocabulary() [CohortDriverNarrationAllocatorCount]CohortDriverNarrationAllocator {
	return cohortDriverNarrationAllocators
}

// ValidCohortDriverNarrationAllocator reports membership; empty is not one.
func ValidCohortDriverNarrationAllocator(value CohortDriverNarrationAllocator) bool {
	for _, member := range cohortDriverNarrationAllocators {
		if member == value {
			return true
		}
	}
	return false
}

// CohortDriverNarrationEvent is narrateCohortDriverJudgments' content-safe
// telemetry payload -- counts and a closed outcome only, never a team name,
// a score, or narration prose (the same "no person-to-person rankings"
// team-to-team analogue CohortRankedEvent's own doc comment states).
type CohortDriverNarrationEvent struct {
	Outcome          CohortDriverNarrationOutcome
	JudgmentsEmitted int
	// Allocator names WHICH budget bounded this narration (CHAOS-5008).
	// It is the CONSUMER-side proof of the fix: the counts alone cannot
	// distinguish a narration the item budget bounded from one the static
	// contract caps bounded, and those are the two behaviours whose
	// difference is the whole point. A closed vocabulary, so a dashboard
	// can filter on it.
	Allocator CohortDriverNarrationAllocator
	// AllocatedItems is the item allowance the ONE allocator published for
	// narration, zero when no item budget was in force. Recorded beside
	// the counts so an operator can see the bound that applied rather than
	// inferring it from what was emitted.
	AllocatedItems int
	// FactsMinted (team-lead standing order) is how many ContextFabricClaimedFact
	// entries this call minted -- always <= JudgmentsEmitted (one claim per
	// narrated judgment, minting follows citation per this composer's own
	// doc comment), so this count is naturally bounded the same way
	// JudgmentsEmitted already is (<=16 members * 3 drivers = 48), never by
	// cohort size.
	FactsMinted              int
	MembersNarrated          int
	MembersSkippedNoEvidence int
	// DriversSkipped (codex R3, CHAOS-4448) counts drivers this composer
	// SELECTED into a member's top-3 and then eliminated, keyed by the
	// closed CohortDriverSkipReason that eliminated each -- root AGENTS.md's
	// decision-basis requirement: a candidate elimination must be
	// diagnosable from the run's own artifacts, never by re-reading source.
	// Without it, a member whose zero-fired deficiency driver ranked and was
	// dropped emits an event identical to one where that driver never
	// ranked at all. Counts keyed by a closed enum, the same content-safe
	// shape CohortRankedEvent.OutcomeCounts already uses -- no team name,
	// no signal value. Absent entries mean no elimination of that kind
	// happened, so a non-zero count always names a real one.
	DriversSkipped map[string]int
	// AnswerNarrativeRecomposed (CHAOS-4580) is true when the engine
	// replaced the pre-narration DirectJudgment/DeterministicAnswer with
	// recomposeCohortAnswerNarrative's output for THIS investigation --
	// i.e. at least one narrated judgment was emitted (see the engine
	// call site's guard). Recorded on the SAME event as every other
	// narration decision-basis field, per root AGENTS.md's same-change
	// telemetry requirement for a branch that changes the answer's own
	// wording.
	AnswerNarrativeRecomposed bool
}

// CohortDriverSkipReason is the closed vocabulary naming WHY a selected
// cohort driver was eliminated before narration.
type CohortDriverSkipReason string

const (
	// CohortDriverSkipNoCitation is the available-zero case: the driver
	// ranks legitimately (its zero Value counts toward Score) but no real
	// CanonicalFact field exists to cite, so it can never be narrated or
	// minted. deficiencySeveritySignal is the one signal that reaches it
	// today -- "ranks but never cites", enforced, not merely documented.
	CohortDriverSkipNoCitation CohortDriverSkipReason = "no_citation"
	// CohortDriverSkipUnknownSignal means a ranked driver carried a Signal
	// with no cohortSignalDriverCategory entry. Defensively unreachable
	// (every RankCohort signal has one), and counted precisely so that if
	// it ever does fire -- a new signal family added to ranking but not to
	// narration -- the run says so instead of quietly narrating less.
	CohortDriverSkipUnknownSignal CohortDriverSkipReason = "unknown_signal"
)

// topDriverIndicesByWeightContributed returns the INDICES (into drivers,
// the member's own original slice) of up to `count` drivers, ordered by
// WeightContributed descending (ties broken by Signal for determinism) --
// the same "top-N by weight_contributed" rule RankingTable's own row-driver
// selection uses (ranking_table.go), applied here to the narration budget
// instead of the Rows panel's fixed top-2. Indices, not copies, because the
// caller mutates drivers[index].SourceClaimedFactIDs in place when it
// mints a citation for that driver (team-lead ruling: minting happens
// here, at narration time, only for a cited driver).
func topDriverIndicesByWeightContributed(drivers []CohortMemberDriver, count int) []int {
	if count <= 0 || len(drivers) == 0 {
		return nil
	}
	indices := make([]int, len(drivers))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		da, db := drivers[indices[a]], drivers[indices[b]]
		if da.WeightContributed != db.WeightContributed {
			return da.WeightContributed > db.WeightContributed
		}
		return da.Signal < db.Signal
	})
	if len(indices) > count {
		indices = indices[:count]
	}
	return indices
}

// cohortDriverJudgmentID mints a deterministic DriverID naming this exact
// (member, narrated-driver-position) pair -- namespaced "cohort-driver-" so
// it can never collide with a model-minted DriverID (validateDrivers
// enforces uniqueness across the WHOLE result.Drivers array, narrated
// entries included).
func cohortDriverJudgmentID(member CohortMember, index int) string {
	return fmt.Sprintf("cohort-driver-%02d-%d", member.AttentionRank, index+1)
}

// deconflictCohortDriverJudgmentID returns base, or a deterministic
// variant of it when base is already taken.
//
// Codex R3 (CHAOS-4448): cohortDriverJudgmentID derives its id from the
// member's AttentionRank alone, and NOTHING forbids the synthesis model
// from authoring a driver_id in the same shape -- "cohort-driver-01-1" is
// a legal model output. validateDrivers enforces DriverID uniqueness
// across the whole result.Drivers array, so appending a colliding id
// rejected the ENTIRE investigation over a harmless naming coincidence.
// Dropping the narrated driver instead would be worse: a judgment the
// cohort earned would vanish because the model picked a string.
//
// The suffix is a sha256 digest of the base id, the same hashed,
// replay-stable scheme cohortDriverClaimID uses -- two passes over
// identical input always produce the same deconflicted id, so a stored
// result and a fresh run still agree. The attempt counter is part of the
// digest INPUT and also appended verbatim past the first candidate, which
// makes every candidate distinct by construction: with a finite taken set
// the loop is guaranteed to terminate rather than spin on an unlucky
// digest, so this can never silently drop a driver.
func deconflictCohortDriverJudgmentID(base string, taken map[string]struct{}) string {
	if _, clash := taken[base]; !clash {
		return base
	}
	for attempt := 0; ; attempt++ {
		digest := sha256.Sum256([]byte(base + "\x00" + strconv.Itoa(attempt)))
		candidate := base + "-" + hex.EncodeToString(digest[:])[:8]
		if attempt > 0 {
			candidate += "-" + strconv.Itoa(attempt)
		}
		if _, clash := taken[candidate]; !clash {
			return candidate
		}
	}
}

// cohortDriverJudgmentTitle/-Summary are the narration prose -- every
// number quoted is already on driver/member (Value, Weight,
// WeightContributed, Score), never re-derived or invented. Kept
// deliberately plain/templated (not natural-language-varied): this is a
// server-composed judgment, not model prose, and the design doc's own
// "no new numbers absent from the structured driver" bar is about the
// NUMBERS, not the wording style.
//
// Codex R2 (CHAOS-4398 PR3b): ContextFabricSubjectRef.Label permits up to
// 512 runes -- the SAME bound as ContextFabricDriverTitleMaxLength -- so
// "<Label>: <display name>" could exceed the title bound for a legal,
// near-max-length label, turning an otherwise-valid investigation into an
// ErrInvalidResult at the very last step (CHAOS-3755 M4's own "a composer
// must never let a rendered field grow past its contract bound" rule,
// applied here). truncateAtSentenceBoundary (model_runtime.go) is the
// established fix for exactly this shape elsewhere in this package --
// reused here rather than a bespoke truncator, with the same visible
// elision marker.
func cohortDriverJudgmentTitle(member CohortMember, driver CohortMemberDriver) string {
	return truncateAtSentenceBoundary(
		fmt.Sprintf("%s: %s", member.Subject.Label, cohortSignalDisplayName[driver.Signal]),
		contractsv1.ContextFabricDriverTitleMaxLength,
	)
}

// cohortDriverJudgmentSummary's template (CHAOS-4580, CHAOS-4533's narrator
// half): "<signal> (weight W, value V) contributed C of <label>'s S
// attention points." A prior version read "...contributed C points to
// <label>'s a S attention score.", which duplicated the possessive ("'s a")
// into a stray article and inverted "points"/"score" against the rest of
// the sentence -- fixed here rather than patched per-clause so the same
// grammar bug cannot recur in only one of the two spots that named a
// score. scoreText carries no possessive/article of its own; the sentence
// supplies exactly one "'s" and one noun ("attention points"), whether or
// not the member has a score.
func cohortDriverJudgmentSummary(member CohortMember, driver CohortMemberDriver) string {
	scoreText := "unranked"
	if member.Score != nil {
		scoreText = fmt.Sprintf("%.1f", *member.Score)
	}
	return fmt.Sprintf(
		"%s (weight %.0f, value %.2f) contributed %.1f of %s's %s attention points.",
		cohortSignalDisplayName[driver.Signal], driver.Weight, driver.Value, driver.WeightContributed, member.Subject.Label, scoreText,
	)
}

// cohortDriverJudgmentConfidence derives Confidence from the member's own
// DataCompleteness -- MORE families available means the member's ranking
// (and therefore this narrated driver's own standing within it) rests on a
// broader evidentiary base, not a re-derivation of Score itself. A fixed,
// closed 3-value mapping, never a model-invented number.
func cohortDriverJudgmentConfidence(member CohortMember) float64 {
	switch member.DataCompleteness {
	case CohortDataComplete:
		return 0.95
	case CohortDataPartial:
		return 0.85
	default: // CohortDataDegraded
		return 0.75
	}
}

// cohortDriverNarrationBudget is design doc §5a's "Ordering problem"
// section turned into a testable formula.
//
// RankCohort (Score/RankingBasis/DataCompleteness) runs BEFORE
// SynthesizeAnswer, unchanged. Cohort DRIVER JUDGMENT emission (this
// function's caller) must run AFTER SynthesizeAnswer returns, because
// nothing before that point knows how many non-cohort driver judgments
// (status, blockers, health, etc.) the model's own synthesis pass actually
// produced -- a fixed reservation is not a safety guarantee, since the
// synthesis contract permits up to maxDrivers total and a legitimate
// synthesis pass combined with a reservation-sized cohort emission can
// still exceed it when appended together.
//
// Codex R1 (CHAOS-4398 PR3b) caught this same reasoning applying to
// ContextFabricClaimedFactsMaxCount, not just maxDrivers: a synthesis
// draft can validly carry up to 250 ClaimedFacts on its own (model-
// authored, ValidateAgainst-checked before this composer ever runs), and
// each narrated driver mints exactly one MORE claim -- so bounding only
// on driver slots let a legitimate synthesis output plus a legitimate
// cohort narration TOGETHER exceed the claimed-facts bound and fail
// result.Validate() post-hoc, turning a valid cohort investigation into a
// rejected one. availableDrivers and availableClaims are therefore both
// computed from ACTUAL synthesis output (never a guess) and the SMALLER
// of the two gates membersToNarrate -- exactly the same "disclose the
// bound, do not silently overrun it" posture applied to a second,
// independently-exhaustible resource.
//
// available = min(maxDrivers-synthesisDriverCount,
// maxClaimedFacts-synthesisClaimedFactCount). membersToNarrate =
// floor(available / 3) (top-3 drivers per narrated member, one claim per
// narrated driver), capped at 16 as an upper bound even if more budget is
// technically available -- a Rows table with drivers for 40+ teams is not
// a more useful answer, and today's maxDrivers=50 means the natural
// floor(available/3) never exceeds 16 anyway (50/3 = 16.67), so the cap is
// forward-looking defense against a future maxDrivers increase, not a
// bound that binds today.
//
// A non-positive available (synthesis alone reached or exceeded EITHER
// budget) returns (0, 0): zero members get narrated drivers this turn,
// matching the same "disclose the bound, do not silently drop" pattern
// Cohort.Truncated already uses for membership -- narrated drivers are a
// bonus on top of the Rows table (Score/RankingBasis/DataCompleteness),
// never a requirement for the table to render.
func cohortDriverNarrationBudget(maxDrivers, synthesisDriverCount, maxClaimedFacts, synthesisClaimedFactCount int) (membersToNarrate, driversPerMember int) {
	const driversPerNarratedMember = 3
	const maxNarratedMembers = 16

	availableDrivers := maxDrivers - synthesisDriverCount
	availableClaims := maxClaimedFacts - synthesisClaimedFactCount
	available := availableDrivers
	if availableClaims < available {
		available = availableClaims
	}
	if available <= 0 {
		return 0, 0
	}
	membersToNarrate = available / driversPerNarratedMember
	if membersToNarrate > maxNarratedMembers {
		membersToNarrate = maxNarratedMembers
	}
	if membersToNarrate == 0 {
		return 0, 0
	}
	return membersToNarrate, driversPerNarratedMember
}

// selectMembersForDriverNarration returns the cohort members that should
// receive narrated driver judgments this turn: the `count` highest-Score
// members RankCohort ranked (AttentionRank 1..count -- RankCohort's own
// score-order, ties already broken by pool order at ranking time), in
// AttentionRank order. A member RankCohort never ranked (RankingComputed
// false), or one ranked below the cutoff, is not returned here -- it keeps
// its own Score/RankingBasis/DataCompleteness (unaffected by this
// function) but receives zero narrated ContextFabricDriverJudgment
// entries this turn.
func selectMembersForDriverNarration(members []CohortMember, count int) []CohortMember {
	if count <= 0 || len(members) == 0 {
		return nil
	}
	selected := make([]CohortMember, 0, count)
	for rank := 1; rank <= count; rank++ {
		for _, member := range members {
			if member.RankingComputed && member.AttentionRank == rank {
				selected = append(selected, member)
				break
			}
		}
	}
	return selected
}

// recomposeCohortAnswerNarrative rewrites DirectJudgment and
// DeterministicAnswer for a cohort answer, once narrateCohortDriverJudgments
// has appended its per-member judgments to result.Drivers.
//
// CHAOS-4690 (chris's language principle, settled design §5, sol r1 F1
// rework) deliberately REVERSES CHAOS-4580's splice: that ticket had this
// function append every PRINCIPAL driver's narrated Summary sentence --
// scoring arithmetic ("(weight 15, value 1.00) contributed 20.0 of
// Fullchaos's 46.7 attention points.") -- onto DeterministicAnswer. Under
// the settled principle, deterministic server prose composes NO scoring
// arithmetic and introduces no new deterministic display language; the
// numbers stay exactly where they already live structurally, on each
// driver's own Weight/Value/WeightContributed fields and its
// cohortDriverJudgmentSummary text (unchanged, still populated on the
// DriverJudgment entries themselves) -- never re-spliced into the lead
// prose. The client's regex prose-parser that once scraped this splice back
// out of DeterministicAnswer dies with it (sibling rip-out ticket deletes
// prose-detail.ts): there is nothing left in the lead for it to parse.
//
// Both fields now compose to the SAME content: the bare status sentence,
// independently truncated at each field's own bound.
// ContextFabricInvestigationResult.Validate() requires a non-empty
// DirectJudgment for an answer-capable (complete/partial) status, so it
// cannot be cleared outright the way CurrentState's own "no canonical
// facts" fallback can; the status sentence is the one content every
// investigation composes regardless of drivers, so it is what both fields
// share now that neither carries a driver clause.
//
// Only ever called when the cohort produced at least one narrated
// principal driver (see the engine call site's guard) -- a non-cohort
// (single-subject) investigation never reaches this function, so its
// DirectJudgment/DeterministicAnswer composition is completely untouched.
func recomposeCohortAnswerNarrative(status InvestigationStatus, resolution SubjectResolution) (directJudgment, deterministicAnswer string) {
	sentence := statusSentence(status, resolution)
	directJudgment = truncateAtSentenceBoundary(sentence, directJudgmentMaxLength)
	deterministicAnswer = truncateAtSentenceBoundary(sentence, deterministicAnswerMaxLength)
	return directJudgment, deterministicAnswer
}
