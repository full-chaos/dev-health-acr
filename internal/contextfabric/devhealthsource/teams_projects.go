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
//
// v3 (CHAOS-3916, local/trial slice; codex xhigh review finding on that
// ticket's own PR, confirmed): CHAOS-3898 rewired queryProjects onto
// identity.Derive (project.v2:<provider>:<id> canonical ids, teams_projects.go)
// and CHAOS-4108 widened queryWorkItemProjects' join
// (teams_projects_edges.go) -- BOTH producers this source, not
// ClickHouseProjectionSource, owns -- WITHOUT ever bumping this constant.
// projectionrun checkpoints are keyed (org_id, source) and compared
// independently per source (ProjectionCheckpointStore.LoadProjectionCheckpoint),
// so CHAOS-3916's own ClickHouseSourceVersion v5->v6 bump does NOTHING for
// an already-projected organization's teams/projects checkpoint: it would
// keep advancing incrementally under a stale "v2" marker forever, never
// re-deriving already-committed project/work-item-to-project rows with
// either identity change, exactly the mixed-format risk CHAOS-3916 exists
// to close. This bump is the SAME deliberate-rebuild discipline as every
// entry above, on the source that actually needed it for these two
// changes. Live-verified AT THE TIME (CHAOS-4108's own re-projection
// decision matrix, standing stack, ground-truth org 70d529e0): zero
// project.v2:-shaped nodes existed anywhere in that org's graph as of this
// bump. STALE as of CHAOS-4348: the org's graph now carries real project/
// team nodes (rebuilt under this and later source-version bumps below) --
// see docs/design/context-fabric-team-project-subjects.md §10 for what
// CHAOS-4348 found projection alone does NOT fix (retrieval reachability).
//
// v4 (CHAOS-4193): the work_item -> project edge's READ SHAPE changed --
// querySubjectProjectMemberships (formerly queryWorkItemProjects,
// teams_projects_edges.go) now reads project_membership_presence instead of
// work_items.project_id directly, and additionally projects pull_request ->
// project edges the old source could never see at all. Same deliberate-
// rebuild discipline as v2/v3: an already-projected organization's
// BELONGS_TO_PROJECT edges carry the OLD RelationshipID shape (no
// pull_request_project edges, no ValidFrom on any of them, and a gitlab
// work item's edge -- sourced from work_items.project_id directly before
// this version -- can no longer be re-derived at all, since
// project_membership_presence's column arm deliberately excludes gitlab,
// Context Fabric ruling 2026-08-24 09:55: "gitlab is not registered for
// this kind at all -- GitLab's own 'project' concept IS this schema's
// repo_id"). Incrementally advancing under the stale v3 marker would leave
// every already-projected organization holding edges neither shape fully
// describes; a full rebuild is what makes every edge unambiguously v4-shaped.
//
// v5 (CHAOS-4109): querySubjectProjectMemberships' transition arm now reads
// project_membership_transitions' FULL touch history directly
// (membershipIntervalsCTE, teams_projects_edges.go) instead of
// project_membership_presence's transition arm, which reported only the
// LATEST touch. A transition-sourced edge's RelationshipID now carries a
// ValidFrom suffix so multiple intervals for the same (subject, project)
// pair coexist as distinct graph edges, and a subject with an
// add-remove-re-add history now projects a CLOSED historical edge alongside
// its current OPEN one, where v4 projected only the open one. Same
// deliberate-rebuild discipline as v2/v3/v4: an already-projected
// organization's transition-sourced edges carry the OLD bare RelationshipID
// (no interval suffix) and would collide with, rather than coexist beside,
// a newly-derived closed interval for the same pair under incremental
// catch-up.
// v6 (CHAOS-4109 fast-follow, codex xhigh review HIGH finding, confirmed
// real): the interval rule membershipIntervalsSubquery derives changed --
// a duplicate ADD (immediately preceded by another ADD, no REMOVE between)
// no longer mints its own edge at all; it collapses into the FIRST add's
// interval (team-lead ruling 2026-08-25). An organization already
// projected under v5 that has any such sequence is still holding the OLD
// v5 shape: a spurious second edge, keyed by the duplicate touch's own
// RelationshipID, that this fix's code no longer emits and the checkpoint
// guard has no other reason to remove -- it only rebuilds on a version
// mismatch, and incremental catch-up under a stale v5 marker never
// revisits an already-committed interval. Same deliberate-rebuild
// discipline as v2-v5: a full rebuild is what retracts the now-invalid
// duplicate edge everywhere it was already projected.
//
// v7 (CHAOS-4390): queryTeams now derives Authorization.RepositorySlugs/
// ProjectIDs from the team's CURRENT ownership rows (team_repo_ownership,
// team_project_ownership -- CHAOS-4321: ownership only, never membership),
// instead of leaving both empty. An empty AuthorizationScope list is the
// shared "*"/unrestricted convention (falkorgraph's authorizationValue,
// graphrank/authorize.go's doc comment) -- correct for a genuinely
// unscoped subject, but a team's real repo/project ownership is knowable
// and this producer simply never read it, so every team in an
// already-projected organization carries the wildcard regardless of what
// it actually owns: any repository-scoped principal can see every team in
// the org, not just the ones that own something in scope. Same
// deliberate-rebuild discipline as v2-v6: an already-projected
// organization's team nodes carry the OLD wildcard authorization_repositories/
// authorization_projects attributes, and incremental catch-up under a
// stale v6 marker never revisits an already-committed team row (its own
// `teams.updated_at` did not change), so only a full rebuild replaces the
// wildcard with the real scope everywhere it was already projected.
//
// v8 (CHAOS-4542): queryProjectTeams (teams_projects_edges.go) now resolves
// team<->project ownership on PROJECT IDENTITY -- project_id, with the
// project_key match kept as a second arm -- instead of on project_key
// alone. CHAOS-4530 nulls project_key on the UUID-keyed ownership rows and
// leaves a real Linear project's projects.project_key nil by design, so the
// old key-only join matches NOTHING for Linear the moment 4530 deploys.
// Same deliberate-rebuild discipline as v2-v7, and for the sharpest reason
// yet: the OLD edges are not merely stale, they are the only edges an
// already-projected organization has, and incremental catch-up under a
// stale v7 marker never revisits an already-committed ownership interval
// (team_project_ownership.updated_at does not move just because this
// producer's join did). Without this bump the fix would land, deploy, and
// change nothing for every organization already projected -- the exact
// silent no-op the v3 entry above exists to remember.
const TeamsProjectsSourceVersion = "devhealthsource.teams_projects.v8"

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
func teamsProjectsTables(omissions *ambiguityLedger, presence *presenceTelemetryLedger, teamAuth *teamAuthorizationLedger) []entityTable {
	return []entityTable{
		{name: "teams", query: teamsQuery(teamAuth)},
		{name: "projects", query: queryProjects},
		{name: "project_membership_presence", query: subjectProjectMembershipsQuery(presence)},
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

	// presenceMu guards presence, CHAOS-4193's own run-scoped telemetry for
	// the project_membership_presence read (rows read by source/subject_kind,
	// rows dropped for an unresolved or ambiguous (provider, project_id)) --
	// same per-organization, accumulate-across-pages discipline as omissions.
	presenceMu sync.Mutex
	presence   map[string]*presenceTelemetryLedger

	// teamAuthMu guards teamAuth, CHAOS-4390's own run-scoped telemetry for
	// how many teams got a real ownership-derived authorization_repositories
	// scope versus the shared wildcard fallback -- same per-organization,
	// accumulate-across-pages discipline as omissions/presence.
	teamAuthMu sync.Mutex
	teamAuth   map[string]*teamAuthorizationLedger

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
	mu        sync.Mutex
	keys      map[string]struct{}
	conflicts map[string]struct{}
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

// addConflict records an ownership row whose project_id and project_key
// resolve to DIFFERENT projects. Kept separate from the ambiguous-key set
// because the two are different operator questions: an ambiguous key means
// "this key names several projects", a conflicting identity means "this ROW
// names two projects and disagrees with itself". Collapsing them into one
// number would tell an operator that something was dropped without saying
// which kind of wrong the data is.
func (l *ambiguityLedger) addConflict(provider, rowKey string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conflicts == nil {
		l.conflicts = map[string]struct{}{}
	}
	l.conflicts[provider+"\x00"+rowKey] = struct{}{}
}

func (l *ambiguityLedger) conflictCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conflicts)
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

// teamAuthorizationLedger (CHAOS-4390) counts, over one source run, how many
// team rows queryTeams scanned were ADMITTED a real, ownership-derived
// authorization_repositories scope (admitted > 0 owned repositories) versus
// how many were DENIED one -- noTeamOwnershipSentinel's doc comment below --
// because this organization's team_repo_ownership carries no CURRENT row
// for that team (denied). Diagnosability from the run's own artifacts
// (AGENTS.md telemetry-same-change rule): without this, an organization
// where every team is denied -- because it genuinely has no ownership data
// yet (the CHAOS-4365 gap), not because of a code defect -- is
// indistinguishable from one where the join itself silently stopped
// matching. Same run-scoped-not-page-scoped discipline as ambiguityLedger
// (CHAOS-3802 codex round-2 F3).
type teamAuthorizationLedger struct {
	mu       sync.Mutex
	admitted int
	denied   int
}

func (l *teamAuthorizationLedger) record(hasOwnedRepositories bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if hasOwnedRepositories {
		l.admitted++
	} else {
		l.denied++
	}
}

func (l *teamAuthorizationLedger) counts() (admitted, denied int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.admitted, l.denied
}

// teamAuthLedgerFor mirrors ledgerFor exactly, for the team-authorization
// telemetry ledger instead of the ownership-omission one.
func (s *TeamsProjectsSource) teamAuthLedgerFor(orgID string, fromScratch bool) *teamAuthorizationLedger {
	s.teamAuthMu.Lock()
	defer s.teamAuthMu.Unlock()
	if s.teamAuth == nil {
		s.teamAuth = map[string]*teamAuthorizationLedger{}
	}
	if fromScratch || s.teamAuth[orgID] == nil {
		s.teamAuth[orgID] = &teamAuthorizationLedger{}
	}
	return s.teamAuth[orgID]
}

// logTeamAuthorizationTelemetry surfaces the admitted-versus-denied-by-
// ownership split once per batch, unconditionally (CHAOS-4193's
// logPresenceTelemetry convention: a zero split is itself informative, e.g.
// an organization with zero admitted denies every team and that should be
// visible, not silently absent). Carries counts and the org id only --
// never a team id or name.
func logTeamAuthorizationTelemetry(ctx context.Context, logger *slog.Logger, orgID string, ledger *teamAuthorizationLedger) {
	if logger == nil {
		return
	}
	// Codex round-1 finding: truly unconditional, matching this doc
	// comment's own claim -- a zero/zero run (this tick touched no team
	// rows at all, e.g. an org with no teams, or a page with none) is
	// itself informative and must not read as "telemetry never ran".
	admitted, denied := ledger.counts()
	logger.InfoContext(ctx, "devhealthsource team authorization scoped by ownership",
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
		"teams_admitted_by_ownership", admitted,
		"teams_denied_no_ownership_data", denied)
}

// presenceTelemetryLedger accumulates CHAOS-4193's own read-shape telemetry
// for one source run: how many project_membership_presence rows this
// producer read, broken down by (source, subject_kind), and how many were
// dropped because (provider, project_id) did not resolve to EXACTLY ONE
// `projects` row. The two failure shapes are counted separately --
// "unresolved" (zero matches) and "ambiguous" (more than one) -- rather
// than folded into one number, because an operator reading this run's log
// needs to tell "nothing ever wrote this project" from "two projects claim
// the same id" apart; conflating them is exactly the kind of measurement
// that reads as one health signal while hiding two different causes.
//
// Same run-scoped-not-page-scoped discipline as ambiguityLedger (CHAOS-3802
// codex round-2 F3): understated telemetry reads as health.
//
// unresolved/ambiguous carry BOTH a distinct-key set and a plain row count
// (codex xhigh review R1, Medium): the set alone answers "how many DIFFERENT
// bad (provider, project_id) values did this run hit", which is what an
// operator acts on, but it silently equals 1 whether that one bad value
// dropped a single row or fanned out across thousands -- exactly the
// "understated telemetry reads as health" failure this ledger's own sibling
// (ambiguityLedger) was built to close, reached from the other direction.
// The two numbers answer different questions and both are logged.
type presenceTelemetryLedger struct {
	mu             sync.Mutex
	reads          map[string]int      // "source\x00subject_kind" -> rows read
	unresolved     map[string]struct{} // distinct "provider\x00project_id" with zero projects matches
	unresolvedRows int                 // total ROWS dropped for an unresolved key (can exceed len(unresolved))
	ambiguous      map[string]struct{} // distinct "provider\x00project_id" with >1 projects matches
	ambiguousRows  int                 // total ROWS dropped for an ambiguous key (can exceed len(ambiguous))
	// malformedTouch/malformedTouchRows (CHAOS-4109) count
	// membershipIntervalsSubquery's is_malformed rows -- a REMOVE touch with
	// no prior ADD to close (either the very first tracked touch of a
	// (subject, project) pair, or a second REMOVE with nothing reopened in
	// between; see that subquery's own doc comment). Same distinct-key/
	// row-count split as unresolved/ambiguous above, for the same reason:
	// one bad touch sequence can recur across many re-syncs of the same
	// pair.
	malformedTouch     map[string]struct{} // distinct "provider\x00project_id" with a dangling-REMOVE touch
	malformedTouchRows int                 // total ROWS skipped for a dangling-REMOVE touch
	// duplicateAdd/duplicateAddRows (CHAOS-4109, team-lead ruling
	// 2026-08-25) count membershipIntervalsSubquery's is_duplicate_add rows
	// -- an ADD touch immediately preceded by another ADD, no REMOVE
	// between. NOT malformed: #1896's discard-and-replay path and an
	// ordinary re-sync of an unchanged board membership both legitimately
	// produce this for a subject that never left, so it is a continuation
	// of the already-open interval (attribution keeps working, unlike a
	// malformed touch) -- but still counted, never silently absorbed.
	duplicateAdd     map[string]struct{} // distinct "provider\x00project_id" with a duplicate-ADD touch
	duplicateAddRows int                 // total ROWS collapsed for a duplicate-ADD touch
}

func (l *presenceTelemetryLedger) recordRead(source, subjectKind string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reads == nil {
		l.reads = map[string]int{}
	}
	l.reads[source+"\x00"+subjectKind]++
}

func (l *presenceTelemetryLedger) recordUnresolved(provider, projectID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unresolved == nil {
		l.unresolved = map[string]struct{}{}
	}
	l.unresolved[provider+"\x00"+projectID] = struct{}{}
	l.unresolvedRows++
}

func (l *presenceTelemetryLedger) recordAmbiguous(provider, projectID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ambiguous == nil {
		l.ambiguous = map[string]struct{}{}
	}
	l.ambiguous[provider+"\x00"+projectID] = struct{}{}
	l.ambiguousRows++
}

func (l *presenceTelemetryLedger) recordMalformedTouch(provider, projectID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.malformedTouch == nil {
		l.malformedTouch = map[string]struct{}{}
	}
	l.malformedTouch[provider+"\x00"+projectID] = struct{}{}
	l.malformedTouchRows++
}

func (l *presenceTelemetryLedger) recordDuplicateAdd(provider, projectID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.duplicateAdd == nil {
		l.duplicateAdd = map[string]struct{}{}
	}
	l.duplicateAdd[provider+"\x00"+projectID] = struct{}{}
	l.duplicateAddRows++
}

// presenceReadCount reports how many rows this run read for one (source,
// subjectKind) pair -- zero if the ledger is nil or that combination was
// never seen, never an error, so a caller can enumerate the full closed
// vocabulary unconditionally.
func (l *presenceTelemetryLedger) presenceReadCount(source, subjectKind string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reads[source+"\x00"+subjectKind]
}

// unresolvedCount is the distinct-key count -- how many DIFFERENT
// (provider, project_id) values failed to resolve.
func (l *presenceTelemetryLedger) unresolvedCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.unresolved)
}

// unresolvedRowCount is the total ROW count -- how many presence rows were
// dropped, which can exceed unresolvedCount when one bad key fans out
// across many rows (codex xhigh review R1, Medium: the distinct-key count
// alone reports "1" whether that key dropped one row or thousands).
func (l *presenceTelemetryLedger) unresolvedRowCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unresolvedRows
}

func (l *presenceTelemetryLedger) ambiguousCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ambiguous)
}

func (l *presenceTelemetryLedger) ambiguousRowCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ambiguousRows
}

func (l *presenceTelemetryLedger) malformedTouchCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.malformedTouch)
}

func (l *presenceTelemetryLedger) malformedTouchRowCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.malformedTouchRows
}

func (l *presenceTelemetryLedger) duplicateAddCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.duplicateAdd)
}

func (l *presenceTelemetryLedger) duplicateAddRowCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.duplicateAddRows
}

// presenceLedgerFor mirrors ledgerFor exactly, for the presence telemetry
// ledger instead of the ownership-omission one.
func (s *TeamsProjectsSource) presenceLedgerFor(orgID string, fromScratch bool) *presenceTelemetryLedger {
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	if s.presence == nil {
		s.presence = map[string]*presenceTelemetryLedger{}
	}
	if fromScratch || s.presence[orgID] == nil {
		s.presence[orgID] = &presenceTelemetryLedger{}
	}
	return s.presence[orgID]
}

// logPresenceTelemetry surfaces the presence read shape once per batch --
// CHAOS-4193's own telemetry-same-change requirement (diagnosability from
// the run's own artifacts). The read-count line is unconditional (a
// zero-row combination is itself informative: e.g. work_item_column
// carrying a pull_request row would violate the view's own invariant and
// must be visible, not silently absent from the log); the drop line only
// fires when there is something to report, matching logAmbiguousProjectKeys'
// own convention. Neither line carries a project id, key, or org id --
// counts and the closed source/subject_kind vocabulary only.
func logPresenceTelemetry(ctx context.Context, logger *slog.Logger, orgID string, ledger *presenceTelemetryLedger) {
	if logger == nil {
		return
	}
	logger.InfoContext(ctx, "devhealthsource read project_membership_presence",
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
		"presence_rows_transition_work_item", ledger.presenceReadCount("transition", "work_item"),
		"presence_rows_transition_pull_request", ledger.presenceReadCount("transition", "pull_request"),
		"presence_rows_work_item_column_work_item", ledger.presenceReadCount("work_item_column", "work_item"),
		"presence_rows_work_item_column_pull_request", ledger.presenceReadCount("work_item_column", "pull_request"))
	unresolved, unresolvedRows := ledger.unresolvedCount(), ledger.unresolvedRowCount()
	ambiguous, ambiguousRows := ledger.ambiguousCount(), ledger.ambiguousRowCount()
	if unresolved > 0 || ambiguous > 0 {
		// Both the distinct-key count and the row count are logged (codex
		// xhigh review R1, Medium): the key count is what an operator
		// investigates (how many DIFFERENT bad ids), the row count is the
		// blast radius (one bad id can drop one row or thousands) -- the
		// key count alone cannot tell those apart.
		logger.WarnContext(ctx, "devhealthsource dropped project membership edges for an unresolved (provider, project_id)",
			"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
			"reason", "(provider, project_id) did not resolve to exactly one projects row",
			"unresolved_project_entity", unresolved, "unresolved_project_entity_rows", unresolvedRows,
			"ambiguous_project_entity", ambiguous, "ambiguous_project_entity_rows", ambiguousRows)
	}
	// CHAOS-4109: malformed touch sequences are a DIFFERENT failure mode
	// from unresolved/ambiguous above (a project-identity join miss) --
	// this one is a data-quality anomaly in project_membership_transitions'
	// own touch history (a REMOVE with no prior ADD to close, see
	// membershipIntervalsSubquery) -- so it gets its own line rather than
	// folding into the warning above and reading as the same cause.
	if malformed, malformedRows := ledger.malformedTouchCount(), ledger.malformedTouchRowCount(); malformed > 0 {
		logger.WarnContext(ctx, "devhealthsource skipped a malformed project membership touch sequence",
			"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
			"reason", "a REMOVE touch with no prior ADD to close (no open interval, nothing reopened)",
			"malformed_touch_entity", malformed, "malformed_touch_entity_rows", malformedRows)
	}
	// CHAOS-4109 (team-lead ruling 2026-08-25): a duplicate ADD is NOT the
	// malformed case above -- it is a legitimate continuation this producer
	// keeps attributing through -- so it gets its own INFO line (not WARN:
	// nothing is missing or wrong, this is diagnostic volume only).
	if duplicate, duplicateRows := ledger.duplicateAddCount(), ledger.duplicateAddRowCount(); duplicate > 0 {
		logger.InfoContext(ctx, "devhealthsource collapsed a duplicate project membership ADD touch",
			"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
			"reason", "an ADD touch immediately preceded by another ADD, no intervening REMOVE -- treated as a continuation of the already-open interval",
			"duplicate_add_entity", duplicate, "duplicate_add_entity_rows", duplicateRows)
	}
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
	// The closure is load-bearing. `defer logAmbiguousProjectKeys(..., ledger.count())`
	// evaluates count() HERE, before any query runs -- always 0, and a zero
	// is suppressed -- so the telemetry reported the PREVIOUS call's total
	// and a single-call run said nothing at all. The two sibling defers
	// below pass their ledger POINTER, so they read it when the deferred
	// call runs; this one passed an int and did not.
	defer func() { logAmbiguousProjectKeys(ctx, s.logger, checkpoint.OrgID, ledger.count()) }()
	defer func() { logConflictingIdentities(ctx, s.logger, checkpoint.OrgID, ledger.conflictCount()) }()
	// Once per call, not once per page: the census is organization-scoped and
	// returns identical rows every page, and the paging loop can iterate many
	// times over fully-omitted pages. It runs BEFORE the read because the rows
	// it describes are precisely the ones no page will contain -- there is
	// nothing in a result set to infer them from.
	if err := recordAmbiguousProjectKeys(ctx, s.client, s.logger, strings.TrimSpace(checkpoint.OrgID), ledger); err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	presence := s.presenceLedgerFor(strings.TrimSpace(checkpoint.OrgID), fromScratch)
	defer logPresenceTelemetry(ctx, s.logger, checkpoint.OrgID, presence)
	teamAuth := s.teamAuthLedgerFor(strings.TrimSpace(checkpoint.OrgID), fromScratch)
	defer logTeamAuthorizationTelemetry(ctx, s.logger, checkpoint.OrgID, teamAuth)
	return sourcePlan{
		client:         s.client,
		source:         TeamsProjectsSourceName,
		version:        TeamsProjectsSourceVersion,
		tables:         teamsProjectsTables(ledger, presence, teamAuth),
		logger:         s.logger,
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
// ownedRepositoriesJoinSQL is queryTeams' LEFT JOIN fragment aggregating
// each team's CURRENT repository ownership (team_repo_ownership, CHAOS-4321:
// ownership only, never team_memberships) into one groupUniqArray per
// team_id, aliased "tro" with a "repos" column plus a "latest_update" column
// (queryTeamsEffectiveUpdatedAtExpr's own doc comment explains the second
// one). "Currently owned" mirrors devhealthfacts/shared.go's
// ownershipValidityPredicate non-timebound arm (valid_from <= now64(3)):
// authorization reflects present-tense visibility, never a historical
// ownership window, so a closed/superseded ownership row must not leave a
// repository in a team's authorization_repositories list.
//
// Codex round-1 finding (HIGH): a bare `WHERE valid_to IS NULL` after FINAL
// is not enough to express "currently owned". team_repo_ownership's
// ReplacingMergeTree key is (org_id, provider, repo_full_name, team_id,
// source, valid_from) -- valid_from is PART of the dedup key, so FINAL
// keeps BOTH an older OPEN assertion (valid_from=t1, valid_to=NULL) and a
// LATER assertion that actually closed it (valid_from=t2>t1,
// valid_to=<past>) as two distinct rows for the identical (team, repo,
// source): they differ only in valid_from. A naive `valid_to IS NULL`
// filter would still surface the repository via the stale open row even
// though the org's LATEST assertion revoked it. The inner subquery below
// resolves this exactly the way queryProjectTeams' own FIRST/SECOND finding
// notes already established for team_project_ownership: collapse to ONE
// row per (team_id, repo_full_name) using the SAME NULL-preserving
// "latest assertion by (valid_from, valid_to IS NULL, valid_to) wins" rule
// -- verified live against this ClickHouse version there, not re-derived
// here -- and only keep the repository when THAT latest assertion is open.
// Collapsed across `source` too (unlike queryProjectTeams' edge, which
// keeps one edge per source): this producer only needs a flat "is the team
// currently authorized for this repository at all" boolean, not a
// per-source relationship identity, so the most recent assertion from ANY
// source is authoritative for that boolean, same as queryTeams' own FINAL
// collapse of the teams table itself never splits by anything narrower
// than the team's own row.
//
// Codex round-2 finding (HIGH): the outer aggregation used to filter to
// `WHERE latest_is_open` BEFORE computing `latest_update`, so a repository
// whose ownership just got REVOKED (latest_is_open flips to false) drops
// out of the aggregation entirely -- taking its own updated_at with it.
// If that revoked repository held the team's highest ownership timestamp,
// `latest_update` regresses to an OLDER value (or NULL, falling back to
// the epoch), which can be LOWER than what an earlier page already
// consumed -- sincePredicate's strict `>` then skips the team forever, and
// the revocation itself is what never gets picked up: exactly the stale-
// authorization failure mode this join exists to close, now triggered by
// a revocation instead of a grant. `groupUniqArrayIf(repo_full_name,
// latest_is_open)` fixes this: the repository LIST is still filtered to
// open-only, but `max(updated_at)` is computed over EVERY row in the
// group -- open and closed alike -- so the watermark advances on a
// revocation exactly as reliably as it does on a grant.
//
// Codex C5 (sincePredicate's own doc comment): every column this join's
// caller selects from `teams` MUST be qualified (tm.id, tm.updated_at, ...)
// -- team_repo_ownership carries its own updated_at, and an unqualified
// reference would silently paginate on the wrong table's column the moment
// this join exists.
// noTeamOwnershipSentinel (CHAOS-4390) is the RepositorySlugs value queryTeams
// uses when this organization's team_repo_ownership carries no CURRENT row
// for a team. Deliberately NOT a bare empty list: falkorgraph's shared
// authorizationValue convention (projection.go, unchanged since the
// FalkorDB adapter's original commit dc88590b) turns an empty
// RepositorySlugs into the literal string "*", which authorizes
// UNCONDITIONALLY for any repository-scoped principal (scopeContainsAttr's
// wildcard branch, authorize.go) -- the exact over-exposure CHAOS-4390
// exists to close: every team in the org was visible to every repo-scoped
// principal, regardless of whether that team owns anything in scope. This
// sentinel forces the ordinary, correct outcome instead: a team with no
// recorded ownership is DENIED to a repository-scoped principal (fail
// closed) until real ownership data exists for it, same sentinel
// discipline as noRepositorySentinel/orphanedRepositorySentinel
// (clickhouse.go) -- a DIFFERENT signal (an ownership table with no
// CURRENT row for this team, not a work item's own repo_id column), so
// deliberately its own distinct literal rather than collapsing into
// either of those. Cannot collide with a real "owner/repo" slug (always
// contains '/') or with either work-item sentinel.
const noTeamOwnershipSentinel = "acr-context-fabric:no-team-repository-ownership"

const ownedRepositoriesJoinSQL = `LEFT JOIN (
	SELECT team_id, groupUniqArrayIf(repo_full_name, latest_is_open) AS repos, max(updated_at) AS latest_update
	FROM (
		SELECT team_id, repo_full_name, max(updated_at) AS updated_at,
			argMax(tuple(valid_to), (valid_from, valid_to IS NULL, ifNull(valid_to, toDateTime64(0, 3, 'UTC')))).1 IS NULL AS latest_is_open
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String} AND valid_from <= now64(3)
		GROUP BY team_id, repo_full_name
	)
	GROUP BY team_id
) AS tro ON tro.team_id = tm.id`

// queryTeamsEffectiveUpdatedAtExpr is the SQL expression queryTeams uses as
// BOTH its cursor/pagination watermark (sincePredicate/orderBy) and its
// scanned "updated_at" column (which becomes candidate.observedAt and
// entity.ObservedAt). Codex round-1 finding (HIGH): pre-fix, the watermark
// was bare `tm.updated_at` -- a grant or revocation that touches ONLY
// team_repo_ownership (the team's own `teams` row untouched) would never
// advance the team's own last-changed timestamp, so incremental catch-up
// (sincePredicate's `WHERE updated_at > {since}`) would never re-select
// that team row again, and the graph would keep serving a STALE
// authorization scope -- either a revoked repository still authorized, or
// a newly granted one never surfacing -- until some UNRELATED team change
// or a full rebuild happened to touch it. `greatest()` over both tables'
// own `updated_at` values makes the watermark advance whenever EITHER
// side changes, the same shared-watermark discipline `queryProjectTeams`
// already applies at the edge level via its own `max(updated_at)`. The
// `ifNull` fallback is a fixed epoch, never `tm.updated_at` again, so the
// expression has no circular self-reference.
const queryTeamsEffectiveUpdatedAtExpr = `greatest(tm.updated_at, ifNull(tro.latest_update, toDateTime64(0, 3, 'UTC')))`

// teamsQuery binds the run-scoped team-authorization telemetry ledger to
// queryTeams, the same closure-constructor shape projectTeamsQuery already
// uses for the ownership-omission ledger.
func teamsQuery(ledger *teamAuthorizationLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return queryTeams(ctx, client, orgID, cursor, limit, ledger)
	}
}

func queryTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, teamAuth *teamAuthorizationLedger) ([]candidate, bool, error) {
	const rowKey = "tm.id"
	statement := `SELECT tm.id, tm.name, ifNull(tm.description, ''), tm.provider, ifNull(tm.native_team_key, ''), tm.is_active, ` + queryTeamsEffectiveUpdatedAtExpr + `, tm.project_keys, ifNull(tro.repos, [])
FROM teams AS tm FINAL
` + ownedRepositoriesJoinSQL + `
WHERE tm.org_id = {org_id:String}` + sincePredicate(cursor, queryTeamsEffectiveUpdatedAtExpr, rowKey) + orderBy(queryTeamsEffectiveUpdatedAtExpr, rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var id, name, description, provider, nativeKey string
		var projectKeys, ownedRepos []string
		var isActive uint8
		var observedAt time.Time
		if err := r.Scan(&id, &name, &description, &provider, &nativeKey, &isActive, &observedAt, &projectKeys, &ownedRepos); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		hasOwnedRepositories := len(ownedRepos) > 0
		teamAuth.record(hasOwnedRepositories)
		// CHAOS-4390: an empty ownedRepos here must NOT reach
		// ContextFabricAuthorizationScope as a bare empty list -- see
		// noTeamOwnershipSentinel's own doc comment for why that would
		// silently authorize this team for every repository-scoped
		// principal in the org.
		repositorySlugs := ownedRepos
		if !hasOwnedRepositories {
			repositorySlugs = []string{noTeamOwnershipSentinel}
		}
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
			Aliases:     distinctNonEmpty(id, nativeKey),
			ProviderIDs: providerID(provider, id),
			Properties:  properties,
			// CHAOS-4390 (v7): RepositorySlugs now carries the team's real,
			// CURRENT repository ownership (ownedRepositoriesJoinSQL above)
			// alongside its own TeamIDs -- never memberships (CHAOS-4321).
			// When this org's data genuinely has no team_repo_ownership rows
			// for this team yet, repositorySlugs is noTeamOwnershipSentinel
			// (denies every repository-scoped principal) rather than a bare
			// empty list (which falkorgraph's shared authorizationValue
			// convention would turn into the "*" wildcard -- see that
			// sentinel's own doc comment for why that must not happen here).
			Authorization:  contractsv1.ContextFabricAuthorizationScope{TeamIDs: []string{id}, RepositorySlugs: repositorySlugs},
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
// Two live facts drive the shape here. First, this ENTITY's own canonical id
// is minted from projects.id verbatim -- including both id SHAPES that space
// contains, a provider-composite ("<org>:gitlab:71133891") and a bare Linear
// UUID -- never from project_key, which would strand every Linear project
// (carries no project_key at all). This is independent of which arm
// work_items.project_id itself joins on the OTHER end of the BELONGS_TO_PROJECT
// edge: that is queryWorkItemProjects' own concern (teams_projects_edges.go),
// which CHAOS-4108 corrected from a false single-arm ("projects.id is the
// ONLY id space work_items.project_id joins") to the real dual-arm join --
// this entity's own canonical id is unaffected either way, since
// queryWorkItemProjects always derives the edge's project endpoint from the
// JOINED row's own (provider, id), the SAME pair this function uses. Second,
// FINAL matters: the ground-truth org holds 56 raw rows that collapse to 20.
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
