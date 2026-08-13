package devhealthsource

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TeamsProjectsSourceName is the Source value a TeamsProjectsSource writes.
const TeamsProjectsSourceName = "dev_health_teams_projects"

// TeamsProjectsSourceVersion is this source's own SourceVersion, independent
// of ClickHouseSourceVersion. Both the name above and this version are what
// make CHAOS-3802's acceptance criterion ("an org rebuild picks the new kinds
// up with no second migration") true for free: projectionrun checkpoints are
// keyed (org_id, source), so this source has never written a checkpoint row
// for any organization. Its first enabled tick therefore reads Cursor == ""
// and takes the full-snapshot path, with no migration and no backfill --
// whereas folding these producers into ClickHouseProjectionSource would have
// required bumping ClickHouseSourceVersion to v4 and forcing
// ErrProjectionSourceVersionChanged (a full rebuild) on every already-projected
// organization, for content none of them asked for.
const TeamsProjectsSourceVersion = "devhealthsource.teams_projects.v1"

// teamsProjectsTables is this source's bounded coverage. Both tables were
// already canonical Dev Health data; neither introduces a new ingest path.
//
// Deliberately NOT read here, with the reason each was ruled out against
// live data (org 70d529e0-3c06-4597-8480-794fd02328b6, 2026-08-13):
//
//   - work_unit_membership: its node_type values are issue/pr/commit and its
//     category_kind values are theme/subcategory. It is work-unit THEME
//     clustering, not team or project membership -- so the work_unit_id
//     (SHA space) vs work_item_id (linear:CHAOS-xxxx space) hazard around it
//     never arises here, because it is not a source for either kind.
//   - team_memberships: there is no person/member SubjectKind to point an
//     edge at. (It is also append-per-sync: 1526 rows collapse to 4 distinct
//     (team_id, member_id) pairs.)
//   - projects.team_ids: the provider's own native team id space. Live, the
//     Linear projects carry ca148f86-…, while that team's teams.id is CHAOS
//     and its teams.team_uuid is 3d89b2cf-… -- the array matches NEITHER, so
//     it cannot carry a project->team edge.
var teamsProjectsTables = []entityTable{
	{name: "teams", query: queryTeams},
	{name: "projects", query: queryProjects},
}

// TeamsProjectsSource is the canonical Dev Health ProjectionSource for Team
// and Project subjects (CHAOS-3802). Both kinds were already members of
// ContextFabricSubjectKind and both were already declared by fact providers;
// they were simply never projected, so no team or project subject could ever
// resolve and every team-scoped fact provider stayed dark.
type TeamsProjectsSource struct {
	client  contextpacket.ClickHouseQueryClient
	enabled bool
	now     func() time.Time
}

// NewTeamsProjectsSource returns a TeamsProjectsSource. enabled mirrors
// ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED; when false the source is
// a documented no-op rather than a partially-working adapter.
func NewTeamsProjectsSource(client contextpacket.ClickHouseQueryClient, enabled bool) (*TeamsProjectsSource, error) {
	if client == nil {
		return nil, fmt.Errorf("devhealthsource: clickhouse query client is required")
	}
	return &TeamsProjectsSource{client: client, enabled: enabled, now: time.Now}, nil
}

func (s *TeamsProjectsSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if s == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: teams/projects source is not configured")
	}
	if !s.enabled {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	return sourcePlan{
		client:  s.client,
		source:  TeamsProjectsSourceName,
		version: TeamsProjectsSourceVersion,
		tables:  teamsProjectsTables,
		now:     s.now,
		// No seed: the synthesized Organization entity belongs to
		// ClickHouseProjectionSource's full snapshot and must be projected
		// exactly once per organization, not once per source.
	}.nextBatch(ctx, checkpoint)
}

// queryTeams projects the Team subject from `teams`
// (ReplacingMergeTree(updated_at) ORDER BY (org_id, id), so FINAL collapses
// cleanly to one row per team).
//
// The cursor reads updated_at, not last_synced: it is the finer of the two
// (DateTime64(6) against last_synced's whole seconds in live data) and it is
// the actual change time. Note updated_at carries NO timezone qualifier --
// live type is DateTime64(6), not DateTime64(6,'UTC') -- while
// sincePredicate binds {since:DateTime64(6,'UTC')}. That is fine and already
// precedented in this file's sibling producers: work_items.updated_at is
// DateTime64(3) and queryWorkItems has always compared it the same way.
// DateTime64's timezone is display metadata; the comparison is on ticks.
func queryTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "id"
	statement := `SELECT id, name, ifNull(description, ''), provider, ifNull(native_team_key, ''), is_active, updated_at
FROM teams FINAL
WHERE org_id = {org_id:String}` + sincePredicate(cursor, "updated_at", rowKey) + orderBy("updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var id, name, description, provider, nativeKey string
		var isActive uint8
		var observedAt time.Time
		if err := r.Scan(&id, &name, &description, &provider, &nativeKey, &isActive, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		label := name
		if label == "" {
			label = id
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(id), Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{"is_active": boolScalar(isActive != 0)}
		if description != "" && description != name {
			properties["description"] = stringScalar(description)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject: subject,
			// falkorgraph's entitySearchText is Label + Aliases +
			// PreviousNames, so these ARE the lexical handles: without
			// them a question naming a team by its key ("CHAOS",
			// "full.chaos") cannot resolve, because the key is not a word
			// inside the display name. Raw UUIDs are deliberately absent
			// -- teams.team_uuid is lexical noise, and exact identity
			// already resolves through ResolveDeps.ExactHint on
			// canonical_id.
			Aliases:        distinctNonEmpty(id, nativeKey),
			ProviderIDs:    providerID(provider, id),
			Properties:     properties,
			Authorization:  contractsv1.ContextFabricAuthorizationScope{TeamIDs: []string{id}},
			EvidenceRefIDs: []string{"acr:v1:team:" + id},
			ObservedAt:     observedAt,
			SourceVersion:  TeamsProjectsSourceVersion,
		}
		applyActiveValidity(&entity, isActive, observedAt)
		return []candidate{{observedAt: observedAt, sortKey: id, entity: &entity}}, nil
	})
}

// queryProjects projects the Project subject from `projects`
// (ReplacingMergeTree(updated_at) ORDER BY (org_id, provider, id)).
//
// Two live facts drive the shape here. First, projects.id is the ONLY id
// space work_items.project_id joins (16 of 18 distinct values resolve; 3080
// of 3086 rows), so the canonical id is projects.id verbatim -- including
// both id SHAPES that space contains, a provider-composite
// ("<org>:gitlab:71133891") and a bare Linear UUID. Deriving an id from
// project_key instead would strand every Linear project, which carries no
// project_key at all. Second, FINAL matters: the ground-truth org holds 56
// raw rows that collapse to 20.
//
// The dedup key includes provider, so projects.id is only unique PER PROVIDER
// in principle. Checked across every organization in live ClickHouse
// (2026-08-13): zero (org_id, id) pairs carry more than one provider. Two
// providers colliding on one id would project two entity candidates sharing a
// canonical id -- a last-write-wins node rather than a rejected batch, since
// ContextFabricProjectionBatch.Validate() enforces uniqueness for
// relationships/contents/episodes/tombstones but not entities. Accepted as a
// bounded, verified-absent risk rather than paid for with a GROUP BY that
// would complicate keyset pagination for no live benefit.
//
// On the reserved-namespace obligation: this IS the "future canonical project
// producer" teams_projects.go's old doc comment and organizationScopePrefix
// (clickhouse.go) both anticipated -- the first producer ever to populate real
// ProjectIDs, the same ContextFabricAuthorizationScope field the synthesized
// Organization entity uses for its organization-wide scope. That obligation is
// discharged here by NOT re-implementing it: since CHAOS-3753 finding W2 the
// check lives in the contract itself, and
// ContextFabricEntityProjection.Validate() rejects a non-organization entity
// whose ProjectIDs fall in the namespace ("authorization: project id falls
// inside the reserved organization-scope namespace"). That is strictly
// stronger than a producer-local guard, because it cannot be forgotten by the
// next producer. A duplicate guard here was written first and then removed:
// mutation-testing showed no test could tell whether it ran, because the
// contract rejected the row either way -- dead defensive code no guard holds.
// The rejection is a rejection, never a silent rename: a renamed project would
// look projected while being unjoinable to every work item referencing its
// real id.
func queryProjects(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "id"
	statement := `SELECT id, name, ifNull(project_key, ''), provider, state, url, is_active, updated_at
FROM projects FINAL
WHERE org_id = {org_id:String}` + sincePredicate(cursor, "updated_at", rowKey) + orderBy("updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var id, name, projectKey, provider, state, url string
		var isActive uint8
		var observedAt time.Time
		if err := r.Scan(&id, &name, &projectKey, &provider, &state, &url, &isActive, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		label := name
		if label == "" {
			label = id
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: projectCanonicalID(id), Label: label}
		properties := map[string]contractsv1.ContextFabricScalarValue{"is_active": boolScalar(isActive != 0)}
		if state != "" {
			properties["state"] = stringScalar(state)
		}
		if url != "" {
			properties["url"] = stringScalar(url)
		}
		entity := contractsv1.ContextFabricEntityProjection{
			Subject:        subject,
			Aliases:        distinctNonEmpty(projectKey),
			ProviderIDs:    providerID(provider, id),
			Properties:     properties,
			Authorization:  contractsv1.ContextFabricAuthorizationScope{ProjectIDs: []string{id}},
			EvidenceRefIDs: []string{"acr:v1:project:" + id},
			ObservedAt:     observedAt,
			SourceVersion:  TeamsProjectsSourceVersion,
		}
		applyActiveValidity(&entity, isActive, observedAt)
		return []candidate{{observedAt: observedAt, sortKey: id, entity: &entity}}, nil
	})
}

// teamCanonicalID is NOT a free choice (CHAOS-3802 §4). devhealthfacts minted
// teamPrefix = "team:" (workload.go) and its subjectIndex strips exactly that
// prefix to feed `team_id IN {ids:Array(String)}` against capacity_forecasts,
// investment_metrics_daily, estimate_coverage_metrics_daily and the team
// health tables -- whose team_id values were live-verified to be the teams.id
// space ({CHAOS} and {gl:full.chaos, CHAOS} respectively, both subsets of
// teams.id). Any other shape leaves all five existing team fact providers
// dark while still looking like a working projection.
func teamCanonicalID(teamID string) string { return "team:" + teamID }

// projectCanonicalID follows teamCanonicalID's convention. No prefix
// precedent existed for projects (nothing reads project-scoped facts), so
// this establishes one.
func projectCanonicalID(projectID string) string { return "project:" + projectID }

// applyActiveValidity states an owned entity's validity window explicitly in
// both directions -- the CHAOS-3785 R3-1 discipline. falkorgraph's OWNED
// write branch asserts valid_from/valid_to either way, so a nil here actively
// CLEARS a window some earlier referenced/stub write may have seeded, rather
// than leaving stale data stacked underneath the canonical row.
//
// Neither `teams` nor `projects` has a validity column; is_active is the only
// lifecycle signal either carries. An inactive row closes its window at
// updated_at instead of becoming a tombstone (CHAOS-3802 D3): is_active = 0
// is not a deletion -- the ground-truth org's inactive projects are
// state = "completed" -- and tombstoning would erase exactly the history the
// CHAOS-3781 temporal axis exists to answer over.
func applyActiveValidity(entity *contractsv1.ContextFabricEntityProjection, isActive uint8, observedAt time.Time) {
	if isActive != 0 {
		entity.ValidFrom, entity.ValidTo = nil, nil
		return
	}
	retiredAt := observedAt
	entity.ValidFrom, entity.ValidTo = nil, &retiredAt
}

func boolScalar(value bool) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{Boolean: &value}
}

func providerID(provider, id string) map[string]string {
	if provider == "" {
		return nil
	}
	return map[string]string{provider: id}
}

// distinctNonEmpty preserves argument order while dropping blanks and
// repeats, so a team whose id and native_team_key are the same string (every
// Linear team: both are "CHAOS") contributes one alias, not two.
func distinctNonEmpty(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
