package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Evidence expansion for Context Fabric evidence refs.
//
// An investigation cites evidence as acr:v1:<entity-type>:<id> refs
// (contractsv1.EvidenceRefID). The projection contract promises that every
// ref an answer carries expands through the hosted evidence route, the same
// route source_evidence calls. A Context Fabric ref is not a packet evidence
// handle, so the packet evidence store cannot resolve it; the record it names
// is the stored investigation result that cited it. ExpandCitedEvidence
// serves the ref from that result: it finds the stored results of the
// caller's organization whose evidence-ref closure holds the ref, re-runs the
// live stored-result authorization for this caller on each (newest first),
// and builds the expansion from the first admitted result's persisted
// content only. What it returns is therefore never more than
// investigation_result would serve the same caller for that result, and a
// caller the result is not readable by gets the same not-found as for a ref
// nobody cited.

// CitedEvidenceCandidateLimit is the page size of the citing-result search.
// The search pages until a result is served or no citing result is left, so
// a readable result is never hidden behind newer ones the caller may not
// read.
const CitedEvidenceCandidateLimit = 16

// EvidenceExpansionLogMessage is the one Info line an expansion emits.
const EvidenceExpansionLogMessage = "context fabric evidence expansion"

// ContextFabricEvidenceSystem is the source system an expansion names. It
// says, in a typed field, that the expansion is the persisted investigation
// evidence record (the label and the citing sites of a stored result), not
// the source row the ref's entity id names.
const ContextFabricEvidenceSystem = "acr-investigation-record"

// ContextFabricEvidenceProvenance is the expansion's provenance: derived from
// a stored investigation result.
const ContextFabricEvidenceProvenance = "derived"

// persistedRecordCitationPrefix opens every expansion's citation, so the
// rendered markdown says what the record is.
const persistedRecordCitationPrefix = "Persisted evidence record (not the source row): "

// CitedEvidenceLookup lists, newest first, one page (offset, limit) of the
// ids of stored results in the principal's organization that may cite
// evidenceRefID. It may over-report; ExpandCitedEvidence re-checks the
// decoded result's closure. It must never return a result of another
// organization. A page shorter than limit is the last one.
type CitedEvidenceLookup interface {
	ResultIDsCitingEvidence(ctx context.Context, principal storage.Principal, evidenceRefID string, offset, limit int) ([]string, error)
}

// ResultScopedCitedEvidenceLookup narrows a citing-result search to one
// stored result (CHAOS-6563). A Context Fabric ref is keyed by its subject, so
// many stored results cite the same ref; a caller that holds the result_id an
// answer returned names that result, and ExpandCitedEvidence then reads, closure
// checks and authorizes that result alone -- never a newer or older result that
// happens to cite the same ref. A result that does not cite the ref, is unknown
// or is not readable is the same not-found as an uncited ref.
type ResultScopedCitedEvidenceLookup struct{ ResultID string }

// ResultIDsCitingEvidence returns the scoped result id as the only candidate.
func (l ResultScopedCitedEvidenceLookup) ResultIDsCitingEvidence(_ context.Context, _ storage.Principal, _ string, offset, limit int) ([]string, error) {
	id := strings.TrimSpace(l.ResultID)
	if id == "" || offset > 0 || limit <= 0 {
		return nil, nil
	}
	return []string{id}, nil
}

// StoredResultAuthorizer decides whether a stored result may be served to a
// principal. *StoredResultGate implements it.
type StoredResultAuthorizer interface {
	Authorize(context.Context, storage.Principal, StoredInvestigationResult, StoredResultSurface) StoredResultAuthorization
}

// EvidenceExpansionReason is the closed outcome of one expansion.
type EvidenceExpansionReason string

const (
	// EvidenceExpansionServed: an admitted stored result cites the ref.
	EvidenceExpansionServed EvidenceExpansionReason = "served"
	// EvidenceExpansionMalformedRef: the id is not acr:v1:<type>:<id>.
	EvidenceExpansionMalformedRef EvidenceExpansionReason = "malformed_ref"
	// EvidenceExpansionLookupUnavailable: this deployment composed no
	// investigation result store that can be searched by evidence ref.
	EvidenceExpansionLookupUnavailable EvidenceExpansionReason = "lookup_unavailable"
	// EvidenceExpansionLookupFailed: the citing-result search failed.
	EvidenceExpansionLookupFailed EvidenceExpansionReason = "lookup_failed"
	// EvidenceExpansionNotCited: no stored result of the organization cites
	// the ref.
	EvidenceExpansionNotCited EvidenceExpansionReason = "not_cited"
	// EvidenceExpansionResultUnreadable: a candidate could not be read and
	// no readable candidate cites the ref.
	EvidenceExpansionResultUnreadable EvidenceExpansionReason = "result_unreadable"
	// EvidenceExpansionAuthorizationDenied: every citing result was denied
	// to this caller.
	EvidenceExpansionAuthorizationDenied EvidenceExpansionReason = "authorization_denied"
	// EvidenceExpansionAuthorizationUnavailable: no citing result was
	// admitted and at least one decision could not be taken.
	EvidenceExpansionAuthorizationUnavailable EvidenceExpansionReason = "authorization_unavailable"
	// EvidenceExpansionInvalid: the built expansion failed the contract.
	EvidenceExpansionInvalid EvidenceExpansionReason = "expansion_invalid"
	// EvidenceExpansionSourceRowServed: the source row the ref names was
	// served (CHAOS-6180); no stored result was read.
	EvidenceExpansionSourceRowServed EvidenceExpansionReason = "source_row_served"
	// EvidenceExpansionSourceRowUnavailable: a source-row read failed, so
	// the persisted record was not tried either.
	EvidenceExpansionSourceRowUnavailable EvidenceExpansionReason = "source_row_unavailable"
)

// EvidenceExpansionReasonVocabulary is the closed set of reasons.
func EvidenceExpansionReasonVocabulary() [11]EvidenceExpansionReason {
	return [11]EvidenceExpansionReason{
		EvidenceExpansionServed, EvidenceExpansionMalformedRef, EvidenceExpansionLookupUnavailable,
		EvidenceExpansionLookupFailed, EvidenceExpansionNotCited, EvidenceExpansionResultUnreadable,
		EvidenceExpansionAuthorizationDenied, EvidenceExpansionAuthorizationUnavailable, EvidenceExpansionInvalid,
		EvidenceExpansionSourceRowServed, EvidenceExpansionSourceRowUnavailable,
	}
}

// EvidenceExpansionUnregisteredType is the entity_type log value for a ref
// whose type segment is outside the closed evidence-entity vocabulary.
const EvidenceExpansionUnregisteredType = "unregistered"

// EvidenceExpansionDecision records what decided one expansion and why.
type EvidenceExpansionDecision struct {
	Reason EvidenceExpansionReason
	// EntityType is the ref's registered entity type, or
	// EvidenceExpansionUnregisteredType.
	EntityType string
	// CandidateCount is how many result ids the lookup returned.
	CandidateCount int
	// CitingCount is how many readable candidates cite the ref.
	CitingCount      int
	UnreadableCount  int
	AdmittedCount    int
	DeniedCount      int
	UnavailableCount int
	// WithheldCount is how many admitted results the result-by-id serving
	// rules withhold from this caller.
	WithheldCount int
	// Authorization is the decisive stored-result decision: the admitted
	// one, else the first unavailable one, else the first denied one. Nil
	// when no citing result reached the gate.
	Authorization *StoredResultAuthorization
	// Source is the source-row resolution that ran first (CHAOS-6180).
	Source SourceRowDecision
	Err    error
}

// Found reports whether the caller receives the expansion.
func (d EvidenceExpansionDecision) Found() bool {
	return d.Reason == EvidenceExpansionServed || d.Reason == EvidenceExpansionSourceRowServed
}

// ServingError maps the decision onto the evidence route's error classes:
// nil when served; storage.ErrNotFound for every outcome a caller must not
// be able to tell apart from an unknown ref; ErrUnavailable otherwise.
func (d EvidenceExpansionDecision) ServingError() error {
	switch d.Reason {
	case EvidenceExpansionServed, EvidenceExpansionSourceRowServed:
		return nil
	case EvidenceExpansionMalformedRef, EvidenceExpansionLookupUnavailable, EvidenceExpansionNotCited, EvidenceExpansionAuthorizationDenied:
		return storage.ErrNotFound
	default:
		if d.Err != nil {
			return fmt.Errorf("%w: evidence expansion %s: %w", ErrUnavailable, d.Reason, d.Err)
		}
		return fmt.Errorf("%w: evidence expansion %s", ErrUnavailable, d.Reason)
	}
}

// IsContextFabricEvidenceRef reports whether ref is in the Context Fabric
// evidence-ref namespace, whatever its entity type.
func IsContextFabricEvidenceRef(ref string) bool {
	return strings.HasPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix)
}

// parseContextFabricEvidenceRef splits acr:v1:<type>:<id...>.
func parseContextFabricEvidenceRef(ref string) (entityType, id string, ok bool) {
	rest, found := strings.CutPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix)
	if !found {
		return "", "", false
	}
	entityType, id, found = strings.Cut(rest, ":")
	if !found || entityType == "" || id == "" || strings.TrimSpace(ref) != ref {
		return "", "", false
	}
	return entityType, id, true
}

func registeredEvidenceEntityType(entityType string) string {
	for _, known := range contractsv1.ContextFabricEvidenceEntityTypeVocabulary() {
		if string(known) == entityType {
			return entityType
		}
	}
	return EvidenceExpansionUnregisteredType
}

// ExpandCitedEvidence expands one Context Fabric evidence ref for principal.
// lookup, results or gate may be nil (a deployment without Context Fabric);
// the decision then says so.
func ExpandCitedEvidence(ctx context.Context, principal storage.Principal, ref string, lookup CitedEvidenceLookup, results InvestigationResultStore, gate StoredResultAuthorizer, now time.Time) (contractsv1.ExpandedEvidence, EvidenceExpansionDecision) {
	decision := EvidenceExpansionDecision{EntityType: EvidenceExpansionUnregisteredType}
	entityType, entityID, ok := parseContextFabricEvidenceRef(ref)
	if !ok {
		decision.Reason = EvidenceExpansionMalformedRef
		return contractsv1.ExpandedEvidence{}, decision
	}
	decision.EntityType = registeredEvidenceEntityType(entityType)
	if storage.IsNil(lookup) || storage.IsNil(results) || storage.IsNil(gate) {
		decision.Reason = EvidenceExpansionLookupUnavailable
		return contractsv1.ExpandedEvidence{}, decision
	}
	var firstUnavailable, firstDenied *StoredResultAuthorization
	var readErr error
	for offset := 0; ; offset += CitedEvidenceCandidateLimit {
		if err := ctx.Err(); err != nil {
			decision.Reason, decision.Err = EvidenceExpansionLookupFailed, err
			return contractsv1.ExpandedEvidence{}, decision
		}
		ids, err := lookup.ResultIDsCitingEvidence(ctx, principal, ref, offset, CitedEvidenceCandidateLimit)
		if err != nil {
			decision.Reason, decision.Err = EvidenceExpansionLookupFailed, err
			return contractsv1.ExpandedEvidence{}, decision
		}
		for _, id := range ids {
			decision.CandidateCount++
			stored, err := results.Get(ctx, principal, id)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					decision.Reason, decision.Err = EvidenceExpansionResultUnreadable, ctxErr
					return contractsv1.ExpandedEvidence{}, decision
				}
				if !errors.Is(err, ErrInvestigationResultNotFound) {
					decision.UnreadableCount++
					if readErr == nil {
						readErr = err
					}
				}
				continue
			}
			if _, cited := contractsv1.ContextFabricEvidenceRefClosure(stored.Result)[ref]; !cited {
				continue
			}
			decision.CitingCount++
			authorization := gate.Authorize(ctx, principal, stored, StoredResultSurfaceEvidenceExpansion)
			switch authorization.Decision {
			case StoredResultAdmitted:
			case StoredResultDenied:
				decision.DeniedCount++
				if firstDenied == nil {
					firstDenied = &authorization
				}
				continue
			default:
				decision.UnavailableCount++
				if firstUnavailable == nil {
					firstUnavailable = &authorization
				}
				continue
			}
			decision.AdmittedCount++
			served, withheld, serveErr := servedStoredResult(stored, principal)
			if serveErr != nil {
				decision.UnreadableCount++
				if readErr == nil {
					readErr = serveErr
				}
				continue
			}
			// No serving rule drops an evidence ref, a label or a citing
			// site (they touch coverage, limitations, outcome rows, status
			// and claim subjects), so the served copy cites ref whenever the
			// stored one does.
			if withheld {
				decision.WithheldCount++
				if firstDenied == nil {
					firstDenied = &authorization
				}
				continue
			}
			decision.Authorization = &authorization
			expanded := citedEvidenceExpansion(served, ref, entityType, entityID, now)
			if err := expanded.Validate(); err != nil {
				decision.Reason, decision.Err = EvidenceExpansionInvalid, err
				return contractsv1.ExpandedEvidence{}, decision
			}
			decision.Reason = EvidenceExpansionServed
			return expanded, decision
		}
		if len(ids) < CitedEvidenceCandidateLimit {
			break
		}
	}
	// A candidate that could not be decided or read may be the one this
	// caller can read, so it outranks a denial: the caller retries a 503,
	// never trusts a not-found that might be wrong.
	switch {
	case firstUnavailable != nil:
		decision.Reason, decision.Authorization, decision.Err = EvidenceExpansionAuthorizationUnavailable, firstUnavailable, firstUnavailable.Err
	case decision.UnreadableCount > 0:
		decision.Reason, decision.Err = EvidenceExpansionResultUnreadable, readErr
	case firstDenied != nil:
		decision.Reason, decision.Authorization = EvidenceExpansionAuthorizationDenied, firstDenied
	default:
		decision.Reason = EvidenceExpansionNotCited
	}
	return contractsv1.ExpandedEvidence{}, decision
}

// servedStoredResult applies the result-by-id route's own serving decisions
// that can withhold a stored result or change which evidence it carries: the
// work-item tuple census rule (a changed authorization digest is not found),
// retained-ranking accounting, and the served-requirement assertion (a row
// that route refuses to serve). The clarification and cardinality-subject
// repairs it also applies change status and claim subjects only, never an
// evidence ref, a label or a citing site. withheld reports a result that
// route would answer as not found; an error is a result it could not serve.
func servedStoredResult(stored StoredInvestigationResult, principal storage.Principal) (InvestigationResult, bool, error) {
	result := stored.Result
	tuple := ServeStoredWorkItemTuple(result, stored.SemanticState, stored.SemanticStateRead, principal)
	if tuple.Err != nil {
		return InvestigationResult{}, false, tuple.Err
	}
	switch tuple.Disposition {
	case WorkItemTupleByIDNotFound:
		return InvestigationResult{}, true, nil
	case WorkItemTupleByIDServed, WorkItemTupleByIDStored:
		result = tuple.Result
	}
	result, _ = AccountForRetainedRanking(result)
	result.Completeness = ComputeAnswerCompleteness(result)
	if err := AssertServedRequirementEvidence(result); err != nil {
		return InvestigationResult{}, false, err
	}
	return result, false, nil
}

// citedEvidenceExpansion builds the expansion from the stored result's own
// persisted content: the label the engine stamped for the ref, the citing
// sites, and the titles/summaries of the drivers and findings citing it.
func citedEvidenceExpansion(result InvestigationResult, ref, entityType, entityID string, now time.Time) contractsv1.ExpandedEvidence {
	label := strings.TrimSpace(result.EvidenceRefLabels[ref])
	if label == "" {
		label, _ = contractsv1.ContextFabricEvidenceRefLabel(ref)
	}
	sites, excerpt := citingSites(result, ref)
	siteNames := make([]string, 0, len(sites))
	for name := range sites {
		siteNames = append(siteNames, name)
	}
	sort.Strings(siteNames)
	structuredSites := make(map[string]any, len(sites))
	for _, name := range siteNames {
		structuredSites[name] = sites[name]
	}
	citation := boundRunes(fmt.Sprintf("%s%s, cited by investigation result %s (%s).", persistedRecordCitationPrefix, label, result.ResultID, strings.Join(siteNames, ", ")), 2000)
	evidence := contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema,
		EvidenceRefID: ref,
		Source: contractsv1.EvidenceSource{
			System: ContextFabricEvidenceSystem, EntityType: entityType, EntityID: entityID, DisplayLabel: boundRunes(label, 1000),
		},
		Provenance:   ContextFabricEvidenceProvenance,
		Confidence:   1,
		Citation:     citation,
		ObservedAt:   result.GeneratedAt.UTC(),
		Availability: contractsv1.EvidenceAvailable,
		Metadata:     map[string]any{"record": "persisted_investigation_evidence", "cited_by": "investigation_result"},
	}
	return contractsv1.ExpandedEvidence{
		SchemaVersion: contractsv1.ExpandedEvidenceSchema,
		Evidence:      evidence,
		ResolvedAt:    now.UTC(),
		Availability:  contractsv1.EvidenceAvailable,
		Excerpt:       boundRunes(excerpt, 1000),
		Structured: map[string]any{
			"result_id":   result.ResultID,
			"entity_type": entityType,
			"cited_by":    structuredSites,
		},
	}
}

// citingSites counts where the result cites ref and collects the text of
// the citing drivers and findings, in result order.
func citingSites(result InvestigationResult, ref string) (map[string]int, string) {
	sites := map[string]int{}
	var lines []string
	cites := func(refs []string) bool {
		for _, candidate := range refs {
			if candidate == ref {
				return true
			}
		}
		return false
	}
	if cites(result.EvidenceRefIDs) {
		sites["result"]++
	}
	for _, driver := range result.Drivers {
		if cites(driver.EvidenceRefIDs) {
			sites["driver"]++
			lines = append(lines, "Driver: "+strings.TrimSpace(driver.Title))
		}
	}
	for _, findings := range [][]contractsv1.ContextFabricFinding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			if cites(finding.EvidenceRefIDs) {
				sites["finding"]++
				lines = append(lines, "Finding: "+strings.TrimSpace(finding.Summary))
			}
		}
	}
	for _, path := range result.Paths {
		if cites(path.EvidenceRefIDs) {
			sites["path"]++
		}
		for _, edge := range path.Edges {
			if cites(edge.EvidenceRefIDs) {
				sites["path_edge"]++
			}
		}
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			if cites(member.EvidenceRefIDs) {
				sites["cohort_member"]++
			}
		}
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		if cites(candidate.EvidenceRefIDs) {
			sites["subject_candidate"]++
		}
	}
	return sites, strings.Join(lines, "\n")
}

// boundRunes truncates s to at most limit runes.
func boundRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// EvidenceExpansionLogArgs renders a decision for the trace; the caller
// appends request_id.
func EvidenceExpansionLogArgs(principal storage.Principal, decision EvidenceExpansionDecision) []any {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"reason", string(decision.Reason),
		"entity_type", SanitizeLogAttr(decision.EntityType),
		"candidate_count", decision.CandidateCount,
		"citing_count", decision.CitingCount,
		"unreadable_count", decision.UnreadableCount,
		"admitted_count", decision.AdmittedCount,
		"denied_count", decision.DeniedCount,
		"unavailable_count", decision.UnavailableCount,
		"withheld_count", decision.WithheldCount,
	}
	if decision.Authorization != nil {
		args = append(args, "authorization_reason", string(decision.Authorization.Reason))
	}
	source := decision.Source
	if source.Reason == "" {
		source.Reason = SourceRowBackendAbsent
	}
	args = append(args, "source_reason", string(source.Reason))
	if source.Query != "" {
		args = append(args, "source_query", SanitizeLogAttr(source.Query))
	}
	if source.Read() {
		if source.Grammar != "" {
			args = append(args, "source_grammar", SanitizeLogAttr(source.Grammar))
		}
		args = append(args, "source_repositories", source.Repositories, "source_admitted", source.Admitted, "source_rows", source.Rows)
	}
	if decision.Err != nil {
		args = append(args, "error_class", SanitizeLogAttr(evidenceExpansionErrorClass(decision.Err)))
	}
	return args
}

func evidenceExpansionErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrUnavailable):
		return "dependency_unavailable"
	default:
		return "internal"
	}
}
