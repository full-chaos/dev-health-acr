package devhealthsource

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
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
//
// v2 (CHAOS-3833, embed-text spec v2 §2/§4 Layer A): queryTeams emits the
// canonicalized project_keys property the team template composes. An
// already-projected organization's team nodes carry text without it, so
// the bump forces the same deliberate rebuild ClickHouseSourceVersion v5
// forces for its own producers -- one operator action covers both.
const TeamsProjectsSourceVersion = "devhealthsource.teams_projects.v2"

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
//
// devhealthschema:not-a-production-replica this is the PRODUCER REGISTRY for the CHAOS-3802
// subjects -- the same table-to-query pairing tables.go holds for the
// original producers. It mirrors no column type, engine or sort key, so it
// cannot drift from production the way a rival schema declaration would;
// devhealthschema stays the only physical source for these four tables.
//
// teamsProjectsTables is built per source rather than declared as a package
// var so the ownership producer can be handed a real logger (it omits
// ambiguous project keys and must say so) without a package-level global or
// shared mutable state -- the coordinator projects organizations
// concurrently, so a shared counter would be a race.
func teamsProjectsTables(omissions *ambiguityLedger) []entityTable {
	return []entityTable{
		{name: "teams", query: queryTeams},
		{name: "projects", query: queryProjects},
		{name: "work_items_projects", query: workItemProjectsQuery(omissions)},
		{name: "work_item_team_attributions", query: queryWorkItemTeams},
		{name: "team_project_ownership", query: projectTeamsQuery(omissions)},
	}
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
	logger  *slog.Logger

	// omissionsMu guards omissions, which accumulates ambiguity telemetry
	// per organization ACROSS the pages of a source run. The coordinator
	// projects organizations concurrently, so this is genuinely shared state.
	omissionsMu sync.Mutex
	omissions   map[string]*ambiguityLedger

	// consumedMu guards consumed, which memoises the furthest cursor a
	// NextProjectionBatch call proved holds nothing publishable, per
	// organization. ConsumedWithoutPublishing hands it to the worker.
	consumedMu sync.Mutex
	consumed   map[string]consumedProgress
}

// consumedProgress is a memo, never a source of truth. It records the cursor
// a call advanced to AND the checkpoint cursor it started from, so
// ConsumedWithoutPublishing can refuse a memo that does not belong to the
// checkpoint being asked about -- a stale memo must never move a cursor.
type consumedProgress struct{ from, to string }

// ConsumedWithoutPublishing implements contextfabric.ProjectionProgress.
//
// The memo comes from the immediately preceding NextProjectionBatch call for
// this organization, which is safe because the coordinator holds a
// single-flight advisory lock per organization -- the same guarantee
// ProjectionWorker.RunOnce's own source-version claim relies on. The
// from-cursor check makes that safety explicit rather than assumed: a memo
// recorded against a different checkpoint is discarded, so the worst case is
// a lost optimisation, never a skipped row.
//
// The memo is consumed on read. If the worker's CAS then fails, the next tick
// simply re-derives the same progress by re-reading the same rows.
func (s *TeamsProjectsSource) ConsumedWithoutPublishing(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ConsumedProgress, bool, error) {
	if s == nil || !s.enabled {
		return contextfabric.ConsumedProgress{}, false, nil
	}
	orgID := strings.TrimSpace(checkpoint.OrgID)
	s.consumedMu.Lock()
	defer s.consumedMu.Unlock()
	progress, ok := s.consumed[orgID]
	if !ok {
		return contextfabric.ConsumedProgress{}, false, nil
	}
	delete(s.consumed, orgID)
	if progress.from != checkpoint.Cursor || progress.to == "" || progress.to == checkpoint.Cursor {
		return contextfabric.ConsumedProgress{}, false, nil
	}
	return contextfabric.ConsumedProgress{NextCursor: progress.to, SourceVersion: TeamsProjectsSourceVersion}, true, nil
}

// forgetConsumed drops any memo for this organization.
//
// Called at the START of a from-scratch call and whenever a call returns a
// batch, which together enforce the memo's whole invariant: a memo may exist
// ONLY for a call that published nothing from the checkpoint it names.
//
// Both halves close real holes (CHAOS-3802, self-found then sharpened by codex
// round-4 F2). noteConsumed fires on the skip path, but a LATER iteration of
// the same paging loop can find payload and return a batch -- leaving behind a
// memo that claims "published nothing" about a call that published something.
// Separately, Coordinator.Rebuild purges the backend and resets the checkpoint
// to an empty cursor, and a surviving memo recorded at an empty cursor would
// then match the post-rebuild checkpoint exactly and skip a backfill the purge
// made mandatory.
//
// Clearing on from-scratch is what makes a rebuild safe without a new
// persisted discriminator: a reset is observable as Cursor == "", so the very
// next call drops the memo before any progress can be offered. A rebuild
// generation counter would cover the same hazard but needs a new durable
// checkpoint field -- schema, contract and migration -- for a condition
// existing state already marks unambiguously. The source-version binding
// (ConsumedProgress.SourceVersion) covers the orthogonal producer-identity
// hazard, so between them no window is left for one discriminator to close.
func (s *TeamsProjectsSource) forgetConsumed(orgID string) {
	s.consumedMu.Lock()
	defer s.consumedMu.Unlock()
	delete(s.consumed, strings.TrimSpace(orgID))
}

func (s *TeamsProjectsSource) recordConsumed(fromCursor string) func(orgID, cursor string) {
	return func(orgID, cursor string) {
		s.consumedMu.Lock()
		defer s.consumedMu.Unlock()
		if s.consumed == nil {
			s.consumed = map[string]consumedProgress{}
		}
		s.consumed[strings.TrimSpace(orgID)] = consumedProgress{from: fromCursor, to: cursor}
	}
}

// ambiguityLedger counts DISTINCT ambiguous (provider, project_key) keys
// whose ownership edges were omitted, accumulated over a source run rather
// than per page (CHAOS-3802 codex round-2 F3).
//
// Both properties were wrong before and both understated the signal. The
// count was keyed by team_id:source, so several distinct ambiguous keys
// collapsed into one; and it was page-scoped, so a multi-page catch-up
// reported each page's slice instead of the run's total. Understated
// omission telemetry reads as health -- measurement failing toward fine.
type ambiguityLedger struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func (l *ambiguityLedger) add(provider, projectKey string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.keys == nil {
		l.keys = map[string]struct{}{}
	}
	l.keys[provider+"\x00"+projectKey] = struct{}{}
}

func (l *ambiguityLedger) count() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}

// ledgerFor returns this organization's ledger, starting a fresh one when the
// run starts from scratch (an empty cursor -- a first projection or a
// post-rebuild reset). That is what bounds "a source run": totals accumulate
// across every page of one catch-up and reset when a new one begins, rather
// than growing forever in a long-lived process.
func (s *TeamsProjectsSource) ledgerFor(orgID string, fromScratch bool) *ambiguityLedger {
	s.omissionsMu.Lock()
	defer s.omissionsMu.Unlock()
	if s.omissions == nil {
		s.omissions = map[string]*ambiguityLedger{}
	}
	if fromScratch || s.omissions[orgID] == nil {
		s.omissions[orgID] = &ambiguityLedger{}
	}
	return s.omissions[orgID]
}

// NewTeamsProjectsSource returns a TeamsProjectsSource. enabled mirrors
// ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED; when false the source is
// a documented no-op rather than a partially-working adapter.
func NewTeamsProjectsSource(client contextpacket.ClickHouseQueryClient, enabled bool) (*TeamsProjectsSource, error) {
	if client == nil {
		return nil, fmt.Errorf("devhealthsource: clickhouse query client is required")
	}
	return &TeamsProjectsSource{client: client, enabled: enabled, now: time.Now, logger: slog.Default()}, nil
}

// WithLogger overrides the default logger, mirroring
// ClickHouseProjectionSource.WithLogger: only cmd/acr-projector wires a real
// one, and a nil logger is a no-op rather than a panic.
func (s *TeamsProjectsSource) WithLogger(logger *slog.Logger) *TeamsProjectsSource {
	if logger != nil {
		s.logger = logger
	}
	return s
}

// CurrentProjectionSourceVersion implements contextfabric.ProjectionSourceVersion
// (CHAOS-3887) so a per-tick freshness signal can be computed for a dormant
// organization even when NextProjectionBatch reports available=false (no new
// rows, or the source is disabled) and never builds a batch to read a
// current SourceVersion from.
func (s *TeamsProjectsSource) CurrentProjectionSourceVersion() string {
	return TeamsProjectsSourceVersion
}

// Enabled implements contextfabric.ProjectionSourceEnablement (CHAOS-3898
// S2a-2): reports the SAME enabled flag NextProjectionBatch already gates
// on, so a build-completion classifier can name a disabled source
// "disabled_at_freeze" rather than conflating it with a genuinely empty one.
func (s *TeamsProjectsSource) Enabled() bool {
	return s != nil && s.enabled
}

func (s *TeamsProjectsSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if s == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: teams/projects source is not configured")
	}
	if !s.enabled {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	fromScratch := checkpoint.Cursor == ""
	if fromScratch {
		// A reset (first run, or what Coordinator.Rebuild leaves behind)
		// invalidates any memo: it was derived from a cursor space that no
		// longer describes what still needs projecting.
		s.forgetConsumed(checkpoint.OrgID)
	}
	ledger := s.ledgerFor(strings.TrimSpace(checkpoint.OrgID), fromScratch)
	defer logAmbiguousProjectKeys(ctx, s.logger, checkpoint.OrgID, ledger.count())
	return sourcePlan{
		client:         s.client,
		source:         TeamsProjectsSourceName,
		version:        TeamsProjectsSourceVersion,
		tables:         teamsProjectsTables(ledger),
		now:            s.now,
		recordConsumed: s.recordConsumed(checkpoint.Cursor),
		dropConsumed:   s.forgetConsumed,
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
	statement := `SELECT id, name, ifNull(description, ''), provider, ifNull(native_team_key, ''), is_active, updated_at, project_keys
FROM teams FINAL
WHERE org_id = {org_id:String}` + sincePredicate(cursor, "updated_at", rowKey) + orderBy("updated_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var id, name, description, provider, nativeKey string
		var projectKeys []string
		var isActive uint8
		var observedAt time.Time
		if err := r.Scan(&id, &name, &description, &provider, &nativeKey, &isActive, &observedAt, &projectKeys); err != nil {
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
		// CHAOS-3833 (embed-text spec §2): the team template's
		// project_keys line, canonicalized at the producer (sorted,
		// deduplicated, capped, joined) so an unordered source array never
		// yields two different texts for the same row.
		setStringProperty(properties, "project_keys", joinedSortedList(projectKeys, 10, 80, ", "), 0)
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
	// CHAOS-3898 D-5: repo_id-shaped precedent -- provider qualifies the
	// pagination rowKey too, so a hypothetical future cross-provider id
	// collision (none live today, per this function's own doc comment)
	// cannot tie at the SQL keyset-pagination tiebreaker the way D-6
	// documents for the other four kinds.
	const rowKey = "concat(provider, ':', id)"
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
		authorization, err := projectAuthorizationScope(id)
		if err != nil {
			return nil, err
		}
		canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
		if err != nil {
			return nil, err
		}
		rowSortKey := provider + ":" + id
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		label := name
		if label == "" {
			label = id
		}
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: canonicalID, Label: label}
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
			Authorization:  authorization,
			EvidenceRefIDs: []string{"acr:v1:project:" + provider + ":" + id},
			ObservedAt:     observedAt,
			SourceVersion:  TeamsProjectsSourceVersion,
		}
		applyActiveValidity(&entity, isActive, observedAt)
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, entity: &entity}}, nil
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

// projectAuthorizationScope builds a project's authorization scope and is the
// producer-side half of the reserved-namespace obligation
// organizationScopePrefix (clickhouse.go) has carried since CHAOS-3753.
// queryProjects is the first producer ever to populate real ProjectIDs -- the
// same ContextFabricAuthorizationScope field the synthesized Organization
// entity uses for its organization-wide scope -- so a project id equal to that
// reserved namespace would silently inherit organization-wide authorization.
//
// The contract enforces this too (ContextFabricEntityProjection.Validate),
// and that backstop is the stronger of the two because it cannot be forgotten
// by a future producer. This guard is not redundant with it: it refuses the
// row at the producer, before a batch is built, so the failure is attributable
// to this table and this id instead of surfacing as a whole-batch validation
// error. A rejection, never a silent rename -- a renamed project would look
// projected while being unjoinable to every work item referencing its real id.
//
// An earlier version of this guard was written inline and then removed after
// mutation-testing showed no test could tell whether it ran, because the
// contract rejected the row either way. The lesson taken was the wrong one:
// the fix is a seam the guard can be tested through
// (ProjectAuthorizationScopeForTest), not deletion of the guard.
func projectAuthorizationScope(projectID string) (contractsv1.ContextFabricAuthorizationScope, error) {
	if IsReservedAuthorizationScopeID(projectID) {
		return contractsv1.ContextFabricAuthorizationScope{}, &ProducerRejection{Reason: "project id falls inside the reserved organization-scope namespace"}
	}
	return contractsv1.ContextFabricAuthorizationScope{ProjectIDs: []string{projectID}}, nil
}

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

// TeamsProjectsSource is the only source in this package that can consume
// unpublishable rows, so it is the only one implementing the optional
// progress capability.
var _ contextfabric.ProjectionProgress = (*TeamsProjectsSource)(nil)
