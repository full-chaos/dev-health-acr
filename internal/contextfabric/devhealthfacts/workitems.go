package devhealthfacts

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// StatusProvider implements contextfabric.FactProvider for FactStatus from
// work_items.status -- the same column devhealthsource/tables.go's
// queryWorkItems already reads.
type StatusProvider struct{ facts clickhouseFacts }

func newStatusProvider(client contextpacket.ClickHouseQueryClient) *StatusProvider {
	return &StatusProvider{facts: clickhouseFacts{client: client}}
}

func (p *StatusProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactStatus, "devhealthfacts.status", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *StatusProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.status", contextfabric.FactStatus, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-5438: ONE owner for the output bound and the truncation verdict --
	// see factBudget. This provider reads a single branch, so the two could
	// not drift here the way they did in the two multi-branch providers; it
	// uses the same helper anyway, because leaving a hand-rolled copy beside
	// the owner is how a second mechanism becomes a second defect.
	budget := newFactBudget()
	// CHAOS-4377: the SQL build + scan half moved to
	// github.com/full-chaos/dev-health-go/readers.ReadWorkItemStatus.
	// CHAOS-5438: PROBE one row past the output bound so a full page and a
	// truncated one are distinguishable -- see shared.go's maxFactRowsProbe.
	scope := workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope)
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item status", settingsErr)
	}
	rows, scanErr := readers.ReadWorkItemStatusWithScopeAndRowLimit(ctx, p.facts.client, orgID, ids, scope, settings, maxFactRowsProbe)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item status", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.RepoID+":"+row.ID]
		if !ok {
			continue
		}
		if !budget.admit() {
			continue
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactStatus, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"status": stringOrNull(row.Status)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
		})
	}
	budget.observe(len(rows))
	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
	return result, nil
}

// WorkProvider implements contextfabric.FactProvider for FactWork -- minimal
// work descriptors (title) from work_items.title, the same column
// devhealthsource/tables.go's queryWorkItems already reads.
type WorkProvider struct{ facts clickhouseFacts }

func newWorkProvider(client contextpacket.ClickHouseQueryClient) *WorkProvider {
	return &WorkProvider{facts: clickhouseFacts{client: client}}
}

func (p *WorkProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactWork, "devhealthfacts.work", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *WorkProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.work", contextfabric.FactWork, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-5438: ONE owner for the output bound and the truncation verdict --
	// see factBudget. This provider reads a single branch, so the two could
	// not drift here the way they did in the two multi-branch providers; it
	// uses the same helper anyway, because leaving a hand-rolled copy beside
	// the owner is how a second mechanism becomes a second defect.
	budget := newFactBudget()
	// CHAOS-4377: the SQL build + scan half moved to
	// github.com/full-chaos/dev-health-go/readers.ReadWorkItemTitle.
	// CHAOS-5438: PROBE one row past the output bound so a full page and a
	// truncated one are distinguishable -- see shared.go's maxFactRowsProbe.
	scope := workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope)
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item work descriptors", settingsErr)
	}
	rows, scanErr := readers.ReadWorkItemTitleWithScopeAndRowLimit(ctx, p.facts.client, orgID, ids, scope, settings, maxFactRowsProbe)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item work descriptors", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.RepoID+":"+row.ID]
		if !ok {
			continue
		}
		if !budget.admit() {
			continue
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWork, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"title": stringOrNull(row.Title)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
		})
	}
	budget.observe(len(rows))
	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
	return result, nil
}

// ActualCompletionProvider implements contextfabric.FactProvider for
// FactActualCompletion from work_items.completed_at.
//
// Deviation from devhealthsource: devhealthsource/tables.go's queryWorkItems
// never selects completed_at (it only needed status/title/url/updated_at for
// projection), but the column is real -- it is seeded by
// testdata/fullstack/v1/seed/clickhouse/001_widget_service.sql's
// `INSERT INTO work_items (... completed_at, closed_at ...)` -- and
// FactActualCompletion has no honest way to answer "did this actually
// complete, and when" without it; a heuristic guess at which work_items.status
// strings count as "done" would be exactly the kind of invented vocabulary
// the fact/evidence semantics this package must not invent. "completed" is
// defined as completed_at being non-null; completed_at is only present in
// Fields when non-null.
type ActualCompletionProvider struct{ facts clickhouseFacts }

func newActualCompletionProvider(client contextpacket.ClickHouseQueryClient) *ActualCompletionProvider {
	return &ActualCompletionProvider{facts: clickhouseFacts{client: client}}
}

func (p *ActualCompletionProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactActualCompletion, "devhealthfacts.actual_completion", []contextfabric.SubjectKind{
		contextfabric.SubjectWorkItem, contextfabric.SubjectProject,
	})
}

// ReadFacts branches by subject kind, mirroring readiness.go/health.go's
// multi-kind shape: the work-item branch reads one work item's own
// completed_at; the project branch (CHAOS-5893) computes the roll-up over
// the same completed_at definition. Both branches share ONE factBudget so
// Truncated means the same thing (at least one authorized candidate fact
// was not served) regardless of which branch produced it -- the
// shared-guard rule shared.go's factBudget doc comment states.
func (p *ActualCompletionProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	scope := workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope)
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item actual completion", settingsErr)
	}
	rejected := 0
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.actual_completion", contextfabric.FactActualCompletion, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	// ONE owner for the output bound and the truncation verdict -- see
	// factBudget. Shared across both branches below (see the ReadFacts doc
	// comment).
	budget := newFactBudget()
	totalRows := 0

	if workItemSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem); len(workItemSubjects) > 0 {
		ids, bySubject, workItemRejected := v2Index(workItemSubjects, identity.KindWorkItem)
		rejected += workItemRejected
		// The SQL build + scan half (the isNotNull/ifNull coalescing, the
		// Tier B "was it done at T" derivation) lives in
		// github.com/full-chaos/dev-health-go/readers.ReadWorkItemCompletion;
		// see its doc comment for that derivation. Probes one row past the
		// output bound so a full page and a truncated one are
		// distinguishable -- see shared.go's maxFactRowsProbe.
		rows, scanErr := readers.ReadWorkItemCompletionWithScopeAndRowLimit(ctx, p.facts.client, orgID, ids, timeBound.neutral(), scope, settings, maxFactRowsProbe)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item actual completion", scanErr)
		}
		for _, row := range rows {
			subject, ok := bySubject[row.RepoID+":"+row.ID]
			if !ok {
				continue
			}
			if !budget.admit() {
				continue
			}
			fields := map[string]contextfabric.FactValue{"completed": contextfabric.BooleanFactValue(row.IsCompleted != 0)}
			if row.IsCompleted != 0 {
				fields["completed_at"] = contextfabric.StringFactValue(row.CompletedAt.UTC().Format(time.RFC3339))
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactActualCompletion, Subject: subject, Fields: fields,
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
			})
		}
		budget.observe(len(rows))
		totalRows += len(rows)
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, servedCount, projectRejected, scanErr := p.readProjectActualCompletion(ctx, orgID, projectSubjects, &facts, timeBound, scope, settings, budget)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project actual completion", scanErr)
		}
		rejected += projectRejected
		budget.observe(rowCount)
		// totalRows drives retentionState (an AVAILABILITY signal), so it
		// takes servedCount, not rowCount -- see readProjectActualCompletion's
		// doc comment for why an all-cancelled project must not read as
		// State=available with zero facts.
		totalRows += servedCount
	}

	state, retentionReason := timeBound.retentionState(totalRows)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: budget.truncated()}
	return result, nil
}

// workItemCancelledStatus is work_items.status' one CANCELLED vocabulary
// member (the normalized column devhealthsource already relies on, not
// status_raw, a per-provider free-text label this package never infers a
// closed-vocabulary value from). Verified against the live
// vocabulary (dh_0906, read-only, counts only): status is a closed set
// {backlog, canceled, done, in_progress, todo, unknown} across every
// seeded provider (github, gitlab, jira, linear, synthetic); "canceled" is
// the one member matching chris's ruling's "cancelled".
//
// ARCHIVED work items need no exclusion logic at all: an archived item
// stops appearing in this source's own sync at the moment it is archived
// (a source-side behavior, verified by its absence -- not in status, not
// in status_raw, not in a work item label, and projects.state, a
// DIFFERENT project-grain column, has no archived member either). An
// archived item is therefore already outside work_item_count by
// construction, the same way a deleted or never-synced item is. The basis
// still names this rather than leaving a reader to assume "archived" was
// never considered -- archivedItemsAbsentFromSource below is the fixed
// value it carries.
//
// workItemUnknownStatus is NOT excluded -- it counts in the denominator,
// same as any other non-cancelled status. It is a distinct, disclosed
// count, never folded into cancelled_count and never silently dropped.
const (
	workItemCancelledStatus = "canceled"
	workItemUnknownStatus   = "unknown"

	// archivedItemsAbsentFromSource is the fixed, closed-vocabulary value
	// the basis' archived_items field always carries: archived work items
	// leave this source's own sync and are never rows in work_items at
	// all, so they are already excluded by construction, not by a filter
	// this producer applies.
	archivedItemsAbsentFromSource = "absent_from_source"
)

// readProjectActualCompletion is the project roll-up: a project's
// completion is the ROLL-UP of its own work items' completion (chris's
// ruling -- a computed, factual aggregate, never deployment completion),
// excluding cancelled work items from both the numerator and the
// denominator (chris: "Cancelled should not [count]"). It is one GROUP BY
// aggregate over the SAME completed_at definition ReadWorkItemCompletion
// uses, never a second notion of "done" -- the exclusion is a status
// filter layered on top, not a redefinition of completion itself. The
// join resolving which work items belong to a project reuses
// projectIdentityJoinSQL/projectIdentityMatchSQL (shared.go) against
// work_items.project_id, the same collision-safe id/key identity
// resolution readiness.go's project rollup already trusts (live-schema
// verified: work_items.project_id already carries whichever identity
// space -- Linear id or GitLab key -- a row's provider uses, the same
// "work_scope_id" convention readers.ProjectIdentityMatchSQL documents).
//
// A project with zero matching work items gets NO ROW from the GROUP BY,
// never a row with a zero count; a project whose work items are ALL
// cancelled gets a row with countedWorkItems == 0 and is likewise not
// emitted as a fact below. Either way the caller's empty-result state
// machinery (retentionState) reports it honestly as no_data, so this
// producer never defaults a project's completion to 0% or 100%.
//
// rowCount and servedCount are DELIBERATELY two different numbers: rowCount
// is every row ClickHouse returned that matched a requested subject (feeds
// budget.observe, a TRUNCATION signal -- the source DID answer for that
// project), while servedCount is only the rows that actually became a fact
// (feeds the caller's retentionState, an AVAILABILITY signal). An
// all-cancelled project is counted in rowCount (ClickHouse answered) but
// NOT in servedCount (nothing servable came of it) -- collapsing the two
// would report State=available for a project this producer served no fact
// for, which is exactly the false-positive chris's ruling forbids.
func (p *ActualCompletionProvider) readProjectActualCompletion(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, scope readers.AuthorizationScope, settings readers.Settings, budget *factBudget) (rowCount, servedCount, rejected int, err error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, rejected, nil
	}
	completedExpr := "isNotNull(w.completed_at)"
	if timeBound.active {
		completedExpr = "(w.completed_at IS NOT NULL AND w.completed_at <= " + timeBound.asOfExpression() + ")"
	}
	statement := readers.WithSettings(workItemProjectCompletionStatement(scope, completedExpr, timeBound.existencePredicate("w.created_at")), settings)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var projectKey string
		var workItemCount, cancelledCount, unknownStatusCount, completedCount uint64
		if scanErr := row.Scan(&projectKey, &workItemCount, &cancelledCount, &unknownStatusCount, &completedCount); scanErr != nil {
			return scanErr
		}
		subject, ok := bySubject[projectKey]
		if !ok {
			return nil
		}
		rowCount++
		// countedWorkItems is the ratio's true denominator (chris: cancelled
		// never counts, in EITHER direction; unknown-status items DO count
		// -- disclosed separately, never excluded). A project whose members
		// are all cancelled is not "0% complete" -- it has nothing left to
		// count, so it is treated exactly like zero members: no fact.
		countedWorkItems := workItemCount - cancelledCount
		if countedWorkItems == 0 {
			return nil
		}
		servedCount++
		if !budget.admit() {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":         contextfabric.StringFactValue("project_work_item_completion"),
			"member_kind":          contextfabric.StringFactValue("work_item"),
			"work_item_count":      contextfabric.IntegerFactValue(int64(workItemCount)),
			"cancelled_count":      contextfabric.IntegerFactValue(int64(cancelledCount)),
			"unknown_status_count": contextfabric.IntegerFactValue(int64(unknownStatusCount)),
			"counted_work_items":   contextfabric.IntegerFactValue(int64(countedWorkItems)),
			"completed_count":      contextfabric.IntegerFactValue(int64(completedCount)),
			"completion_ratio":     contextfabric.NumberFactValue(float64(completedCount) / float64(countedWorkItems)),
			"archived_items":       contextfabric.StringFactValue(archivedItemsAbsentFromSource),
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactActualCompletion, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return 0, 0, rejected, scanErr
	}
	return rowCount, servedCount, rejected, nil
}

// workItemProjectCompletionStatement is the project-grain counterpart to
// ReadWorkItemCompletion's per-item statement: one GROUP BY aggregate,
// never a fetch-then-count in Go, so a project's true population is never
// silently narrowed by an output-row cap the way a per-item fetch would be
// (this query's output is one row per REQUESTED project, not per work
// item -- maxFactRowsPerQuery bounds project count here, never member
// count). The same repository-authorization scope as the work-item read
// applies, so a project's roll-up never counts a work item the requesting
// principal could not otherwise see. completedExpr never overlaps the
// cancelled countIf: a cancelled item's completed_at is irrelevant to the
// ratio either way, since countedWorkItems (Go side) subtracts it from
// the denominator regardless of what completedExpr would have said.
func workItemProjectCompletionStatement(scope readers.AuthorizationScope, completedExpr, existencePredicate string) string {
	rendered := readers.WorkItemScopeSQL(scope)
	from := `FROM work_items AS w FINAL`
	if rendered.JoinSQL != "" {
		from += "\n" + rendered.JoinSQL
	}
	from += "\nINNER JOIN " + projectIdentityJoinSQL() + " ON " + projectIdentityMatchSQL("w", "project_id")
	statement := `SELECT concat(p.provider, ':', p.id), count(), countIf(w.status = '` + workItemCancelledStatus + `'), countIf(w.status = '` + workItemUnknownStatus + `'), countIf(` + completedExpr + `)
` + from + `
WHERE w.org_id = {org_id:String} AND w.project_id != '' AND (` + rendered.AuthorizationExpr + `)` + existencePredicate + `
GROUP BY p.provider, p.id
ORDER BY p.id`
	return withRowLimit(statement)
}
