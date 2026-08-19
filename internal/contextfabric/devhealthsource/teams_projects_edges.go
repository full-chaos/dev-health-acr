package devhealthsource

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TeamsProjectsRelationshipTypes lists every ContextFabricRelationshipType
// this source's edge producers can write, the same contract
// ProducedRelationshipTypes (clickhouse.go) holds for the repository/work-item
// source. cmd/acr-projector's AC-3779-9 cross-wiring test unions both.
func TeamsProjectsRelationshipTypes() []contractsv1.ContextFabricRelationshipType {
	return []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBelongsToProject,
		contractsv1.ContextFabricRelationshipOwnedByTeam,
	}
}

// attributionProperties carries an Ops-computed attribution's own derivation
// metadata onto the edge. Both attribution tables below record HOW they
// decided (a source enum; work_item_team_attributions also a confidence
// enum), and that is exactly the information a consumer needs in order not to
// read a 'manual_fallback'/'inferred' attribution as though it were a
// canonical column -- see the derivation note on each producer.
func attributionProperties(source, confidence string) map[string]contractsv1.ContextFabricScalarValue {
	properties := map[string]contractsv1.ContextFabricScalarValue{}
	if source != "" {
		properties["attribution_source"] = stringScalar(source)
	}
	if confidence != "" {
		properties["attribution_confidence"] = stringScalar(confidence)
	}
	return properties
}

// queryWorkItemProjects projects work_item -> project (BELONGS_TO_PROJECT)
// from work_items.project_id.
//
// project_id is the ONLY usable link. work_items.project_key exists but is
// empty on every one of the ground-truth org's 3304 rows (live-verified), so
// a project_key-based join would silently produce zero edges while looking
// correct. The INNER JOIN to projects is the same resolvability discipline
// queryWorkItemHierarchy applies to parent_id: live, 16 of 18 distinct
// project_id values resolve and 3080 of 3086 rows join, so the join drops 6
// rows naming a project that no longer exists rather than writing a dangling
// endpoint into the graph.
//
// Authorization comes from the WORK ITEM's own repo (LEFT JOIN repos), routed
// through workItemAuthorization -- the CHAOS-3785 zero-UUID discipline. A
// Linear-sourced work item carries repo_id = the zero UUID, which is
// repo-less by design and must not be mistaken for an orphan.
//
// CHAOS-3898 (design brief §1.4): projects.id alone does not name a
// provider, and project.v2's canonical id needs one. key_resolution_count
// (count() OVER (PARTITION BY id), the SAME anchor_collision-detection
// shape queryProjectTeams already uses for project_key) proves whether THIS
// id resolves to exactly one provider within the organization; live,
// queryProjects' own doc comment records zero such collisions across every
// organization checked, so this is a defensive, currently-inert guard, not
// a live behavior change. An ambiguous id is omitted (never guessed) and
// counted in the SAME run-scoped ambiguity ledger queryProjectTeams
// reports through logAmbiguousProjectKeys, via projectTeamsQuery's own
// closure convention.
func workItemProjectsQuery(omissions *ambiguityLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return queryWorkItemProjects(ctx, client, orgID, cursor, limit, omissions)
	}
}

func queryWorkItemProjects(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, omissions *ambiguityLedger) ([]candidate, bool, error) {
	const rowKey = "concat(toString(w.repo_id), ':', w.work_item_id)"
	statement := `SELECT w.work_item_id, w.project_id, toString(w.repo_id), ifNull(r.repo, ''), w.updated_at, p.provider, p.key_resolution_count
FROM work_items AS w FINAL
INNER JOIN (
  SELECT id, provider, count() OVER (PARTITION BY id) AS key_resolution_count
  FROM projects FINAL WHERE org_id = {org_id:String}
) AS p ON p.id = w.project_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String} AND w.project_id != ''` + sincePredicate(cursor, "w.updated_at", rowKey) + orderBy("w.updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, projectID, repoID, repoSlug, provider string
		var observedAt time.Time
		var keyResolutionCount uint64
		if err := r.Scan(&workItemID, &projectID, &repoID, &repoSlug, &observedAt, &provider, &keyResolutionCount); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := repoID + ":" + workItemID
		if keyResolutionCount > 1 {
			omissions.add(provider, projectID)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		workItemCanonicalID, workItemOmitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
		if err != nil {
			return nil, err
		}
		projectCanonicalID, projectOmitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil {
			return nil, err
		}
		if workItemOmitted || projectOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship:work_item_project:" + repoID + ":" + workItemID + ":" + provider + ":" + projectID,
			Type:           contractsv1.ContextFabricRelationshipBelongsToProject,
			From:           contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: workItemCanonicalID, Label: workItemID},
			To:             contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: projectCanonicalID, Label: projectID},
			// A plain canonical column on work_items, not an Ops-computed
			// attribution -- unlike the two producers below.
			Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization:   workItemAuthorization(repoID, repoSlug),
			EvidenceRefIDs:  []string{"acr:v1:work-item:" + repoID + ":" + workItemID},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}

// queryWorkItemTeams projects work_item -> team (OWNED_BY_TEAM) from
// work_item_team_attributions, restricted to the primary attribution.
//
// is_primary = 1 is what makes this a well-defined edge rather than a fan-out:
// live, 3304 work items carry a primary team attribution, every one resolves
// against work_items, and ZERO work items carry more than one primary team,
// more than one repo_id, or more than one source among their primaries
// (all four counts verified directly). Without the is_primary filter the same
// work item carries up to five attributions from different sources
// (native_team, assignee_membership, issue_project, linked_issue,
// project_ownership), which would collapse onto duplicate relationship IDs
// and fail ContextFabricProjectionBatch.Validate() outright.
//
// Derivation is rule_inferred/source_asserted, NOT canonical_structured: this
// table is Ops' own computed attribution (its source enum spans native_team
// through manual_fallback, with a confidence enum beside it), not a canonical
// column the way work_items.project_id is. Relabelling a low-confidence
// manual_fallback attribution as observed canonical truth is precisely the
// "graph discoveries may not mint canonical truth" line in this package's
// AGENTS.md. Both enums ride along as edge properties so a consumer can see
// which it got.
//
// Authorization deliberately derives from the work item's own repo via
// work_items, NOT from work_item_team_attributions.repo_id: 5077 of that
// column's 5089 rows are the zero UUID (CHAOS-3785's trap), so scoping on it
// would be meaningless.
func queryWorkItemTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(w.repo_id), ':', a.work_item_id)"
	statement := `SELECT a.work_item_id, ifNull(a.team_id, ''), toString(a.source), toString(a.confidence), toString(w.repo_id), ifNull(r.repo, ''), a.computed_at
FROM work_item_team_attributions AS a FINAL
INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.org_id = a.org_id
INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = ifNull(a.team_id, '')
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE a.org_id = {org_id:String} AND a.is_primary = 1 AND ifNull(a.team_id, '') != ''` + sincePredicate(cursor, "a.computed_at", rowKey) + orderBy("a.computed_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, teamID, source, confidence, repoID, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&workItemID, &teamID, &source, &confidence, &repoID, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := repoID + ":" + workItemID
		workItemCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
		if err != nil {
			return nil, err
		}
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID:  "relationship:work_item_team:" + repoID + ":" + workItemID + ":" + teamID,
			Type:            contractsv1.ContextFabricRelationshipOwnedByTeam,
			From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: workItemCanonicalID, Label: workItemID},
			To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(teamID), Label: teamID},
			Properties:      attributionProperties(source, confidence),
			Derivation:      contractsv1.ContextFabricDerivationRuleInferred,
			EpistemicStatus: contractsv1.ContextFabricEpistemicSourceAsserted,
			Authorization:   workItemAuthorization(repoID, repoSlug),
			EvidenceRefIDs:  []string{"acr:v1:work-item-team:" + repoID + ":" + workItemID + ":" + teamID},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}

// queryProjectTeams projects project -> team (OWNED_BY_TEAM) from
// team_project_ownership. Two live findings shape every line of it.
//
// FIRST -- the collapse, and why FINAL is not enough. This table's ORDER BY is
// (org_id, provider, project_id, team_id, source, valid_from), so valid_from
// is part of the ReplacingMergeTree dedup key and FINAL returns 616 rows for
// the ground-truth org's THREE real edges -- one of them alone accounts for
// 608, every window still open. Read row-per-row that is 616 relationship
// candidates carrying 3 distinct relationship IDs;
// ContextFabricProjectionBatch.Validate() rejects the batch outright
// ("relationship IDs must be unique within a batch"), and because a rejected
// batch never advances a checkpoint, the organization's team/project
// projection would wedge permanently on the first tick. The GROUP BY below is
// therefore load-bearing, not an optimization: it is what makes this producer
// correct at all. Verified against live data: 616 rows in, exactly 3 out.
//
// SECOND -- the id-space trap. This table's own project_id column is NOT
// projects.id: for gitlab rows it holds the project KEY
// ('full.chaos/chaos-ops') where projects.id is
// '<org>:gitlab:71133891', and only 1 of 3 distinct values resolves. The
// project_key column joins cleanly instead (3 of 3). So the join is on
// project_key and the projected canonical id comes from the PROJECTS row,
// never from this table. (projects.team_ids is unusable for the same class of
// reason -- it carries the provider's own native team UUID, matching neither
// teams.id nor teams.team_uuid.)
//
// THIRD -- provider scoping (codex round-1 F1). project_key is only unique
// WITHIN a provider, so both the aggregation and the projects join carry
// provider. Without it, two providers' independent ownership assertions merge
// on a shared key and the join fabricates cross-provider project->team edges
// -- asserting an ownership nobody recorded -- and can emit two rows
// resolving to one projects.id, duplicating a RelationshipID and wedging the
// batch. Live today there are zero such collisions (0 keys under >1 provider
// in either table, across every organization), so this is latent rather than
// active; it is guarded because nothing in the schema prevents it and the
// failure mode is silent fabrication, not an error.
//
// The GROUP BY deliberately does NOT include this table's own project_id,
// though it is part of the source's natural key. The projected identity is
// built from projects.id, so two rows differing only in project_id would
// resolve to the SAME projects.id and duplicate a RelationshipID -- the exact
// wedge this producer exists to avoid. Grouping on (provider, project_key)
// instead cannot do that, because (org, provider, project_key) resolves to
// exactly one projects.id (verified across every organization), and no
// information is lost: zero groups map to more than one project_id.
//
// THIRD-B -- ambiguity, the failure mode provider-scoping MOVES rather than
// removes. Grouping on (provider, project_key) and resolving through projects
// assumes (org, provider, project_key) names exactly one project. That holds
// in live data today (verified across every organization), but nothing in the
// schema enforces it, and the failure is invisible where every other duplicate
// is loud: a key resolving to two projects fans this join out to two rows with
// two DISTINCT projects.id, so their RelationshipIDs differ and
// ContextFabricProjectionBatch.Validate never trips. The result would be a
// fabricated ownership edge to a project the source never asserted.
//
// key_resolution_count carries the per-(provider, project_key) project count
// out of SQL, and the scan omits BOTH candidates when it exceeds one. Omitted,
// not guessed: choosing between two equally-matching projects would be minting
// canonical truth from a coin flip. Omitted, not fatal: one ambiguous key must
// not take an organization's whole projection down, so this never returns an
// error -- it drops the edge and logs a bounded reason plus a count
// (logAmbiguousProjectKeys), because an unlogged omission is indistinguishable
// from an ownership that simply does not exist.
//
// FOURTH -- validity, and a ClickHouse NULL trap (codex round-1 F2). The
// window runs from the EARLIEST assertion ever observed to whatever the
// LATEST assertion says, keyed on valid_from. Neither obvious spelling works:
// max(valid_to) reports the largest date rather than the newest assertion, so
// an ownership superseded by an earlier close looks live for months longer;
// and plain argMax(valid_to, valid_from) SKIPS a NULL, so a newer OPEN
// assertion loses to an older closed one and a live ownership reads as ended.
// Both were verified directly against this ClickHouse version.
// argMax(tuple(valid_to), ...).1 preserves the NULL, which is why the tuple
// wrapper is load-bearing rather than decorative -- see
// TestOwnershipWindowTakesTheLatestAssertion, which asserts both directions
// because each spelling passes one and fails the other.
//
// The ordering key is a TUPLE, not valid_from alone (codex round-2 F2):
// valid_from alone leaves two assertions stamped at the same instant
// unordered, so a group holding one open and one closed assertion at that
// instant could project either, flipping with merge order. The key is
// (valid_from, valid_to IS NULL, ifNull(valid_to, epoch)):
//
//   - latest valid_from wins -- the latest-assertion rule;
//   - on a tie, OPEN outranks CLOSED, so a same-instant assertion of ongoing
//     ownership is never hidden by a simultaneous closure;
//   - among tied closed assertions, the latest valid_to wins, so even that
//     case is ordered rather than arbitrary.
//
// The open-wins choice is justified semantically, NOT empirically, and that
// distinction is worth stating: live data cannot adjudicate it, because the
// writer has never emitted a closed ownership row at all (0 of 618 rows carry
// a valid_to). Reaching the tie also requires rows differing in project_id --
// the only column inside this table's ORDER BY but outside the GROUP BY,
// which FINAL would otherwise collapse -- and that is realistic exactly
// because project_id here is unreliable (see the id-space note above).
// projectTeamsQuery binds the run-scoped ambiguity ledger to the ownership
// producer. The producer records omissions; the SOURCE logs the run total
// once per batch, so the number an operator sees is the run's distinct-key
// count rather than one page's slice.
func projectTeamsQuery(omissions *ambiguityLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return queryProjectTeams(ctx, client, orgID, cursor, limit, omissions)
	}
}

func queryProjectTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, omissions *ambiguityLedger) ([]candidate, bool, error) {
	const rowKey = "concat(p.id, ':', o.team_id, ':', o.source_name)"
	statement := `SELECT p.id, o.team_id, o.source_name, o.first_valid_from, o.latest_is_open, o.latest_valid_to, o.updated_at, p.key_resolution_count, o.provider, o.project_key
FROM (
  SELECT provider, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name,
         min(valid_from) AS first_valid_from,
         argMax(tuple(valid_to), (valid_from, valid_to IS NULL, ifNull(valid_to, toDateTime64(0, 3, 'UTC')))).1 IS NULL AS latest_is_open,
         ifNull(argMax(tuple(valid_to), (valid_from, valid_to IS NULL, ifNull(valid_to, toDateTime64(0, 3, 'UTC')))).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
         max(updated_at) AS updated_at
  FROM team_project_ownership FINAL
  WHERE org_id = {org_id:String}
  GROUP BY provider, project_key, team_id, source_name
) AS o
INNER JOIN (
  SELECT id, provider, project_key, count() OVER (PARTITION BY provider, project_key) AS key_resolution_count
  FROM (SELECT id, provider, ifNull(project_key, '') AS project_key FROM projects FINAL WHERE org_id = {org_id:String})
) AS p USING (provider, project_key)
INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = o.team_id
WHERE o.project_key != ''` + sincePredicate(cursor, "o.updated_at", rowKey) + orderBy("o.updated_at", rowKey)
	rows, truncated, err := fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var projectID, teamID, source, provider, projectKey string
		var validFrom, latestValidTo, observedAt time.Time
		var latestIsOpen uint8
		var keyResolutionCount uint64
		if err := r.Scan(&projectID, &teamID, &source, &validFrom, &latestIsOpen, &latestValidTo, &observedAt, &keyResolutionCount, &provider, &projectKey); err != nil {
			return nil, err
		}
		observedAt, validFrom, latestValidTo = observedAt.UTC(), validFrom.UTC(), latestValidTo.UTC()
		rowSortKey := projectID + ":" + teamID + ":" + source
		// An ambiguous project_key is omitted, not guessed and not fatal --
		// see this function's ambiguity note. It still yields a PROGRESS
		// candidate: the row was consumed and spent page budget, so the
		// cursor must advance past it or a page of omissions stalls the walk
		// forever (progressCandidate's doc comment).
		if keyResolutionCount > 1 {
			omissions.add(provider, projectKey)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		projectCanonicalID, projectOmitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil {
			return nil, err
		}
		if projectOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship:project_team:" + provider + ":" + projectID + ":" + teamID + ":" + source,
			Type:           contractsv1.ContextFabricRelationshipOwnedByTeam,
			From:           contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: projectCanonicalID, Label: projectID},
			To:             contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(teamID), Label: teamID},
			Properties:     attributionProperties(source, ""),
			// Same reasoning as queryWorkItemTeams: an ownership row's source
			// enum spans native through inferred/manual, so this is Ops
			// asserting an ownership, not a canonical structural column.
			Derivation:      contractsv1.ContextFabricDerivationRuleInferred,
			EpistemicStatus: contractsv1.ContextFabricEpistemicSourceAsserted,
			Authorization:   contractsv1.ContextFabricAuthorizationScope{ProjectIDs: []string{projectID}, TeamIDs: []string{teamID}},
			EvidenceRefIDs:  []string{"acr:v1:project-team:" + provider + ":" + projectID + ":" + teamID},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
		}
		relationship.ValidFrom, relationship.ValidTo = ownershipValidity(validFrom, latestIsOpen, latestValidTo)
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
	if err != nil {
		return nil, false, err
	}
	return rows, truncated, nil
}

// logAmbiguousProjectKeys surfaces omitted ownership edges as a bounded
// signal, counting DISTINCT (provider, project_key) keys accumulated over the
// whole source run rather than one page's slice keyed by something else. Without it, an omission is indistinguishable from an
// ownership that simply does not exist, which is the shape of silence this
// wave keeps removing. The message carries a fixed reason and a COUNT only --
// never a project key, project id or team id, which are tenant data; the
// organization is hashed the same way logOrphanedWorkItems hashes it.
func logAmbiguousProjectKeys(ctx context.Context, logger *slog.Logger, orgID string, omitted int) {
	if logger == nil || omitted == 0 {
		return
	}
	logger.WarnContext(ctx, "devhealthsource omitted project ownership edges for ambiguous project keys",
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
		"reason", "project_key resolves to more than one project within its provider", "omitted_ambiguous_project_keys", omitted)
}

// ownershipValidity states a project->team edge's window explicitly in both
// directions, the same owned-write discipline queryTeams/queryProjects apply
// to entities (CHAOS-3785 R3-1). Ownership begins at the earliest assertion
// ever observed for the edge and ends per the LATEST assertion -- open if that
// assertion left it open, otherwise at its valid_to.
func ownershipValidity(validFrom time.Time, latestIsOpen uint8, latestValidTo time.Time) (*time.Time, *time.Time) {
	from := validFrom
	if latestIsOpen != 0 {
		return &from, nil
	}
	to := latestValidTo
	return &from, &to
}
