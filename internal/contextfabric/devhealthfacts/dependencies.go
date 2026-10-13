package devhealthfacts

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// work_item_dependencies has one free-text relationship_type column (see
// devhealthsource/tables.go's queryWorkItemDependencies and its seed fixture
// testdata/fullstack/v1/seed/clickhouse/001_widget_service.sql, whose only
// seeded row uses 'blocks'); there is no published canonical vocabulary for
// every other value the column can hold. blockerRelationshipType is the one
// value this package treats as confirmed: a row where relationship_type is
// this value and target_work_item_id is the subject means the row's source
// work item blocks the subject. Every other relationship_type value sourced
// from the subject is treated as a required-child/dependency edge by
// BlockersProvider's sibling, RequiredChildrenProvider -- see its doc
// comment for why that split, rather than a second hardcoded string, is the
// honest choice here.
const blockerRelationshipType = "blocks"

// The counterpart's canonical work item id (CHAOS-7120). A dependency row
// names the OTHER work item by its bare work_item_id, which carries no
// repository and is not unique across repositories, so the direct read gate
// cannot decide it. Both providers therefore also resolve the counterpart's
// repository and emit its canonical "work_item.v2:<repo_id>:<work_item_id>"
// id beside the bare one (blocked_by_work_item_ref /
// required_child_work_item_ref), which the gate decides exactly like a work
// item root. The bare-id field is kept unchanged, so the engine and any
// stored answer read the same value as before; the direct read tool declares
// it opaque.
//
// The id is emitted only when the bare id resolves to exactly ONE
// repository in the organization: with two, which one the row means is not
// knowable from the row, and guessing would name a subject the row may not
// mean. The LEFT JOIN's unmatched default (count 0) also emits nothing.
const counterpartRepositoryColumnsSQL = `ifNull(c.counterpart_repo_id, ''), toUInt64(ifNull(c.counterpart_repo_count, 0))`

func counterpartRepositoryJoinSQL(counterpartColumn string) string {
	return `LEFT JOIN (SELECT work_item_id, toString(any(repo_id)) AS counterpart_repo_id, uniqExact(repo_id) AS counterpart_repo_count
  FROM work_items FINAL WHERE org_id = {org_id:String} GROUP BY work_item_id) AS c ON c.work_item_id = ` + counterpartColumn
}

// counterpartWorkItemRef returns the counterpart's canonical work item id,
// or false when the bare id does not resolve to exactly one repository or
// the id cannot be derived.
func counterpartWorkItemRef(repoID string, repoCount uint64, workItemID string) (string, bool) {
	if repoCount != 1 || repoID == "" || workItemID == "" {
		return "", false
	}
	canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		return "", false
	}
	return canonical, true
}

// BlockersProvider implements contextfabric.FactProvider for FactBlockers:
// for a work item subject, every work_item_dependencies row where that
// subject is the target and relationship_type is blockerRelationshipType --
// i.e. another work item blocking it.
type BlockersProvider struct{ facts clickhouseFacts }

func newBlockersProvider(client contextpacket.ClickHouseQueryClient) *BlockersProvider {
	return &BlockersProvider{facts: clickhouseFacts{client: client}}
}

func (p *BlockersProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactBlockers, "devhealthfacts.blockers", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem, contextfabric.SubjectTeam, contextfabric.SubjectProject})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam: {contextfabric.FactTableBreakdown},
	}
	return capability
}

func (p *BlockersProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), identity.KindWorkItem)
	defer func() {
		attachOwnershipNoteToFailure(ctx, p.facts.client, orgID, query.Subjects, teamOwnedOrReachedRepositories, factTimeBound{}, err)
	}()
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.blockers", contextfabric.FactBlockers, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	var teamOutcome teamRollupOutcome
	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		var teamErr error
		teamOutcome, teamErr = p.readTeamRollup(ctx, principal, orgID, query.RequestedRepositoryScope, orgWideFigureAllowed(principal, query.RequestedRepositoryScope), teamSubjects, &facts)
		if teamErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team blockers", teamErr)
		}
		rejected += teamOutcome.rejected
	}
	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		projectRejected, projectErr := p.readProjectRollup(ctx, principal, orgID, query.RequestedRepositoryScope, projectSubjects, &facts)
		if projectErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project blockers", projectErr)
		}
		rejected += projectRejected
	}
	teamFactCount := len(facts)
	if len(ids) == 0 {
		state, emptyReason := currentAxisReadState(teamFactCount)
		result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: teamOutcome.truncated}
		applyTeamsOwningNothing(&result, query.Subjects, teamOutcome.teamsRequested, teamOutcome.teamsWithoutRepos)
		return result, nil
	}
	// CHAOS-5438: ONE owner -- see factBudget, and WorkProvider's identical
	// note on why a single-branch provider uses it too.
	budget := newFactBudget()
	// blockerRelationshipType is an internal Go constant, not a caller
	// supplied value, so it is safe to inline as a SQL string literal here
	// rather than a bound parameter.
	//
	// work_item_dependencies carries no repo_id of its own (mirroring
	// devhealthsource/tables.go's queryWorkItemDependencies), so the INNER
	// JOIN to work_items resolves the target's repo_id -- the same repo_id
	// component v2Index decoded out of the subject's own canonical id --
	// letting the WHERE clause scope on the composite key rather than the
	// bare (cross-repo-collidable) target_work_item_id alone.
	// CHAOS-5438: the probe limit, not the output limit -- see
	// maxFactRowsProbe. Dependency cardinality can exceed subject
	// cardinality (one work item can have thousands of blockers), so this
	// provider is exactly the shape the row bound was introduced for, and
	// exactly the shape where reporting a full page as truncated is wrong.
	//
	// BOTH ENDS pass the shared work-item authorization rule: the blocked
	// subject and the blocking item it names. A blocker is itself a work
	// item's identity, so an authorized subject must never carry the id of
	// one the principal may not see.
	authorized, authorizationBindings := workItemAuthorizedIDsSQL(workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope))
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item blockers", settingsErr)
	}
	statement := readers.WithSettings(withRowProbeLimit(`SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, ''), toString(t.repo_id), `+counterpartRepositoryColumnsSQL+`
FROM `+blockerReaderRowsSQL("d", authorized)+`
INNER JOIN work_items AS t FINAL ON t.org_id = {org_id:String} AND t.work_item_id = d.blocked_id
`+counterpartRepositoryJoinSQL("d.blocker_id")+`
WHERE concat(toString(t.repo_id), ':', d.blocked_id) IN {ids:Array(String)}
ORDER BY toString(t.repo_id), d.blocked_id, d.blocker_id, d.rk, lower(ifNull(d.relationship_type, ''))
LIMIT 1 BY toString(t.repo_id), d.blocked_id, d.blocker_id`), settings)
	rowCount := 0
	seenBlockers := map[string]struct{}{}
	scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemBlockers", statement, orgID, ids, func(row readers.RowScanner) error {
		var storedSourceID, storedTargetID, relationshipType, targetRepoID, sourceRepoID string
		var sourceRepoCount uint64
		if err := row.Scan(&storedSourceID, &storedTargetID, &relationshipType, &targetRepoID, &sourceRepoID, &sourceRepoCount); err != nil {
			return err
		}
		relationKey := dependencyrelation.Key(relationshipType)
		blockedID, blockerID := storedTargetID, storedSourceID
		if relationKey == blockerInverseKey {
			blockedID, blockerID = storedSourceID, storedTargetID
		}
		subject, ok := bySubject[targetRepoID+":"+blockedID]
		if !ok {
			return nil
		}
		// One pair blocks once, whatever spellings or directions its rows
		// are stored under; the SQL order makes the surviving row stable.
		blockKey := targetRepoID + "\x00" + blockedID + "\x00" + blockerID
		if _, dup := seenBlockers[blockKey]; dup {
			return nil
		}
		seenBlockers[blockKey] = struct{}{}
		rowCount++
		if !budget.admit() {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"blocked_by_work_item_id": contextfabric.StringFactValue(blockerID)}
		if ref, ok := counterpartWorkItemRef(sourceRepoID, sourceRepoCount, blockerID); ok {
			fields["blocked_by_work_item_ref"] = contextfabric.StringFactValue(ref)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactBlockers, Subject: subject,
			Fields:         fields,
			EvidenceRefIDs: []string{dependencyEvidenceRefID(storedSourceID, storedTargetID, relationKey)},
		})
		return nil
	}, authorizationBindings...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item blockers", scanErr)
	}
	budget.observe(rowCount)
	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated() || teamOutcome.truncated}
	applyTeamsOwningNothing(&result, query.Subjects, teamOutcome.teamsRequested, teamOutcome.teamsWithoutRepos)
	return result, nil
}

// RequiredChildrenProvider implements contextfabric.FactProvider for
// FactRequiredChildren: for a work item subject, every
// work_item_dependencies row where that subject is the source and
// relationship_type is anything other than blockerRelationshipType -- i.e.
// a dependency this work item requires, as distinct from a blocking
// relationship (which BlockersProvider already reports, from the other
// side, as a blocker of whichever work item is being blocked).
type RequiredChildrenProvider struct{ facts clickhouseFacts }

func newRequiredChildrenProvider(client contextpacket.ClickHouseQueryClient) *RequiredChildrenProvider {
	return &RequiredChildrenProvider{facts: clickhouseFacts{client: client}}
}

func (p *RequiredChildrenProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactRequiredChildren, "devhealthfacts.required_children", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *RequiredChildrenProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
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
			applySubjectShapeRejection(&result, "devhealthfacts.required_children", contextfabric.FactRequiredChildren, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-5438: ONE owner -- see factBudget, and WorkProvider's identical
	// note on why a single-branch provider uses it too.
	budget := newFactBudget()
	// See BlockersProvider's doc comment on the same JOIN: work_item_dependencies
	// has no repo_id of its own, so the source's repo_id is resolved via
	// work_items the same way devhealthsource's own producer does.
	// CHAOS-5438: probe limit, output bound separate -- see the same note
	// on BlockersProvider above.
	//
	// BOTH ENDS pass the shared work-item authorization rule, as on
	// BlockersProvider: the subject and the required child it names.
	authorized, authorizationBindings := workItemAuthorizedIDsSQL(workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope))
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item required children", settingsErr)
	}
	statement := readers.WithSettings(withRowProbeLimit(`SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, ''), toString(s.repo_id), `+counterpartRepositoryColumnsSQL+`
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS s FINAL ON s.org_id = {org_id:String} AND s.work_item_id = d.source_work_item_id
`+counterpartRepositoryJoinSQL("d.target_work_item_id")+`
WHERE d.org_id = {org_id:String} AND concat(toString(s.repo_id), ':', d.source_work_item_id) IN {ids:Array(String)} AND NOT (`+blockerRelationPredicateSQL(dependencyrelation.KeySQL("d.relationship_type"))+`)
  AND d.source_work_item_id IN `+authorized+` AND d.target_work_item_id IN `+authorized+`
ORDER BY toString(s.repo_id), d.source_work_item_id, d.target_work_item_id, `+dependencyrelation.KeySQL("d.relationship_type")+`, lower(ifNull(d.relationship_type, ''))
LIMIT 1 BY toString(s.repo_id), d.source_work_item_id, d.target_work_item_id, `+dependencyrelation.KeySQL("d.relationship_type")), settings)
	rowCount := 0
	seenRelations := map[string]struct{}{}
	scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemRequiredChildren", statement, orgID, ids, func(row readers.RowScanner) error {
		var sourceID, targetID, relationshipType, sourceRepoID, targetRepoID string
		var targetRepoCount uint64
		if err := row.Scan(&sourceID, &targetID, &relationshipType, &sourceRepoID, &targetRepoID, &targetRepoCount); err != nil {
			return err
		}
		subject, ok := bySubject[sourceRepoID+":"+sourceID]
		if !ok {
			return nil
		}
		// CHAOS-7177: resolve the row's type through the projector's alias
		// table so a stale 'relates' row and a live 'relates_to' row for one
		// pair yield ONE fact. Dedupe before admit(): a twin must not spend
		// output budget.
		relationKey := dependencyrelation.Key(relationshipType)
		emittedType := dependencyrelation.EmittedSpelling(relationshipType)
		// Empty and whitespace-only types share one key too (':fwd').
		dedupeKey := sourceRepoID + "\x00" + sourceID + "\x00" + targetID + "\x00" + relationKey
		if _, dup := seenRelations[dedupeKey]; dup {
			return nil
		}
		seenRelations[dedupeKey] = struct{}{}
		// Count DISTINCT relations only: the SQL already collapses twins before
		// the probe limit, and a Go-side twin must not read as truncation.
		rowCount++
		if !budget.admit() {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"required_child_work_item_id": contextfabric.StringFactValue(targetID)}
		if ref, ok := counterpartWorkItemRef(targetRepoID, targetRepoCount, targetID); ok {
			fields["required_child_work_item_ref"] = contextfabric.StringFactValue(ref)
		}
		if emittedType != "" {
			fields["relationship_type"] = contextfabric.StringFactValue(emittedType)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactRequiredChildren, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{dependencyEvidenceRefID(sourceID, targetID, relationKey)},
		})
		return nil
	}, authorizationBindings...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item required children", scanErr)
	}
	budget.observe(rowCount)
	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
	return result, nil
}

// dependencyEvidenceRefID is the evidence identity of ONE dependency relation:
// (source, target, canonical relation key), each component escaped
// (CHAOS-7252: work item ids and relation keys hold ':', so a bare-':' join
// named two relations). It is the same string the projection mints for the
// relation and the one the source-row statement work_item_dependencies.v2
// derives, so the ref expands to exactly this relation.
func dependencyEvidenceRefID(sourceID, targetID, relationKey string) string {
	ref, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, sourceID, targetID, relationKey)
	return ref
}

// blockerRollupRepo is one owned repository's recorded blocker edges whose
// target work item lives in it.
type blockerRollupRepo struct {
	blockedItems, blockerEdges, excludedEdges int64
	// weakestLinkRank is the weakest tier (linkTierRankSQL: native 3,
	// explicit_text 2, heuristic 1) among the links that placed this
	// repository's blocked items in it.
	weakestLinkRank int64
}

// blockerTeamTotals are one team's distinct blocked items and edges across its
// owned repositories.
type blockerTeamTotals struct {
	blockedItems, blockerEdges, excludedEdges int64
	// projectBlockedItems and projectWeakestRank describe the blocked items
	// reached through the team's owned projects' linked pull request rows
	// (they are inside the totals above, counted once with owned-repository
	// items); projectWeakestRank is 0 when no project item contributed.
	projectBlockedItems, projectWeakestRank int64
}

// blockerTeamAge is one team's open blocker edges by what is known of their
// start: openEdges are the distinct open edges, edgesWithStart those with a
// known start, edgesWithExactStart those whose start is the provider's own
// time of the link. oldestStartMillis is the earliest known start in unix
// milliseconds (0 when no open edge has one).
type blockerTeamAge struct {
	openEdges, edgesWithStart, edgesWithExactStart, oldestStartMillis int64
}

const (
	blockerAgeBasisExact       = "exact"
	blockerAgeBasisLowerBound  = "lower_bound"
	blockerAgeNoneOpen         = "none_open"
	blockerAgeNoRelationStart  = "no_relation_start"
	blockerAgeStartUnavailable = "start_unavailable"
	millisPerDay               = int64(86400000)
)

// ageReadFailure decides what a failed age statement does to the read: the age
// is an addition to the counts, so a failure other than the caller's own
// cancellation leaves the age absent with a reason and a warning, and the
// counts are still served.
func ageReadFailure(ctx context.Context, subject string, err error) error {
	if ctx.Err() != nil {
		return err
	}
	slog.WarnContext(ctx, "devhealthfacts.blocker_age_read_failed", "subject_kind", contextfabric.SanitizeLogAttr(subject), "reason", contextfabric.SanitizeLogAttr(blockerAgeStartUnavailable), "error", contextfabric.SanitizeLogAttr(err.Error()))
	return nil
}

// blockerOldestAgeOrUnavailable is blockerOldestAge, or the start_unavailable
// reason when the age statement failed.
func blockerOldestAgeOrUnavailable(a blockerTeamAge, now time.Time, unavailable bool) (days int64, basis, reason string, ok bool) {
	if unavailable {
		return 0, "", blockerAgeStartUnavailable, false
	}
	return blockerOldestAge(a, now)
}

// blockerOldestAge is the age of the oldest open blocker edge in whole days at
// now. A known start gives a lower bound on the oldest age whatever the edges
// without a start hold, so the largest known age is served with a basis: exact
// when every open edge carries the provider's time of the link, lower_bound
// otherwise. With no open edge, or no open edge with a start, days is absent
// (ok false) and reason says which; an age is never zero for missing.
func blockerOldestAge(a blockerTeamAge, now time.Time) (days int64, basis, reason string, ok bool) {
	switch {
	case a.openEdges <= 0:
		return 0, "", blockerAgeNoneOpen, false
	case a.edgesWithStart <= 0 || a.oldestStartMillis <= 0:
		return 0, "", blockerAgeNoRelationStart, false
	}
	days = (now.UnixMilli() - a.oldestStartMillis) / millisPerDay
	if days < 0 {
		days = 0
	}
	basis = blockerAgeBasisLowerBound
	if a.edgesWithExactStart == a.openEdges {
		basis = blockerAgeBasisExact
	}
	return days, basis, "", true
}

// blockerPairSeparator joins a team id and a repository id in the pair list
// bound to the totals query; the unit separator is in neither identifier.
const blockerPairSeparator = "\x1f"

// blockerEndpointIDsSQL is the ids at either end of the organization's blocker
// edges. The team blocker statements read work items, their link rows and
// their authorization for these ids only, so a statement's cost follows the
// blocker edges, not the size of the organization's work item table.
var blockerEndpointIDsSQL = `(SELECT arrayJoin([be.blocker_id, be.blocked_id]) FROM ` + blockerRowsSQL("be", "work_item_dependencies AS r FINAL", false, "") + `)`

// blockerWorkItemsSQL is the work_items rows of the blocker edge endpoints
// under alias, with the columns the team blocker statements read.
func blockerWorkItemsSQL(alias string) string {
	return `(SELECT org_id, work_item_id, status, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN ` + blockerEndpointIDsSQL + `) AS ` + alias
}

// blockerStartJoinSQL is the left join that gives a blocker edge its first
// sync time. The subselect reads the first-seen rows of the blocker edge
// endpoints only, so its cost follows the blocker edges.
var blockerStartJoinSQL = ` LEFT JOIN (SELECT f.blocker_id AS blocker_id, f.blocked_id AS blocked_id, min(f.first_seen_at) AS first_seen, 1 AS seen
    FROM ` + blockerRowsSQL("f", "work_item_dependency_first_seen AS r", false, "r.source_work_item_id IN "+blockerEndpointIDsSQL+" AND r.target_work_item_id IN "+blockerEndpointIDsSQL, "first_seen_at") + `
    GROUP BY blocker_id, blocked_id) AS fs
    ON fs.blocker_id = d.blocker_id AND fs.blocked_id = d.blocked_id`

// blockerStartOutputSQL are the per-edge start columns carried through the
// edge grouping: start_unix is the provider time of the link when the synced
// payload carries one, else the first time a sync wrote the relation (never an
// item's creation time), in unix milliseconds, NULL when neither is stored.
// start_exact is 1 when the provider time is the source. The first sync time
// is later than or equal to the true start, so an age from it never
// overstates.
const blockerStartOutputSQL = `, min(if(d.relation_started_at IS NOT NULL, toNullable(toUnixTimestamp64Milli(d.relation_started_at)), if(fs.seen = 1, toNullable(toUnixTimestamp64Milli(fs.first_seen)), NULL))) AS start_unix, min(toUInt8(d.relation_started_at IS NOT NULL)) AS start_exact`

// blockerEdgeRowsSQL is the one reading of a blocker edge's repository: the
// distinct (repository, blocked item, blocker) rows with the item's strongest
// link tier in that repository. A blocked item reaches a repository only
// through its linked pull request rows (work_graph_issue_pr, the pull
// request's repository), never through the work item's own repo_id column,
// read with the same strict link semantics as the project scope expansion
// (linkedPullRequestOnSQL, workItemIsIssueSQL).
func blockerEdgeRowsSQL(statusPredicate, authorized string, withStart bool) string {
	startOutput, startJoin := blockerStartParts(withStart)
	return `SELECT l.pr_repo_id AS rid, d.blocked_id AS tgt, d.blocker_id AS src, (` + statusPredicate + `) AS is_open, max(l.link_rank) AS item_rank` + startOutput + `
  FROM ` + blockerDependenciesSQL("d", authorized) + startJoin + `
  INNER JOIN ` + admittedIssueLinksAmongSQL(blockerEndpointIDsSQL) + ` AS l ON l.work_item_id = d.blocked_id
  INNER JOIN ` + blockerWorkItemsSQL("s") + ` ON s.org_id = {org_id:String} AND s.work_item_id = d.blocker_id
  WHERE toString(l.pr_repo_id) IN {ids:Array(String)}
  GROUP BY rid, tgt, src, is_open`
}

// blockerProjectEdgeRowsSQL is the project arm of the same reading: the
// distinct (team, blocked item, blocker) rows of items that belong to a
// project the team owns and reach a repository through a linked pull request
// row (teamProjectLinkRowsSQL), with the item's strongest link tier. Same
// issue, status and authorization rules as blockerEdgeRowsSQL.
func blockerProjectEdgeRowsSQL(statusPredicate, authorized string, withStart bool) string {
	startOutput, startJoin := blockerStartParts(withStart)
	return `SELECT pl.team_id AS team_id, d.blocked_id AS tgt, d.blocker_id AS src, (` + statusPredicate + `) AS is_open, max(pl.link_rank) AS item_rank` + startOutput + `
  FROM ` + blockerDependenciesSQL("d", authorized) + startJoin + `
  INNER JOIN ` + blockerWorkItemsSQL("t") + ` ON t.org_id = {org_id:String} AND t.work_item_id = d.blocked_id
  INNER JOIN ` + teamProjectLinkRowsAmongSQL("team_ids", factTimeBound{}, blockerEndpointIDsSQL) + ` AS pl ON pl.work_item_id = d.blocked_id
  INNER JOIN ` + blockerWorkItemsSQL("s") + ` ON s.org_id = {org_id:String} AND s.work_item_id = d.blocker_id
  WHERE true
    AND ` + workItemIsIssueSQL("t") + `
  GROUP BY team_id, tgt, src, is_open`
}

func blockerStartParts(withStart bool) (output, join string) {
	if !withStart {
		return "", ""
	}
	return blockerStartOutputSQL, blockerStartJoinSQL
}

// blockerLinkTierName names a link rank; a rank outside 1..3 is never emitted.
func blockerLinkTierName(rank int64) string {
	switch rank {
	case 3:
		return "native"
	case 2:
		return "explicit_text"
	case 1:
		return "heuristic"
	}
	return ""
}

const blockerStatusBasisOpen = "open"

// blockerBreakdownProjectLimitation is served beside a total that includes
// blocked items reached through an owned project: the per-repository
// breakdown lists the owned repositories only.
const blockerBreakdownProjectLimitation = "repository_breakdown lists the team's owned repositories only; blocked work items reached through its owned projects' linked pull requests are in the totals and in project_blocked_work_items_current, not per repository"

// blockerOpenPredicate is the SQL over the blocking item's normalized
// status: a blocker counts while it is not done or canceled. Both ends of an
// edge pass the work-item authorization rule, which is built from work_items
// rows, so a blocker with no work_items row is outside the caller's authorized
// population and is counted nowhere, neither as open nor as excluded.
const blockerOpenPredicate = "lower(s.status) NOT IN ('done', '" + workItemCancelledStatus + "')"

// readTeamRollup serves a team subject on the current axis only:
// work_item_dependencies carries no event time, so no window is claimed and
// the counts are *_current. A blocker edge counts while the blocking item is
// open (not done or canceled); the edges the status excluded are counted apart. Both ends pass the shared
// work-item authorization rule, so the counts cover the caller's authorized
// items and are declared caller-scoped.
func (p *BlockersProvider) readTeamRollup(ctx context.Context, principal storage.Principal, orgID string, requestedScope []string, orgWide bool, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact) (outcome teamRollupOutcome, err error) {
	teamIDs, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	outcome.rejected = rejected
	outcome.teamsRequested = len(teamIDs)
	if len(teamIDs) == 0 {
		return outcome, nil
	}
	owned, err := teamOwnedRepositories(ctx, p.facts.client, orgID, teamIDs, factTimeBound{})
	if err != nil {
		return outcome, err
	}
	reached, err := teamProjectReachedRepositories(ctx, p.facts.client, orgID, teamIDs, factTimeBound{})
	if err != nil {
		return outcome, err
	}
	reachedOnly := reachedOnlyRepositories(reached, owned)
	statusPredicate := blockerOpenPredicate
	byRepo := map[string]blockerRollupRepo{}
	totals := map[string]blockerTeamTotals{}
	ages := map[string]blockerTeamAge{}
	ageUnavailable := map[string]bool{}
	var authorizedMemo *blockerAuthorizationMemo
	if repoKeys := repoKeysOf(owned); len(repoKeys) > 0 || len(reached) > 0 {
		settings, settingsErr := workItemReaderSettings(ctx)
		if settingsErr != nil {
			return outcome, settingsErr
		}
		authorized, authorizationBindings, authErr := p.blockerAuthorization(ctx, principal, orgID, requestedScope, settings)
		if authErr != nil {
			return outcome, authErr
		}
		authorizedMemo = &blockerAuthorizationMemo{authorized, authorizationBindings, settings}
		edgeRows := blockerEdgeRowsSQL(statusPredicate, authorized, false)
		// The weakest tier of a repository's counted items; the per-repository
		// counts feed the breakdown only. A team total is NOT the sum of these:
		// an item linked to two owned repositories is one blocked item.
		statement := readers.WithSettings(`SELECT toString(rid), toInt64(uniqExactIf(tgt, is_open)), toInt64(uniqExactIf((tgt, src), is_open)), toInt64(uniqExactIf((tgt, src), NOT is_open)), toInt64(if(countIf(is_open) > 0, minIf(item_rank, is_open), min(item_rank)))
FROM (`+edgeRows+`)
GROUP BY rid
ORDER BY rid`, settings)
		if len(repoKeys) > 0 {
			if scanErr := forEachRepoKeyChunk(repoKeys, func(chunk []string) error {
				return readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamBlockerRollup", statement, orgID, chunk, func(row readers.RowScanner) error {
					var repoID string
					var r blockerRollupRepo
					if err := row.Scan(&repoID, &r.blockedItems, &r.blockerEdges, &r.excludedEdges, &r.weakestLinkRank); err != nil {
						return err
					}
					byRepo[repoID] = r
					return nil
				}, authorizationBindings...)
			}); scanErr != nil {
				return outcome, scanErr
			}
		}
		// Team totals: distinct blocked items and edges across two arms, the
		// team's owned repositories (one row per team and repository pair) and
		// the items of its owned projects that reach a repository through a
		// linked pull request row. One item in both arms is one blocked item.
		// The read groups by team, so team ids go in chunks under the
		// statement's result row cap; a team's rows live in one chunk only.
		const projectArm = "arm = 'project'"
		totalsStatement := readers.WithSettings(`SELECT team_id, toInt64(uniqExactIf(tgt, is_open)), toInt64(uniqExactIf((tgt, src), is_open)), toInt64(uniqExactIf((tgt, src), NOT is_open)),
  toInt64(uniqExactIf(tgt, is_open AND `+projectArm+`)),
  toInt64(if(countIf(is_open AND `+projectArm+`) > 0, minIf(item_rank, is_open AND `+projectArm+`), minIf(item_rank, `+projectArm+`)))
FROM (
  SELECT tp.team_id AS team_id, e.tgt AS tgt, e.src AS src, toUInt8(e.is_open) AS is_open, toInt64(e.item_rank) AS item_rank, 'repository' AS arm
  FROM (`+edgeRows+`) AS e
  INNER JOIN (
    SELECT splitByChar(char(31), pair)[1] AS team_id, splitByChar(char(31), pair)[2] AS rid_str
    FROM (SELECT arrayJoin({team_repo_pairs:Array(String)}) AS pair)
  ) AS tp ON tp.rid_str = toString(e.rid)
  UNION ALL
  SELECT team_id, tgt, src, toUInt8(is_open) AS is_open, toInt64(item_rank) AS item_rank, 'project' AS arm
  FROM (`+blockerProjectEdgeRowsSQL(statusPredicate, authorized, false)+`)
)
GROUP BY team_id
ORDER BY team_id`, settings)
		ageStatement := readers.WithSettings(`SELECT team_id, toInt64(uniqExactIf((tgt, src), is_open)), toInt64(uniqExactIf((tgt, src), is_open AND start_unix IS NOT NULL)), toInt64(uniqExactIf((tgt, src), is_open AND start_exact = 1)),
  ifNull(toInt64(minIf(start_unix, is_open)), 0)
FROM (
  SELECT tp.team_id AS team_id, e.tgt AS tgt, e.src AS src, toUInt8(e.is_open) AS is_open, e.start_unix AS start_unix, e.start_exact AS start_exact
  FROM (`+blockerEdgeRowsSQL(statusPredicate, authorized, true)+`) AS e
  INNER JOIN (
    SELECT splitByChar(char(31), pair)[1] AS team_id, splitByChar(char(31), pair)[2] AS rid_str
    FROM (SELECT arrayJoin({team_repo_pairs:Array(String)}) AS pair)
  ) AS tp ON tp.rid_str = toString(e.rid)
  UNION ALL
  SELECT team_id, tgt, src, toUInt8(is_open) AS is_open, start_unix, start_exact
  FROM (`+blockerProjectEdgeRowsSQL(statusPredicate, authorized, true)+`)
)
GROUP BY team_id
ORDER BY team_id`, settings)
		if scanErr := forEachTeamIDChunk(teamIDs, func(teamChunk []string) error {
			chunkOwned := make(map[string][]teamOwnedRepo, len(teamChunk))
			var pairs []string
			for _, teamID := range teamChunk {
				chunkOwned[teamID] = owned[teamID]
				for _, repo := range owned[teamID] {
					pairs = append(pairs, teamID+blockerPairSeparator+repo.key)
				}
			}
			extra := append([]readers.Binding{{Name: "team_repo_pairs", Value: pairs}, {Name: "team_ids", Value: teamChunk}}, authorizationBindings...)
			err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamBlockerTotals", totalsStatement, orgID, repoKeysOf(chunkOwned), func(row readers.RowScanner) error {
				var teamID string
				var t blockerTeamTotals
				if err := row.Scan(&teamID, &t.blockedItems, &t.blockerEdges, &t.excludedEdges, &t.projectBlockedItems, &t.projectWeakestRank); err != nil {
					return err
				}
				totals[teamID] = t
				return nil
			}, extra...)
			if err != nil {
				return err
			}
			chunkAges := map[string]blockerTeamAge{}
			ageErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamBlockerOldestAge", ageStatement, orgID, repoKeysOf(chunkOwned), func(row readers.RowScanner) error {
				var teamID string
				var a blockerTeamAge
				if err := row.Scan(&teamID, &a.openEdges, &a.edgesWithStart, &a.edgesWithExactStart, &a.oldestStartMillis); err != nil {
					return err
				}
				chunkAges[teamID] = a
				return nil
			}, extra...)
			if ageErr != nil {
				if err := ageReadFailure(ctx, "team", ageErr); err != nil {
					return err
				}
				for _, teamID := range teamChunk {
					ageUnavailable[teamID] = true
				}
				return nil
			}
			for teamID, a := range chunkAges {
				ages[teamID] = a
			}
			return nil
		}); scanErr != nil {
			return outcome, scanErr
		}
	}
	// Blocked items that are in no link row reach no repository, so they are
	// counted under no team. The count is organization-wide (caller-authorized)
	// and withheld from a repository-restricted caller, like the incident
	// rollup's not-attributable figure.
	var withoutLink int64
	if orgWide && (len(owned) > 0 || len(reached) > 0) {
		memo := authorizedMemo
		if memo == nil {
			settings, settingsErr := workItemReaderSettings(ctx)
			if settingsErr != nil {
				return outcome, settingsErr
			}
			authorized, authorizationBindings, authErr := p.blockerAuthorization(ctx, principal, orgID, requestedScope, settings)
			if authErr != nil {
				return outcome, authErr
			}
			memo = &blockerAuthorizationMemo{authorized, authorizationBindings, settings}
		}
		authorized, authorizationBindings, settings := memo.sql, memo.bindings, memo.settings
		statement := readers.WithSettings(`SELECT toInt64(uniqExact(d.blocked_id))
FROM `+blockerDependenciesSQL("d", authorized)+`
INNER JOIN `+blockerWorkItemsSQL("t")+` ON t.org_id = {org_id:String} AND t.work_item_id = d.blocked_id
INNER JOIN `+blockerWorkItemsSQL("s")+` ON s.org_id = {org_id:String} AND s.work_item_id = d.blocker_id
WHERE true
  AND `+statusPredicate+` AND `+workItemIsIssueSQL("t")+`
  AND d.blocked_id NOT IN (SELECT work_item_id FROM `+admittedIssueLinksAmongSQL(blockerEndpointIDsSQL)+`)`, settings)
		if scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamBlockerUnlinkedItems", statement, orgID, nil, func(row readers.RowScanner) error {
			return row.Scan(&withoutLink)
		}, authorizationBindings...); scanErr != nil {
			return outcome, scanErr
		}
	}
	tables := teamRollupTablesIncluded(len(teamIDs))
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		repos := owned[teamID]
		if len(repos) == 0 && len(reachedOnly[teamID]) == 0 {
			outcome.teamsWithoutRepos = append(outcome.teamsWithoutRepos, subject)
			continue
		}
		var blockedItems, blockerEdges, excludedEdges, withData int64
		weakest := int64(0)
		breakdown := make([]contextfabric.FactValueRow, 0, len(repos))
		for _, repo := range repos {
			r, ok := byRepo[repo.key]
			if !ok {
				continue
			}
			outcome.contributed++
			withData++
			if weakest == 0 || (r.weakestLinkRank > 0 && r.weakestLinkRank < weakest) {
				weakest = r.weakestLinkRank
			}
			if tables {
				cells := map[string]contextfabric.FactValue{
					"repository_id":                contextfabric.StringFactValue(repo.key),
					"blocked_work_items_current":   contextfabric.IntegerFactValue(r.blockedItems),
					"blocker_dependencies_current": contextfabric.IntegerFactValue(r.blockerEdges),
				}
				if tier := blockerLinkTierName(r.weakestLinkRank); tier != "" {
					cells["link_tier"] = contextfabric.StringFactValue(tier)
				}
				if repo.name != "" {
					cells["repository_name"] = contextfabric.StringFactValue(repo.name)
				}
				breakdown = append(breakdown, contextfabric.FactValueRow{Fields: cells})
			}
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                    contextfabric.StringFactValue(teamRollupBasis),
			"repository_tables_basis":         contextfabric.StringFactValue(teamRollupTablesBasis(tables)),
			"blocker_status_basis":            contextfabric.StringFactValue(blockerStatusBasisOpen),
			"owned_repository_count":          contextfabric.IntegerFactValue(int64(len(repos))),
			"repositories_with_data_count":    contextfabric.IntegerFactValue(withData),
			"repositories_without_data_count": contextfabric.IntegerFactValue(int64(len(repos)) - withData),
		}
		if table, omitted, ok := ownedRepositoriesFactValue(repos); ok && tables {
			fields["owned_repositories"] = table
			if omitted > 0 {
				outcome.truncated = true
				fields["owned_repositories_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			}
		}
		if orgWide {
			fields["blocked_work_items_without_link_count"] = contextfabric.IntegerFactValue(withoutLink)
		}
		projectData := false
		if t, ok := totals[teamID]; ok {
			blockedItems, blockerEdges, excludedEdges = t.blockedItems, t.blockerEdges, t.excludedEdges
			if t.projectWeakestRank > 0 {
				projectData = true
				if weakest == 0 || t.projectWeakestRank < weakest {
					weakest = t.projectWeakestRank
				}
				fields["project_blocked_work_items_current"] = contextfabric.IntegerFactValue(t.projectBlockedItems)
				fields["repository_breakdown_limitation"] = contextfabric.StringFactValue(blockerBreakdownProjectLimitation)
			}
		}
		if n := len(reachedOnly[teamID]); n > 0 {
			fields["project_reached_repository_count"] = contextfabric.IntegerFactValue(int64(n))
		}
		if withData > 0 || projectData {
			if tier := blockerLinkTierName(weakest); tier != "" {
				fields["repository_link_basis"] = contextfabric.StringFactValue(tier)
			}
			fields["blocked_work_items_current"] = contextfabric.IntegerFactValue(blockedItems)
			fields["blocker_dependencies_current"] = contextfabric.IntegerFactValue(blockerEdges)
			fields["blocker_dependencies_excluded_by_status_current"] = contextfabric.IntegerFactValue(excludedEdges)
			if days, basis, reason, ok := blockerOldestAgeOrUnavailable(ages[teamID], clock(), ageUnavailable[teamID]); ok {
				fields["oldest_blocked_age_days"] = contextfabric.IntegerFactValue(days)
				fields["oldest_blocked_age_basis"] = contextfabric.StringFactValue(basis)
			} else {
				fields["oldest_blocked_age_unavailable_reason"] = contextfabric.StringFactValue(reason)
			}
			if table, omitted, ok := repositoryBreakdownFactValue(breakdown, "",
				[]string{"blocked_work_items_current", "blocker_dependencies_current"}, []string{"repository_name", "link_tier"}); ok && tables {
				fields["repository_breakdown"] = table
				if omitted > 0 {
					outcome.truncated = true
					fields["repository_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactBlockers, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return outcome, nil
}

var (
	blockerForwardKey = dependencyrelation.Key(blockerRelationshipType)
	blockerInverseKey = dependencyrelation.Key("blocked_by")
)

// blockerRelationKeyColumn is the column the blocker row sources carry the
// canonical relation key in, so the spelling map is evaluated once per source.
const blockerRelationKeyColumn = "rk"

// blockerRelationPredicateSQL holds for a stored row whose relation key column
// states a blocker in either direction: every spelling the graph projection
// maps onto a blocks edge.
func blockerRelationPredicateSQL(keyColumn string) string {
	return keyColumn + " IN ('" + blockerForwardKey + "', '" + blockerInverseKey + "')"
}

// blockerRowsSQL is a source of the stored blocker rows with the ends
// normalised: blocker_id blocks blocked_id, for every stored spelling and
// direction (an inverse spelling states the blocked item as the source), plus
// the extra columns named. With stored set it also carries the stored source,
// target and relationship type and the relation key rk, which only the work
// item reader needs; the edge statements carry the two ids alone, so their
// joins hold no column they do not read. from is the table with its alias r
// (and FINAL, which must follow the alias); rawFilter, when set, is a
// condition on the stored r.source_work_item_id / r.target_work_item_id that
// stays on the table columns so the read can prune by them before the FINAL
// merge (a condition on the derived ids cannot); alias names the result. The
// spelling map is evaluated once, in the innermost select, and the organization
// is fixed there.
func blockerRowsSQL(alias, from string, stored bool, rawFilter string, extraColumns ...string) string {
	extra := ""
	outerExtra := ""
	for _, column := range extraColumns {
		extra += ", r." + column
		outerExtra += ", x." + column + " AS " + column
	}
	key := "x." + blockerRelationKeyColumn
	storedInner, storedOuter := "", ""
	if stored {
		storedInner = ", r.relationship_type"
		storedOuter = ", x.source_work_item_id AS source_work_item_id, x.target_work_item_id AS target_work_item_id, x.relationship_type AS relationship_type, " + key + " AS " + blockerRelationKeyColumn
	}
	inner := "SELECT r.source_work_item_id, r.target_work_item_id" + storedInner + extra + ", " + dependencyrelation.KeySQL("r.relationship_type") + " AS " + blockerRelationKeyColumn +
		" FROM " + from + " WHERE r.org_id = {org_id:String}"
	if rawFilter != "" {
		inner += " AND " + rawFilter
	}
	return "(SELECT if(" + key + " = '" + blockerInverseKey + "', x.target_work_item_id, x.source_work_item_id) AS blocker_id" +
		", if(" + key + " = '" + blockerInverseKey + "', x.source_work_item_id, x.target_work_item_id) AS blocked_id" + storedOuter + outerExtra +
		" FROM (" + inner + ") AS x WHERE " + blockerRelationPredicateSQL(key) + ") AS " + alias
}

// blockerDependenciesSQL is blockerRowsSQL over work_item_dependencies.
func blockerDependenciesSQL(alias, authorized string) string {
	return blockerRowsSQL(alias, "work_item_dependencies AS r FINAL", false, "r.source_work_item_id IN "+authorized+" AND r.target_work_item_id IN "+authorized, "relation_started_at")
}

// blockerReaderRowsSQL is the same rows with the stored columns the work item
// blocker reader cites its evidence from.
func blockerReaderRowsSQL(alias, authorized string) string {
	return blockerRowsSQL(alias, "work_item_dependencies AS r FINAL", true, "r.source_work_item_id IN "+authorized+" AND r.target_work_item_id IN "+authorized)
}
