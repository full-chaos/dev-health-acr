package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The cost of the scoped work-item census on the real path, at production
// size and above: a requested scope of 25 repositories (the most one census
// walks), the largest with 1200 linked issues (production's largest
// repository has about 750; the whole scope stays under the census bound of
// 2000 issues), every issue and pull request node carrying a
// long title and an embedding of the production width; and a repository past
// the census bound (2500 linked issues) for the cut row. Seeded ClickHouse
// rows, projected by the REAL producer into a REAL FalkorDB, read by the REAL
// resolver with the REAL census. Needs Docker; run by CI.

const (
	costScopeRepositories = maxLinkScopedRepositories
	costLargestLinks      = 1200
	costOtherLinks        = 30
	costPastBoundLinks    = 2500
)

func TestTheScopedCensusCostAtProductionSize(t *testing.T) {
	ctx := context.Background()
	query, direct := scopedClickHouse(t, ctx)
	tracer := &scopedRoundTracer{}
	adapter := scopedFalkor(t, ctx, query, tracer)
	orgID := scopedUUID("cost-org")
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-20 * 24 * time.Hour)
	longText := strings.Repeat("A sentence of the kind an issue title or a pull request description carries in production. ", 6)

	repoIDs := map[string]string{}
	links := map[string]int{"big/huge": costPastBoundLinks}
	for i := 0; i < costScopeRepositories; i++ {
		n := costOtherLinks
		if i == 0 {
			n = costLargestLinks
		}
		links[fmt.Sprintf("acme/r%02d", i)] = n
	}
	for slug := range links {
		repoIDs[slug] = scopedUUID("cost repo " + slug)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoIDs[slug], orgID, slug, "github", now); err != nil {
			t.Fatal(err)
		}
	}
	pulls, err := direct.PrepareBatch(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, body, state, created_at, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	items, err := direct.PrepareBatch(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	linkRows, err := direct.PrepareBatch(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)`)
	if err != nil {
		t.Fatal(err)
	}
	for slug, n := range links {
		for i := 1; i <= n; i++ {
			title, body := fmt.Sprintf("Change %d: %s", i, longText), longText+longText
			state := "merged"
			if err := pulls.Append(repoIDs[slug], orgID, uint32(i), &title, &body, &state, created, now); err != nil {
				t.Fatal(err)
			}
			issue := fmt.Sprintf("gh:%s#%d", slug, 100000+i)
			if err := items.Append(issue, repoIDs[slug], orgID, fmt.Sprintf("Issue %d: %s", i, longText), "issue", "open", "github", "", created, now, now); err != nil {
				t.Fatal(err)
			}
			if err := linkRows.Append(repoIDs[slug], issue, uint32(i), float32(0.9), "native", "", now, orgID); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The two keys the census counts: one repository-less issue linked into
	// the largest scoped repository, one linked to the repository past the
	// bound. Fillers make the search stall so the census runs.
	for key, slug := range map[string]string{"linear:CHAOS-77": "acme/r00", "linear:CHAOS-78": "big/huge"} {
		if err := items.Append(key, scopedZeroRepo, orgID, "Retry "+key, "issue", "open", "linear", "", created, now, now); err != nil {
			t.Fatal(err)
		}
		if err := linkRows.Append(repoIDs[slug], key, uint32(1), float32(0.9), "native", "", now, orgID); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 30; i++ {
		if err := items.Append(fmt.Sprintf("linear:CHAOS-9%02d", i), scopedZeroRepo, orgID, "CHAOS-77 CHAOS-78 follow-up", "issue", "open", "linear", "", created, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for name, batch := range map[string]interface{ Send() error }{"pull requests": pulls, "work items": items, "links": linkRows} {
		if err := batch.Send(); err != nil {
			t.Fatalf("send %s: %v", name, err)
		}
	}
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	scopedDrain(t, ctx, main, devhealthsource.SourceName, orgID, adapter)
	org := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	key, err := adapter.effectiveKey(ctx, orgID, binding)
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]interface{}, walkMemoryDimension)
	for i := range vector {
		vector[i] = float64(i%97) / 97
	}
	for _, kind := range []string{"work_item", "pull_request"} {
		rows, err := adapter.api.query(ctx, key, fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) RETURN n.%s AS id", labelSubject, propOrgID, propKind, propCanonicalID), map[string]interface{}{"org": orgID, "kind": kind}, true)
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(rows); start += 50 {
			ids := []interface{}{}
			for _, r := range rows[start:min(start+50, len(rows))] {
				ids = append(ids, r["id"])
			}
			cypher := fmt.Sprintf("UNWIND $ids AS id MATCH (n:%s {%s:$org, %s:$kind, %s:id}) SET n.%s = vecf32($vec)", labelSubject, propOrgID, propKind, propCanonicalID, propEmbedding)
			if _, err := adapter.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": kind, "ids": ids, "vec": vector}, false); err != nil {
				t.Fatalf("write embeddings: %v", err)
			}
		}
	}

	scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/*"}}
	wantPopulation := costLargestLinks + 1 + (costScopeRepositories-1)*costOtherLinks
	var population map[string]string
	var complete bool
	var walkErr error
	start := time.Now()
	allocated, peak := walkAllocation(func() {
		population, complete, walkErr = adapter.linkScopedIssues(ctx, key, org, scope, contextfabric.WorkItemMembershipCensusLimit)
	})
	walkTime := time.Since(start)
	t.Logf("scoped census walk, %d repositories, %d issues: %s, allocated %d MiB, peak live heap +%d MiB (race detector %t)", costScopeRepositories, len(population), walkTime, allocated>>20, peak>>20, raceDetectorEnabled)
	if walkErr != nil || !complete || len(population) != wantPopulation {
		t.Fatalf("population %d complete %t err %v, want all %d issues of the 25 repositories", len(population), complete, walkErr, wantPopulation)
	}
	if allocated > walkMemoryBound {
		t.Fatalf("the scoped census walk allocated %d MiB, want at most %d MiB", allocated>>20, walkMemoryBound>>20)
	}

	resolve := func(t *testing.T, handle string, slugs []string) ([]string, []graphrank.ResolutionTraceEvent, time.Duration, uint64) {
		t.Helper()
		tracer.take()
		request := contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_scoped_census_cost", Question: "What is the status of " + handle + "?",
			TimeContext:    contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{RepositorySlugs: slugs},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		}
		interpreted := contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{handle},
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		}
		var resolution contextfabric.SubjectResolution
		var err error
		began := time.Now()
		allocated, _ := walkAllocation(func() {
			resolution, _, _, _, err = adapter.ResolveSubjects(ctx, org, request, interpreted, binding, nil, nil, nil, "")
		})
		took := time.Since(began)
		if err != nil {
			t.Fatal(err)
		}
		var committed []string
		for _, s := range resolution.Committed {
			if s.Kind == contextfabric.SubjectWorkItem {
				committed = append(committed, s.CanonicalID)
			}
		}
		return committed, tracer.take(), took, allocated
	}
	reasons := func(rounds []graphrank.ResolutionTraceEvent) string {
		out := []string{}
		for _, r := range rounds {
			out = append(out, r.ShadowOutcome+"/"+r.ShadowReason)
		}
		return strings.Join(out, ",")
	}

	t.Run("a scope of 25 repositories is one census inside the round budget", func(t *testing.T) {
		committed, rounds, took, allocated := resolve(t, "CHAOS-77", []string{"acme/*"})
		t.Logf("resolution with the scoped census: %s, allocated %d MiB, rounds %s (round budget 3s, race detector %t)", took, allocated>>20, reasons(rounds), raceDetectorEnabled)
		if raceDetectorEnabled {
			return
		}
		want, _, _ := identity.Derive(identity.KindWorkItem, []string{scopedZeroRepo, "linear:CHAOS-77"}, nil)
		if len(committed) != 1 || committed[0] != want {
			t.Fatalf("committed %v (rounds %s), want the linked issue: the census did not complete inside its budget", committed, reasons(rounds))
		}
		if allocated > walkMemoryBound {
			t.Fatalf("the resolution allocated %d MiB, want at most %d MiB", allocated>>20, walkMemoryBound>>20)
		}
	})

	t.Run("a repository past the census bound is no census, never a smaller count", func(t *testing.T) {
		committed, rounds, took, _ := resolve(t, "CHAOS-78", []string{"big/huge"})
		t.Logf("resolution past the bound: %s, rounds %s", took, reasons(rounds))
		if len(committed) != 0 {
			t.Fatalf("committed %v from a cut walk", committed)
		}
		if got := reasons(rounds); !strings.Contains(got, string(graphrank.ReasonCensusError)) {
			t.Fatalf("rounds %s, want the census reported incomplete", got)
		}
	})
}
