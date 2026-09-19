package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A stored result is served to a caller only when the live authorization
// decision a fresh turn would take about every subject the result names admits
// that caller NOW. The organization check the store applies on Get is
// necessary and not sufficient: a credential scoped to one repository must not
// read an answer about a subject outside that repository just because both
// live in the same organization. Nothing about the caller is remembered with
// the result, so a grant that narrowed after the answer was saved is honoured
// on the next read.
//
// Every reader of a stored result takes this one decision: the result-by-id
// route (and the MCP tool, which forwards that route), every engine read of a
// prior result through the Engine's results store, which is wrapped at
// construction so no engine reader can reach a stored payload undecided, and
// every answer-reuse candidate before it can be served.

// StoredSubjectOutcome is the live decision for one subject a stored result
// names.
type StoredSubjectOutcome string

const (
	// StoredSubjectAdmitted: the subject's graph node exists in the caller's
	// organization graph and the shared authorization predicate admits the
	// caller.
	StoredSubjectAdmitted StoredSubjectOutcome = "admitted"
	// StoredSubjectDenied: the node exists and the predicate refuses the
	// caller.
	StoredSubjectDenied StoredSubjectOutcome = "denied"
	// StoredSubjectAbsent: no node exists under the subject's identity, so its
	// visibility cannot be proven. Treated as a refusal.
	StoredSubjectAbsent StoredSubjectOutcome = "absent"
)

// StoredSubjectAuthorizer decides visibility for a batch of subjects in the
// caller's organization graph. It returns one outcome per subject, in order.
// The graph backend implements it; the decision rule itself is
// graphrank.AuthorizeStoredSubjectNodes, the same predicate every live read
// site applies.
type StoredSubjectAuthorizer interface {
	AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error)
}

// StoredResultSurface names the reader that asked for the decision.
type StoredResultSurface string

const (
	// StoredResultSurfaceResultByID is the result-by-id route. The MCP
	// investigation_result tool forwards that route, so it reports here too.
	StoredResultSurfaceResultByID StoredResultSurface = "result_by_id"
	// StoredResultSurfacePriorResult is any engine read of a prior result:
	// receipts, carried axes, window continuations, parent resolution.
	StoredResultSurfacePriorResult StoredResultSurface = "prior_result"
	// StoredResultSurfaceAnswerReuse is a stored answer offered for reuse in
	// place of a fresh investigation.
	StoredResultSurfaceAnswerReuse StoredResultSurface = "answer_reuse"
)

// StoredResultSurfaceVocabulary is the closed set of surfaces.
func StoredResultSurfaceVocabulary() [3]StoredResultSurface {
	return [3]StoredResultSurface{StoredResultSurfaceResultByID, StoredResultSurfacePriorResult, StoredResultSurfaceAnswerReuse}
}

// StoredResultDecision is the served outcome.
type StoredResultDecision string

const (
	StoredResultAdmitted StoredResultDecision = "admitted"
	// StoredResultDenied: the result does not exist for this caller. Surfaces
	// answer exactly as for an unknown result id.
	StoredResultDenied StoredResultDecision = "denied"
	// StoredResultUnavailable: the decision could not be taken. Surfaces fail
	// closed with a retryable unavailability, never by serving.
	StoredResultUnavailable StoredResultDecision = "unavailable"
)

// StoredResultDecisionVocabulary is the closed set of decisions.
func StoredResultDecisionVocabulary() [3]StoredResultDecision {
	return [3]StoredResultDecision{StoredResultAdmitted, StoredResultDenied, StoredResultUnavailable}
}

// StoredResultAuthorizationReason is why the decision came out as it did.
type StoredResultAuthorizationReason string

const (
	// Admitted reasons.
	StoredResultReasonNoSubjects            StoredResultAuthorizationReason = "no_subjects"
	StoredResultReasonUnrestrictedPrincipal StoredResultAuthorizationReason = "unrestricted_principal"
	StoredResultReasonSubjectsAdmitted      StoredResultAuthorizationReason = "subjects_admitted"
	// Denied reasons, in precedence order.
	StoredResultReasonOrganizationMismatch StoredResultAuthorizationReason = "organization_mismatch"
	StoredResultReasonSubjectDenied        StoredResultAuthorizationReason = "subject_denied"
	StoredResultReasonSubjectAbsent        StoredResultAuthorizationReason = "subject_absent"
	StoredResultReasonGroupUnproven        StoredResultAuthorizationReason = "group_unproven"
	StoredResultReasonGraphNotProjected    StoredResultAuthorizationReason = "graph_not_projected"
	// Unavailable reasons.
	StoredResultReasonAuthorizerMissing StoredResultAuthorizationReason = "authorizer_missing"
	StoredResultReasonGraphReadFailed   StoredResultAuthorizationReason = "graph_read_failed"
)

// StoredResultAuthorizationReasonVocabulary is the closed set of reasons.
func StoredResultAuthorizationReasonVocabulary() [10]StoredResultAuthorizationReason {
	return [10]StoredResultAuthorizationReason{
		StoredResultReasonNoSubjects, StoredResultReasonUnrestrictedPrincipal, StoredResultReasonSubjectsAdmitted,
		StoredResultReasonOrganizationMismatch, StoredResultReasonSubjectDenied, StoredResultReasonSubjectAbsent,
		StoredResultReasonGroupUnproven, StoredResultReasonGraphNotProjected,
		StoredResultReasonAuthorizerMissing, StoredResultReasonGraphReadFailed,
	}
}

// StoredResultPrincipalScope classifies the caller's repository grant the way
// the shared predicate reads it.
type StoredResultPrincipalScope string

const (
	// StoredResultScopeUnrestricted: no repository grant list at all. The
	// shared predicate applies no repository check to such a caller.
	StoredResultScopeUnrestricted StoredResultPrincipalScope = "unrestricted"
	// StoredResultScopeUniversal: the grant list holds "*".
	StoredResultScopeUniversal StoredResultPrincipalScope = "universal"
	// StoredResultScopeRestricted: specific repositories or owner wildcards.
	StoredResultScopeRestricted StoredResultPrincipalScope = "restricted"
)

// StoredResultPrincipalScopeVocabulary is the closed set of scope classes.
func StoredResultPrincipalScopeVocabulary() [3]StoredResultPrincipalScope {
	return [3]StoredResultPrincipalScope{StoredResultScopeUnrestricted, StoredResultScopeUniversal, StoredResultScopeRestricted}
}

func classifyStoredResultPrincipalScope(principal storage.Principal) StoredResultPrincipalScope {
	switch {
	case len(principal.RepositoryScopes) == 0:
		return StoredResultScopeUnrestricted
	case slices.ContainsFunc(principal.RepositoryScopes, func(scope string) bool { return strings.TrimSpace(scope) == "*" }):
		return StoredResultScopeUniversal
	default:
		return StoredResultScopeRestricted
	}
}

// StoredResultAuthorization is one decision and everything the trace needs to
// rebuild it: what the caller holds, what the result names, and what each
// class of subject came out as. It carries counts and kinds only, never ids or
// labels.
type StoredResultAuthorization struct {
	Surface              StoredResultSurface
	PrincipalScope       StoredResultPrincipalScope
	RepositoryScopeCount int
	Decision             StoredResultDecision
	Reason               StoredResultAuthorizationReason
	// SubjectCount is every distinct subject the result names.
	SubjectCount int
	// GraphSubjectCount is the subset decided by the graph predicate.
	GraphSubjectCount int
	// UnkindedSubjectCount is the subset of graph subjects the result names by
	// canonical id alone, decided by every node carrying that id.
	UnkindedSubjectCount int
	AdmittedCount        int
	DeniedCount          int
	AbsentCount          int
	// OrganizationSubjectCount / OrganizationMismatchCount: subjects of kind
	// organization, decided against the caller's own organization.
	OrganizationSubjectCount  int
	OrganizationMismatchCount int
	// GroupCount / GroupUnprovenCount: grouped-cohort group subjects. A group
	// key is produced by the fact plane from its members, not a graph node, so
	// it is admitted exactly when every member it lists is an admitted cohort
	// member.
	GroupCount         int
	GroupUnprovenCount int
	// RefusedKinds is the sorted set of subject kinds that were denied, absent,
	// mismatched or unproven.
	RefusedKinds []string
	// Err is the plane failure behind an unavailable decision, nil otherwise.
	Err error
}

// Admitted reports whether the result may be served.
func (a StoredResultAuthorization) Admitted() bool { return a.Decision == StoredResultAdmitted }

// ServingError maps the decision to the error a reader returns in place of
// the result: nil when admitted, ErrInvestigationResultNotFound when denied
// (the result does not exist for this caller), and ErrUnavailable wrapping
// the plane failure when the decision could not be taken.
func (a StoredResultAuthorization) ServingError() error {
	switch a.Decision {
	case StoredResultAdmitted:
		return nil
	case StoredResultDenied:
		return ErrInvestigationResultNotFound
	default:
		if a.Err != nil {
			return fmt.Errorf("%w: stored result authorization %s: %w", ErrUnavailable, a.Reason, a.Err)
		}
		return fmt.Errorf("%w: stored result authorization %s", ErrUnavailable, a.Reason)
	}
}

// StoredResultAuthorizationRecorder receives every engine-side decision.
// EngineTelemetry embeds it. The result-by-id route writes the same line with
// its own request logger.
type StoredResultAuthorizationRecorder interface {
	RecordStoredResultAuthorization(context.Context, storage.Principal, StoredResultAuthorization)
}

// StoredResultGate takes the stored-result decision. One gate is built per
// Engine from the Engine's own graph; the result-by-id route uses the Engine's
// gate, so both surfaces take the identical decision.
type StoredResultGate struct {
	graph    GraphReader
	subjects StoredSubjectAuthorizer
}

// NewStoredResultGate builds a gate over graph. The graph must implement
// StoredSubjectAuthorizer for a restricted caller to be admitted to any result
// that names a graph subject; without it such reads fail closed as
// unavailable.
func NewStoredResultGate(graph GraphReader) *StoredResultGate {
	gate := &StoredResultGate{graph: graph}
	if authorizer, ok := graph.(StoredSubjectAuthorizer); ok {
		gate.subjects = authorizer
	}
	return gate
}

// Authorize decides whether stored may be served to principal on surface.
// The caller writes the decision to the trace.
func (g *StoredResultGate) Authorize(ctx context.Context, principal storage.Principal, stored StoredInvestigationResult, surface StoredResultSurface) StoredResultAuthorization {
	return g.decide(ctx, principal, stored.Result, surface)
}

func (g *StoredResultGate) decide(ctx context.Context, principal storage.Principal, result InvestigationResult, surface StoredResultSurface) StoredResultAuthorization {
	decision := StoredResultAuthorization{
		Surface:              surface,
		PrincipalScope:       classifyStoredResultPrincipalScope(principal),
		RepositoryScopeCount: len(principal.RepositoryScopes),
	}
	subjects := StoredResultSubjects(result)
	decision.SubjectCount = len(subjects)

	groups := storedResultGroups(result)
	decision.GroupCount = len(groups)
	groupKeys := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupKeys[SubjectMapKey(group.Subject)] = struct{}{}
	}

	refused := map[string]struct{}{}
	var graphSubjects []SubjectRef
	for _, subject := range subjects {
		if _, isGroup := groupKeys[SubjectMapKey(subject)]; isGroup {
			continue
		}
		if subject.Kind == contractsv1.ContextFabricSubjectOrganization {
			decision.OrganizationSubjectCount++
			if !organizationSubjectIsCallers(principal, subject) {
				decision.OrganizationMismatchCount++
				refused[string(subject.Kind)] = struct{}{}
			}
			continue
		}
		graphSubjects = append(graphSubjects, subject)
	}
	decision.GraphSubjectCount = len(graphSubjects)
	for _, subject := range graphSubjects {
		if subject.Kind == "" {
			decision.UnkindedSubjectCount++
		}
	}

	admittedMembers := map[string]struct{}{}
	switch {
	case len(graphSubjects) == 0:
	case decision.PrincipalScope == StoredResultScopeUnrestricted:
		decision.AdmittedCount = len(graphSubjects)
		for _, subject := range graphSubjects {
			admittedMembers[SubjectMapKey(subject)] = struct{}{}
		}
	case g == nil || g.subjects == nil:
		decision.Decision, decision.Reason = StoredResultUnavailable, StoredResultReasonAuthorizerMissing
		return finishStoredResultAuthorization(decision, refused)
	default:
		binding, err := g.graph.ResolveInvestigationBinding(ctx, principal)
		var outcomes []StoredSubjectOutcome
		if err == nil {
			outcomes, err = g.subjects.AuthorizeStoredSubjects(ctx, principal, binding, graphSubjects)
		}
		switch {
		case errors.Is(err, ErrGraphNotProjected):
			decision.Decision, decision.Reason = StoredResultDenied, StoredResultReasonGraphNotProjected
			for _, subject := range graphSubjects {
				refuseKind(refused, subject)
			}
			decision.AbsentCount = len(graphSubjects)
			return finishStoredResultAuthorization(decision, refused)
		case err != nil:
			decision.Decision, decision.Reason, decision.Err = StoredResultUnavailable, StoredResultReasonGraphReadFailed, err
			return finishStoredResultAuthorization(decision, refused)
		case len(outcomes) != len(graphSubjects):
			decision.Decision, decision.Reason = StoredResultUnavailable, StoredResultReasonGraphReadFailed
			decision.Err = fmt.Errorf("stored subject authorizer returned %d outcomes for %d subjects", len(outcomes), len(graphSubjects))
			return finishStoredResultAuthorization(decision, refused)
		}
		for index, outcome := range outcomes {
			subject := graphSubjects[index]
			switch outcome {
			case StoredSubjectAdmitted:
				decision.AdmittedCount++
				admittedMembers[SubjectMapKey(subject)] = struct{}{}
			case StoredSubjectDenied:
				decision.DeniedCount++
				refuseKind(refused, subject)
			default:
				decision.AbsentCount++
				refuseKind(refused, subject)
			}
		}
	}

	for _, group := range groups {
		if !groupProvenByMembers(group, result.Cohort, admittedMembers) {
			decision.GroupUnprovenCount++
			refused[string(group.Subject.Kind)] = struct{}{}
		}
	}

	switch {
	case decision.OrganizationMismatchCount > 0:
		decision.Decision, decision.Reason = StoredResultDenied, StoredResultReasonOrganizationMismatch
	case decision.DeniedCount > 0:
		decision.Decision, decision.Reason = StoredResultDenied, StoredResultReasonSubjectDenied
	case decision.AbsentCount > 0:
		decision.Decision, decision.Reason = StoredResultDenied, StoredResultReasonSubjectAbsent
	case decision.GroupUnprovenCount > 0:
		decision.Decision, decision.Reason = StoredResultDenied, StoredResultReasonGroupUnproven
	case decision.SubjectCount == 0:
		decision.Decision, decision.Reason = StoredResultAdmitted, StoredResultReasonNoSubjects
	case decision.PrincipalScope == StoredResultScopeUnrestricted:
		decision.Decision, decision.Reason = StoredResultAdmitted, StoredResultReasonUnrestrictedPrincipal
	default:
		decision.Decision, decision.Reason = StoredResultAdmitted, StoredResultReasonSubjectsAdmitted
	}
	return finishStoredResultAuthorization(decision, refused)
}

// refuseKind names a refused subject's kind on the trace. A subject named by
// canonical id alone carries no kind; it is counted in unkinded_subject_count
// and its refusal in the denied/absent counts.
func refuseKind(refused map[string]struct{}, subject SubjectRef) {
	if subject.Kind != "" {
		refused[string(subject.Kind)] = struct{}{}
	}
}

func finishStoredResultAuthorization(decision StoredResultAuthorization, refused map[string]struct{}) StoredResultAuthorization {
	decision.RefusedKinds = make([]string, 0, len(refused))
	for kind := range refused {
		decision.RefusedKinds = append(decision.RefusedKinds, kind)
	}
	sort.Strings(decision.RefusedKinds)
	return decision
}

// organizationSubjectIsCallers reports whether an organization subject names
// the caller's own organization, under either identity form the engine has
// written: the graph's "organization:<id>" key and the bare organization id.
func organizationSubjectIsCallers(principal storage.Principal, subject SubjectRef) bool {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return false
	}
	return subject.CanonicalID == orgID || subject.CanonicalID == "organization:"+orgID
}

// groupProvenByMembers: a group is shown only through the members it
// groups, so it is admitted exactly when it lists at least one member and
// every listed member is an admitted cohort member of the cohort's kind.
func groupProvenByMembers(group contractsv1.ContextFabricCohortGroup, cohort *contractsv1.ContextFabricCohort, admitted map[string]struct{}) bool {
	if cohort == nil || len(group.MemberCanonicalIDs) == 0 {
		return false
	}
	memberKind := map[string]SubjectKind{}
	for _, member := range cohort.Members {
		memberKind[member.Subject.CanonicalID] = member.Subject.Kind
	}
	for _, id := range group.MemberCanonicalIDs {
		kind, listed := memberKind[id]
		if !listed {
			return false
		}
		if _, ok := admitted[SubjectMapKey(SubjectRef{Kind: kind, CanonicalID: id})]; !ok {
			return false
		}
	}
	return true
}

func storedResultGroups(result InvestigationResult) []contractsv1.ContextFabricCohortGroup {
	if result.Cohort == nil {
		return nil
	}
	return result.Cohort.Groups
}

var (
	subjectKindType = reflect.TypeOf(contractsv1.ContextFabricSubjectKind(""))
	stringType      = reflect.TypeOf("")
)

// StoredResultSubjects returns every distinct subject a result names, in
// first-seen order. It walks the WHOLE result by reflection, so a field added
// to the contract later is decided without a change here. A subject is named
// in one of two shapes:
//
//   - a struct carrying a subject kind and a canonical id (a subject
//     reference, an offer option);
//   - a canonical id carried without its kind: a confirmed structure entry
//     whose member names a subject (anchor, candidate, handle), or a field
//     named SubjectCanonicalID. These are returned with an empty kind and are
//     decided by every graph node carrying the id.
func StoredResultSubjects(result InvestigationResult) []SubjectRef {
	seen := map[string]struct{}{}
	var subjects []SubjectRef
	collectStoredResultSubjects(reflect.ValueOf(result), func(subject SubjectRef) {
		if strings.TrimSpace(subject.CanonicalID) == "" {
			return
		}
		key := SubjectMapKey(subject)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		subjects = append(subjects, subject)
	})
	return subjects
}

// storedSubjectStructureMembers are the confirmed-structure members whose
// applied value is a subject identity rather than a kind or a window.
var storedSubjectStructureMembers = map[contractsv1.ContextFabricStructureNeedKind]bool{
	contractsv1.ContextFabricStructureNeedSubjectAnchor:    true,
	contractsv1.ContextFabricStructureNeedSubjectCandidate: true,
	contractsv1.ContextFabricStructureNeedSubjectHandle:    true,
}

var confirmedStructureEntryType = reflect.TypeOf(contractsv1.ContextFabricConfirmedStructureEntry{})

func collectStoredResultSubjects(value reflect.Value, emit func(SubjectRef)) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		// Elem of a nil pointer or interface is the invalid Value, which the
		// switch ignores.
		collectStoredResultSubjects(value.Elem(), emit)
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			collectStoredResultSubjects(value.Index(index), emit)
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			collectStoredResultSubjects(iterator.Value(), emit)
		}
	case reflect.Struct:
		if subject, ok := subjectShaped(value); ok {
			emit(subject)
		}
		if value.Type() == confirmedStructureEntryType {
			entry := value.Interface().(contractsv1.ContextFabricConfirmedStructureEntry)
			if storedSubjectStructureMembers[entry.Member] {
				emit(SubjectRef{CanonicalID: entry.AppliedValue})
			}
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			if field.Name == "SubjectCanonicalID" && field.Type == stringType {
				emit(SubjectRef{CanonicalID: value.Field(index).String()})
			}
			collectStoredResultSubjects(value.Field(index), emit)
		}
	}
}

// subjectShaped reports whether a struct identifies a subject: an exported
// Kind of the subject-kind type and an exported string CanonicalID.
func subjectShaped(value reflect.Value) (SubjectRef, bool) {
	kind := value.FieldByName("Kind")
	canonicalID := value.FieldByName("CanonicalID")
	if !kind.IsValid() || !canonicalID.IsValid() || kind.Type() != subjectKindType || canonicalID.Type() != stringType {
		return SubjectRef{}, false
	}
	return SubjectRef{Kind: SubjectKind(kind.String()), CanonicalID: canonicalID.String()}, true
}

// authorizedResultStore is the Engine's view of its result store: every Get
// is decided by the gate before the payload reaches any engine reader, so a
// prior result the caller may not read is indistinguishable from one that
// does not exist, on every carry, receipt and window path at once.
type authorizedResultStore struct {
	InvestigationResultStore
	gate     *StoredResultGate
	recorder StoredResultAuthorizationRecorder
}

func (s authorizedResultStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	stored, err := s.InvestigationResultStore.Get(ctx, principal, resultID)
	if err != nil {
		return StoredInvestigationResult{}, err
	}
	decision := s.gate.Authorize(ctx, principal, stored, StoredResultSurfacePriorResult)
	if s.recorder != nil {
		s.recorder.RecordStoredResultAuthorization(ctx, principal, decision)
	}
	if err := decision.ServingError(); err != nil {
		return StoredInvestigationResult{}, err
	}
	return stored, nil
}

// StoredResultAuthorizationLogMessage is the Info line every decision emits.
const StoredResultAuthorizationLogMessage = "context fabric stored result authorization"

// StoredResultAuthorizationLogArgs renders a decision for the trace; the
// caller appends request_id.
func StoredResultAuthorizationLogArgs(principal storage.Principal, decision StoredResultAuthorization) []any {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"surface", string(decision.Surface),
		"principal_scope", string(decision.PrincipalScope),
		"repository_scope_count", decision.RepositoryScopeCount,
		"decision", string(decision.Decision),
		"reason", string(decision.Reason),
		"subject_count", decision.SubjectCount,
		"graph_subject_count", decision.GraphSubjectCount,
		"unkinded_subject_count", decision.UnkindedSubjectCount,
		"admitted_count", decision.AdmittedCount,
		"denied_count", decision.DeniedCount,
		"absent_count", decision.AbsentCount,
		"organization_subject_count", decision.OrganizationSubjectCount,
		"organization_mismatch_count", decision.OrganizationMismatchCount,
		"group_count", decision.GroupCount,
		"group_unproven_count", decision.GroupUnprovenCount,
		"refused_kinds", SanitizeLogStrings(append([]string{}, decision.RefusedKinds...)),
	}
	if decision.Err != nil {
		args = append(args, "error_class", SanitizeLogAttr(storedResultErrorClass(decision.Err)))
	}
	return args
}

func (t SlogEngineTelemetry) RecordStoredResultAuthorization(ctx context.Context, principal storage.Principal, decision StoredResultAuthorization) {
	args := StoredResultAuthorizationLogArgs(principal, decision)
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, StoredResultAuthorizationLogMessage, args...)
}

// storedResultErrorClass names a plane failure without its text, which may
// carry query content.
func storedResultErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrUnavailable):
		return "dependency_unavailable"
	default:
		return "graph_error"
	}
}

// StoredResultGate returns the gate this Engine decides every prior-result
// read with, so the result-by-id route takes the identical decision.
func (e *Engine) StoredResultGate() *StoredResultGate {
	if e == nil {
		return nil
	}
	return e.storedResultGate
}
