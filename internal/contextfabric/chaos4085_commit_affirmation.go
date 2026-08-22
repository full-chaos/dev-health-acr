package contextfabric

// CHAOS-4085: the post-synthesis commit-affirmation gate.
//
// THE DEFECT. In the v9 trial (tag 20260822T091538Z) an ext-corpus
// never-commit control committed a subject. Subject resolution ranked ten
// candidates, the top three tied at exactly the same confidence, the search
// had truncated, and the CHAOS-3829 vector-margin rescue broke the tie and
// committed the winner. The synthesized answer then said, three independent
// times, that the supplied data established nothing about the question --
// and named a different subject entirely in what little it did ground. The
// result nevertheless carried a committed subject, because the commit
// decision is made in graphrank BEFORE the fact read and BEFORE synthesis,
// and nothing downstream could revisit it.
//
// WHY THE FIX CANNOT LIVE AT RESOLUTION TIME ALONE. The same run committed
// the RIGHT subject out of an identically-shaped resolution -- same tie
// structure, same mechanisms, same truncation, same rescue. At the moment
// of the commit decision the two are indistinguishable. CHAOS-4085
// therefore acts in two places, and they are deliberately different in
// kind:
//
//  1. AT RESOLUTION (graphrank, tiedStatisticalTopUnderTruncation): remove
//     the demonstrated-unsafe population -- a tied top under a truncated
//     search -- from the rescue outright. This costs the correct sibling
//     too. Under DP9 (zero wrong commits) that is the price, not a reason
//     to keep the class.
//  2. HERE, AFTER SYNTHESIS: require a subject that was committed on
//     STATISTICAL grounds to be independently supported by the answer that
//     was actually produced about it.
//
// MODEL OUTPUT IS A VETO, NEVER A PROOF. This gate can only ever REMOVE a
// subject from Committed. It can never add one, never restore one an
// earlier gate refused, and never promote a candidate's state. That
// asymmetry is load-bearing: synthesis is correlated with the very
// lexical/embedding proximity that produced the wrong candidate in the
// first place, so a model handed a wrong-but-similar subject can and does
// write plausible supporting prose about it. A signal that can be fooled in
// the permissive direction is safe as a veto and unsafe as a licence.
//
// WHAT "AFFIRMED" MEANS (sol@xhigh review, change 1). Not "the answer
// mentions this id somewhere". The answer must carry a SUBJECT-BOUND
// SUPPORT RELATIONSHIP: a typed, subject-bearing structure naming the
// committed subject, AND evidence attributable to that same subject
// standing behind it. A TOP-LEVEL citation does NOT affirm on its own --
// result-level EvidenceRefIDs are validated only for membership in what was
// already supplied, so a bulk or incidental citation would otherwise be
// enough to retain a wrong commit. See commitSubjectAffirmed.

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CommitGateVersion is the deployment-current identity of the commit-gate
// RULES -- both halves: graphrank's tied-top-under-truncation refusal and
// this file's affirmation gate. It is threaded into the answer-reuse key
// (ReuseKey.CommitGateVersion) so a stored answer produced under different
// rules can never be replayed as if produced under these.
//
// BUMP THIS whenever either half's decision changes. The reuse lookup runs
// before Interpret and before synthesis, so a cached row is served with its
// stored Committed list fully intact, having never passed through this gate
// at all -- a gate a cache can bypass is not a gate.
//
// "cg_v2", not v1: v1 names the pre-CHAOS-4085 behavior, which no row
// records, so starting at v2 keeps the vocabulary honest about the fact
// that a first generation existed and is exactly what is being fenced off.
const CommitGateVersion = "cg_v2"

// commitRetractionLimitation is the answer-facing disclosure appended when
// this gate retracts a commit.
//
// FIXED and non-interpolated, the same discipline withRetrievalDegradation
// already applies (engine.go): it names no subject, no candidate, no
// mechanism, no score, and no model output. A limitation is prose a reader
// sees, and every cause that lands here -- a tie broken on the wrong side,
// a synthesis that found nothing to say about the subject it was handed --
// has one consequence for that reader: the system declined to name a
// subject it could not stand behind. Operator-facing detail belongs in
// telemetry, which receives it.
const commitRetractionLimitation = "A candidate subject was identified but not committed: the evidence assembled for it does not support naming it as the answer to this question."

// CommitAffirmationOutcome is one retraction event, reported to telemetry.
// Counts and closed labels only -- never question text, never answer text,
// never a model string.
type CommitAffirmationOutcome struct {
	// Basis is the retracted subject's own commit basis. Always a
	// non-IdentityProven value (this gate never evaluates the others), so
	// in practice CommitBasisStatistical or CommitBasisUnknown -- and the
	// two are worth telling apart: a spike in Unknown means a GraphReader
	// stopped reporting its basis, which is safe but would silently cost
	// commits, and is exactly the sort of regression that otherwise
	// presents only as "reuse got worse".
	Basis CommitBasis
	// SubjectKind is the retracted subject's kind. A closed enum from the
	// contract, never an identifier.
	SubjectKind SubjectKind
	// ProvisionalCommitted/FinalCommitted are the Committed cardinalities
	// before and after THIS investigation's whole reduction, so a reader
	// can tell a partial retraction from a total one without joining
	// events.
	ProvisionalCommitted int
	FinalCommitted       int
}

// CommitAffirmationTelemetry is the optional sink for retraction events.
// Engine's own EngineTelemetry implements it when it can; a telemetry
// implementation that does not is simply not called (see
// recordCommitAffirmation).
type CommitAffirmationTelemetry interface {
	RecordCommitAffirmationRetraction(ctx context.Context, principal storage.Principal, outcome CommitAffirmationOutcome)
}

// affirmationInputs is everything the gate reads, gathered explicitly so
// the decision is a pure function of named values rather than of whatever
// happens to be in scope at the call site.
type affirmationInputs struct {
	// Bases is the per-subject commit basis from resolution. A nil or
	// incomplete set is SAFE, not broken: every unrecorded subject reads
	// CommitBasisUnknown, which is not IdentityProven, which means it must
	// be affirmed like any statistical commit.
	Bases CommitBasisSet
	// Candidates is the resolution's own candidate list -- the source of a
	// committed subject's own evidence-ref ids.
	Candidates []SubjectCandidate
	// Graph is the discovery output whose relationship paths and driver
	// candidates carry the evidence refs attributable to a subject. A zero
	// value contributes nothing, which fails CLOSED (fewer affirming refs
	// means more retractions, never fewer).
	Graph GraphContext
	// Facts is the canonical fact bundle read for this investigation.
	Facts CanonicalFactBundle
}

// affirmingEvidenceRefs returns the evidence-ref ids that are ATTRIBUTABLE
// TO subject -- the closed set a cited ref must fall inside for a citation
// to count as support FOR THIS SUBJECT rather than support for something
// else that happened to be in the same answer.
//
// Built entirely from what the ENGINE supplied to synthesis, never by
// parsing a ref string. Three sources, all structural:
//
//   - every relationship path whose Nodes include the subject, plus that
//     path's edges. This is what admits the legitimate shape where an
//     answer's driver is about a NEIGHBOUR ("X blocks Y") and cites the
//     dependency/hierarchy edge rather than Y itself -- the committed
//     subject is genuinely an endpoint of the cited evidence, and a rule
//     that demanded the subject's own bare ref would falsely retract it;
//   - every driver candidate the graph proposed for the subject;
//   - every canonical fact read for the subject.
//
// WHAT IS DELIBERATELY EXCLUDED, and why each exclusion is load-bearing:
//
//   - the subject's OWN candidate evidence ref (codex xhigh review round 1,
//     HIGH finding 2). A candidate's ref is minted FROM the candidate's
//     identity and is handed to synthesis purely because the subject was
//     proposed -- so a model that names the committed subject and cites
//     that ref has cited nothing the retrieval step did not already
//     assert. That is circular: it would let a wrong-but-similar candidate
//     "support" itself using only what its own proposal supplied, which is
//     exactly the correlated-model-output failure this gate exists to
//     resist. Requiring evidence the INVESTIGATION gathered about the
//     subject -- a graph relationship it participates in, a driver the
//     graph proposed for it, a canonical fact read for it -- is the
//     difference between the answer echoing the retrieval and the answer
//     standing on something. Measured against all 51 commit events in the
//     v9 trial, this exclusion changed no outcome: every one of the 18
//     affirmed commits stood on path or driver-candidate evidence, never
//     on its own candidate ref.
//   - a cohort member's own refs. Cohort membership says the subject was
//     in a discovered set, not that evidence was gathered about it -- and
//     the trial's wrong commit sat in exactly that position, a subject the
//     answer discussed only as cohort context.
func affirmingEvidenceRefs(subject SubjectRef, inputs affirmationInputs) map[string]struct{} {
	refs := make(map[string]struct{})
	add := func(values []string) {
		for _, value := range values {
			if value == "" {
				continue
			}
			refs[value] = struct{}{}
		}
	}
	key := SubjectMapKey(subject)
	for _, path := range inputs.Graph.Paths {
		if !pathTouchesSubject(path, key) {
			continue
		}
		add(path.EvidenceRefIDs)
		for _, edge := range path.Edges {
			add(edge.EvidenceRefIDs)
		}
	}
	for _, driver := range inputs.Graph.DriverCandidates {
		if subjectsContain(driver.AffectedSubjects, key) {
			add(driver.EvidenceRefIDs)
		}
	}
	for _, fact := range inputs.Facts.Facts {
		if SubjectMapKey(fact.Subject) == key {
			add(fact.EvidenceRefIDs)
		}
	}
	return refs
}

func pathTouchesSubject(path RelationshipPath, key string) bool {
	for _, node := range path.Nodes {
		if SubjectMapKey(node) == key {
			return true
		}
	}
	for _, edge := range path.Edges {
		if SubjectMapKey(edge.From) == key || SubjectMapKey(edge.To) == key {
			return true
		}
	}
	return false
}

func subjectsContain(subjects []SubjectRef, key string) bool {
	for _, subject := range subjects {
		if SubjectMapKey(subject) == key {
			return true
		}
	}
	return false
}

// commitSubjectAffirmed reports whether the synthesized result carries a
// SUBJECT-BOUND SUPPORT RELATIONSHIP for subject.
//
// Exactly three shapes affirm, and each pairs a typed subject-bearing
// structure with evidence attributable to that same subject:
//
//  1. a ClaimedFact whose Subject IS the committed subject. A claimed fact
//     is already the closed, checkable canonical-observation form -- it
//     names the subject, the field, and the value, and the result contract
//     independently checks it against what was supplied -- so it needs no
//     second evidence conjunct;
//  2. a Driver naming the subject in AffectedSubjects, standing on an
//     evidence ref attributable to the subject, a relationship path the
//     subject is on, or a ClaimedFact about the subject;
//  3. a Finding (remaining work, readiness gap, or conflict) naming the
//     subject in Subjects, under the same evidence-or-claim conjunct.
//
// What deliberately does NOT affirm:
//
//   - result-level EvidenceRefIDs on their own. Validation permits any ref
//     already present in what was supplied, so a bulk or incidental
//     citation carries no subject-bound claim, and accepting it would let
//     a stray reference retain a wrong commit. This is sol@xhigh's change
//     1, and it is the difference between "the answer cited something"
//     and "the answer said something about THIS subject";
//   - a driver or finding that names the subject but cites only evidence
//     belonging to some other subject, or a path the subject is not on. It
//     is talking around the subject, not about it;
//   - a driver or finding standing ONLY on the committed subject's own
//     candidate evidence ref. See affirmingEvidenceRefs for why that is
//     circular rather than supporting;
//   - the deterministic answer prose. Free text is never read here.
func commitSubjectAffirmed(subject SubjectRef, result InvestigationResult, inputs affirmationInputs) bool {
	key := SubjectMapKey(subject)

	// A canonical fact the ENGINE actually read for this subject. This is
	// the non-model half of shape 1's conjunct: ClaimedFacts is model
	// output, and the result contract does not itself check a claimed
	// fact's VALUE against the canonical facts that were supplied (see
	// ContextFabricClaimedFact's own doc comment), so a claimed fact alone
	// would let a fabricated claim about the committed subject affirm it.
	// Pairing it with "the fact read returned at least one fact for this
	// subject" restores sol@xhigh change 1's requirement that a
	// subject-bearing claim stand on a canonical fact attributable to that
	// same subject.
	subjectHasCanonicalFact := false
	for _, fact := range inputs.Facts.Facts {
		if SubjectMapKey(fact.Subject) == key {
			subjectHasCanonicalFact = true
			break
		}
	}

	// Every claim id the answer asserts ABOUT THIS SUBJECT. Collected in
	// full (never short-circuited) because shapes 2 and 3 below cite claims
	// by id, and a driver may cite the second or third such claim rather
	// than the first.
	claimIDs := make(map[string]struct{})
	claimedAboutSubject := false
	for _, claim := range result.ClaimedFacts {
		if SubjectMapKey(claim.Subject) != key {
			continue
		}
		claimedAboutSubject = true
		if claim.ClaimID != "" {
			claimIDs[claim.ClaimID] = struct{}{}
		}
	}
	// Shape 1: a claim about the subject, standing on a canonical fact the
	// engine actually read for that same subject.
	if claimedAboutSubject && subjectHasCanonicalFact {
		return true
	}

	refs := affirmingEvidenceRefs(subject, inputs)
	supported := func(evidenceRefIDs, claimedFactIDs []string) bool {
		for _, ref := range evidenceRefIDs {
			if _, ok := refs[ref]; ok {
				return true
			}
		}
		for _, claimID := range claimedFactIDs {
			if _, ok := claimIDs[claimID]; ok {
				return true
			}
		}
		return false
	}
	// affirmingPathIDs (codex xhigh review round 1, MEDIUM finding 3) is the
	// PATH-ID half of the same "evidence attributable to this subject"
	// question. A driver is contract-valid with PathIDs and NO evidence
	// refs at all -- validate_context_fabric_result.go requires only that a
	// non-withheld driver carry ONE of the two -- and the synthesis prompt
	// explicitly offers a relationship path as driver grounding. Judging
	// only the evidence-ref half would therefore have falsely retracted a
	// correct commit whose driver grounded on the path itself, which is the
	// most natural form for exactly the relationship-shaped driver this
	// gate most wants to admit.
	//
	// Same attribution rule as the ref set, so the two halves cannot
	// diverge: a path counts only when the subject is genuinely ON it.
	affirmingPathIDs := make(map[string]struct{})
	for _, path := range inputs.Graph.Paths {
		if pathTouchesSubject(path, key) && path.PathID != "" {
			affirmingPathIDs[path.PathID] = struct{}{}
		}
	}
	supportedByPath := func(pathIDs []string) bool {
		for _, pathID := range pathIDs {
			if _, ok := affirmingPathIDs[pathID]; ok {
				return true
			}
		}
		return false
	}

	// Shape 2: a driver about the subject, standing on evidence -- or on a
	// relationship path -- attributable to that same subject.
	for _, driver := range result.Drivers {
		if !subjectsContain(driver.AffectedSubjects, key) {
			continue
		}
		if supported(driver.EvidenceRefIDs, driver.ClaimedFactIDs) || supportedByPath(driver.PathIDs) {
			return true
		}
	}
	// Shape 3: a finding about the subject, same conjunct. All three
	// finding lists are governed by the same contract type and the same
	// evidence rule, so they are checked identically rather than ranked.
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			if subjectsContain(finding.Subjects, key) && supported(finding.EvidenceRefIDs, finding.ClaimedFactIDs) {
				return true
			}
		}
	}
	return false
}

// applyCommitAffirmation is the reducer. It rewrites result IN PLACE to
// retain only those committed subjects that either stand on a proven
// identity or are affirmed by the answer, and returns how many were
// retracted.
//
// INVARIANTS, all structural rather than incidental (sol@xhigh change 4):
//
//   - MONOTONE: the final Committed list is a SUBSEQUENCE of the
//     provisional one. Cardinality can only fall or stay equal. Built by
//     filtering the original slice in order, so this cannot be violated by
//     construction -- there is no path that appends.
//   - NO REWRITE, NO PROMOTION, NO RESURRECTION: subject values are copied
//     unchanged; the only candidate State written is Committed -> Proposed,
//     and only for a subject actually retracted. No candidate is ever moved
//     INTO Committed, and a candidate that was not committed is not touched
//     at all.
//   - STATES MIRROR COMMITS: after this runs, a candidate carries State ==
//     Committed if and only if its subject survives in Committed.
//   - IDEMPOTENT: a second call is a no-op. Retracted subjects are gone
//     from Committed, so the loop that could retract them no longer visits
//     them; the disclosure is appended through appendBoundedLimitations,
//     which skips an addition already stated; and
//     Coverage.Partial is already true.
//   - DEFAULT-RETRACT, EXHAUSTIVE: the survive branch is the one that must
//     be earned. Every subject is either IdentityProven or affirmed, and
//     anything else -- including an unknown basis, a nil input, a zero
//     GraphContext, a subject with no candidate entry -- falls through to
//     retraction. There is no third outcome and no early return that skips
//     the reduction.
//   - ATOMIC DISCLOSURE: if anything was retracted, the limitation and
//     Coverage.Partial are set in the same pass, before the caller
//     validates or saves.
func applyCommitAffirmation(result *InvestigationResult, inputs affirmationInputs) []CommitAffirmationOutcome {
	if result == nil || len(result.SubjectResolution.Committed) == 0 {
		return nil
	}
	provisional := result.SubjectResolution.Committed
	retained := make([]SubjectRef, 0, len(provisional))
	retractedKeys := make(map[string]struct{})
	outcomes := make([]CommitAffirmationOutcome, 0, len(provisional))

	for _, subject := range provisional {
		basis := inputs.Bases.For(subject)
		if basis.IdentityProven() || commitSubjectAffirmed(subject, *result, inputs) {
			retained = append(retained, subject)
			continue
		}
		retractedKeys[SubjectMapKey(subject)] = struct{}{}
		outcomes = append(outcomes, CommitAffirmationOutcome{
			Basis:                basis,
			SubjectKind:          subject.Kind,
			ProvisionalCommitted: len(provisional),
		})
	}
	if len(retractedKeys) == 0 {
		return nil
	}
	// Committed is assigned a NON-NIL slice even when everything was
	// retracted: the result contract requires Committed to be non-nil
	// (validate_context_fabric_result.go) and distinguishes "no subject
	// committed" from "this field was never populated".
	result.SubjectResolution.Committed = retained
	for index := range result.SubjectResolution.Candidates {
		candidate := &result.SubjectResolution.Candidates[index]
		if _, retractedHere := retractedKeys[SubjectMapKey(candidate.Subject)]; !retractedHere {
			continue
		}
		if candidate.State == ResolutionCommitted {
			// Proposed, not Ambiguous: this candidate remains a real,
			// authorized, correctly-ranked possibility that the answer
			// simply did not stand behind. Ambiguous is the resolution's
			// own word for "the gates could not separate these", which is
			// a different statement and would misdescribe a lone
			// candidate that was retracted on its own merits.
			candidate.State = ResolutionProposed
		}
	}
	// appendBoundedLimitations is the ONE path by which anything is added
	// to a composed result's limitations (CHAOS-3746 round-17 finding 1) --
	// it dedups (which is what makes this reducer idempotent on the
	// disclosure), respects the contract cap, and never displaces a
	// service-authored disclosure. The retraction disclosure is registered
	// as service-authored in serviceAuthoredLimitations, so it can add to
	// the count of displaced model caveats but can never itself be the
	// caveat displaced.
	composedLimitations, displaced := appendBoundedLimitations(result.Limitations, []string{commitRetractionLimitation})
	result.Limitations = composedLimitations
	result.LimitationsDisplaced += displaced
	// An answer that just lost a subject it was written about does not
	// cover what it set out to cover. Coverage.Partial is the contract's
	// existing word for that, and it is the same field the
	// retrieval-degradation path (engine.go) sets for the same reason.
	result.Coverage.Partial = true

	final := len(result.SubjectResolution.Committed)
	for index := range outcomes {
		outcomes[index].FinalCommitted = final
	}
	return outcomes
}

// recordCommitAffirmation emits one telemetry event per retraction, when
// the composed EngineTelemetry can receive them. A telemetry implementation
// that does not implement CommitAffirmationTelemetry is silently skipped:
// this is an OBSERVABILITY path, and no commit decision depends on whether
// it runs.
func (e *Engine) recordCommitAffirmation(ctx context.Context, principal storage.Principal, outcomes []CommitAffirmationOutcome) {
	if len(outcomes) == 0 || e.telemetry == nil {
		return
	}
	sink, ok := e.telemetry.(CommitAffirmationTelemetry)
	if !ok {
		return
	}
	for _, outcome := range outcomes {
		sink.RecordCommitAffirmationRetraction(ctx, principal, outcome)
	}
}
