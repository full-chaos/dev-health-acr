package contextfabric

import (
	"context"

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
	// an anchor, and at least one committed subject is not of the member kind
	// -- the anchor the member set was retrieved under. The count stands.
	CountPopulationScopeAnchorCommitted CountPopulationScopeDecision = "anchor_committed"
	// CountPopulationScopeAnchorUnresolved: the question counts members under
	// an anchor, no committed subject can be that anchor, and at most one
	// candidate was offered. The member set is not bounded by the requested
	// scope, so it is not counted.
	CountPopulationScopeAnchorUnresolved CountPopulationScopeDecision = "anchor_unresolved"
	// CountPopulationScopeAnchorAmbiguous: as unresolved, but more than one
	// candidate was offered and none committed. Not counted.
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
	// CommittedAnchors is how many of them are NOT of the member kind -- the
	// subjects that can be the anchor a scoped member set hangs off.
	CommittedAnchors int
	// Candidates is how many uncommitted candidates the resolution offered.
	Candidates int
}

// Counts reports whether the resolved member set is the requested population.
func (s CountPopulationScope) Counts() bool {
	return s.Decision == CountPopulationScopeOrganization || s.Decision == CountPopulationScopeAnchorCommitted
}

// DecideCountPopulationScope decides whether a resolved member set may be
// counted as the population the frame asks about.
//
// PURE: reads its arguments and mutates nothing.
func DecideCountPopulationScope(frame *QuestionFrame, resolution SubjectResolution) CountPopulationScope {
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
	for _, subject := range resolution.Committed {
		if subject.Kind != scope.MemberKind {
			scope.CommittedAnchors++
		}
	}
	switch {
	case scope.ExpressionKind != SubjectExpressionChildrenOfScope:
		scope.Decision = CountPopulationScopeOrganization
	case scope.CommittedAnchors > 0:
		scope.Decision = CountPopulationScopeAnchorCommitted
	case scope.Candidates > 1:
		scope.Decision = CountPopulationScopeAnchorAmbiguous
	default:
		scope.Decision = CountPopulationScopeAnchorUnresolved
	}
	return scope
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
		"candidates", SanitizeLogInt(int64(event.Scope.Candidates)),
		"member_set_resolved", event.MemberSetResolved,
		"members", SanitizeLogInt(int64(event.Members)),
		// DECISION.
		"decision", SanitizeLogAttr(string(event.Scope.Decision)),
		// POST-DECISION: what the served document states.
		"assembled_outcome", SanitizeLogAttr(string(event.Assembly)),
		"counted", event.Counted,
		"served", SanitizeLogInt(int64(event.Served)),
		"reused", event.Reused,
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
