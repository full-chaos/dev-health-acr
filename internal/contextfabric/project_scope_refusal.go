package contextfabric

import (
	"context"
	"fmt"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// requestedProjectNarrowingRefused reports a project-only request that names
// projects (CHAOS-12330, ruling B). It is the node rule's own test
// (graphrank.projectOnlyAdmits), so the stop and the refusal cannot disagree:
// such a request admits no node, and an answer over what is left would read
// as "nothing found". CHAOS-12324 narrows it once CHAOS-12229 maps raw ids.
func requestedProjectNarrowingRefused(principal storage.Principal, scope RequestedScope) bool {
	return len(scope.ProjectIDs) > 0 && projectScopeWithheld(principal)
}

// projectNarrowingRefusalPlaceholderJudgment mirrors the other refusing
// terminals': the refused request was never interpreted.
const projectNarrowingRefusalPlaceholderJudgment = "requested project narrowing not available"

// projectNarrowingRefusalResult is the terminal of a refused project narrowing.
// It runs before every read: no reuse lookup, no interpretation, no graph
// read (the narrowing resolver is one) and no fact read, so nothing is
// answered over the whole grant. The status is no_match because there is no
// judgment to state, and the typed degrading detail is what tells it apart
// from a search that found nothing.
//
// Never reusable, by the same nil snapshots every refusing terminal saves
// with: the store reads them as "never reusable".
func (e *Engine) projectNarrowingRefusalResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding) (InvestigationResult, error) {
	limitation := contractsv1.ContextFabricRequestedProjectNarrowingUnavailableLimitation
	limitations, _ := appendBoundedLimitations(nil, []string{limitation})
	coverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	appendProjectScopeKindWithheld(&coverage, SubjectProject)
	refusedInterpretation := InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: projectNarrowingRefusalPlaceholderJudgment,
		TimeContext:       request.TimeContext,
		FactRequirements:  []FactRequirement{},
	}
	result := InvestigationResult{
		SchemaVersion:       InvestigationResultSchemaV1,
		ResultID:            e.mintResultID(ctx),
		RequestID:           request.RequestID,
		GeneratedAt:         e.now().UTC(),
		Status:              InvestigationNoMatch,
		Question:            request.Question,
		Reused:              false,
		Interpretation:      refusedInterpretation,
		SubjectResolution:   SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		StrongestPressures:  []string{},
		Drivers:             []DriverJudgment{},
		RemainingWork:       []Finding{},
		ReadinessGaps:       []Finding{},
		Paths:               []RelationshipPath{},
		Conflicts:           []Finding{},
		Limitations:         limitations,
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []ClaimedFact{},
		Coverage:            coverage,
		Temporal:            composeTemporalLabel(refusedInterpretation, coverage, ""),
		Versions:            e.terminalVersions(ctx),
		DeterministicAnswer: limitation,
		Warnings:            []string{},
	}
	result.Completeness = ComputeAnswerCompleteness(result)
	if fallbacks := applyCoverageDisplayLabels(&result); fallbacks > 0 && e.telemetry != nil {
		e.telemetry.RecordEvidenceLabelFallback(ctx, principal, fallbacks)
	}
	result, err := e.finalizeServed(ctx, principal, BudgetAssertSubjectlessTerminal, result, nil, e.effectiveResponseBudget(request))
	if err != nil {
		return InvestigationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		ancestryParent := ancestryRoot(request, receiptsNotYetValidated())
		if err := e.saveResult(ctx, principal, BudgetAssertSubjectlessTerminal, result, nil, nil, TimeAxisKeyFor(request.TimeContext), binding.Epoch, ancestryParent, absentSemanticState(SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}

// projectScopeRefusedKinds are the subject kinds a project grant does not
// reach yet and an answer plan can ask for by name (CHAOS-12299 rulings A and
// C; CHAOS-12323 admits teams, CHAOS-12308 pull requests).
var projectScopeRefusedKinds = []SubjectKind{SubjectTeam, SubjectPullRequest, SubjectRepository}

// discloseProjectScopeRefusedKinds files one degrading detail per refused kind
// the answer plan ASKED for, on every serving path. Only an asked kind: a
// detail on every answer would say nothing, and it states the grant class,
// never that a subject of the kind exists.
//
// The asked kind is read from the plan's requirement rows as well as from its
// member kind, which is set only once a cohort exists: a refused kind has
// none. A "none found" row of the kind goes, because the search could not see
// that kind, so "none" is a claim it never measured (CHAOS-12330: never zero).
func discloseProjectScopeRefusedKinds(principal storage.Principal, result InvestigationResult) InvestigationResult {
	if result.AnswerPlan == nil || !projectScopeWithheld(principal) {
		return result
	}
	for _, kind := range projectScopeRefusedKinds {
		if planAsksForKind(*result.AnswerPlan, kind) {
			dropNoMemberFoundRow(&result.Coverage, kind)
			appendProjectScopeKindWithheld(&result.Coverage, kind)
		}
	}
	return result
}

func planAsksForKind(plan AnswerPlan, kind SubjectKind) bool {
	if plan.MemberKind == kind || plan.GroupKind == kind {
		return true
	}
	return slices.ContainsFunc(plan.Requirements, func(requirement contractsv1.ContextFabricPlanRequirement) bool {
		return requirement.Subject == kind
	})
}

// dropNoMemberFoundRow removes the kind's graph_no_member_found detail and the
// degraded reason it pairs with. Clones, as appendProjectScopeKindWithheld
// clips: a finalization runs on copies of one document.
func dropNoMemberFoundRow(coverage *Coverage, kind SubjectKind) {
	index := slices.IndexFunc(coverage.Details, func(detail CoverageDetail) bool {
		return detail.Code == contractsv1.ContextFabricCoverageDetailGraphNoMemberFound && detail.Kind == kind
	})
	if index < 0 {
		return
	}
	raw := coverage.Details[index].Raw
	coverage.Details = slices.Delete(slices.Clone(coverage.Details), index, index+1)
	if reason := slices.Index(coverage.DegradedReasons, raw); reason >= 0 {
		coverage.DegradedReasons = slices.Delete(slices.Clone(coverage.DegradedReasons), reason, reason+1)
	}
}

// appendProjectScopeKindWithheld files a refused subject kind, once. The label
// is the raw text, so the degraded reason pairs with the detail and no model
// wrote either. The slices are clipped: a finalization runs on copies of one
// document, and they must not share what is appended here.
func appendProjectScopeKindWithheld(coverage *Coverage, kind SubjectKind) {
	for _, existing := range coverage.Details {
		if existing.Code == contractsv1.ContextFabricCoverageDetailProjectScopeWithheld && existing.Kind == kind {
			return
		}
	}
	detail := CoverageDetail{
		DetailID: fmt.Sprintf("cov-scope-%02d", len(coverage.Details)+1), Source: "context-fabric:scope",
		Code: contractsv1.ContextFabricCoverageDetailProjectScopeWithheld, Degrading: true, Kind: kind, SourceState: SourceUnauthorized,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	detail.Raw = detail.Label
	coverage.Partial = true
	coverage.DegradedReasons = append(slices.Clip(coverage.DegradedReasons), detail.Raw)
	coverage.Details = append(slices.Clip(coverage.Details), detail)
}
