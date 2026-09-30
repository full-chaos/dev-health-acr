package devhealthsource_test

// CHAOS-7263: the projection cursor keys on each row's INGEST time, not its
// provider-stamped updated_at, so a row that lands after the cursor passed but
// carries an OLD provider timestamp (backfill, newly connected provider) is
// projected; the exposed ObservedAt stays the provider time; cursors saved
// before the change decode as a reset; and a bounded overlap re-reads rows that
// land just behind the frontier (ingest stamped before the insert landed).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type ingestHarness struct {
	t      *testing.T
	ctx    context.Context
	direct clickhousedriver.Conn
	src    *devhealthsource.ClickHouseProjectionSource
	orgID  string
	repo   string
}

func (h *ingestHarness) workItem(id string, providerUpdated, ingested time.Time) {
	h.t.Helper()
	mustExec(h.t, h.ctx, h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, h.repo, h.orgID, "issue "+id, "open", "", "", "linear", "", providerUpdated, ingested)
}

type drained struct {
	cursor  string
	all     map[string]contractsv1.ContextFabricEntityProjection // every entity by canonical id
	items   map[string]contractsv1.ContextFabricEntityProjection
	batches []contextfabric.ProjectionBatch
}

func (h *ingestHarness) drain(cursor string) drained {
	h.t.Helper()
	out := drained{items: map[string]contractsv1.ContextFabricEntityProjection{}, all: map[string]contractsv1.ContextFabricEntityProjection{}}
	for i := 0; i < 60; i++ {
		b, ok, err := h.src.NextProjectionBatch(h.ctx, contextfabric.ProjectionCheckpoint{OrgID: h.orgID, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			h.t.Fatalf("NextProjectionBatch: %v", err)
		}
		if !ok {
			out.cursor = cursor
			return out
		}
		out.batches = append(out.batches, b)
		cursor = b.NextCursor
		for _, e := range b.Entities {
			out.all[e.Subject.CanonicalID] = e
			if e.Subject.Kind == contextfabric.SubjectWorkItem {
				out.items[e.Subject.Label] = e
			}
		}
	}
	h.t.Fatalf("did not converge")
	return out
}

func TestCHAOS7263IngestTimeCursor(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, st := range productionSchemaDDL() {
		if err := direct.Exec(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	now := time.Now().UTC().Truncate(time.Second)
	newHarness := func(t *testing.T, orgID, repoID string, overlap time.Duration) *ingestHarness {
		src, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		if overlap > 0 {
			if src, err = src.WithOverlap(overlap); err != nil {
				t.Fatal(err)
			}
		}
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repoID, "acme/probe", nil, now, nil, now.Add(-3*time.Hour), orgID, "linear")
		return &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, orgID: orgID, repo: repoID}
	}
	title := func(id string) string { return "issue " + id }

	t.Run("a backfilled row with an OLD provider updated_at landing after the cursor passed is projected; ObservedAt stays provider time", func(t *testing.T) {
		h := newHarness(t, "72630000-0000-4000-8000-000000000001", "72630000-0000-4000-8000-0000000000a1", 0)
		h.workItem("WI-recent", now.Add(-time.Hour), now.Add(-time.Hour))
		first := h.drain("")
		if _, ok := first.items[title("WI-recent")]; !ok {
			t.Fatalf("first drain missed WI-recent: %v", first.items)
		}
		// Landing NOW with a provider timestamp 30 days old (a newly connected
		// project's first sync): ingest stamp = now, well after the cursor.
		providerOld := now.Add(-30 * 24 * time.Hour)
		h.workItem("WI-backfill", providerOld, now)
		second := h.drain(first.cursor)
		got, ok := second.items[title("WI-backfill")]
		if !ok {
			t.Fatalf("the backfilled row was SKIPPED (cursor keyed on provider time): %v", second.items)
		}
		if !got.ObservedAt.Equal(providerOld) {
			t.Fatalf("exposed ObservedAt = %v, want the PROVIDER time %v (freshness honesty: never the ingest time %v)", got.ObservedAt, providerOld, now)
		}
	})

	t.Run("a cursor saved before the ingest space decodes as a reset: full re-read under the original cursor, new-space NextCursor", func(t *testing.T) {
		h := newHarness(t, "72630000-0000-4000-8000-000000000002", "72630000-0000-4000-8000-0000000000a2", 0)
		h.workItem("WI-a", now.Add(-48*time.Hour), now.Add(-2*time.Hour))
		h.workItem("WI-b", now.Add(-24*time.Hour), now.Add(-time.Hour))
		raw, _ := json.Marshal(map[string]any{"since": now, "after": "zzz"}) // legacy shape: no "space"
		old := base64.RawURLEncoding.EncodeToString(raw)
		b, ok, err := h.src.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: h.orgID, Source: devhealthsource.SourceName, Cursor: old})
		if err != nil || !ok {
			t.Fatalf("legacy cursor: ok=%v err=%v", ok, err)
		}
		if b.Cursor != old {
			t.Fatalf("batch.Cursor = %q, want the ORIGINAL cursor (the worker requires it)", b.Cursor)
		}
		names := map[string]bool{}
		for _, e := range b.Entities {
			names[e.Subject.Label] = true
		}
		if !names[title("WI-a")] || !names[title("WI-b")] {
			t.Fatalf("a legacy cursor must trigger a full re-read; got %v", names)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(b.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		var next map[string]any
		_ = json.Unmarshal(decoded, &next)
		if next["space"] != "ingest.v1" {
			t.Fatalf("NextCursor %s is not in the ingest space", decoded)
		}
	})

	t.Run("overlap: a row stamped just BEHIND the frontier that lands after the cursor passed is projected once, cursor unchanged; idempotent", func(t *testing.T) {
		h := newHarness(t, "72630000-0000-4000-8000-000000000003", "72630000-0000-4000-8000-0000000000a3", 15*time.Minute)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		// A slower writer stamped this row 5 minutes ago (inside the 15m
		// overlap) but its insert only lands now, after the cursor passed.
		h.workItem("WI-late", now.Add(-3*time.Hour), now.Add(-15*time.Minute+10*time.Second))
		second := h.drain(first.cursor)
		if _, ok := second.items[title("WI-late")]; !ok {
			t.Fatalf("a row stamped inside the overlap that landed after the cursor passed was NOT projected: %v", second.items)
		}
		for _, b := range second.batches {
			if b.NextCursor != first.cursor && b.Cursor == first.cursor {
				t.Fatalf("the overlap batch moved the cursor (%q -> %q); it must not", b.Cursor, b.NextCursor)
			}
		}
		// Idempotent: the same window is not re-emitted while nothing new lands.
		third := h.drain(second.cursor)
		if len(third.batches) != 0 {
			t.Fatalf("a caught-up tick re-emitted the overlap window (%d batches)", len(third.batches))
		}
		// A row stamped OUTSIDE the overlap that lands late is the documented bound.
		h.workItem("WI-too-late", now.Add(-5*time.Hour), now.Add(-2*time.Hour))
		if fourth := h.drain(third.cursor); len(fourth.items) != 0 {
			t.Fatalf("a row older than the overlap must not be re-read (bounded window), got %v", fourth.items)
		}
	})

	// Same shape on the other provider-timed tables: a deployment whose
	// deployed_at is old but whose row is ingested after the cursor passed, and
	// a review whose submitted_at is old.
	t.Run("deployments and reviews with old provider timestamps landing after the cursor are projected", func(t *testing.T) {
		const orgID, repoID = "72630000-0000-4000-8000-000000000004", "72630000-0000-4000-8000-0000000000a4"
		h := newHarness(t, orgID, repoID, 0)
		h.workItem("WI-anchor", now.Add(-time.Hour), now.Add(-time.Hour))
		first := h.drain("")
		old := now.Add(-45 * 24 * time.Hour)
		mustExec(t, ctx, direct, `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, orgID, "dep-old", "success", "prod", old, old, now)
		mustExec(t, ctx, direct, `INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, org_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			repoID, uint32(7), "pr seven", "open", old, orgID, now.Add(-2*time.Hour))
		mustExec(t, ctx, direct, `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"review-old", repoID, orgID, uint32(7), "approved", old, now)
		second := h.drain(first.cursor)
		var sawDeployment, sawReview bool
		for id, e := range second.all {
			switch e.Subject.Kind {
			case contextfabric.SubjectDeployment:
				sawDeployment = true
				if !e.ObservedAt.Equal(old) {
					t.Errorf("deployment %s ObservedAt = %v, want the provider time %v", id, e.ObservedAt, old)
				}
			}
		}
		for _, b := range second.batches {
			for _, r := range b.Relationships {
				if r.From.Kind == contractsv1.ContextFabricSubjectPullRequestReview || r.To.Kind == contractsv1.ContextFabricSubjectPullRequestReview {
					sawReview = true
				}
			}
			for _, e := range b.Entities {
				if e.Subject.Kind == contractsv1.ContextFabricSubjectPullRequestReview {
					sawReview = true
					if !e.ObservedAt.Equal(old) {
						t.Errorf("review ObservedAt = %v, want the provider time %v", e.ObservedAt, old)
					}
				}
			}
		}
		if !sawDeployment {
			t.Errorf("the deployment with an old deployed_at landing after the cursor was skipped")
		}
		if !sawReview {
			t.Errorf("the review with an old submitted_at landing after the cursor was skipped")
		}
	})
}
