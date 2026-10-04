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

// TestIssuePullRequestLinkServedReadAuthorizationOnRealStores reads the
// LINKS_PULL_REQUEST edge through the SERVED path of read_relationships
// (directread.RelationshipsReader over the real falkorgraph.Adapter, the same
// composition the route uses) as repository-restricted callers, after the
// REAL producer projected seeded ClickHouse rows into a REAL FalkorDB.
//
// What the served path does with an edge whose end node the caller may not
// see: the edge is DROPPED, and only counted in withheld.edges_not_visible
// (directread/read_relationships.go gateEdges -> EdgeGate in edge_gate.go).
// A root the caller may not see is refused whole: status "denied", no edges.
// The served edge carries the tier in provenance.link_tier (the stored
// property_link_provenance); the stored edge is also read through
// DirectEdgePage as a seed guard, so the test cannot pass on an empty store.
//
// A repository-less issue (zero UUID repo_id) is stored with the authorization
// sentinel "acr-context-fabric:no-repository". read_relationships has NO
// "admitted by its link to a granted pull request" rule (that rule lives only
// in falkorgraph/tree_walk.go admitted()), so a restricted caller never sees
// that issue or its edge through this tool, from either side.
//
// Needs Docker (ClickHouse and FalkorDB containers). Written to be run by CI
// or by the lane owner; not run in the authoring sandbox.
func TestIssuePullRequestLinkServedReadAuthorizationOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	applyProductionSchema(t, ctx, direct, "")
	adapter := chaos7074FalkorAdapter(t, ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-90 * 24 * time.Hour)
	orgID := "13000000-0000-4000-8000-000000000003"
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

	const (
		issueOwn, issueLess, issueCross = "gh:acme/svc#1", "linear:LESS-1", "gh:acme/svc#2"
		prSvcNumber, prOtherNumber      = 11, 21
	)
	issueRepo := map[string]string{issueOwn: repoSvc, issueLess: zeroRepo, issueCross: repoSvc}
	for id, repo := range issueRepo {
		provider := "github"
		if repo == zeroRepo {
			provider = "linear"
		}
		exec("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repo, orgID, id, "issue", "open", provider, created, now, now)
	}
	for _, p := range []struct {
		repo   string
		number uint32
	}{{repoSvc, prSvcNumber}, {repoOther, prOtherNumber}} {
		exec(fmt.Sprintf("pull request %d", p.number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.repo, orgID, p.number, fmt.Sprintf("PR %d", p.number), "open", created, now)
	}
	type seededLink struct {
		issue, tier string
		prRepo      string
		number      uint32
	}
	for i, l := range []seededLink{
		{issueOwn, "native", repoSvc, prSvcNumber},
		{issueLess, "native", repoSvc, prSvcNumber},
		{issueCross, "explicit_text", repoOther, prOtherNumber},
	} {
		exec(fmt.Sprintf("link %d", i), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			l.prRepo, l.issue, l.number, float32(0.9), l.tier, "", now.Add(time.Duration(i)*time.Minute), orgID)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, source, adapter, orgID, devhealthsource.SourceName)

	workItemID := func(id string) string {
		out, _, err := identity.Derive(identity.KindWorkItem, []string{issueRepo[id], id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	idOwn, idLess, idCross := workItemID(issueOwn), workItemID(issueLess), workItemID(issueCross)
	idPRSvc := fmt.Sprintf("pull_request:%s:%d", repoSvc, prSvcNumber)
	idPROther := fmt.Sprintf("pull_request:%s:%d", repoOther, prOtherNumber)
	// The three edges, by endpoints, with the tier each must carry.
	type edgeWant struct{ key, tier, rank string }
	edgeOwn := edgeWant{idOwn + " -> " + idPRSvc, "native", "3"}
	edgeLess := edgeWant{idLess + " -> " + idPRSvc, "native", "3"}
	edgeCross := edgeWant{idCross + " -> " + idPROther, "explicit_text", "2"}
	all := []edgeWant{edgeOwn, edgeLess, edgeCross}

	// Seed guard (unrestricted adapter read, no gate): all three stored edges
	// with their tier and rank. The seed or projection producing less than this
	// fails the test, so it cannot pass on an empty store. The tier assertion
	// itself is on the served path below.
	unrestricted := storage.Principal{OrgID: orgID, Subject: "link", CredentialID: "link"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, unrestricted)
	if err != nil {
		t.Fatalf("resolve graph binding: %v", err)
	}
	stored, err := adapter.DirectEdgePage(ctx, unrestricted, binding, directread.EdgePageQuery{
		Origins: []contextfabric.SubjectRef{
			{Kind: contextfabric.SubjectPullRequest, CanonicalID: idPRSvc},
			{Kind: contextfabric.SubjectPullRequest, CanonicalID: idPROther},
		},
		Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 100, ValidAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("stored edge read: %v", err)
	}
	storedTier := map[string]string{} // relationship id -> tier/rank
	storedKey := map[string]string{}  // relationship id -> from -> to
	for _, e := range stored.Edges {
		key := e.From.Subject.CanonicalID + " -> " + e.To.Subject.CanonicalID
		storedKey[e.Key.RelationshipID] = key
		storedTier[e.Key.RelationshipID] = fmt.Sprint(e.Attributes["property_link_provenance"]) + "/" + fmt.Sprint(e.Attributes["property_link_provenance_rank"])
	}
	if len(stored.Edges) != len(all) {
		t.Fatalf("stored LINKS_PULL_REQUEST edges = %d, want %d (seed or projection produced too little): %v", len(stored.Edges), len(all), storedKey)
	}
	for _, w := range all {
		found := false
		for id, key := range storedKey {
			if key == w.key {
				found = true
				if storedTier[id] != w.tier+"/"+w.rank {
					t.Fatalf("stored edge %s tier/rank = %q, want %s/%s", w.key, storedTier[id], w.tier, w.rank)
				}
			}
		}
		if !found {
			t.Fatalf("stored edge %s missing: %v", w.key, storedKey)
		}
	}

	keyring := directread.CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}}
	reader, err := directread.NewRelationshipsReader(directread.NewSubjectGate(adapter, nil), adapter, nil, keyring)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	read := func(t *testing.T, principal storage.Principal, kind, id, direction string) directread.RelationshipsResponse {
		t.Helper()
		calls++
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", t.Name(), calls)))
		response, err := reader.Read(observability.WithRequestID(ctx, "req_"+hex.EncodeToString(sum[:16])), principal, directread.RelationshipsRequest{
			Subject: directread.RelationshipsSubject{Kind: kind, CanonicalID: id},
			Types:   []string{"LINKS_PULL_REQUEST"}, Direction: direction, Limit: 100,
		})
		if err != nil {
			t.Fatalf("read_relationships %s %s: %v", kind, id, err)
		}
		return response
	}

	type readCase struct {
		name      string
		kind, id  string
		direction string
		// want: the edges served; hidden: the subject ids that must not appear
		// anywhere in the served edges; denied: the root itself is refused.
		want     []edgeWant
		hidden   []string
		denied   bool
		withheld int // edges_not_visible when the root is admitted
	}
	pr := func(id, dir string) readCase { return readCase{kind: "pull_request", id: id, direction: dir} }
	iss := func(id string) readCase { return readCase{kind: "work_item", id: id, direction: "out"} }
	with := func(c readCase, name string, mut func(*readCase)) readCase {
		c.name = name
		mut(&c)
		return c
	}
	run := func(t *testing.T, principal storage.Principal, cases []readCase) {
		t.Helper()
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				response := read(t, principal, c.kind, c.id, c.direction)
				if c.denied {
					if response.Status != directread.RelationshipsDenied || response.Reason != directread.RelationshipsRefusalDeniedOrNotFound || len(response.Edges) != 0 {
						t.Fatalf("root %s: status=%q reason=%q edges=%d, want a denied root with no edges", c.id, response.Status, response.Reason, len(response.Edges))
					}
					return
				}
				if response.Status != directread.RelationshipsComplete {
					t.Fatalf("status = %q, want complete", response.Status)
				}
				got := []string{}
				for _, e := range response.Edges {
					key := e.From.CanonicalID + " -> " + e.To.CanonicalID
					got = append(got, key)
					if e.Type != "LINKS_PULL_REQUEST" {
						t.Errorf("edge %s type = %q", key, e.Type)
					}
					// The tier is asserted THROUGH THE SERVED PATH: the served
					// edge's provenance.link_tier, matched by endpoints.
					var tier string
					for _, w := range all {
						if w.key == key {
							tier = w.tier
						}
					}
					if tier == "" || e.Provenance.LinkTier != tier {
						t.Errorf("served edge %s (%s): provenance.link_tier = %q, want %q", key, e.RelationshipID, e.Provenance.LinkTier, tier)
					}
				}
				want := []string{}
				for _, w := range c.want {
					want = append(want, w.key)
				}
				sort.Strings(got)
				sort.Strings(want)
				if strings.Join(got, "|") != strings.Join(want, "|") {
					t.Fatalf("served edges\n got  %v\n want %v", got, want)
				}
				if response.Withheld.EdgesNotVisible != c.withheld {
					t.Errorf("withheld.edges_not_visible = %d, want %d", response.Withheld.EdgesNotVisible, c.withheld)
				}
				raw, err := json.Marshal(response.Edges)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range c.hidden {
					if strings.Contains(string(raw), id) {
						t.Errorf("served edges name %s, which the caller may not see: %s", id, raw)
					}
				}
			})
		}
	}

	t.Run("unrestricted caller sees all three edges", func(t *testing.T) {
		run(t, unrestricted, []readCase{
			with(pr(idPRSvc, "in"), "pr svc side", func(c *readCase) { c.want = []edgeWant{edgeOwn, edgeLess} }),
			with(pr(idPROther, "in"), "pr other side", func(c *readCase) { c.want = []edgeWant{edgeCross} }),
			with(iss(idOwn), "issue own side", func(c *readCase) { c.want = []edgeWant{edgeOwn} }),
			with(iss(idLess), "issue repo-less side", func(c *readCase) { c.want = []edgeWant{edgeLess} }),
			with(iss(idCross), "issue cross side", func(c *readCase) { c.want = []edgeWant{edgeCross} }),
		})
	})

	// A grant of acme/svc, written three ways, behaves alike for the svc edges.
	for _, grant := range []string{"acme/svc", "ACME/Svc", "acme/*"} {
		grant := grant
		t.Run("granted "+grant, func(t *testing.T) {
			principal := storage.Principal{OrgID: orgID, Subject: "link", CredentialID: "link", RepositoryScopes: []string{grant}}
			wildcard := grant == "acme/*"
			cross := with(iss(idCross), "issue cross side", func(c *readCase) {
				if wildcard { // acme/other is granted too: the edge and its pull request are visible
					c.want = []edgeWant{edgeCross}
				} else { // the edge and the pull request are acme/other's: dropped, counted
					c.hidden, c.withheld = []string{idPROther}, 1
				}
			})
			prOther := with(pr(idPROther, "in"), "pr other side", func(c *readCase) {
				if wildcard {
					c.want = []edgeWant{edgeCross}
				} else { // the root is acme/other's: refused whole
					c.denied = true
				}
			})
			run(t, principal, []readCase{
				// svc edge visible, with its tier; the repo-less issue's edge is dropped.
				with(pr(idPRSvc, "in"), "pr svc side", func(c *readCase) {
					c.want, c.hidden, c.withheld = []edgeWant{edgeOwn}, []string{idLess}, 1
				}),
				with(iss(idOwn), "issue own side", func(c *readCase) { c.want = []edgeWant{edgeOwn} }),
				// the repo-less issue is refused as a root: no rule of read_relationships admits it.
				with(iss(idLess), "issue repo-less side", func(c *readCase) { c.denied = true }),
				cross, prOther,
			})
		})
	}

	t.Run("granted acme/other only", func(t *testing.T) {
		principal := storage.Principal{OrgID: orgID, Subject: "link", CredentialID: "link", RepositoryScopes: []string{"acme/other"}}
		run(t, principal, []readCase{
			// every svc root is refused: none of the svc edges or endpoints is served.
			with(pr(idPRSvc, "in"), "pr svc side", func(c *readCase) { c.denied = true }),
			with(iss(idOwn), "issue own side", func(c *readCase) { c.denied = true }),
			with(iss(idLess), "issue repo-less side", func(c *readCase) { c.denied = true }),
			with(iss(idCross), "issue cross side", func(c *readCase) { c.denied = true }),
			// the acme/other pull request is the caller's, its issue is acme/svc's:
			// the edge needs BOTH ends, so it is dropped and counted, the issue never named.
			with(pr(idPROther, "in"), "pr other side", func(c *readCase) { c.hidden, c.withheld = []string{idCross}, 1 }),
		})
	})
}
