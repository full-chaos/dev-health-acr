package contextpacket

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type EvidenceScope string

const (
	EvidenceScopeCommit EvidenceScope = "commit"
	EvidenceScopeBranch EvidenceScope = "branch"
	EvidenceScopeRepo   EvidenceScope = "repo"
)

// SourceQuery is a versioned projection of an existing Dev Health table. Its
// statement returns the shared evidence row shape consumed by the executor.
type SourceQuery struct {
	ID        string
	Source    string
	Scope     EvidenceScope
	Statement string
}

type SourceQueryExecutor interface {
	QueryEvidence(context.Context, SourceQuery, []ClickHouseBinding) ([]contractsv1.EvidenceRef, error)
}

// SourceFreshnessExecutor is optionally implemented by an executor that can
// report a source's newest observed_at independent of the evidence confidence
// floor (CHAOS-6565).
type SourceFreshnessExecutor interface {
	QueryLatestObserved(context.Context, SourceQuery, []ClickHouseBinding) (time.Time, bool, error)
}

type CatalogResult struct {
	Evidence    []contractsv1.EvidenceRef
	Watermarks  []contractsv1.SourceWatermark
	Unavailable []contractsv1.UnavailableSource
	// Warnings are non-fatal disclosures the catalog read produced, e.g.
	// freshness_from_evidence:<source> when the source-latest read failed and
	// the watermark fell back to the (confidence-filtered) evidence max.
	Warnings []string
}

const repositoryWideSourceLabelSuffix = " (repository-wide)"

// reasonNoEvidenceTaskRefFiltered marks a catalog source whose statement
// applies an exact task_ref match (see taskRefFilteredSources) and returned
// zero rows for a request that supplied a task_ref. CHAOS-4016: a caller
// with a stale/wrong task_ref must see this attributed to the filter it
// applied, not the generic "no_evidence" a source that ignores task_ref
// entirely gets for the same empty result.
const reasonNoEvidenceTaskRefFiltered = "no_evidence_task_ref_filtered"

// taskRefFilteredSources are the catalog source IDs whose statement in
// source_queries.go narrows rows by an exact task_ref match. Keep this in
// sync with those statements.
var taskRefFilteredSources = map[string]bool{
	"work_items.v1":             true,
	"work_item_dependencies.v1": true,
	"work_graph.v1":             true,
}

func catalogSourceQuery(id string) *SourceQuery {
	for index := range SourceQueryCatalogV1 {
		if SourceQueryCatalogV1[index].ID == id {
			return &SourceQueryCatalogV1[index]
		}
	}
	return nil
}

func ExecuteCatalog(ctx context.Context, executor SourceQueryExecutor, plan ReadPlan) (CatalogResult, error) {
	return ExecuteCatalogObserved(ctx, executor, plan, nil)
}

func ExecuteCatalogObserved(ctx context.Context, executor SourceQueryExecutor, plan ReadPlan, observer AssemblyObserver) (CatalogResult, error) {
	if executor == nil {
		return CatalogResult{}, fmt.Errorf("contextpacket: source query executor is required")
	}
	result := CatalogResult{Evidence: []contractsv1.EvidenceRef{}, Unavailable: []contractsv1.UnavailableSource{}}
	latestObserved := map[string]time.Time{}
	freshness, _ := executor.(SourceFreshnessExecutor)
	for _, query := range SourceQueryCatalogV1 {
		if err := ctx.Err(); err != nil {
			return CatalogResult{}, err
		}
		if reason := catalogScopeUnavailableReason(query, plan); reason != "" {
			result.Unavailable = append(result.Unavailable, contractsv1.UnavailableSource{Source: query.ID, Reason: reason})
			continue
		}
		started := time.Now()
		rows, err := executor.QueryEvidence(ctx, query, plan.Bindings())
		if observer != nil {
			observer.ObserveStoreQuery(ctx, StoreQueryObservation{
				Operation: StoreOperationEvidence, Backend: StoreBackendClickHouse,
				Outcome: operationOutcome(err), Duration: time.Since(started), SourceID: query.ID, SourcePhase: sourceQueryFailurePhase(err),
			})
		}
		if err != nil {
			if ctx.Err() != nil {
				return CatalogResult{}, ctx.Err()
			}
			result.Unavailable = append(result.Unavailable, contractsv1.UnavailableSource{Source: query.ID, Reason: "source_unavailable"})
			continue
		}
		if freshness != nil {
			// A failed freshness read falls back to the evidence max; it must
			// not turn a served source into an unavailable one.
			if latest, found, ferr := freshness.QueryLatestObserved(ctx, query, plan.Bindings()); ferr == nil && found {
				latestObserved[query.ID] = latest
			} else if ctx.Err() != nil {
				return CatalogResult{}, ctx.Err()
			} else if ferr != nil {
				// LOUD fallback: the watermark below is derived from the
				// confidence-filtered evidence rows, which can under-report
				// freshness (CHAOS-6565). Log it and disclose it in the packet.
				attrs := []any{"source", query.ID, "error", ferr.Error()}
				if plan.RequestID != "" {
					attrs = append(attrs, "request_id", plan.RequestID)
				}
				slog.WarnContext(ctx, "context packet source freshness read failed; watermark derived from evidence rows", attrs...)
				result.Warnings = append(result.Warnings, "freshness_from_evidence:"+query.ID)
			}
		}
		for index := range rows {
			rows[index].SourceVersion = query.ID
			readsRepositoryWide := query.Scope == EvidenceScopeRepo ||
				(query.Scope == EvidenceScopeCommit && plan.CommitSHA == "" && querySupportsRepoWideRead(query.ID))
			if plan.Branch != "" && readsRepositoryWide {
				rows[index].Source.DisplayLabel += repositoryWideSourceLabelSuffix
				rows[index].Metadata = withRepositoryWideScope(rows[index].Metadata)
			}
		}
		result.Evidence = append(result.Evidence, rows...)
	}
	result.Unavailable = appendMissingCatalogSources(result.Unavailable, result.Evidence, plan.TaskRef)
	result.Watermarks = catalogWatermarks(plan, result.Evidence, result.Unavailable, latestObserved)
	return result, nil
}

func catalogScopeUnavailableReason(query SourceQuery, plan ReadPlan) string {
	if plan.CommitSHA != "" || query.Scope != EvidenceScopeCommit {
		return ""
	}
	if querySupportsRepoWideRead(query.ID) {
		return ""
	}
	return "commit_scope_not_requested"
}

func querySupportsRepoWideRead(id string) bool {
	// Both statements use `({commit_sha:String} = '' OR ...)`, so an empty
	// commit_sha deliberately selects repository-wide evidence.
	return id == "git_commits.v1" || id == "git_commit_files.v1"
}

func withRepositoryWideScope(metadata map[string]any) map[string]any {
	result := make(map[string]any, len(metadata)+1)
	maps.Copy(result, metadata)
	result["scope_breadth"] = "repository-wide"
	return result
}

func appendMissingCatalogSources(unavailable []contractsv1.UnavailableSource, evidence []contractsv1.EvidenceRef, taskRef string) []contractsv1.UnavailableSource {
	available := map[string]bool{}
	for _, ref := range evidence {
		available[ref.SourceVersion] = true
	}
	failed := map[string]bool{}
	for _, source := range unavailable {
		if isCatalogUnavailableReason(source.Reason) {
			failed[source.Source] = true
		}
	}
	for _, query := range SourceQueryCatalogV1 {
		if !available[query.ID] && !failed[query.ID] {
			reason := "no_evidence"
			if taskRef != "" && taskRefFilteredSources[query.ID] {
				reason = reasonNoEvidenceTaskRefFiltered
			}
			unavailable = append(unavailable, contractsv1.UnavailableSource{Source: query.ID, Reason: reason})
		}
	}
	return unavailable
}

func catalogWatermarks(plan ReadPlan, evidence []contractsv1.EvidenceRef, unavailable []contractsv1.UnavailableSource, sourceLatest map[string]time.Time) []contractsv1.SourceWatermark {
	latest := map[string]time.Time{}
	for _, ref := range evidence {
		if observed, found := latest[ref.SourceVersion]; !found || ref.ObservedAt.After(observed) {
			latest[ref.SourceVersion] = ref.ObservedAt
		}
	}
	// CHAOS-6565: the source's own newest row wins over the confidence-
	// filtered evidence subset (it can only be later or equal).
	for id, observed := range sourceLatest {
		if current, found := latest[id]; !found || observed.After(current) {
			latest[id] = observed
		}
	}
	failed := map[string]bool{}
	for _, source := range unavailable {
		if isCatalogUnavailableReason(source.Reason) {
			failed[source.Source] = true
		}
	}
	asOf := time.Now().UTC()
	if plan.AsOf != nil {
		asOf = plan.AsOf.UTC()
	}
	watermarks := make([]contractsv1.SourceWatermark, 0, len(SourceQueryCatalogV1))
	for _, query := range SourceQueryCatalogV1 {
		observed, found := latest[query.ID]
		status := "missing"
		if failed[query.ID] {
			status = "unavailable"
		} else if found {
			status = "fresh"
			if observed.Before(asOf.Add(-24 * time.Hour)) {
				status = "stale"
			}
		}
		watermarks = append(watermarks, contractsv1.SourceWatermark{Source: query.ID, LastIngestedAt: watermarkTime(found, observed), Status: status})
	}
	sort.Slice(watermarks, func(i, j int) bool { return watermarks[i].Source < watermarks[j].Source })
	return watermarks
}

func isCatalogUnavailableReason(reason string) bool {
	return reason == "source_unavailable" || reason == "scope_unresolved" || reason == "repo_fallback_branch_not_supported" || reason == "commit_scope_not_requested"
}

func watermarkTime(found bool, value time.Time) *time.Time {
	if !found {
		return nil
	}
	return &value
}
