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

// ContextFabricEvidenceRefPrefix starts every Context Fabric evidence ref.
const ContextFabricEvidenceRefPrefix = "acr:v1:"

// CitedEvidenceCandidateLimit bounds how many citing results one expansion
// reads and authorizes.
const CitedEvidenceCandidateLimit = 16

// EvidenceExpansionLogMessage is the one Info line an expansion emits.
const EvidenceExpansionLogMessage = "context fabric evidence expansion"

// ContextFabricEvidenceSystem is the source system an expansion names.
const ContextFabricEvidenceSystem = "dev-health"

// CitedEvidenceLookup lists, newest first, the ids of stored results in the
// principal's organization that may cite evidenceRefID. It may over-report;
// ExpandCitedEvidence re-checks the decoded result's closure. It must never
// return a result of another organization.
type CitedEvidenceLookup interface {
	ResultIDsCitingEvidence(ctx context.Context, principal storage.Principal, evidenceRefID string, limit int) ([]string, error)
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
)

// EvidenceExpansionReasonVocabulary is the closed set of reasons.
func EvidenceExpansionReasonVocabulary() [9]EvidenceExpansionReason {
	return [9]EvidenceExpansionReason{
		EvidenceExpansionServed, EvidenceExpansionMalformedRef, EvidenceExpansionLookupUnavailable,
		EvidenceExpansionLookupFailed, EvidenceExpansionNotCited, EvidenceExpansionResultUnreadable,
		EvidenceExpansionAuthorizationDenied, EvidenceExpansionAuthorizationUnavailable, EvidenceExpansionInvalid,
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
	// Authorization is the decisive stored-result decision: the admitted
	// one, else the first unavailable one, else the first denied one. Nil
	// when no citing result reached the gate.
	Authorization *StoredResultAuthorization
	Err           error
}

// Found reports whether the caller receives the expansion.
func (d EvidenceExpansionDecision) Found() bool { return d.Reason == EvidenceExpansionServed }

// ServingError maps the decision onto the evidence route's error classes:
// nil when served; storage.ErrNotFound for every outcome a caller must not
// be able to tell apart from an unknown ref; ErrUnavailable otherwise.
func (d EvidenceExpansionDecision) ServingError() error {
	switch d.Reason {
	case EvidenceExpansionServed:
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
	return strings.HasPrefix(ref, ContextFabricEvidenceRefPrefix)
}

// parseContextFabricEvidenceRef splits acr:v1:<type>:<id...>.
func parseContextFabricEvidenceRef(ref string) (entityType, id string, ok bool) {
	rest, found := strings.CutPrefix(ref, ContextFabricEvidenceRefPrefix)
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
	ids, err := lookup.ResultIDsCitingEvidence(ctx, principal, ref, CitedEvidenceCandidateLimit)
	if err != nil {
		decision.Reason, decision.Err = EvidenceExpansionLookupFailed, err
		return contractsv1.ExpandedEvidence{}, decision
	}
	if len(ids) > CitedEvidenceCandidateLimit {
		ids = ids[:CitedEvidenceCandidateLimit]
	}
	decision.CandidateCount = len(ids)
	var firstUnavailable, firstDenied *StoredResultAuthorization
	var readErr error
	for _, id := range ids {
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
			decision.AdmittedCount++
			decision.Authorization = &authorization
			expanded := citedEvidenceExpansion(stored.Result, ref, entityType, entityID, now)
			if err := expanded.Validate(); err != nil {
				decision.Reason, decision.Err = EvidenceExpansionInvalid, err
				return contractsv1.ExpandedEvidence{}, decision
			}
			decision.Reason = EvidenceExpansionServed
			return expanded, decision
		case StoredResultDenied:
			decision.DeniedCount++
			if firstDenied == nil {
				firstDenied = &authorization
			}
		default:
			decision.UnavailableCount++
			if firstUnavailable == nil {
				firstUnavailable = &authorization
			}
		}
	}
	switch {
	case firstUnavailable != nil:
		decision.Reason, decision.Authorization, decision.Err = EvidenceExpansionAuthorizationUnavailable, firstUnavailable, firstUnavailable.Err
	case firstDenied != nil:
		decision.Reason, decision.Authorization = EvidenceExpansionAuthorizationDenied, firstDenied
	case decision.UnreadableCount > 0:
		decision.Reason, decision.Err = EvidenceExpansionResultUnreadable, readErr
	default:
		decision.Reason = EvidenceExpansionNotCited
	}
	return contractsv1.ExpandedEvidence{}, decision
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
	citation := boundRunes(fmt.Sprintf("%s, cited by investigation result %s (%s).", label, result.ResultID, strings.Join(siteNames, ", ")), 2000)
	evidence := contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema,
		EvidenceRefID: ref,
		Source: contractsv1.EvidenceSource{
			System: ContextFabricEvidenceSystem, EntityType: entityType, EntityID: entityID, DisplayLabel: boundRunes(label, 1000),
		},
		Provenance:   "derived",
		Confidence:   1,
		Citation:     citation,
		ObservedAt:   result.GeneratedAt.UTC(),
		Availability: contractsv1.EvidenceAvailable,
		Metadata:     map[string]any{"cited_by": "investigation_result"},
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
	}
	if decision.Authorization != nil {
		args = append(args, "authorization_reason", string(decision.Authorization.Reason))
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
