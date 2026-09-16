package contextfabric

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Whether a counted member set is the population the question asked about.
//
// A count describes the requested, authorized population or it describes
// nothing. The `membership_cardinality` step counts the member set retrieval
// resolved, and until this file it treated ANY resolved member set as the
// population: a question counting the members UNDER a named anchor whose
// anchor never resolved still reached assembly with a member set -- retrieval
// matched members of the right kind without any anchor bounding them -- and
// the step served that number as an exact count, claimed and stated in the
// answer, beside a terminal that said the anchor was never found.
//
// The decision is made ONCE per pass, from the turn's frame and the subject
// resolution retrieval ran under, on both paths that state a count: the fresh
// assembly, and the reuse backfill over the frame of the reading persisted
// beside the stored row.
//
// WHAT DECIDES THE SCOPE IS THE FRAME'S SUBJECT EXPRESSION, never the family.
// The family is a lossy projection and no stage may branch on it
// (DeriveQuestionFamily's own record); the expression is the requested
// population's definition. Only children_of_scope hangs its members off an
// anchor. Every other expression counts an organization-level population (a
// discovered kind, a grouped or compared set, or the organization itself), and
// its count is unchanged here.

// CountPopulationScopeDecision is the closed decision this file makes.
type CountPopulationScopeDecision string

const (
	// CountPopulationScopeOrganization: the frame's expression counts an
	// organization-level population, so a resolved member set is that
	// population. The count stands.
	CountPopulationScopeOrganization CountPopulationScopeDecision = "organization_scope"
	// CountPopulationScopeAnchorCommitted: the question counts members under
	// an anchor, and a committed subject is BOUND to that anchor -- of the
	// anchor's kind when the reading states one, and recorded by resolution
	// as the anchor term's match or committed on the caller's own canonical
	// id. The count stands.
	CountPopulationScopeAnchorCommitted CountPopulationScopeDecision = "anchor_committed"
	// CountPopulationScopeAnchorUnresolved: the question counts members under
	// an anchor, no committed subject is bound to that anchor (a committed
	// subject of another identity is not the anchor), and at most one
	// candidate was offered. The member set is not bounded by the requested
	// scope, so it is not counted.
	CountPopulationScopeAnchorUnresolved CountPopulationScopeDecision = "anchor_unresolved"
	// CountPopulationScopeAnchorAmbiguous: as unresolved, but more than one
	// candidate that could be the anchor was offered -- not of the member
	// kind, and of the reading's anchor kind when one is stated -- and none
	// is bound. Not counted.
	CountPopulationScopeAnchorAmbiguous CountPopulationScopeDecision = "anchor_ambiguous"
	// CountPopulationScopeFrameAbsent: no frame records which population the
	// question asked for -- on reuse, a stored row whose persisted reading is
	// absent or unreadable. Not counted: a count whose population cannot be
	// named is the defect this file removes.
	CountPopulationScopeFrameAbsent CountPopulationScopeDecision = "frame_absent"
)

// CountPopulationScopeDecisionVocabulary is the closed vocabulary, in
// declaration order, for the telemetry specification to read.
func CountPopulationScopeDecisionVocabulary() []string {
	return []string{
		string(CountPopulationScopeOrganization),
		string(CountPopulationScopeAnchorCommitted),
		string(CountPopulationScopeAnchorUnresolved),
		string(CountPopulationScopeAnchorAmbiguous),
		string(CountPopulationScopeFrameAbsent),
	}
}

// CountPopulationScope is the decision and the measured inputs that produced
// it, carried together so the trace can rebuild the decision from its own line.
type CountPopulationScope struct {
	Decision       CountPopulationScopeDecision
	ExpressionKind SubjectExpressionKind
	MemberKind     SubjectKind
	// Committed is how many subjects the resolution committed.
	Committed int
	// CommittedAnchors is how many of them are BOUND to the frame's anchor
	// (see anchorBound).
	CommittedAnchors int
	// CommittedUnbound is how many committed subjects are not of the member
	// kind and are NOT bound to the anchor: a subject of another identity,
	// the shape an unbound "any non-member subject" rule counted.
	CommittedUnbound int
	// AnchorKind is the reading's anchor kind (ScopeAnchorRetrievalKind's
	// verdict), "" when the reading states none.
	AnchorKind SubjectKind
	// AnchorID is the canonical id of the first bound anchor, "" when none.
	AnchorID string
	// AnchorSubjectKind is the first bound anchor's OWN kind, as resolution
	// committed it -- distinct from AnchorKind, which is the reading's stated
	// kind and is "" whenever the question named no explicit kind even though
	// an anchor is bound. This is the identity a minted cardinality claim's
	// subject uses, and it is never absent when CommittedAnchors > 0.
	AnchorSubjectKind SubjectKind
	// Candidates is how many candidates the resolution carries.
	Candidates int
	// AnchorCandidates is how many of them could be the anchor: not of the
	// member kind, and of the reading's anchor kind when one is stated. Only
	// these make an unbound anchor ambiguous.
	AnchorCandidates int
}

// Counts reports whether the resolved member set is the requested population.
func (s CountPopulationScope) Counts() bool {
	return s.Decision == CountPopulationScopeOrganization || s.Decision == CountPopulationScopeAnchorCommitted
}

// DecideCountPopulationScope decides whether a resolved member set may be
// counted as the population the frame asks about.
//
// sampleAnchorKind is the reading's stated anchor kind (the winning sample's
// on the fresh path, the persisted reading's on reuse); bases is the
// resolution's commit basis set, nil where none is carried.
//
// PURE: reads its arguments and mutates nothing.
func DecideCountPopulationScope(frame *QuestionFrame, sampleAnchorKind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) CountPopulationScope {
	scope := CountPopulationScope{
		Committed:  len(resolution.Committed),
		Candidates: len(resolution.Candidates),
	}
	if frame == nil {
		scope.Decision = CountPopulationScopeFrameAbsent
		return scope
	}
	scope.ExpressionKind = frame.SubjectExpression.Kind
	scope.MemberKind, _ = frame.SubjectExpression.MemberKind()
	scope.AnchorKind = ScopeAnchorRetrievalKind(frame, sampleAnchorKind)
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == scope.MemberKind {
			continue
		}
		if scope.AnchorKind != "" && candidate.Subject.Kind != scope.AnchorKind {
			continue
		}
		scope.AnchorCandidates++
	}
	for _, subject := range resolution.Committed {
		if subject.Kind == scope.MemberKind {
			continue
		}
		if anchorBound(frame, scope.AnchorKind, subject, resolution, bases) {
			scope.CommittedAnchors++
			if scope.AnchorID == "" {
				scope.AnchorID = subject.CanonicalID
				scope.AnchorSubjectKind = subject.Kind
			}
			continue
		}
		scope.CommittedUnbound++
	}
	switch {
	case scope.ExpressionKind != SubjectExpressionChildrenOfScope:
		scope.Decision = CountPopulationScopeOrganization
	case scope.CommittedAnchors > 0:
		scope.Decision = CountPopulationScopeAnchorCommitted
	case scope.AnchorCandidates > 1:
		scope.Decision = CountPopulationScopeAnchorAmbiguous
	default:
		scope.Decision = CountPopulationScopeAnchorUnresolved
	}
	return scope
}

// anchorBound reports whether a committed subject is the frame's anchor, from
// the resolution's own record rather than from its kind alone.
//
// KIND: when the reading states an anchor kind, only a subject of that kind can
// be the anchor. PROVENANCE: the subject was committed on the caller's own
// canonical id, or resolution recorded it as a match for one of the frame's
// anchor terms. The terms are compared as retrieval pointers against
// resolution's record of what each pointer matched, never as values.
func anchorBound(frame *QuestionFrame, anchorKind SubjectKind, subject SubjectRef, resolution SubjectResolution, bases CommitBasisSet) bool {
	if anchorKind != "" && subject.Kind != anchorKind {
		return false
	}
	if bases.For(subject) == CommitBasisCallerCanonicalID {
		return true
	}
	if frame.SubjectExpression.Scoped == nil {
		return false
	}
	terms := make(map[string]struct{}, len(frame.SubjectExpression.Scoped.AnchorTerms))
	for _, term := range frame.SubjectExpression.Scoped.AnchorTerms {
		if normalized := NormalizeRetrievalTerm(term); normalized != "" {
			terms[normalized] = struct{}{}
		}
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != subject.Kind || candidate.Subject.CanonicalID != subject.CanonicalID {
			continue
		}
		for _, matched := range candidate.MatchedTerms {
			if _, anchor := terms[NormalizeRetrievalTerm(matched)]; anchor {
				return true
			}
		}
	}
	return false
}

// NormalizeRetrievalTerm is the one normalization a retrieval pointer is
// compared under: trimmed and lower-cased. graphrank.NormalizeAliasTerm is
// defined as this function, so resolution's matching and this comparison
// cannot drift apart.
func NormalizeRetrievalTerm(term string) string {
	return strings.ToLower(strings.TrimSpace(term))
}

// storedCountReading is what the count scope decision reads from the reading
// persisted beside a stored row.
type storedCountReading struct {
	Frame      *QuestionFrame
	AnchorKind SubjectKind
}

// storedCountReadingOf is the reading persisted beside a stored row, or the
// zero reading when it is not available -- absent, unreadable, or carrying no
// frame. A zero reading is an absence the decision refuses to count over.
func storedCountReadingOf(stored StoredInvestigationResult) storedCountReading {
	if stored.SemanticStateRead != SemanticStateReadAvailable || stored.SemanticState == nil || !stored.SemanticState.FramePresent {
		return storedCountReading{}
	}
	return storedCountReading{Frame: stored.SemanticState.Frame, AnchorKind: stored.SemanticState.ScopeAnchor.Kind}
}

// storedDocumentStatesCount reports whether a stored document already states a
// count: an assembled count row that counted, or a cardinality claim. The
// answer sentence is composed only beside those two, so it is not read.
func storedDocumentStatesCount(result InvestigationResult) bool {
	if resultCarriesCardinalityClaim(result) {
		return true
	}
	for _, row := range result.Completeness.Outcomes {
		if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult || row.Obligation != string(ObligationCount) {
			continue
		}
		if row.Outcome == contractsv1.ContextFabricRequirementSatisfied || row.Outcome == contractsv1.ContextFabricRequirementNarrowed {
			return true
		}
	}
	return false
}

// scopedMembershipCardinality applies the decision to a computed cardinality.
//
// A member set that is not the requested population is reported the way an
// absent member set is -- `Resolved` false -- because for this question it is
// one: no member set of the requested scope was resolved. Every consumer of
// the value (the outcome row, the claim, the answer sentence and the
// each-member read population) already treats that as an absence, never as a
// count of zero, so the whole document says the same thing about it.
//
// The decision rides on the value either way, because it is made over the
// resolution retrieval ran under. The served document's resolution can differ:
// commit affirmation runs after synthesis and may retract a committed anchor
// the answer did not itself stand behind, and a line re-deriving the decision
// from the served document would then describe a decision nobody made.
func scopedMembershipCardinality(cardinality MembershipCardinality, scope CountPopulationScope) MembershipCardinality {
	if !scope.Counts() {
		return MembershipCardinality{Scope: scope}
	}
	cardinality.Scope = scope
	return cardinality
}

// CountPopulationScopeLogMessage is the msg of the decision's Info line.
const CountPopulationScopeLogMessage = "context fabric count population scope"

// CountPopulationScopeEvent is the decision's telemetry, read off the SERVED
// document: the requested population (expression, member kind, requirement), what
// resolution measured, the decision, and what the document then states.
type CountPopulationScopeEvent struct {
	Requirement       string
	Scope             CountPopulationScope
	MemberSetResolved bool
	Members           int
	// Counted is whether the served document's assembled count row states a
	// count (satisfied or narrowed); Served is that row's served number.
	Counted  bool
	Served   int
	Assembly contractsv1.ContextFabricPlanRequirementOutcome
	Reused   bool
	// SubjectKind and SubjectID are the minted cardinality claim's own
	// subject, READ OFF the served document's claim rather than re-derived
	// from Scope -- the same discipline Counted/Served already follow, so a
	// claim dropped at the contract cap (cardinalityClaimAdmitted) reports an
	// empty subject here even though Scope still says the count is owed.
	// Both are "" when the served document carries no cardinality claim.
	SubjectKind SubjectKind
	SubjectID   string
}

// countPopulationScopeEventFrom builds the event from the served document, or
// reports false when the document owes no count.
// The scope is the decision the pass CARRIED, never re-derived here.
func countPopulationScopeEventFrom(result InvestigationResult, scope CountPopulationScope, reused bool) (CountPopulationScopeEvent, bool) {
	requirement, obligation := countRequirement(result.Completeness.Outcomes)
	if requirement == "" {
		return CountPopulationScopeEvent{}, false
	}
	event := CountPopulationScopeEvent{
		Requirement: requirement,
		Scope:       scope,
		Reused:      reused,
		Assembly:    countPopulationScopeAssemblyNone,
	}
	if result.Cohort != nil {
		event.MemberSetResolved = true
		event.Members = len(result.Cohort.Members)
	}
	for _, row := range result.Completeness.Outcomes {
		if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult || row.Requirement != requirement || row.Obligation != obligation {
			continue
		}
		event.Assembly = row.Outcome
		event.Served = row.Served
		event.Counted = row.Outcome == contractsv1.ContextFabricRequirementSatisfied || row.Outcome == contractsv1.ContextFabricRequirementNarrowed
		break
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contractsv1.ContextFabricFactCardinality {
			event.SubjectKind = claim.Subject.Kind
			event.SubjectID = claim.Subject.CanonicalID
			break
		}
	}
	return event, true
}

// countPopulationScopeAssemblyNone is the line's explicit token for a document
// that carries no assembled count row.
const countPopulationScopeAssemblyNone contractsv1.ContextFabricPlanRequirementOutcome = "none"

// CountPopulationScopeLogArgs builds the Info line's fields. Every key is
// written on every line, zeroes included.
func CountPopulationScopeLogArgs(event CountPopulationScopeEvent, orgID string) []any {
	return []any{
		"org_id", SanitizeLogAttr(orgID),
		// PRE-ENTRY: the population the question asked for.
		"expression_kind", SanitizeLogAttr(string(event.Scope.ExpressionKind)),
		"member_kind", SanitizeLogAttr(string(event.Scope.MemberKind)),
		"requirement", SanitizeLogAttr(event.Requirement),
		// PRE-DECISION: what resolution and retrieval measured.
		"committed", SanitizeLogInt(int64(event.Scope.Committed)),
		"committed_anchors", SanitizeLogInt(int64(event.Scope.CommittedAnchors)),
		"committed_unbound", SanitizeLogInt(int64(event.Scope.CommittedUnbound)),
		"anchor_kind", SanitizeLogAttr(string(event.Scope.AnchorKind)),
		"anchor_id", SanitizeLogAttr(event.Scope.AnchorID),
		"candidates", SanitizeLogInt(int64(event.Scope.Candidates)),
		"anchor_candidates", SanitizeLogInt(int64(event.Scope.AnchorCandidates)),
		"member_set_resolved", event.MemberSetResolved,
		"members", SanitizeLogInt(int64(event.Members)),
		// DECISION.
		"decision", SanitizeLogAttr(string(event.Scope.Decision)),
		// POST-DECISION: what the served document states.
		"assembled_outcome", SanitizeLogAttr(string(event.Assembly)),
		"counted", event.Counted,
		"served", SanitizeLogInt(int64(event.Served)),
		"reused", event.Reused,
		// The minted claim's own subject, "" when the document carries none.
		"subject_kind", SanitizeLogAttr(string(event.SubjectKind)),
		"subject_id", SanitizeLogAttr(event.SubjectID),
	}
}

// recordCountPopulationScope emits the served document's decision, if it owes
// a count.
func (e *Engine) recordCountPopulationScope(ctx context.Context, principal storage.Principal, result InvestigationResult, scope CountPopulationScope, reused bool) {
	if e.telemetry == nil {
		return
	}
	if event, owed := countPopulationScopeEventFrom(result, scope, reused); owed {
		e.telemetry.RecordCountPopulationScope(ctx, principal, event)
	}
}
