package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// queryVersion is this package's query_version-equivalent: every
// FactProviderResult and FactCapability this package produces carries it,
// so a consumer can tell exactly which SQL/column shape produced a fact.
const queryVersion = "devhealthfacts.clickhouse.v1"

// defaultTimeout is the FactCapability.Timeout this package advertises for
// every provider. The registry (fact_registry.go's readProvider) wraps each
// ReadFacts call in a context with this deadline; providers here never add
// a second timeout of their own -- they only ever propagate the ctx they
// are given into ClickHouseQueryClient.Query.
const defaultTimeout = 5 * time.Second

// maxFactRowsPerQuery bounds how many rows a single provider query is
// allowed to read from ClickHouse (H6/H7 adversarial finding: "unbounded
// fanout" -- a single subject with a pathological number of matching rows,
// e.g. thousands of work_item_dependencies rows, otherwise returns them all
// before any truncation happens). 200 gives generous headroom for any one
// subject's rows (the providers in this package never return more than a
// handful of rows per subject in the ordinary case) while still sitting
// well under CanonicalFactBundle's bundle-level bounds, so one pathological
// subject can't blow the whole investigation's fact budget.
const maxFactRowsPerQuery = 200

// withRowLimit appends a LIMIT clause bounding a provider's SELECT to
// maxFactRowsPerQuery. maxFactRowsPerQuery is an internal Go constant, never
// a caller-supplied value, so -- mirroring this package's existing
// convention for such constants (see dependencies.go's
// blockerRelationshipType, inlined the same way) -- it is safe to inline
// directly into the statement rather than route it through
// clickhouseFacts.query's bindings, which only ever carry caller/subject
// scoped values (org_id, ids).
func withRowLimit(statement string) string {
	return statement + "\nLIMIT " + strconv.Itoa(maxFactRowsPerQuery)
}

// clickhouseFacts is the shared ClickHouse query boundary every provider in
// this package embeds. It reuses internal/contextpacket.ClickHouseQueryClient
// -- the same query boundary internal/contextfabric/devhealthsource uses --
// rather than opening a second database path.
type clickhouseFacts struct {
	client contextpacket.ClickHouseQueryClient
}

// query runs statement scoped to orgID and the given raw ids, invoking scan
// once per returned row. It never adds its own timeout; ctx is propagated
// straight through to the query client.
func (f clickhouseFacts) query(ctx context.Context, statement, orgID string, ids []string, scan func(contextpacket.ClickHouseRowScanner) error) error {
	if f.client == nil {
		return errors.New("devhealthfacts: clickhouse query client is required")
	}
	rows, err := f.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "ids", Value: ids},
	})
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// readFailure wraps a query/scan error into the *contextfabric.FactReadFailure
// shape the registry classifies (fact_registry.go's classifyFactReadError),
// rather than a bare error.
//
// Reason is a fixed, non-parameterized string built only from action (an
// internal Go string literal every caller controls -- never caller/request
// supplied) -- it deliberately never embeds err.Error() or "%v" of err (M6
// adversarial finding: fact_registry.go's classifyFactReadError copies this
// Reason straight into the PUBLIC context_fabric_investigation_result.v1
// response's coverage.sources[].reason, so a raw ClickHouse driver error --
// which can carry connection details, internal hostnames, or query
// fragments -- must never reach it). This mirrors
// internal/contextfabric/falkorgraph/client.go's safeDependencyError, which
// classifies to a fixed reason and never embeds the raw SDK error either.
// contextfabric.FactReadFailure carries no field for the original err, and
// this package has no server-side logging seam to hand it to (inventing one
// here is out of scope for this fix), so err is accepted for call sites'
// context but intentionally never reaches the returned error at all.
func readFailure(action string, err error) error {
	return &contextfabric.FactReadFailure{
		State:  contextfabric.SourceUnavailable,
		Reason: "devhealthfacts: " + action + " failed",
	}
}

// timeUnsupportedReason is the fixed, non-parameterized Reason every
// provider in this package returns when checkCurrentTimeOnly refuses a
// query (H6 adversarial finding: "false historical answers"). It never
// interpolates the requested query -- only ever this literal string.
const timeUnsupportedReason = "devhealthfacts: only current-time (axis=current) queries are supported; requested axis was rejected to avoid presenting current data as if it answered a historical/point-in-time question"

// checkCurrentTimeOnly reports whether query.Time.Axis requests anything
// other than contextfabric.TemporalCurrent. No provider in this package has
// a historical/point-in-time query path -- every ReadFacts here always
// queries and returns CURRENT ClickHouse state -- so honoring any other
// axis would silently answer a historical question (e.g. "what was the
// status last month") with today's data presented as if it were the
// answer. RULING: refuse instead of guessing. When this returns
// (result, true), the caller must return result, nil as ReadFacts' entire
// result, without ever calling clickhouseFacts.query.
//
// The zero value of TimeContext.Axis is treated as unsupported too, not as
// an implicit "current": fact_registry.go's buildFactQuery always copies
// request.Question.TimeContext straight from a validated
// CanonicalFactRequest, and contractsv1.ContextFabricTimeContext.Validate
// rejects an empty Axis outright (it must be one of the four defined enum
// values) -- so a genuinely empty Axis reaching a provider here is itself
// evidence of an unvalidated caller, never evidence the caller wants "now".
func checkCurrentTimeOnly(query contextfabric.FactQuery) (contextfabric.FactProviderResult, bool) {
	if query.Time.Axis == contextfabric.TemporalCurrent {
		return contextfabric.FactProviderResult{}, false
	}
	return contextfabric.FactProviderResult{
		State:  contextfabric.SourceUnconfigured,
		Reason: timeUnsupportedReason,
	}, true
}

// requireOrgID validates principal.OrgID the same way every provider in this
// package must: org-scoping comes from storage.Principal, never from the
// query or any other caller-supplied value.
func requireOrgID(orgID string) (string, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return "", &contextfabric.FactReadFailure{State: contextfabric.SourceUnavailable, Reason: "devhealthfacts: authenticated organization is required"}
	}
	return orgID, nil
}

// subjectsOfKind filters subjects down to the ones matching kind, preserving
// order.
func subjectsOfKind(subjects []contextfabric.SubjectRef, kind contextfabric.SubjectKind) []contextfabric.SubjectRef {
	filtered := make([]contextfabric.SubjectRef, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Kind == kind {
			filtered = append(filtered, subject)
		}
	}
	return filtered
}

// subjectIndex strips prefix from each subject's CanonicalID to recover the
// raw ClickHouse row key devhealthsource itself would have produced (e.g.
// "work_item:WIDGET-101" -> "WIDGET-101"), and returns both the list of raw
// ids (for the query's IN clause -- so the query only ever asks ClickHouse
// about subjects the caller actually requested, never the whole org) and a
// lookup back to the exact SubjectRef the caller supplied. Reusing that
// SubjectRef verbatim (rather than reconstructing one from the scanned row)
// guarantees the fact's Subject is byte-for-byte the same value the
// registry already validated into its allowed set, Label included.
//
// A subject whose CanonicalID doesn't carry prefix is skipped, not errored:
// callers only ever receive subjects contextfabric.buildFactQuery already
// filtered to this provider's SupportedSubjectKinds, so this only guards
// against a malformed id, and skipping it just means that one subject gets
// no fact entry -- the same "partial coverage is fine" contract a zero-row
// query result gets.
func subjectIndex(subjects []contextfabric.SubjectRef, prefix string) (ids []string, bySubject map[string]contextfabric.SubjectRef) {
	bySubject = make(map[string]contextfabric.SubjectRef, len(subjects))
	for _, subject := range subjects {
		raw := strings.TrimPrefix(subject.CanonicalID, prefix)
		if raw == "" || raw == subject.CanonicalID {
			continue
		}
		bySubject[raw] = subject
		ids = append(ids, raw)
	}
	return ids, bySubject
}

// pullRequestKey builds the same "repoID:number" composite row key
// devhealthsource/tables.go's queryPullRequests uses as its rowSortKey, so a
// git_pull_requests row (which has no single-column primary key) can be
// matched back to the subject that asked for it.
func pullRequestKey(repoID string, number int64) string {
	return fmt.Sprintf("%s:%d", repoID, number)
}

// evidenceRefID mirrors devhealthsource/clickhouse.go's inline
// "acr:v1:<entity-type>:<id>" evidence ref convention (e.g.
// queryWorkItems' `EvidenceRefIDs: []string{"acr:v1:work-item:" + workItemID}`)
// so evidence refs minted here resolve through the same source_evidence
// path as the ones devhealthsource already produces.
func evidenceRefID(entityType, id string) string {
	return "acr:v1:" + entityType + ":" + id
}

func newCapability(kind contextfabric.FactKind, name string, subjectKinds []contextfabric.SubjectKind) contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind:                  kind,
		Name:                  name,
		Version:               queryVersion,
		SupportedSubjectKinds: subjectKinds,
		RequiresEvidence:      true,
		Timeout:               defaultTimeout,
	}
}

func stringOrNull(value string) contextfabric.FactValue {
	if value == "" {
		return contextfabric.NullFactValue()
	}
	return contextfabric.StringFactValue(value)
}
