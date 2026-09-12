package contextfabric

import (
	"context"
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4636: synthesis and post-synthesis assembly, lifted out of
// Investigate so it can be RUN TWICE.
//
// Why it must be re-runnable rather than trimmable. Design §6.3a records
// three earlier specifications of the budget's terminal stage, and the third
// tried to TRIM the composed answer. That is wrong in two independent ways:
// the engine cannot reach the route's encoder (import cycle), and trimming in
// the route is worse still, because by then render selection, validation and
// persistence have run -- so dropping a driver can orphan a render-shape
// point that cites it and make the STORED and the SERVED answer diverge.
// Trimming was never sufficient anyway: drivers and findings carry no rank,
// the gate also charges candidates and claimed facts, and any trim leaves the
// already-composed prose describing content that is no longer there.
//
// Re-synthesizing with a smaller input has the property trimming cannot:
// prose, evidence closure, grounding, minted claims and render shapes are all
// REGENERATED CONSISTENTLY, because they are produced together.
//
// THREE THINGS MAKE A SECOND CALL SAFE, and none of them is the extraction
// itself. Each was verified in the code rather than assumed:
//
//  1. Resolution must be DEEP-COPIED. applyCommitAffirmation downgrades a
//     retracted candidate's State in place
//     (chaos4085_commit_affirmation.go's own loop over
//     result.SubjectResolution.Candidates), and that backing array is shared
//     with Investigate's `resolution`. Without a copy, a second pass starts
//     from the first pass's dirtied candidate states and re-runs the
//     retraction against a resolution that no longer describes what the graph
//     returned.
//  2. The cohort must be DEEP-COPIED. narrateCohortDriverJudgments stamps
//     SourceClaimedFactIDs onto cohort.Members[i].Drivers[j] in place -- its
//     own doc comment says so -- and result.Cohort is a pointer ALIAS to the
//     same cohort, not a copy.
//  3. EVERY per-investigation decision event this function produces is
//     DEFERRED, not emitted. All of them. That is a CLASS rule, and it is
//     written as one because fixing a single member of it was not enough:
//     round 2 found the commit-affirmation retraction double-emitting on a
//     retry, I deferred that one emitter, and round 3 immediately found two
//     more in the same function (window canonicalization and cohort driver
//     narration) doing exactly the same thing. The defect was never "this
//     emitter"; it was "this function emits, and it runs twice".
//
//     So: an emission added to this function in future MUST go into
//     assemblyTelemetry and be emitted by the caller. The whole point of
//     `assemblyTelemetry` being a struct rather than a bool is that adding a
//     field is the visible, reviewable act of declaring a new deferred event.
//
//     Deferral rather than labelling, because the first pass's result is
//     DISCARDED: its window outcome, its narration and its retraction never
//     happened as far as the caller is concerned, and a labelled-but-present
//     event still has to be filtered by every consumer of the counter.

// synthesisAssemblyParams is every value the assembly reads from
// Investigate's scope. It is a struct rather than a parameter list so that
// adding an input is a visible change to this stage's contract.
type synthesisAssemblyParams struct {
	Request        InvestigationRequest
	Interpretation InterpretedQuestion
	// Frame is this turn's validated QuestionFrame, nil when none
	// validated. CARRIED (CHAOS-4736 bar 5), never re-derived: the retry
	// pass finalizes through the same render-shape selection the first
	// pass did, and selecting on a different frame -- or on none -- would
	// let a re-synthesized answer draw a different chart than the one the
	// budget measured.
	Frame                   *QuestionFrame
	Graph                   GraphContext
	Facts                   CanonicalFactBundle
	Resolution              SubjectResolution
	CohortSignalCitations   cohortMemberSignalCitations
	EffectiveWindow         *contractsv1.ContextFabricEffectiveEvidenceWindow
	WindowCanon             requestWindowCanonicalization
	WindowCarry             windowCarryResult
	StructureCanon          requestStructureCanonicalization
	CarriedStructureEntries []*ConfirmedStructureEntry
	CommitBases             CommitBasisSet
	CommitDigests           CommitDecisionDigestSet
	// GroupedNarrowingBasis is which grouped order (if any) already shaped
	// Graph.Cohort before assembly ran -- stage 2's own selection, carried
	// forward so stage 3's "fits" event (which measures this cohort as-is,
	// having narrowed nothing itself) can report the order that actually
	// produced it instead of a stale default (codex round 3, EXECUTED: a
	// grouped cohort narrowed by overlap_aware_set_cover at stage 2, that
	// then fits without a stage-3 narrowing, reported
	// largest_group_round_robin). Zero value when stage 2 narrowed nothing.
	GroupedNarrowingBasis contractsv1.ContextFabricNarrowingBasis
	// GroupingRefusal is a grouping refusal this request already took, carried
	// so the ANSWER can disclose it. Zero on every request that grouped, and
	// on every request that never planned a group axis.
	GroupingRefusal CohortGroupingOutcome
	// GroupReadDisclosure and GroupReadKind are what the served document
	// must say about the group read (round 2, P1-3), carried like
	// GroupingRefusal so the ANSWER discloses it.
	GroupReadDisclosure GroupReadDisclosure
	GroupReadKind       SubjectKind
	// Plan is the answer plan this pass was budgeted against. Carried so the
	// ONE allocator can be derived HERE, from the same plan every other
	// spender reads, rather than each spender consulting a ceiling of its
	// own -- which is exactly how narration came to charge the static
	// contract caps while the budget charged something else.
	Plan AnswerPlan
	// Allocation is the ONE allocation this pass is budgeted against, derived
	// where these params are built and CARRIED to every reader.
	//
	// It is a field rather than a per-reader `AllocateItems` call because a
	// keystone review found the alternative in the wild: synthesis derived one
	// here and stage three derived another from the same inputs. They agreed on
	// every honest input -- AllocateItems is pure -- so nothing noticed, until a
	// producer-local corruption of synthesis's copy sailed past a guard that was
	// re-deriving a replacement. One derivation, one object, every reader.
	//
	// The RETRY re-allocates deliberately, and that is not a second authority:
	// it is a different cohort, so it is a different budget.
	Allocation ItemAllocation
	// Retry marks the SECOND pass. It exists so the doubled emissions above
	// are attributable rather than silent.
	Retry bool
}

// snapshot deep-copies every value a pass mutates through a shared backing
// array, and MUST be taken BEFORE the first pass runs.
//
// Copying at retry-prep time was the defect: by then pass one had already
// dirtied the source, so the "deep copy" faithfully copied corrupted state.
// The rule this encodes is ordering, not existence -- the copy always existed.
// Two fields are affected and BOTH were dirtied, though only one was reported:
//
//   - Resolution: applyCommitAffirmation demotes a retracted candidate IN
//     PLACE, through the array result.SubjectResolution shares with this.
//     Consequence: a retry that re-affirms the subject serves it in Committed
//     while reporting its candidate state as `proposed`.
//   - Graph.Cohort: narrateCohortDriverJudgments stamps SourceClaimedFactIDs
//     onto member drivers IN PLACE. Idempotent in practice (the claim ids are
//     deterministic), which is exactly why it went unnoticed -- but relying on
//     an accident of idempotency is not the same as taking the copy in time.
//
// Taking ONE snapshot before pass one makes the ordering structural: stage 3
// cannot narrow or retry from anything but pristine state, because dirtied
// state is no longer reachable from it.
func (p synthesisAssemblyParams) snapshot() synthesisAssemblyParams {
	clean := p
	clean.Resolution = copySubjectResolutionForRetry(p.Resolution)
	clean.Graph.Cohort = copyCohortForRetry(p.Graph.Cohort)
	clean.Graph.Resolution = clean.Resolution
	return clean
}

// forRetry returns params with every value a second pass would otherwise
// mutate through a shared backing array replaced by a copy.
//
// The copies are explicit rather than relying on GraphContext and
// SubjectResolution being passed by value: they are structs, so the copy is
// SHALLOW, and the slices underneath are exactly what bites.
//
// THE ALLOCATION IS A PARAMETER, and that is the whole point of this signature.
//
// It used to ride through on `retry := p`, so the retry's synthesis SPENT the
// first pass's grants -- written for the un-narrowed cohort -- while stage
// three derived the narrowed cohort's allocation AFTER synthesis and measured
// against that one. Consumed and validated were two different documents, which
// is the same defect a keystone review found in the first pass and 91408cc1
// fixed there and only there.
//
// The lesson that shapes this signature: that first fix pinned the first pass
// by COUNTING derivations, and this recurrence was introduced by OMISSION --
// nobody wrote a second derivation, they merely failed to carry the right one
// through a struct copy, which a counting pin cannot see. Requiring the
// allocation here makes the omission a compile error, so a future retry path
// must state which allocation its document is produced under rather than
// silently inheriting one.
func (p synthesisAssemblyParams) forRetry(graph GraphContext, facts CanonicalFactBundle, allocation ItemAllocation) synthesisAssemblyParams {
	retry := p
	retry.Retry = true
	retry.Graph = graph
	retry.Facts = facts
	retry.Allocation = allocation
	retry.Resolution = copySubjectResolutionForRetry(p.Resolution)
	retry.Graph.Cohort = copyCohortForRetry(graph.Cohort)
	retry.Graph.Resolution = retry.Resolution
	return retry
}

// copySubjectResolutionForRetry copies the two slices a pass mutates.
//
// NIL-PRESERVING, and that is not a nicety. `append([]T(nil), empty...)`
// returns NIL, not an empty slice -- so a naive copy turns a non-nil empty
// Candidates/Committed into nil, and the result contract distinguishes the
// two: validate_context_fabric_result.go rejects a nil Committed because "no
// subject committed" and "this field was never populated" are different
// statements. A subjectless cohort has exactly that shape, so the naive copy
// made every retried cohort answer fail validation.
func copySubjectResolutionForRetry(resolution SubjectResolution) SubjectResolution {
	copied := resolution
	copied.Candidates = copySlicePreservingEmpty(resolution.Candidates)
	copied.Committed = copySlicePreservingEmpty(resolution.Committed)
	return copied
}

// copySlicePreservingEmpty copies a slice, keeping nil nil and keeping a
// non-nil empty slice non-nil.
func copySlicePreservingEmpty[T any](source []T) []T {
	if source == nil {
		return nil
	}
	copied := make([]T, len(source))
	copy(copied, source)
	return copied
}

// copyCohortForRetry copies the cohort down to the per-member driver slices,
// which is the depth narrateCohortDriverJudgments writes at.
func copyCohortForRetry(cohort *Cohort) *Cohort {
	if cohort == nil {
		return nil
	}
	copied := *cohort
	copied.Members = copySlicePreservingEmpty(cohort.Members)
	for index := range copied.Members {
		copied.Members[index].Drivers = copySlicePreservingEmpty(cohort.Members[index].Drivers)
	}
	copied.Groups = copySlicePreservingEmpty(cohort.Groups)
	copied.Exclusions = copySlicePreservingEmpty(cohort.Exclusions)
	return &copied
}

// synthesizeAndAssemble runs synthesis and every post-synthesis composer, up
// to but NOT including render-shape selection, completeness stamping,
// validation and persistence -- those run once, on whichever pass produced
// the answer that is actually served.
// synthesizeAndAssemble RETURNS THE ALLOCATION IT CONSUMED, and that return
// value is the invariant this seam now rests on.
//
// The rule (team-lead, 2026-09-06): *the allocation a pass CONSUMED is the value
// the producer RETURNS; the guard measures the returned value; no consumer holds
// a private copy the guard cannot see.*
//
// Three fixes reached this the long way. Each closed one instance and left the
// class open, because each stopped at "derive it once and pass it in" -- and
// derive-once still lets a producer alias the value into a local that the guard
// never sees. `ItemAllocation` is a VALUE type and `params` is by-value, so no
// fault inside this function can reach a guard in the caller: reading a
// different FIELD cannot fix that, and only returning what was spent can.
// Sites: the first-pass re-derivation (keystone #3), the retry inheriting the
// first pass's grants (keystone #4), and narration spending a re-copied local
// (keystone #5).
// The fifth return is THE cardinality for this pass, computed before synthesis
// and carried out so nothing downstream computes a second one. See the
// `membership_cardinality` block below for why it is computed where it is.
func (e *Engine) synthesizeAndAssemble(ctx context.Context, principal storage.Principal, params synthesisAssemblyParams) (InvestigationResult, ItemAllocation, assemblyTelemetry, MembershipCardinality, error) {
	// pending holds every per-investigation decision event this pass
	// produces. NOTHING here emits -- see point 3 in this file's header.
	var pending assemblyTelemetry
	request := params.Request
	interpretation := params.Interpretation
	graphContext := params.Graph
	facts := params.Facts
	resolution := params.Resolution
	cohortSignalCitations := params.CohortSignalCitations
	effectiveWindow := params.EffectiveWindow
	windowCanon := params.WindowCanon
	windowCarry := params.WindowCarry
	structureCanon := params.StructureCanon
	carriedStructureEntries := params.CarriedStructureEntries
	commitBases := params.CommitBases
	commitDigests := params.CommitDigests

	// The SAME allocator every other spender reads, derived ONCE where these
	// params were built and CARRIED here. A second derivation at the prompt
	// site, inside narration, or in stage three is exactly how one budget came
	// to have two authorities -- and stage three did precisely that until a
	// keystone review injected a fault into this copy and watched the guard
	// validate the other one.
	// THE one allocation this pass consumes, and the one it RETURNS. Every
	// consumer below -- synthesis at the call just under this, narration further
	// down -- reads THIS identifier, and the caller measures what comes back, so
	// a fault applied to it is spent and seen by the same object. A second
	// binding of `params.Allocation` anywhere below would recreate exactly the
	// private copy this return exists to abolish.
	synthesisAllocation := params.Allocation
	// THE `membership_cardinality` STEP, RUN BEFORE SYNTHESIS AND ONCE.
	//
	// It used to run in finalizeResult, after the model had already answered.
	// That was right about WHICH member set it must describe -- the one the
	// served document carries -- and it made the number unusable for anything
	// the answer itself is built from: a value that does not exist until after
	// synthesis cannot be handed to synthesis, and cannot be minted into the
	// document's own claims without a second, later computation that could
	// disagree with this one.
	//
	// Computing it HERE keeps the correctness argument intact rather than
	// trading it away. The cohort this reads is `params.Graph.Cohort`, which IS
	// the member set this pass will serve: stage 3 does not narrow a document in
	// place, it narrows the graph and re-enters this function, so a narrowed
	// answer is a NEW pass that recomputes against its own cohort. The number
	// therefore always describes the document it travels with, and there is
	// exactly one of it per pass.
	//
	// The second return is discarded because it now rides INSIDE the value as
	// `Resolved` -- see that field's own doc comment for why a pair that can be
	// carried separately across four boundaries is a pair that can be carried
	// inconsistently. An unresolved cardinality is still an absence, never a
	// count of zero.
	cardinality, _ := ComputeMembershipCardinality(params.Graph.Cohort, params.Graph.CohortPopulation, params.Plan.Narrowing)
	result, err := e.synthesizer.Synthesize(ctx, principal, SynthesisInput{
		Allocation: synthesisAllocation,
		Request:    request, Interpretation: interpretation, Graph: graphContext, Facts: facts,
	})
	if err != nil {
		return InvestigationResult{}, synthesisAllocation, assemblyTelemetry{}, MembershipCardinality{}, stageError(StageSynthesis, fmt.Errorf("synthesize investigation: %w", err))
	}
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = e.newResultID()
	result.RequestID = request.RequestID
	result.GeneratedAt = e.now().UTC()
	// Codex round-1 F8: explicit, not merely the zero value -- a
	// Synthesizer implementation that (incorrectly) set Reused=true on
	// its returned draft must not have that survive into a genuinely
	// fresh result. Reused=true is ONLY ever valid on the exact object
	// tryReuse returns.
	result.Reused = false
	result.Question = request.Question
	result.Interpretation = interpretation
	result.SubjectResolution = resolution
	// Codex round-1 F4, per the orchestrator's ruling: a retrieval mechanism
	// that was unavailable for THIS resolution is folded into the answer here,
	// at the engine, rather than by inventing a path from ResolveSubjects into
	// the graph adapter's own Coverage construction. ResolveSubjects reports
	// the request-scoped marker; the engine owns what an answer says about
	// itself.
	//
	// The limitation string is FIXED and non-interpolated. It names no
	// mechanism, no provider, no model, and no error text: a limitation is
	// answer-facing prose, and every cause here (an embed timeout, an
	// unreachable embedder, a server that served the wrong model, a fenced-off
	// stale index) has the same consequence for a reader -- retrieval saw less
	// than it should have. The operator-facing detail belongs in telemetry,
	// which already receives it.
	if resolution.RetrievalDegraded {
		// Deduplicated across BOTH spellings, not by exact equality: a draft
		// that already carries either form must not gain a second, differently
		// worded copy of the same statement. At the contract's cap the last
		// model-authored caveat is DISPLACED rather than the disclosure being
		// dropped or the whole answer refused -- see withRetrievalDegradation.
		composed, displaced := withRetrievalDegradation(result.Limitations)
		result.Limitations = composed
		// Recorded on the RESULT, because the loss is canonical: a model
		// caveat this investigation produced is gone from the stored
		// answer, and the API's canonical view is as much a consumer as
		// the projection is. It cannot be inferred downstream either --
		// a displaced list and a list that simply had room are the same
		// shape and the same length, both ending with the disclosure.
		result.LimitationsDisplaced += displaced
		result.Coverage.Partial = true
	}
	if result.Cohort == nil {
		result.Cohort = graphContext.Cohort
	}
	if strings.TrimSpace(result.Versions.ServiceVersion) == "" {
		result.Versions.ServiceVersion = e.serviceVersion
	}
	if strings.TrimSpace(result.Versions.ContractVersion) == "" {
		result.Versions.ContractVersion = InvestigationResultSchemaV1
	}
	if strings.TrimSpace(result.Versions.CanonicalServiceVersion) == "" {
		result.Versions.CanonicalServiceVersion = facts.Version
	}
	if strings.TrimSpace(result.Versions.ModelIdentity) == "" {
		result.Versions.ModelIdentity = "unwired"
	}
	// CHAOS-3781 AC-3781-2: a historical answer states the time it speaks
	// for in a structured field. Composed HERE, from the interpretation
	// and the coverage the sources actually returned, rather than inside
	// any AnswerSynthesizer: a synthesizer may use a model, and what time
	// an answer covers is a fact about which reads ran, never something a
	// model may assert. The result contract refuses a non-current axis
	// carrying no label, so a composition bug fails loudly here rather
	// than shipping an unlabeled historical answer.
	result.Temporal = composeTemporalLabel(interpretation, result.Coverage, facts.TemporalGrain)
	temporallyLimited, temporalDisplaced := appendTemporalLimitations(result.Limitations, interpretation)
	result.Limitations = temporallyLimited
	result.LimitationsDisplaced += temporalDisplaced
	// effectiveWindow: composed ABOVE, before ResolveSubjects (CHAOS-4040
	// reordering -- see that call site's own comment), reused here
	// unchanged. By construction it can no longer be Provenance==inferred_default
	// here (that case already gated and returned above) -- every path
	// reaching this line carries a confirmed/stated window or none at all.
	result.EffectiveEvidenceWindow = effectiveWindow
	windowOutcome := windowCanonicalizationOutcome(windowCanon, result.EffectiveEvidenceWindow, windowCarry.Outcome == WindowCarryHit)
	pending.WindowCanonicalization = &windowOutcome
	// CHAOS-3900 W2 (design brief §4): the fresh disclosure W1's own scope
	// note deferred -- nil unless the effective window is genuinely
	// inferred. CHAOS-4040 (sol-max ruling 2026-08-21) makes this call
	// permanently a no-op ON THIS DECISIVE PATH: composeWindowClarification
	// only returns non-nil for Provenance==inferred_default, and the gate
	// above (windowConfirmationRequiredResult) already intercepts every
	// such window before this line -- see result.EffectiveEvidenceWindow's
	// own assignment comment above ("every path reaching this line carries
	// a confirmed/stated window or none at all"). Left in place rather
	// than removed: it stays correct (nil) if that invariant ever changes,
	// and matches windowConfirmationRequiredResult's own identical call
	// for the SAME data, on the gate terminal instead of this one.
	result.WindowClarification = composeWindowClarification(result.EffectiveEvidenceWindow, result.ResultID, e.now())
	if result.WindowClarification != nil && request.Options.WindowConfirmationMode == contractsv1.ContextFabricWindowConfirmationNudge {
		result.Warnings = appendUniqueWarning(result.Warnings, windowConfirmationNudgeSentence)
	}
	// CHAOS-3900 P1: the confirmed_structure echo, composed unconditionally
	// (empty/nil when this request carried no structure receipts) --
	// mirrors EffectiveEvidenceWindow's own placement, right beside the
	// window echo it is the structure-frame sibling of.
	// CHAOS-4360: every carried axis is disclosed here too, appended after
	// every receipt/explicit entry -- appendCarriedStructureEntry is a
	// no-op for any axis whose own carry did not hit above.
	result.ConfirmedStructure = appendCarriedStructureEntry(composeConfirmedStructure(mergeConfirmedMembers(structureCanon.Confirmed, windowCanon.ConfirmedMember), structureCanon.Explicit), carriedStructureEntries...)
	// CHAOS-3900 P1.G (design brief §2.1 B5): a decisive result reached via
	// structure confirmation still carries the full (offered, selected)
	// pair the Bridge needs. No guard needed: structureCanon.OfferSnapshot
	// is only ever non-nil alongside structureCanon.Confirmed (see
	// requestStructureCanonicalization's own doc comment) -- an empty
	// Confirmed set means OfferSnapshot is already nil by construction.
	result.StructureOfferSnapshot = structureCanon.OfferSnapshot
	// CHAOS-4098: the decisive path's synthesized-status override. Placed
	// HERE, immediately BEFORE the commit-affirmation gate below, for two
	// reasons that are both ordering constraints rather than preferences.
	//
	// AFTER every limitation composer (retrieval degradation, temporal
	// disclosures) and BEFORE Validate and Save, so its disclosure and its
	// Coverage.Partial flag are part of the SAME object that is validated,
	// returned and persisted -- the identical argument the gate below
	// makes for its own placement.
	//
	// BEFORE the gate, not after, because this override RECOMPOSES
	// DirectJudgment and DeterministicAnswer from the resolution, and the
	// gate deliberately does NOT recompose them after a retraction (see
	// its own comment). Running afterwards would silently re-render those
	// two fields against a post-retraction resolution and change a
	// decision CHAOS-4085 ruled on, in a ticket that is not about it.
	// Running first means the override sees exactly the resolution the
	// original composition saw, so recomposition is a pure status swap.
	// When BOTH fire on the same result -- the observed case-60 shape --
	// the prose is therefore composed against the pre-retraction
	// resolution, which is CHAOS-4085's own documented residual, neither
	// widened nor narrowed here.
	// applySynthesisStatusOverride MUTATES result, so it still runs on every
	// pass; only the RECORDING is deferred.
	pending.SynthesisStatusOverride = applySynthesisStatusOverride(&result)
	// CHAOS-4099: the answer's own statement that some requested evidence
	// was never reachable. Placed alongside the other post-synthesis
	// composers and before the commit-affirmation gate, Validate and Save,
	// so the disclosure, its Coverage.Partial flag and the answer are one
	// object throughout.
	applyFactScopeDisclosure(&result, facts.Scope)
	// CHAOS-5405 D-d: the SERVED census of what the scope resolver actually
	// decided. Placed immediately beside the disclosure it complements and on
	// the same object, for the same reason stated above -- the census, the
	// disclosure, the Coverage.Partial flag and the answer are validated,
	// returned and persisted together or they can disagree.
	//
	// Beside applyFactScopeDisclosure rather than anywhere else because the two
	// then have IDENTICAL reachability: any path that would serve a gap
	// disclosure serves a census, and any path that resolves no scope serves
	// neither. A census reachable on a different set of paths from the
	// disclosure would make "no census" ambiguous between "no scope" and "this
	// path forgot", which is the ambiguity D-d exists to remove.
	applyFactScopeCensus(&result, facts.Scope)
	// CHAOS-4962: a grouped question answered ungrouped says so on the wire,
	// not only in telemetry. Placed with the other disclosure composers and
	// before the commit-affirmation gate, Validate and Save, so the sentence,
	// its Coverage.Partial flag and the answer are one object throughout.
	applyGroupingRefusalDisclosure(&result, params.GroupingRefusal)
	// The group read's half: groups this answer did not read, or a group
	// list too large to carry, said on the wire beside the grouping refusal
	// and for the same reason -- telemetry alone is not disclosure.
	applyGroupReadDisclosure(&result, params.GroupReadDisclosure, params.GroupReadKind)
	// CHAOS-4398 PR3b: §5a narrated cohort driver judgments. Placed HERE --
	// AFTER synthesis (synthesisDriverCount = len(result.Drivers) and
	// synthesisClaimedFactCount = len(result.ClaimedFacts) are the ACTUAL
	// counts the model produced, not a guess) and BEFORE the
	// commit-affirmation gate, Validate and Save, same ordering discipline
	// as every other post-synthesis composer on this path. Appended to
	// result.Drivers (never replacing what synthesis already produced) --
	// narrateCohortDriverJudgments' own budget math already bounds the
	// combined total at BOTH ContextFabricDriversMaxCount and
	// ContextFabricClaimedFactsMaxCount (codex R1: a synthesis draft can
	// legitimately carry up to 250 claims on its own, and narration mints
	// one more claim per narrated driver, so the claimed-facts budget must
	// be tracked independently of the driver budget, not assumed to always
	// have headroom).
	// THE COMPUTED COUNT, MINTED AS A CLAIM -- BEFORE NARRATION, ON PURPOSE.
	//
	// Claimed facts are CHARGED ITEMS. Narration below budgets itself against
	// `len(result.ClaimedFacts)` and the static claim cap, so a claim appended
	// after it is an item the allocator never saw: the document spends one
	// nobody granted, which is the second-authority-over-one-number defect
	// this file's own allocator comments exist to prevent. Minting here puts
	// the count inside the same accounting as every other claim, and narration
	// then budgets around it.
	//
	// It goes FIRST among the two because it is a served fact the server
	// computed, not a narration of something already claimed: if the budget
	// can afford exactly one more claim, the count is the one worth keeping.
	//
	// DROPPED, NEVER OVERFLOWED, at the contract cap. A 251st claim fails
	// ContextFabricClaimedFact bounds and invalidates the WHOLE answer, so a
	// document already at the cap serves its answer without the claim rather
	// than serving nothing. The outcome row still states the count correctly;
	// what is lost is the addressable field, and the loss is reported.
	if claim, ok := cardinalityClaim(principal, cardinality); ok {
		if len(result.ClaimedFacts) < contractsv1.ContextFabricClaimedFactsMaxCount {
			result.ClaimedFacts = append(result.ClaimedFacts, claim)
		}
	}
	if graphContext.Cohort != nil {
		// synthesisAllocation, NOT a second AllocateItems call. Two
		// derivations in one function would be two authorities over one
		// number -- the defect this allocator exists to remove,
		// reintroduced at the site that removes it.
		narrated, mintedClaims, narrationEvent := narrateCohortDriverJudgments(graphContext.Cohort, result.Drivers, len(result.ClaimedFacts), cohortSignalCitations, synthesisAllocation)
		// codex R1 (CHAOS-4398 PR3b), team-lead ruling: every narration-
		// minted claim must pass the SAME grounding check a model-authored
		// claim gets from SynthesisDraft.ValidateAgainst -- which this
		// composer's claims never reach, since narration runs entirely
		// AFTER that validation already completed. validateMintedClaimsGrounded
		// re-derives each claim's (Kind, Subject, Field, Value) against
		// facts.Facts (the SAME canonical fact bundle RankCohort itself
		// read) BEFORE anything is appended -- fail closed, never serve a
		// claim that cannot be traced back to a real canonical fact.
		if err := validateMintedClaimsGrounded(mintedClaims, facts.Facts); err != nil {
			return InvestigationResult{}, synthesisAllocation, assemblyTelemetry{}, MembershipCardinality{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
		}
		result.Drivers = append(result.Drivers, narrated...)
		// CHAOS-4398 PR3b: append the claims THIS composer minted (only for
		// a driver it actually narrated) AFTER the model's own
		// draft.ClaimedFacts (Synthesize's own composer already set
		// result.ClaimedFacts from those) -- append, never overwrite, so
		// neither side's claims are ever lost. Every narrated driver's
		// SourceClaimedFactIDs already names its own entry here by
		// construction (narrateCohortDriverJudgments set both together).
		result.ClaimedFacts = append(result.ClaimedFacts, mintedClaims...)
		// CHAOS-4690 (was CHAOS-4580): once narration produced at least one
		// judgment, recompose the answer prose to the status sentence alone
		// -- CHAOS-4580 had this splice the principal driver's narrated
		// Summary (scoring arithmetic) into DeterministicAnswer; the settled
		// CHAOS-4690 language principle reverses that, so recompose now only
		// avoids restating the pre-narration synthesis composition. Guarded
		// on len(narrated)>0 (not just graphContext.Cohort!=nil) so a cohort
		// that produced zero narrated judgments -- no_drivers/budget_exhausted,
		// or every candidate lacked evidence -- leaves the original synthesis
		// composition alone, same as before this ticket. A non-cohort
		// (single-subject) investigation never enters this block at all, so
		// its answer composition is unaffected.
		if len(narrated) > 0 {
			result.DirectJudgment, result.DeterministicAnswer = recomposeCohortAnswerNarrative(result.Status, result.SubjectResolution)
			narrationEvent.AnswerNarrativeRecomposed = true
		}
		pending.CohortNarration = &narrationEvent
	}
	// THE PROSE, COMPOSED AFTER THE RECOMPOSE -- and the split from the mint
	// above is deliberate, not an accident of layout.
	//
	// The CLAIM must be minted before narration, because claims are charged
	// items and narration budgets against the count of them. The SENTENCE must
	// be composed after it, because recomposeCohortAnswerNarrative REPLACES
	// DeterministicAnswer wholesale -- a sentence appended earlier is silently
	// discarded on exactly the cohort path this feature is for. Two placements,
	// one `cardinality` value, so the claim and the prose still cannot disagree.
	//
	// It extends the STATUS COMPOSITION. The rule that recompose enforces
	// excludes narration detail -- a driver's scoring arithmetic, restated --
	// and a count the server computed over the served member set is not that.
	if sentence := cardinalityAnswerSentence(cardinality); sentence != "" {
		result.DeterministicAnswer = strings.TrimSpace(result.DeterministicAnswer + " " + sentence)
	}
	// CHAOS-4085: the post-synthesis commit-affirmation gate. Placed HERE
	// deliberately -- after every composer that touches Limitations or
	// Coverage has run (retrieval degradation, temporal disclosures), and
	// BEFORE Validate and Save, so the retraction, its disclosure and its
	// Coverage.Partial flag are all part of the SAME object that is
	// validated, returned, and persisted. A retraction applied after Save
	// would leave the stored row disagreeing with the served answer, and a
	// retraction applied before the limitation composers would be re-capped
	// underneath it.
	//
	// This is the ONLY place a commit is revisited after resolution, and it
	// is strictly subtractive -- see applyCommitAffirmation's own invariant
	// list. The deterministic answer is NOT recomposed: an unaffirmed
	// subject is by construction one the answer does not stand behind, so
	// the prose already reads as the non-answer it is, and re-synthesizing
	// would mean a second model call to restate a conclusion the engine has
	// already reached structurally.
	//
	// A retraction that empties Committed does NOT convert this into a
	// clarification terminal: the caller still receives the answer that was
	// computed, now honestly carrying no committed subject and saying so in
	// its limitations. Routing to the subjectless terminal here would
	// discard a paid-for answer and change this path's contract outcome on
	// a signal the terminal's own logic never sees.
	if outcomes := applyCommitAffirmation(&result, affirmationInputs{
		Bases: commitBases,
		// result.SubjectResolution.Candidates, not the local resolution's:
		// the same backing array today, but the RESULT's copy is the one
		// this gate rewrites states on, so reading and writing the same
		// authoritative list keeps that true if the two ever diverge.
		Candidates: result.SubjectResolution.Candidates,
		Graph:      graphContext,
		Facts:      facts,
	}); len(outcomes) > 0 {
		pending.CommitAffirmations = outcomes
	}
	// CHAOS-4087: stamped AFTER applyCommitAffirmation, not before -- that
	// gate can RETRACT a subject from result.SubjectResolution.Committed
	// (affirmationInputs.Bases is the SAME commitBases this reads), so
	// building the digest list from the resolution-time commitDigests
	// BEFORE affirmation ran would leave a stale entry describing a
	// subject that is no longer committed. Reading the FINAL Committed
	// here means a retracted subject's digest is never persisted at all --
	// exactly the outcome CommitBasisSet's own "a stale proven basis
	// attached to a subject nothing committed" concern (ResetTo's doc
	// comment) describes, applied to this wire-safe companion set. One
	// entry per committed subject, always, even when commitDigests has
	// none for it (the fail-closed CommitGate=="" reading) -- see
	// ContextFabricCommitDecisionDigest's own doc comment.
	if len(result.SubjectResolution.Committed) > 0 {
		digests := make([]contractsv1.ContextFabricCommitDecisionDigest, 0, len(result.SubjectResolution.Committed))
		for _, subject := range result.SubjectResolution.Committed {
			d := commitDigests.For(subject)
			digests = append(digests, contractsv1.ContextFabricCommitDecisionDigest{
				Subject: subject, CommitGate: d.CommitGate, IdentityProven: d.IdentityProven,
				SearchTruncated: d.SearchTruncated, AliasLookupComplete: d.AliasLookupComplete,
			})
		}
		result.SubjectResolution.CommitDecisionDigests = digests
	}
	return result, synthesisAllocation, pending, cardinality, nil
}

// assemblyTelemetry is every per-investigation decision event one assembly
// pass produced, held rather than emitted.
//
// It is a STRUCT rather than a flag because that makes adding a deferred
// event a visible, reviewable act: a new emission in synthesizeAndAssemble
// has to add a field here and a line to emit(), and a reviewer can see both.
// The alternative -- a bool that each emitter consults -- is what let three
// emitters drift out of the rule one at a time.
type assemblyTelemetry struct {
	WindowCanonicalization  *WindowCanonicalizationOutcome
	SynthesisStatusOverride *SynthesisStatusOverrideOutcome
	CohortNarration         *CohortDriverNarrationEvent
	// CohortRanked is seeded by the engine from the pre-synthesis ranking and
	// REPLACED by stage 3 when a retry re-ranks a narrowed cohort, so the
	// event published always describes the cohort actually served.
	CohortRanked       *CohortRankedEvent
	CommitAffirmations []CommitAffirmationOutcome
	// ObservationCover holds every read-requirement observation-cover event
	// from EVERY pass this investigation ran (finalizeResult APPENDS to it,
	// never replaces it), tagged per event with which pass produced it -- see
	// ReadRequirementObservationCoverEvent.Pass.
	//
	// THIS DIFFERS FROM CohortRanked ON PURPOSE, even though both are
	// per-pass values seeded by the engine and touched again on a retry.
	// CohortRanked is REPLACED because it describes the cohort actually
	// served, and a discarded pass's ranking is not itself a decision a
	// reader needs. A discarded PASS is a decision, though: a P1 review found
	// finalizeResult emitting cover events inline, once per pass, so a served
	// investigation logged two sets of cover lines and the FIRST described an
	// answer nobody received. The fix here is not to suppress that first set
	// -- the governing rule for this event is that a reader must be able to
	// rebuild the whole decision graph from the trace, and "the first answer
	// was too big, here is what it would have covered" is part of that graph.
	// So every pass's events are kept, and (*Engine).emit marks which one was
	// actually served.
	ObservationCover []ReadRequirementObservationCoverEvent
}

// emit publishes the held events. The engine calls it EXACTLY ONCE, for the
// result it actually serves, so a retry's discarded first pass contributes
// nothing to any per-investigation counter.
//
// ObservationCover is the ONE DELIBERATE EXCEPTION to that rule: it publishes
// EVERY pass's events, not just the served one's. See
// assemblyTelemetry.ObservationCover for why -- in short, a discarded pass is
// itself a decision the observability bar requires a reader be able to
// rebuild, so suppressing it here would trade one defect (double-counting)
// for another (losing the record of the first attempt entirely).
func (e *Engine) emit(ctx context.Context, principal storage.Principal, pending assemblyTelemetry) {
	if e.telemetry != nil && pending.WindowCanonicalization != nil {
		e.telemetry.RecordWindowCanonicalization(ctx, principal, *pending.WindowCanonicalization)
	}
	e.recordSynthesisStatusOverride(ctx, principal, pending.SynthesisStatusOverride)
	if e.telemetry != nil && pending.CohortRanked != nil {
		e.telemetry.RecordCohortRanked(ctx, principal, *pending.CohortRanked)
	}
	if e.telemetry != nil && pending.CohortNarration != nil {
		e.telemetry.RecordCohortDriverNarration(ctx, principal, *pending.CohortNarration)
	}
	e.recordCommitAffirmation(ctx, principal, pending.CommitAffirmations)
	// The observation-cover events are NOT published here. emit runs before
	// the final budget assertion, validation and persistence, any of which can
	// still withhold the answer, and a cover line marked served for an answer
	// the caller never received is the defect this ordering removes. They are
	// published once, at Investigate's exit -- see publishObservationCover.
}

// publishObservationCover publishes every pass's observation-cover events,
// ONCE, from Investigate's exit, when it is known whether the answer was
// returned.
//
// answered: the FINAL pass's events -- the highest Pass value, the one whose
// result is returned -- read Served=true and every earlier pass's false.
// Not answered (a refusal, a failed validation, a save-time supersession, a
// persistence failure after evaluation): every event reads Served=false and
// AnswerWithheld=true, so no line claims an answer the caller never received.
func (e *Engine) publishObservationCover(ctx context.Context, principal storage.Principal, events []ReadRequirementObservationCoverEvent, answered bool) {
	if e.telemetry == nil || len(events) == 0 {
		return
	}
	finalPass := events[0].Pass
	for _, event := range events {
		if event.Pass > finalPass {
			finalPass = event.Pass
		}
	}
	for _, event := range events {
		event.Served = answered && event.Pass == finalPass
		event.AnswerWithheld = !answered
		e.telemetry.RecordReadRequirementObservationCover(ctx, principal, event)
	}
}
