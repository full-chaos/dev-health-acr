package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
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
func readFailure(action string, err error) error {
	return &contextfabric.FactReadFailure{
		State:  contextfabric.SourceUnavailable,
		Reason: fmt.Sprintf("devhealthfacts: %s: %v", action, err),
	}
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
