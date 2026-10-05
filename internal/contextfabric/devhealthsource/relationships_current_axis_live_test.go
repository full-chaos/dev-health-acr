package devhealthsource_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestRelationshipsCurrentAxisServesEndedSubjectsOnRealStores reads
// read_relationships through the served path (directread.RelationshipsReader
// over the real falkorgraph.Adapter, the composition the route uses) after the
// REAL producers projected seeded ClickHouse rows of the shape production
// holds: completed issues, merged pull requests, open ones, and work items
// moved between projects. Each answer is checked as the JSON document the
// route writes (json.Marshal of the response), which is the document the MCP
// tool returns to the client unchanged.
//
// On the current axis (no as_of) an ended subject is still a subject: its
// edges are served when they lasted until the first of their end nodes
// ended, with their stored window. An edge that ended while both of its end
// nodes were still valid (a membership that stopped) is not served. The
// as_of axis keeps the strict window on the edge and both end nodes.
//
// Needs Docker (ClickHouse and FalkorDB containers).
func TestRelationshipsCurrentAxisServesEndedSubjectsOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	applyProductionSchema(t, ctx, direct, "")
	adapter := chaos7074FalkorAdapter(t, ctx)

	// Whole seconds: git_pull_requests and team_project_ownership keep
	// their times to the second.
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * day) }
	orgID := "13000000-0000-4000-8000-0000000000c1"
	repoSvc, repoOther := o3UUID(orgID+"svc"), o3UUID(orgID+"other")
	const zeroRepo = "00000000-0000-0000-0000-000000000000"
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	exec("repo svc", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoSvc, orgID, "acme/svc", "github", now)
	exec("repo other", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoOther, orgID, "acme/other", "github", now)
	for _, p := range [][2]string{{"P-old", "OLD"}, {"P-new", "NEW"}} {
		exec("project "+p[0], `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', ?, ?, 1, 'started', '', ?, ?)`,
			p[0], orgID, p[1], "project "+p[1], ago(60), ago(60))
	}
	// A retired project: inactive, so its window closes at its updated_at
	// (25 days ago). Its ownership by team TOWN and one item's membership
	// both end 20 days ago, after it retired.
	retired := ago(25)
	exec("retired project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', ?, ?, 0, 'completed', '', ?, ?)`,
		"P-ret", orgID, "RET", "project RET", retired, retired)
	exec("team", `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"TOWN", "team TOWN", "", ago(60), ago(60), orgID, "linear", "TOWN", []string{}, uint8(1))
	exec("team repository ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "TOWN", repoSvc, "acme/svc", "exact", "native", uint8(1), uint16(1), int32(1), ago(60), nil, ago(60))
	ownershipEnded := ago(20)
	exec("team project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced) VALUES (?, 'linear', ?, ?, ?, 'native', ?, ?, ?, ?)`,
		orgID, "TOWN", "P-ret", "RET", ago(60), ownershipEnded, ownershipEnded, ownershipEnded)

	const (
		linDone, linWip, linOpen     = "linear:DONE-1", "linear:WIP-1", "linear:OPEN-1"
		linMovedOpen, linMovedDone   = "linear:MOVE-1", "linear:MOVE-2"
		linMovedRetired              = "linear:MOVE-3"
		ghDone                       = "gh:acme/svc#1"
		prMerged, prOpen, prOtherNum = 11, 12, 21
	)
	type seededItem struct {
		repo             string
		created          time.Time
		completed        *time.Time
		provider, status string
	}
	done := ago(8)
	items := map[string]seededItem{
		linDone:         {zeroRepo, ago(30), &done, "linear", "done"},
		linWip:          {zeroRepo, ago(30), nil, "linear", "in progress"},
		linOpen:         {zeroRepo, ago(30), nil, "linear", "todo"},
		linMovedOpen:    {zeroRepo, ago(40), nil, "linear", "in progress"},
		linMovedDone:    {zeroRepo, ago(40), &done, "linear", "done"},
		linMovedRetired: {zeroRepo, ago(40), nil, "linear", "in progress"},
		ghDone:          {repoSvc, ago(30), &done, "github", "closed"},
	}
	for id, item := range items {
		if item.completed == nil {
			exec("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, item.repo, orgID, id, "issue", item.status, item.provider, item.created, now, now)
			continue
		}
		exec("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, created_at, updated_at, completed_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, item.repo, orgID, id, "issue", item.status, item.provider, item.created, now, *item.completed, now)
	}
	merged, otherMerged := ago(10), ago(5)
	exec("merged pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, merged_at, closed_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoSvc, orgID, uint32(prMerged), "merged PR", "merged", ago(20), merged, merged, now)
	exec("open pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		repoSvc, orgID, uint32(prOpen), "open PR", "open", ago(20), now)
	exec("other pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, merged_at, closed_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoOther, orgID, uint32(prOtherNum), "other PR", "merged", ago(20), otherMerged, otherMerged, now)

	type seededLink struct {
		issue, tier, prRepo string
		number              uint32
	}
	for i, l := range []seededLink{
		{linDone, "native", repoSvc, prMerged},
		{linWip, "native", repoSvc, prMerged},
		{linWip, "native", repoSvc, prOpen},
		{linOpen, "native", repoSvc, prOpen},
		{ghDone, "native", repoSvc, prMerged},
		{ghDone, "explicit_text", repoOther, prOtherNum},
	} {
		exec(fmt.Sprintf("link %d", i), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			l.prRepo, l.issue, l.number, float32(0.9), l.tier, "", now.Add(time.Duration(i)*time.Second), orgID)
	}

	// Project membership through the transition log: one ADD for the
	// completed issue, and an ADD then a MOVE for the two moved items (the
	// MOVE closes P-old and opens P-new, both while the item was open).
	transition := func(subject, fromID, fromKey, toID, toKey, event string, at time.Time) {
		exec("transition "+event, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, ?, ?, '', ?, ?, ?, ?)`,
			orgID, zeroRepo, subject, fromID, toID, fromKey, toKey, at, at, event, now)
	}
	transition(linDone, "", "", "P-new", "NEW", "evt-done-add", ago(29))
	for _, id := range []string{linMovedOpen, linMovedDone} {
		transition(id, "", "", "P-old", "OLD", "evt-add-"+id, ago(39))
		transition(id, "P-old", "OLD", "P-new", "NEW", "evt-move-"+id, ago(20))
	}
	transition(linMovedRetired, "", "", "P-ret", "RET", "evt-add-retired", ago(39))
	transition(linMovedRetired, "P-ret", "RET", "P-new", "NEW", "evt-move-retired", ownershipEnded)

	mainSource, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, mainSource, adapter, orgID, devhealthsource.SourceName)
	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, teams, adapter, orgID, devhealthsource.TeamsProjectsSourceName)

	workItemID := func(id string) string {
		out, _, err := identity.Derive(identity.KindWorkItem, []string{items[id].repo, id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	projectID := func(id string) string {
		out, _, err := identity.Derive(identity.KindProject, []string{"linear", id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	prID := func(repo string, number int) string { return fmt.Sprintf("pull_request:%s:%d", repo, number) }
	idPRMerged, idPROpen, idPROther := prID(repoSvc, prMerged), prID(repoSvc, prOpen), prID(repoOther, prOtherNum)
	idPOld, idPNew, idPRet := projectID("P-old"), projectID("P-new"), projectID("P-ret")
	idTeam, idRepoSvc := contextfabric.TeamCanonicalID("TOWN"), "repository:"+repoSvc
	edge := func(from, to string) string { return from + " -> " + to }
	link := func(issue, pr string) string { return edge(workItemID(issue), pr) }
	member := func(issue, project string) string { return edge(workItemID(issue), project) }

	keyring := directread.CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}}
	reader, err := directread.NewRelationshipsReader(directread.NewSubjectGate(adapter, nil), adapter, nil, keyring)
	if err != nil {
		t.Fatal(err)
	}

	// servedDoc is the part of the route's JSON document a client reads.
	type servedDoc struct {
		Status string `json:"status"`
		Edges  []struct {
			Type string `json:"type"`
			Hop  int    `json:"hop"`
			From struct {
				CanonicalID string `json:"canonical_id"`
			} `json:"from"`
			To struct {
				CanonicalID string `json:"canonical_id"`
			} `json:"to"`
			Provenance struct {
				ValidFrom *string `json:"valid_from"`
				ValidTo   *string `json:"valid_to"`
				LinkTier  string  `json:"link_tier"`
			} `json:"provenance"`
		} `json:"edges"`
		Withheld struct {
			EdgesNotVisible int `json:"edges_not_visible"`
		} `json:"withheld"`
		Page struct {
			Complete   bool   `json:"complete"`
			NextCursor string `json:"next_cursor"`
		} `json:"page"`
	}
	type servedEdge struct {
		hop       int
		tier      string
		validFrom *time.Time
		validTo   *time.Time
	}
	type answer struct {
		status   string
		edges    map[string]servedEdge // "from -> to" -> edge
		withheld int
		raw      string
	}
	calls := 0
	read := func(t *testing.T, principal storage.Principal, request directread.RelationshipsRequest) answer {
		t.Helper()
		out := answer{edges: map[string]servedEdge{}}
		for page := 0; page < 20; page++ {
			calls++
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", t.Name(), calls)))
			response, err := reader.Read(observability.WithRequestID(ctx, "req_"+hex.EncodeToString(sum[:16])), principal, request)
			if err != nil {
				t.Fatalf("read_relationships %+v: %v", request, err)
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var doc servedDoc
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			out.raw += string(raw)
			// A page before the last is partial by design (a cursor
			// follows); the answer's status is the last page's.
			out.status = doc.Status
			out.withheld += doc.Withheld.EdgesNotVisible
			for _, e := range doc.Edges {
				key := edge(e.From.CanonicalID, e.To.CanonicalID)
				if _, dup := out.edges[key+" "+e.Type]; dup {
					t.Fatalf("edge %s %s served twice", key, e.Type)
				}
				parse := func(s *string) *time.Time {
					if s == nil {
						return nil
					}
					v, err := time.Parse(time.RFC3339Nano, *s)
					if err != nil {
						t.Fatalf("edge %s: time %q: %v", key, *s, err)
					}
					return &v
				}
				out.edges[key+" "+e.Type] = servedEdge{hop: e.Hop, tier: e.Provenance.LinkTier, validFrom: parse(e.Provenance.ValidFrom), validTo: parse(e.Provenance.ValidTo)}
			}
			if doc.Page.Complete {
				return out
			}
			if doc.Page.NextCursor == "" {
				t.Fatalf("page %d is not complete and has no cursor", page)
			}
			request.Cursor = doc.Page.NextCursor
		}
		t.Fatal("read did not finish in 20 pages")
		return out
	}
	request := func(kind, id, direction string, types ...string) directread.RelationshipsRequest {
		return directread.RelationshipsRequest{Subject: directread.RelationshipsSubject{Kind: kind, CanonicalID: id}, Types: types, Direction: direction, Limit: 100}
	}
	asOf := func(r directread.RelationshipsRequest, at time.Time) directread.RelationshipsRequest {
		r.AsOf = at.Format(time.RFC3339Nano)
		return r
	}
	depth2 := func(r directread.RelationshipsRequest) directread.RelationshipsRequest {
		r.Depth = 2
		return r
	}
	const links, belongs, owned = "LINKS_PULL_REQUEST", "BELONGS_TO_PROJECT", "OWNED_BY_TEAM"
	keys := func(a answer) []string {
		out := make([]string, 0, len(a.edges))
		for k := range a.edges {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	expect := func(t *testing.T, a answer, status string, withheld int, want ...string) {
		t.Helper()
		sort.Strings(want)
		if a.status != status {
			t.Fatalf("status = %q, want %q", a.status, status)
		}
		if got := keys(a); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("served edges\n got  %v\n want %v", got, want)
		}
		if a.withheld != withheld {
			t.Fatalf("withheld.edges_not_visible = %d, want %d", a.withheld, withheld)
		}
	}
	at := func(t *testing.T, a answer, key string) servedEdge {
		t.Helper()
		e, ok := a.edges[key]
		if !ok {
			t.Fatalf("edge %s not served: %v", key, keys(a))
		}
		return e
	}
	sameTime := func(t *testing.T, label string, got *time.Time, want time.Time) {
		t.Helper()
		if got == nil || !got.Equal(want) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}

	unrestricted := storage.Principal{OrgID: orgID, Subject: "axis", CredentialID: "axis"}
	svcOnly := storage.Principal{OrgID: orgID, Subject: "axis", CredentialID: "axis", RepositoryScopes: []string{"acme/svc"}}

	t.Run("current axis", func(t *testing.T) {
		t.Run("merged pull request keeps the links of its completed and open issues", func(t *testing.T) {
			a := read(t, unrestricted, request("pull_request", idPRMerged, "in", links))
			expect(t, a, "complete", 0,
				link(linDone, idPRMerged)+" "+links, link(linWip, idPRMerged)+" "+links, link(ghDone, idPRMerged)+" "+links)
			e := at(t, a, link(linDone, idPRMerged)+" "+links)
			if e.tier != "native" || e.hop != 1 {
				t.Fatalf("tier/hop = %q/%d, want native/1", e.tier, e.hop)
			}
			// The stored window, served unchanged: from the pull request's
			// creation to its merge (the earlier of the two ends).
			sameTime(t, "valid_from", e.validFrom, ago(20))
			sameTime(t, "valid_to", e.validTo, merged)
		})
		t.Run("completed issue keeps its link and its project", func(t *testing.T) {
			a := read(t, unrestricted, request("work_item", workItemID(linDone), "out"))
			expect(t, a, "complete", 0, link(linDone, idPRMerged)+" "+links, member(linDone, idPNew)+" "+belongs)
			if e := at(t, a, member(linDone, idPNew)+" "+belongs); e.validTo != nil {
				t.Fatalf("membership valid_to = %v, want null", e.validTo)
			}
		})
		t.Run("in-progress issue keeps its merged and its open pull request", func(t *testing.T) {
			a := read(t, unrestricted, request("work_item", workItemID(linWip), "out", links))
			expect(t, a, "complete", 0, link(linWip, idPRMerged)+" "+links, link(linWip, idPROpen)+" "+links)
		})
		t.Run("open issue and open pull request", func(t *testing.T) {
			a := read(t, unrestricted, request("work_item", workItemID(linOpen), "out", links))
			expect(t, a, "complete", 0, link(linOpen, idPROpen)+" "+links)
			if e := at(t, a, link(linOpen, idPROpen)+" "+links); e.validTo != nil || e.tier != "native" {
				t.Fatalf("open link valid_to/tier = %v/%q, want nil/native", e.validTo, e.tier)
			}
		})
		t.Run("membership that ended while the item was open is not served", func(t *testing.T) {
			a := read(t, unrestricted, request("work_item", workItemID(linMovedOpen), "out", belongs))
			expect(t, a, "complete", 0, member(linMovedOpen, idPNew)+" "+belongs)
		})
		t.Run("membership that ended before the item completed is not served", func(t *testing.T) {
			a := read(t, unrestricted, request("work_item", workItemID(linMovedDone), "out", belongs))
			expect(t, a, "complete", 0, member(linMovedDone, idPNew)+" "+belongs)
			if strings.Contains(a.raw, idPOld) {
				t.Fatalf("the ended membership's project is named: %s", a.raw)
			}
		})
		t.Run("ownership and membership that lasted until their project retired", func(t *testing.T) {
			a := read(t, unrestricted, request("project", idPRet, "both", owned, belongs))
			expect(t, a, "complete", 0, edge(idPRet, idTeam)+" "+owned, member(linMovedRetired, idPRet)+" "+belongs)
			sameTime(t, "ownership valid_to", at(t, a, edge(idPRet, idTeam)+" "+owned).validTo, ownershipEnded)
			sameTime(t, "membership valid_to", at(t, a, member(linMovedRetired, idPRet)+" "+belongs).validTo, ownershipEnded)
		})
		t.Run("depth two from the merged pull request", func(t *testing.T) {
			a := read(t, unrestricted, depth2(request("pull_request", idPRMerged, "both", links, belongs)))
			expect(t, a, "complete", 0,
				link(linDone, idPRMerged)+" "+links, link(linWip, idPRMerged)+" "+links, link(ghDone, idPRMerged)+" "+links,
				member(linDone, idPNew)+" "+belongs, link(linWip, idPROpen)+" "+links, link(ghDone, idPROther)+" "+links)
			for key, e := range a.edges {
				want := 2
				if strings.Contains(key, idPRMerged) {
					want = 1
				}
				if e.hop != want {
					t.Fatalf("edge %s hop = %d, want %d", key, e.hop, want)
				}
			}
		})
	})

	t.Run("as_of axis keeps the strict window", func(t *testing.T) {
		t.Run("before the link started", func(t *testing.T) {
			expect(t, read(t, unrestricted, asOf(request("pull_request", idPRMerged, "in", links), ago(25))), "complete", 0)
		})
		t.Run("inside the link window", func(t *testing.T) {
			expect(t, read(t, unrestricted, asOf(request("pull_request", idPRMerged, "in", links), ago(15))), "complete", 0,
				link(linDone, idPRMerged)+" "+links, link(linWip, idPRMerged)+" "+links, link(ghDone, idPRMerged)+" "+links)
		})
		t.Run("after both ends ended", func(t *testing.T) {
			expect(t, read(t, unrestricted, asOf(request("pull_request", idPRMerged, "in", links), ago(2))), "complete", 0)
		})
		t.Run("the ended membership inside its own window", func(t *testing.T) {
			expect(t, read(t, unrestricted, asOf(request("work_item", workItemID(linMovedDone), "out", belongs), ago(30))), "complete", 0,
				member(linMovedDone, idPOld)+" "+belongs)
		})
	})

	t.Run("restricted caller", func(t *testing.T) {
		t.Run("merged pull request: the repository-less issues stay withheld", func(t *testing.T) {
			a := read(t, svcOnly, request("pull_request", idPRMerged, "in", links))
			expect(t, a, "complete", 2, link(ghDone, idPRMerged)+" "+links)
			for _, hidden := range []string{workItemID(linDone), workItemID(linWip)} {
				if strings.Contains(a.raw, hidden) {
					t.Fatalf("served document names %s, which the caller may not see", hidden)
				}
			}
		})
		t.Run("completed issue: the link to an ungranted repository stays withheld", func(t *testing.T) {
			a := read(t, svcOnly, request("work_item", workItemID(ghDone), "out", links))
			expect(t, a, "complete", 1, link(ghDone, idPRMerged)+" "+links)
			if strings.Contains(a.raw, idPROther) {
				t.Fatalf("served document names %s, which the caller may not see", idPROther)
			}
		})
		t.Run("an ended ownership grants nothing", func(t *testing.T) {
			// The team is the caller's through its current acme/svc
			// ownership. The retired project's ownership edge ended: the
			// gate's ownership reach reads only current ownership, so the
			// project is not admitted, the edge is withheld and counted,
			// and the project is never named.
			a := read(t, svcOnly, request("team", idTeam, "in", owned))
			expect(t, a, "complete", 1, edge(idRepoSvc, idTeam)+" "+owned)
			if strings.Contains(a.raw, idPRet) {
				t.Fatalf("served document names %s, which the caller may not see", idPRet)
			}
			expect(t, read(t, svcOnly, request("project", idPRet, "both")), string(directread.RelationshipsDenied), 0)
		})
		t.Run("pull request of an ungranted repository is refused", func(t *testing.T) {
			a := read(t, svcOnly, request("pull_request", idPROther, "in", links))
			expect(t, a, string(directread.RelationshipsDenied), 0)
		})
		t.Run("repository-less completed issue is refused", func(t *testing.T) {
			a := read(t, svcOnly, request("work_item", workItemID(linDone), "out"))
			expect(t, a, string(directread.RelationshipsDenied), 0)
		})
	})
}
