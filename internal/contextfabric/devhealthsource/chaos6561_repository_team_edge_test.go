package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-6561. team_repo_ownership is the ownership source of truth for
// team<->repository (AGENTS.md: team attribution = ownership rows), and
// queryTeams already reads it for a team's authorization scope -- but no
// producer projected it as an edge, so the graph had no repository<->team
// OWNED_BY_TEAM relationship at all. These tests pin the new producer.
//
// fakeClient returns canned rows without executing SQL, so what is pinned here
// is the SCAN half: identity, provenance, validity, the omission/orphan
// ledger, and the checkpoint marker. The SQL half is covered against a real
// ClickHouse by the schema-parity integration test (testcontainers).

// repositoryTeamsMarker is the substring that routes a fake query to the
// repository<->team producer. It must NOT match queryTeams' own
// team_repo_ownership join (`FROM team_repo_ownership FINAL`), or a fixture
// would feed ownership rows into the team scan.
const repositoryTeamsMarker = "FROM team_repo_ownership AS rto FINAL"

// repositoryTeamRow mirrors queryRepositoryTeams' SELECT list exactly.
type repositoryTeamFixture struct {
	repoKey, nullRepoName, repoFullName, repoSlug string
	teamID, source, provider, matchType           string
	isPrimary                                     uint8
	specificity, priority                         int64
	validFrom                                     time.Time
	latestIsOpen                                  uint8
	latestValidTo, observedAt                     time.Time
}

func (f repositoryTeamFixture) row() []any {
	return []any{f.repoKey, f.nullRepoName, f.repoFullName, f.repoSlug, f.teamID, f.source, f.provider, f.matchType,
		f.isPrimary, f.specificity, f.priority, f.validFrom, f.latestIsOpen, f.latestValidTo, f.observedAt}
}

func openRepositoryTeam(repoID, slug, teamID, provider, source string, at time.Time) repositoryTeamFixture {
	return repositoryTeamFixture{
		repoKey: repoID, repoFullName: slug, repoSlug: slug, teamID: teamID, source: source, provider: provider,
		matchType: "exact", isPrimary: 1, specificity: 100, priority: 10,
		validFrom: at.Add(-72 * time.Hour), latestIsOpen: 1, latestValidTo: time.Unix(0, 0).UTC(), observedAt: at,
	}
}

func repositoryTeamClient(rows ...repositoryTeamFixture) *fakeClient {
	raw := make([][]any, len(rows))
	for i, row := range rows {
		raw[i] = row.row()
	}
	return &fakeClient{tables: []fakeTable{{match: repositoryTeamsMarker, rows: raw}}}
}

func repositoryTeamBatch(t *testing.T, client *fakeClient, logged *bytes.Buffer) contextfabric.ProjectionBatch {
	t.Helper()
	source := enabledTeamsProjectsSource(t, client)
	if logged != nil {
		source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch carrying the repository->team edge")
	}
	return batch
}

func repositoryTeamEdges(batch contextfabric.ProjectionBatch) []contractsv1.ContextFabricRelationshipProjection {
	edges := []contractsv1.ContextFabricRelationshipProjection{}
	for _, relationship := range batch.Relationships {
		if relationship.From.Kind == contractsv1.ContextFabricSubjectRepository && relationship.Type == contractsv1.ContextFabricRelationshipOwnedByTeam {
			edges = append(edges, relationship)
		}
	}
	return edges
}

const (
	repoGitHubID = "cd620f84-2602-8dea-7809-8d1f11825cf4"
	repoGitLabID = "1b0e2a53-0e5d-4a53-9d3b-3e1c2b1f9a10"
	repoJiraID   = "2c1f3b64-1f6e-4b64-8e4c-4f2d3c2a8b21"
	repoLinearID = "3d204c75-207f-4c75-9f5d-503e4d3b7c32"
)

// (a) One ownership row projects EXACTLY ONE repository -> team OWNED_BY_TEAM
// edge, carrying the ownership row's own provenance.
func TestChaos6561_OwnershipRowProjectsOneRepositoryToTeamEdge(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fixture := openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "github", "native", at)
	batch := repositoryTeamBatch(t, repositoryTeamClient(fixture), nil)

	edges := repositoryTeamEdges(batch)
	if len(edges) != 1 {
		t.Fatalf("repository->team OWNED_BY_TEAM edges = %d, want exactly 1; relationships=%+v", len(edges), batch.Relationships)
	}
	edge := edges[0]
	if want := devhealthsource.RepositoryTeamRelationshipIDForTest(repoGitHubID, "gh:ops-team", "github", "native"); edge.RelationshipID != want {
		t.Fatalf("relationship id = %q, want %q", edge.RelationshipID, want)
	}
	if edge.From.CanonicalID != "repository:"+repoGitHubID {
		t.Fatalf("From = %+v, want the projected repository node's canonical id", edge.From)
	}
	if edge.To.Kind != contractsv1.ContextFabricSubjectTeam || edge.To.CanonicalID != "team:gh:ops-team" {
		t.Fatalf("To = %+v, want the team subject at the fact-provider identity", edge.To)
	}
	for key, want := range map[string]string{"attribution_source": "native", "match_type": "exact"} {
		got := edge.Properties[key]
		if got.String == nil || *got.String != want {
			t.Fatalf("property %s = %+v, want %q", key, got, want)
		}
	}
	if got := edge.Properties["is_primary"]; got.Boolean == nil || !*got.Boolean {
		t.Fatalf("is_primary = %+v, want true", got)
	}
	for key, want := range map[string]int64{"specificity": 100, "priority": 10} {
		got := edge.Properties[key]
		if got.Integer == nil || *got.Integer != want {
			t.Fatalf("property %s = %+v, want %d", key, got, want)
		}
	}
	// team_repo_ownership has no confidence column; the edge must not invent one.
	if _, invented := edge.Properties["attribution_confidence"]; invented {
		t.Fatalf("edge carries attribution_confidence %+v, but team_repo_ownership has no confidence column", edge.Properties["attribution_confidence"])
	}
	if edge.Derivation != contractsv1.ContextFabricDerivationRuleInferred || edge.EpistemicStatus != contractsv1.ContextFabricEpistemicSourceAsserted {
		t.Fatalf("derivation/status = %q/%q, want rule_inferred/source_asserted for a native exact row", edge.Derivation, edge.EpistemicStatus)
	}
	if got := edge.Authorization; len(got.RepositorySlugs) != 1 || got.RepositorySlugs[0] != "full-chaos/dev-health-ops" || len(got.TeamIDs) != 1 || got.TeamIDs[0] != "gh:ops-team" {
		t.Fatalf("authorization = %+v, want both endpoints' scopes (repo slug + team id)", got)
	}
	wantEvidence := map[string]bool{
		contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, repoGitHubID): false,
		contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "gh:ops-team"):      false,
	}
	for _, ref := range edge.EvidenceRefIDs {
		if _, ok := wantEvidence[ref]; ok {
			wantEvidence[ref] = true
		}
	}
	for ref, seen := range wantEvidence {
		if !seen {
			t.Fatalf("evidence refs %v lack %q", edge.EvidenceRefIDs, ref)
		}
	}
	if edge.SourceVersion != devhealthsource.TeamsProjectsSourceVersion {
		t.Fatalf("source version = %q, want %q", edge.SourceVersion, devhealthsource.TeamsProjectsSourceVersion)
	}
	if edge.ValidFrom == nil || !edge.ValidFrom.Equal(fixture.validFrom) || edge.ValidTo != nil {
		t.Fatalf("validity = %v..%v, want %v..open", edge.ValidFrom, edge.ValidTo, fixture.validFrom)
	}
}

// (b) Provider x entity matrix (AGENTS.md: never Linear-only coverage). A
// row for each of the four providers projects its own edge, and the same
// repo/team/source under two providers is two distinct edges, not one id.
func TestChaos6561_EveryProviderProjectsARepositoryTeamEdge(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fixtures := []repositoryTeamFixture{
		openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "github", "native", at),
		openRepositoryTeam(repoGitLabID, "full.chaos/chaos-ops", "gl:full.chaos", "gitlab", "provider_access", at.Add(time.Second)),
		openRepositoryTeam(repoJiraID, "full-chaos/jira-linked", "JIRA-TEAM", "jira", "jira_legacy", at.Add(2*time.Second)),
		openRepositoryTeam(repoLinearID, "full-chaos/linear-linked", "CHAOS", "linear", "native", at.Add(3*time.Second)),
		// Same repo, team and source as the github row, different provider.
		openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "gitlab", "native", at.Add(4*time.Second)),
	}
	batch := repositoryTeamBatch(t, repositoryTeamClient(fixtures...), nil)
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch does not validate: %v", err)
	}
	for _, f := range fixtures {
		id := devhealthsource.RepositoryTeamRelationshipIDForTest(f.repoKey, f.teamID, f.provider, f.source)
		if !hasRelationshipID(batch, id) {
			t.Errorf("provider %q: no repository->team edge %q for repo %s team %s", f.provider, id, f.repoKey, f.teamID)
		}
	}
	if got := len(repositoryTeamEdges(batch)); got != len(fixtures) {
		t.Fatalf("repository->team edges = %d, want %d (one per provider row)", got, len(fixtures))
	}
}

// (c) A group whose LATEST assertion is closed ends the edge at that
// assertion's valid_to -- the same validity rule the sibling project->team
// edge applies (ownershipValidity) -- and a heuristic source or pattern match
// is labelled inferred, never source_asserted.
func TestChaos6561_ClosedLatestAssertionEndsTheEdge(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	closed := openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "github", "native", at)
	closed.latestIsOpen = 0
	closed.latestValidTo = at.Add(-time.Hour)
	inferred := openRepositoryTeam(repoGitLabID, "full.chaos/chaos-ops", "gl:full.chaos", "gitlab", "inferred", at.Add(time.Second))
	pattern := openRepositoryTeam(repoJiraID, "full-chaos/jira-linked", "JIRA-TEAM", "jira", "manual", at.Add(2*time.Second))
	pattern.matchType = "pattern"
	batch := repositoryTeamBatch(t, repositoryTeamClient(closed, inferred, pattern), nil)

	edge := relationshipByID(t, batch, devhealthsource.RepositoryTeamRelationshipIDForTest(repoGitHubID, "gh:ops-team", "github", "native"))
	if edge.ValidTo == nil || !edge.ValidTo.Equal(closed.latestValidTo) {
		t.Fatalf("ValidTo = %v, want the latest assertion's valid_to %v", edge.ValidTo, closed.latestValidTo)
	}
	if edge.ValidFrom == nil || !edge.ValidFrom.Equal(closed.validFrom) {
		t.Fatalf("ValidFrom = %v, want %v", edge.ValidFrom, closed.validFrom)
	}
	if len(batch.Tombstones) != 0 {
		t.Fatalf("a closed ownership is history, not a retraction: tombstones = %+v", batch.Tombstones)
	}
	for _, f := range []repositoryTeamFixture{inferred, pattern} {
		got := relationshipByID(t, batch, devhealthsource.RepositoryTeamRelationshipIDForTest(f.repoKey, f.teamID, f.provider, f.source))
		if got.EpistemicStatus != contractsv1.ContextFabricEpistemicInferred {
			t.Errorf("source=%s match_type=%s: epistemic status = %q, want inferred", f.source, f.matchType, got.EpistemicStatus)
		}
	}
}

// (d) A NULL repo_id row names no repository node, so it is OMITTED -- and
// the omission is counted and logged, never silent. A row whose repo_id has
// no repos row still projects (the repository endpoint is a deterministic
// id) but is scoped fail-closed to the orphan sentinel, and counted.
func TestChaos6561_NullRepoIDIsOmittedAndLogged(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	valid := openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "github", "native", at)
	nullRepo := openRepositoryTeam("", "full-chaos/*", "gh:ops-team", "github", "inferred", at.Add(time.Second))
	nullRepo.nullRepoName = "full-chaos/*"
	nullRepo.repoSlug = ""
	orphan := openRepositoryTeam(repoGitLabID, "full.chaos/gone", "gl:full.chaos", "gitlab", "native", at.Add(2*time.Second))
	orphan.repoSlug = ""
	var logged bytes.Buffer
	batch := repositoryTeamBatch(t, repositoryTeamClient(valid, nullRepo, orphan), &logged)

	if got := len(repositoryTeamEdges(batch)); got != 2 {
		t.Fatalf("repository->team edges = %d, want 2 (the NULL repo_id row omitted)", got)
	}
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "repository:" {
			t.Fatalf("a NULL repo_id row minted an edge from the empty repository id: %+v", relationship)
		}
	}
	gone := relationshipByID(t, batch, devhealthsource.RepositoryTeamRelationshipIDForTest(repoGitLabID, "gl:full.chaos", "gitlab", "native"))
	if got := gone.Authorization.RepositorySlugs; len(got) != 1 || got[0] != "acr-context-fabric:orphaned-repository" {
		t.Fatalf("orphan repository edge scoped as %v, want the orphaned-repository sentinel", got)
	}
	text := logged.String()
	for _, want := range []string{
		"repository_team_rows_omitted_null_repo_id=1",
		"repository_team_edges_orphaned_repository=1",
		"repository_team_edges_asserted=2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "full-chaos/*") || strings.Contains(text, "gh:ops-team") {
		t.Errorf("log leaks tenant identifiers (repo name / team id):\n%s", text)
	}
}

// (f) The marker moved, and the checkpoint guard acts on it: an organization
// already caught up under v11 has no repository->team edges, and its ownership
// rows' updated_at will not move just because a producer now reads them, so
// only a forced rebuild projects the backlog. The worker must refuse the
// incremental advance under the v11 marker, and accept the current one.
func TestChaos6561_V11CheckpointForcesARebuild(t *testing.T) {
	t.Parallel()
	const deployedBefore = "devhealthsource.teams_projects.v11"
	if devhealthsource.TeamsProjectsSourceVersion == deployedBefore {
		t.Fatalf("TeamsProjectsSourceVersion is still %q: every organization already projected keeps advancing incrementally and never gains a repository->team edge", deployedBefore)
	}
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		stored      string
		wantRebuild bool
	}{
		{"v11 marker forces a rebuild", deployedBefore, true},
		{"current marker advances", devhealthsource.TeamsProjectsSourceVersion, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := enabledTeamsProjectsSource(t, repositoryTeamClient(openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "github", "native", at)))
			backend := &countingBackend{}
			store := &singleCheckpointStore{checkpoint: contextfabric.ProjectionCheckpoint{
				OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName,
				Cursor: testCursor(t, time.Unix(0, 0).UTC(), ""), SourceVersion: tc.stored,
			}}
			worker, err := contextfabric.NewProjectionWorker(source, backend, store, contextfabric.ProjectionWorkerOptions{})
			if err != nil {
				t.Fatalf("NewProjectionWorker: %v", err)
			}
			_, err = worker.RunOnce(context.Background(), liveOrgID, devhealthsource.TeamsProjectsSourceName)
			if tc.wantRebuild {
				if !errors.Is(err, contextfabric.ErrProjectionSourceVersionChanged) {
					t.Fatalf("RunOnce error = %v, want ErrProjectionSourceVersionChanged", err)
				}
				if backend.applied != 0 {
					t.Fatalf("backend applied %d batches under a stale marker", backend.applied)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunOnce under the current marker: %v", err)
			}
			if backend.applied != 1 {
				t.Fatalf("backend applied %d batches, want 1", backend.applied)
			}
		})
	}
}

type countingBackend struct{ applied int }

func (b *countingBackend) ApplyProjectionBatch(_ context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.applied++
	return contextfabric.ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Now().UTC(), BackendWatermark: "w"}, nil
}

func (b *countingBackend) ProjectionWatermark(context.Context, string, string) (contextfabric.ProjectionWatermark, error) {
	return contextfabric.ProjectionWatermark{}, contextfabric.ErrProjectionWatermarkNotFound
}

func (b *countingBackend) PurgeOrganization(context.Context, string) error { return nil }

type singleCheckpointStore struct {
	checkpoint contextfabric.ProjectionCheckpoint
}

func (s *singleCheckpointStore) LoadProjectionCheckpoint(context.Context, string, string) (contextfabric.ProjectionCheckpoint, error) {
	return s.checkpoint, nil
}

func (s *singleCheckpointStore) CompareAndSwapProjectionCheckpoint(_ context.Context, _ contextfabric.ProjectionCheckpoint, next contextfabric.ProjectionCheckpoint) error {
	s.checkpoint = next
	return nil
}
