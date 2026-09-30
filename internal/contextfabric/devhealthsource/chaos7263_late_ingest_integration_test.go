package devhealthsource_test

// CHAOS-7263 late ingest: a row whose provider/event timestamp is OLDER than
// the projection cursor, written AFTER the cursor passed it (a backfill, a
// newly connected provider, a delayed flush), must still be projected on the
// next incremental drain -- and its exposed ObservedAt must stay the provider
// time. One case per table whose cursor column moved from a provider/event
// timestamp to the row's ingest stamp. This file uses no API newer than the
// cursor change itself, so it runs unchanged against the pre-change producer,
// where every case fails.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type ingestHarness struct {
	t      *testing.T
	ctx    context.Context
	direct clickhousedriver.Conn
	src    contextfabric.ProjectionSource
	source string
	orgID  string
	repo   string
}

func (h *ingestHarness) workItem(id string, providerUpdated, ingested time.Time) {
	h.t.Helper()
	mustExec(h.t, h.ctx, h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, h.repo, h.orgID, "issue "+id, "open", "", "", "linear", "", providerUpdated, ingested)
}

type drained struct {
	cursor        string
	all           map[string]contractsv1.ContextFabricEntityProjection // every entity by canonical id
	items         map[string]contractsv1.ContextFabricEntityProjection // work items by label
	relationships map[contractsv1.ContextFabricRelationshipType][]contractsv1.ContextFabricRelationshipProjection
	batches       []contextfabric.ProjectionBatch
}

// requireCursorProgress is the paging invariant every drain loop in this
// package asserts. A batch either moves the cursor position, or it is a
// trailing overlap batch: rows re-read behind an unchanged frontier
// (overlap.go), whose NextCursor keeps the position and only acknowledges
// the batch. The second kind is legitimate exactly once per content; the
// SAME rows in a non-advancing batch twice is a walk that would re-emit them
// forever, which is the stall the old "every batch advances" check existed
// to catch. Content, not the batch id: the id follows the cursor string,
// which carries the previous acknowledgment.
func requireCursorProgress(t *testing.T, where, cursor string, batch contextfabric.ProjectionBatch, replays map[string]bool) {
	t.Helper()
	if keysetPosition(t, batch.NextCursor) != keysetPosition(t, cursor) {
		return
	}
	key := batchContentKey(batch)
	if replays[key] {
		t.Fatalf("%s: the same non-advancing batch (%s) was emitted twice -- projection would loop forever", where, batch.BatchID)
	}
	replays[key] = true
}

// keysetPosition is the keyset position a cursor names, read from its JSON
// (since, after). An empty cursor is the empty position.
func keysetPosition(t *testing.T, cursor string) string {
	t.Helper()
	if cursor == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	var position struct {
		Since time.Time `json:"since"`
		After string    `json:"after"`
	}
	if err := json.Unmarshal(raw, &position); err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	return position.Since.UTC().Format(time.RFC3339Nano) + "|" + position.After
}

// batchContentKey names what a batch writes: its entities, relationships
// and tombstones, by id.
func batchContentKey(batch contextfabric.ProjectionBatch) string {
	var ids []string
	for _, e := range batch.Entities {
		ids = append(ids, "entity "+e.Subject.CanonicalID)
	}
	for _, r := range batch.Relationships {
		ids = append(ids, "relationship "+r.RelationshipID)
	}
	for _, tomb := range batch.Tombstones {
		ids = append(ids, "tombstone "+tomb.CanonicalID)
	}
	sort.Strings(ids)
	return strings.Join(ids, "\n")
}

// drain pages until the source reports nothing available.
func (h *ingestHarness) drain(cursor string) drained {
	h.t.Helper()
	return h.drainEpoch(cursor, 0)
}

// drainEpoch is drain against one graph epoch's checkpoint.
func (h *ingestHarness) drainEpoch(cursor string, epoch int64) drained {
	h.t.Helper()
	out := drained{
		items: map[string]contractsv1.ContextFabricEntityProjection{}, all: map[string]contractsv1.ContextFabricEntityProjection{},
		relationships: map[contractsv1.ContextFabricRelationshipType][]contractsv1.ContextFabricRelationshipProjection{},
	}
	replays := map[string]bool{}
	for i := 0; i < 60; i++ {
		b, ok, err := h.src.NextProjectionBatch(h.ctx, contextfabric.ProjectionCheckpoint{OrgID: h.orgID, Source: h.source, Epoch: epoch, Cursor: cursor})
		if err != nil {
			h.t.Fatalf("NextProjectionBatch: %v", err)
		}
		if !ok {
			out.cursor = cursor
			return out
		}
		requireCursorProgress(h.t, fmt.Sprintf("page %d", i), cursor, b, replays)
		out.batches = append(out.batches, b)
		cursor = b.NextCursor
		for _, e := range b.Entities {
			out.all[e.Subject.CanonicalID] = e
			if e.Subject.Kind == contextfabric.SubjectWorkItem {
				out.items[e.Subject.Label] = e
			}
		}
		for _, r := range b.Relationships {
			out.relationships[r.Type] = append(out.relationships[r.Type], r)
		}
	}
	h.t.Fatalf("did not converge")
	return out
}

// entityOfKind returns the one entity of kind in d, failing if there is not
// exactly one.
func (d drained) entityOfKind(t *testing.T, kind contractsv1.ContextFabricSubjectKind) (contractsv1.ContextFabricEntityProjection, bool) {
	t.Helper()
	var found []contractsv1.ContextFabricEntityProjection
	for _, e := range d.all {
		if e.Subject.Kind == kind {
			found = append(found, e)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%d %s entities in the drain, want at most 1: %v", len(found), kind, found)
	}
	if len(found) == 0 {
		return contractsv1.ContextFabricEntityProjection{}, false
	}
	return found[0], true
}

func requireProviderTime(t *testing.T, what string, got, provider time.Time) {
	t.Helper()
	if !got.Equal(provider) {
		t.Fatalf("%s ObservedAt = %v, want the PROVIDER time %v (freshness honesty: never the ingest time)", what, got, provider)
	}
}

func TestCHAOS7263LateIngestedRowsAreProjected(t *testing.T) {
	ctx := context.Background()
	// The package's shared org-scoped container (orgIsolationClickHouseFixture,
	// productionSchemaDDL's tables): no container start of its own. Every
	// case below uses its own organization.
	query, direct := orgIsolationClickHouseFixture(t)
	createProjectMembershipPresenceView(t, ctx, direct)
	now := time.Now().UTC().Truncate(time.Second)
	hourAgo := now.Add(-time.Hour)
	old := now.Add(-45 * 24 * time.Hour)

	devHealth := func(t *testing.T, orgID, repoID string) *ingestHarness {
		src, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repoID, "acme/probe-"+orgID[len(orgID)-2:], nil, now, nil, now.Add(-3*time.Hour), orgID, "github")
		h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: orgID, repo: repoID}
		// The anchor: ingested an hour ago, so the cursor stands an hour ago
		// once drained -- every late row below carries a provider/event time
		// 45 days BEHIND it and an ingest stamp AFTER it.
		h.workItem("WI-anchor", hourAgo, hourAgo)
		return h
	}
	teamsProjects := func(t *testing.T, orgID string) *ingestHarness {
		src, err := devhealthsource.NewTeamsProjectsSource(query, true)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', ?, ?, 1, 'started', '', ?, ?)`,
			"P-anchor", orgID, "ANCHOR", "anchor project", hourAgo, hourAgo)
		return &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.TeamsProjectsSourceName, orgID: orgID}
	}

	t.Run("work_items: a backfilled work item", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000011", "72630000-0000-4000-8000-0000000000b1")
		first := h.drain("")
		if _, ok := first.items["issue WI-anchor"]; !ok {
			t.Fatalf("first drain missed the anchor: %v", first.items)
		}
		h.workItem("WI-backfill", old, now)
		second := h.drain(first.cursor)
		got, ok := second.items["issue WI-backfill"]
		if !ok {
			t.Fatalf("the backfilled work item was SKIPPED: %v", second.items)
		}
		requireProviderTime(t, "work item", got.ObservedAt, old)
	})

	t.Run("work_items_hierarchy: a backfilled parent link", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000012", "72630000-0000-4000-8000-0000000000b2")
		first := h.drain("")
		// The parent was projected with the anchor; the child (and so the
		// link) lands late with an old provider timestamp.
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"WI-child", h.repo, h.orgID, "issue WI-child", "open", "", "WI-anchor", "linear", "", old, now)
		second := h.drain(first.cursor)
		links := second.relationships[contractsv1.ContextFabricRelationshipPartOf]
		if len(links) != 1 {
			t.Fatalf("the backfilled parent link was SKIPPED: %d PART_OF relationships", len(links))
		}
		requireProviderTime(t, "parent link", links[0].ObservedAt, old)
	})

	t.Run("deployments: a backfilled deployment", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000013", "72630000-0000-4000-8000-0000000000b3")
		first := h.drain("")
		mustExec(t, ctx, direct, `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			h.repo, h.orgID, "dep-old", "success", "prod", old, old, now)
		got, ok := h.drain(first.cursor).entityOfKind(t, contractsv1.ContextFabricSubjectDeployment)
		if !ok {
			t.Fatal("the backfilled deployment was SKIPPED")
		}
		requireProviderTime(t, "deployment", got.ObservedAt, old)
	})

	t.Run("operational_incidents: a backfilled incident", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000014", "72630000-0000-4000-8000-0000000000b4")
		mustExec(t, ctx, direct, `INSERT INTO operational_service_repository_mappings (org_id, service_id, repo_id, is_active) VALUES (?, ?, ?, ?)`, h.orgID, "svc-late", h.repo, uint8(1))
		first := h.drain("")
		mustExec(t, ctx, direct, `INSERT INTO operational_incidents (id, org_id, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"inc-old", h.orgID, "svc-late", "late incident", "resolved", "resolved", "low", "low", old, old, old, uint8(0), now)
		got, ok := h.drain(first.cursor).entityOfKind(t, contractsv1.ContextFabricSubjectIncident)
		if !ok {
			t.Fatal("the backfilled incident was SKIPPED")
		}
		requireProviderTime(t, "incident", got.ObservedAt, old)
	})

	t.Run("work_graph_deployment_incident_edges: a late-computed edge", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000015", "72630000-0000-4000-8000-0000000000b5")
		mustExec(t, ctx, direct, `INSERT INTO operational_service_repository_mappings (org_id, service_id, repo_id, is_active) VALUES (?, ?, ?, ?)`, h.orgID, "svc-edge", h.repo, uint8(1))
		mustExec(t, ctx, direct, `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			h.repo, h.orgID, "dep-edge", "success", "prod", hourAgo, hourAgo, hourAgo)
		mustExec(t, ctx, direct, `INSERT INTO operational_incidents (id, org_id, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"inc-edge", h.orgID, "svc-edge", "edge incident", "resolved", "resolved", "low", "low", hourAgo, hourAgo, hourAgo, uint8(0), hourAgo)
		first := h.drain("")
		mustExec(t, ctx, direct, `INSERT INTO work_graph_deployment_incident_edges (edge_id, deployment_id, incident_id, repo_id, org_id, observed_at, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"edge-late", "dep-edge", "inc-edge", h.repo, h.orgID, old, now)
		edges := h.drain(first.cursor).relationships["CORRELATED_WITH_INCIDENT"]
		if len(edges) != 1 {
			t.Fatalf("the late-computed deployment/incident edge was SKIPPED: %d edges", len(edges))
		}
		requireProviderTime(t, "deployment/incident edge", edges[0].ObservedAt, old)
	})

	t.Run("git_pull_request_reviews: a backfilled review", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000016", "72630000-0000-4000-8000-0000000000b6")
		mustExec(t, ctx, direct, `INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, org_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			h.repo, uint32(7), "pr seven", "open", old, h.orgID, hourAgo.Add(-time.Minute))
		first := h.drain("")
		mustExec(t, ctx, direct, `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"review-old", h.repo, h.orgID, uint32(7), "approved", old, now)
		got, ok := h.drain(first.cursor).entityOfKind(t, contractsv1.ContextFabricSubjectPullRequestReview)
		if !ok {
			t.Fatal("the backfilled review was SKIPPED")
		}
		requireProviderTime(t, "review", got.ObservedAt, old)
	})

	t.Run("ci_pipeline_runs: a backfilled CI run", func(t *testing.T) {
		t.Parallel()
		h := devHealth(t, "72630000-0000-4000-8000-000000000017", "72630000-0000-4000-8000-0000000000b7")
		first := h.drain("")
		mustExec(t, ctx, direct, `INSERT INTO ci_pipeline_runs (run_id, repo_id, org_id, branch, status, started_at, finished_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			"run-old", h.repo, h.orgID, "main", "success", old.Add(-time.Minute), old, now)
		got, ok := h.drain(first.cursor).entityOfKind(t, contractsv1.ContextFabricSubjectCIRun)
		if !ok {
			t.Fatal("the backfilled CI run was SKIPPED")
		}
		requireProviderTime(t, "CI run", got.ObservedAt, old)
	})

	t.Run("teams: a team whose provider updated_at is old", func(t *testing.T) {
		t.Parallel()
		h := teamsProjects(t, "72630000-0000-4000-8000-000000000018")
		first := h.drain("")
		if _, ok := first.entityOfKind(t, contractsv1.ContextFabricSubjectProject); !ok {
			t.Fatal("first drain missed the anchor project")
		}
		mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"T-late", "late team", "", old, now, h.orgID, "linear", "T-late", []string{}, uint8(1))
		got, ok := h.drain(first.cursor).entityOfKind(t, contractsv1.ContextFabricSubjectTeam)
		if !ok {
			t.Fatal("the team with an old provider updated_at was SKIPPED")
		}
		requireProviderTime(t, "team", got.ObservedAt, old)
	})

	t.Run("projects: a project whose provider updated_at is old", func(t *testing.T) {
		t.Parallel()
		h := teamsProjects(t, "72630000-0000-4000-8000-000000000019")
		first := h.drain("")
		mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', ?, ?, 1, 'started', '', ?, ?)`,
			"P-late", h.orgID, "LATE", "late project", old, now)
		second := h.drain(first.cursor)
		var found bool
		for _, e := range second.all {
			if e.Subject.Kind == contractsv1.ContextFabricSubjectProject && e.Subject.Label == "late project" {
				found = true
				requireProviderTime(t, "project", e.ObservedAt, old)
			}
		}
		if !found {
			t.Fatal("the project with an old provider updated_at was SKIPPED")
		}
	})
}
