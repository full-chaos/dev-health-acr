package devhealthfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLinkAdmittedWorkItemsHaveTheirOwnFactsReadUnderTheGrantsOnRealStores:
// the work items a repository walk admits under a requested repository scope
// (the link predicate of the walk) are read for their own facts through the
// REAL fact registry and the REAL dev-health-go library readers, without the
// requested repository selector and under the caller's grants. Seeded
// ClickHouse rows, projected by the REAL producers into a REAL FalkorDB, walked
// by the REAL falkorgraph walk. Needs Docker; run by CI.
//
// Rows that fail on the code before the change (the fact read applied the
// requested selector to the work item's own repository, so a link-admitted
// member was served with no facts): (i) and (iii).
func TestLinkAdmittedWorkItemsHaveTheirOwnFactsReadUnderTheGrantsOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := oracleClickHouse(t, ctx)
	adapter := oracleFalkor(t, ctx)
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-30 * 24 * time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	repoIDs := map[string]string{}
	for _, slug := range []string{"acme/svc", "acme/secret", "acme/out"} {
		repoIDs[slug] = oracleUUID("link-scoped " + slug)
		exec("repo "+slug, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoIDs[slug], oracleOrg, slug, "github", now)
		exec("pull request "+slug, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			repoIDs[slug], oracleOrg, uint32(1), "Change", "merged", created, now)
	}
	items := []struct{ id, slug, provider, itemType, status string }{
		{"linear:CHAOS-21", "", "linear", "issue", "In Progress"}, // repository-less, native to acme/svc#1 and to acme/secret#1
		{"linear:CHAOS-22", "", "linear", "issue", "Todo"},        // repository-less, native to acme/out#1 only
		{"gh:acme/out#5", "acme/out", "github", "issue", "open"},  // own repository acme/out, linked by text to acme/svc#1
	}
	itemRepo := map[string]string{}
	for _, i := range items {
		repoID := oracleZeroRepo
		if i.slug != "" {
			repoID = repoIDs[i.slug]
		}
		itemRepo[i.id] = repoID
		exec("work item "+i.id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.id, repoID, oracleOrg, "Title of "+i.id, i.itemType, i.status, i.provider, "", created, now, now)
	}
	for n, l := range []struct{ issue, slug, tier string }{
		{"linear:CHAOS-21", "acme/svc", "native"},
		{"linear:CHAOS-21", "acme/secret", "native"},
		{"linear:CHAOS-22", "acme/out", "native"},
		{"gh:acme/out#5", "acme/svc", "explicit_text"},
	} {
		exec(fmt.Sprintf("link %d", n), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoIDs[l.slug], l.issue, uint32(1), float32(0.9), l.tier, "", now, oracleOrg)
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	oracleDrain(t, ctx, source, devhealthsource.SourceName, adapter)

	registry, err := contextfabric.NewFactCapabilityRegistry(NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(id string) string { return oracleCanonical(t, itemRepo[id], id) }
	anchor := func(slug string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoIDs[slug], Label: slug}
	}
	// walk is the repository walk of one request; read reads its members' own
	// facts through the registry, with the members the walk admitted passed in
	// as link-scoped (what the engine passes), or with none (linked=false).
	walk := func(grants, scope []string, slug string) (storage.Principal, contextfabric.TreeWorkItemWalk) {
		t.Helper()
		principal := storage.Principal{OrgID: oracleOrg, Subject: "u", CredentialID: "c", RepositoryScopes: grants}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		w, err := adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{RepositorySlugs: scope}, anchor(slug), 100)
		if err != nil {
			t.Fatal(err)
		}
		return principal, w
	}
	read := func(principal storage.Principal, scope []string, subjects []contextfabric.SubjectRef, linked bool) map[string]string {
		t.Helper()
		request := contextfabric.CanonicalFactRequest{
			Question:                 contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
			Subjects:                 subjects,
			Requirements:             []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}, {Kind: contextfabric.FactWork}},
			RequestedRepositoryScope: scope,
		}
		_ = linked // main has no link-admitted set: the requested selector is applied to every read
		bundle, err := registry.ReadFacts(ctx, principal, request)
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		statuses := map[string]string{}
		for _, fact := range bundle.Facts {
			if fact.Kind != contextfabric.FactStatus {
				continue
			}
			value := fact.Fields["status"]
			if value.String != nil {
				statuses[fact.Subject.CanonicalID] = *value.String
			}
		}
		encoded, _ := json.Marshal(bundle)
		statuses["__bundle"] = string(encoded)
		return statuses
	}
	members := func(w contextfabric.TreeWorkItemWalk) ([]contextfabric.SubjectRef, string) {
		var subjects []contextfabric.SubjectRef
		var tiers []string
		for _, m := range w.Members {
			subjects = append(subjects, m.Subject)
			tiers = append(tiers, m.Subject.CanonicalID+"="+m.Tier)
		}
		sort.Strings(tiers)
		return subjects, strings.Join(tiers, ",")
	}

	t.Run("(i) org-wide caller, scope on the pull request's repository: members and their facts", func(t *testing.T) {
		scope := []string{"acme/svc"}
		principal, w := walk(nil, scope, "acme/svc")
		subjects, tiers := members(w)
		want := strings.Join(oracleSorted([]string{canonical("linear:CHAOS-21") + "=native", canonical("gh:acme/out#5") + "=explicit_text"}), ",")
		if tiers != want {
			t.Fatalf("members %s, want %s: the repository-less issue by its native link, the acme/out issue by its text link", tiers, want)
		}
		statuses := read(principal, scope, subjects, true)
		if statuses[canonical("linear:CHAOS-21")] != "In Progress" || statuses[canonical("gh:acme/out#5")] != "open" {
			t.Fatalf("statuses %v, want both members' own status served", statuses)
		}
		// The library's requested selector alone (no link-admitted set) tests
		// the work item's own repository: both are withheld. This is the
		// relation the walk no longer uses.
		if withheld := read(principal, scope, subjects, false); withheld[canonical("linear:CHAOS-21")] != "" || withheld[canonical("gh:acme/out#5")] != "" {
			t.Fatalf("own-repository selector served %v, want both withheld (the library rule)", withheld)
		}
	})

	t.Run("(ii) the issue linked only outside the scope: no member, no facts", func(t *testing.T) {
		scope := []string{"acme/svc"}
		principal, w := walk(nil, scope, "acme/svc")
		for _, m := range w.Members {
			if m.Subject.CanonicalID == canonical("linear:CHAOS-22") {
				t.Fatalf("the issue linked only to acme/out is a member of acme/svc")
			}
		}
		subjects, _ := members(w)
		statuses := read(principal, scope, append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonical("linear:CHAOS-22")}), false)
		if statuses[canonical("linear:CHAOS-22")] != "" {
			t.Fatalf("the issue linked only outside the scope has a served status %q", statuses[canonical("linear:CHAOS-22")])
		}
	})

	t.Run("(iii) restricted caller granted the scoped repository: facts by today's grant rule, nothing of the other repository", func(t *testing.T) {
		scope := []string{"acme/svc"}
		principal, w := walk([]string{"acme/svc"}, scope, "acme/svc")
		subjects, tiers := members(w)
		if tiers != canonical("linear:CHAOS-21")+"=native" {
			t.Fatalf("members %s, want only the repository-less issue natively linked to the granted repository (the acme/out issue's own repository is not granted)", tiers)
		}
		statuses := read(principal, scope, subjects, true)
		if statuses[canonical("linear:CHAOS-21")] != "In Progress" {
			t.Fatalf("statuses %v, want the member's own status under the grant of its native link", statuses)
		}
		encodedWalk, _ := json.Marshal(w)
		for _, revealing := range []string{"acme/secret", repoIDs["acme/secret"]} {
			if strings.Contains(statuses["__bundle"], revealing) || strings.Contains(string(encodedWalk), revealing) {
				t.Fatalf("the served facts or the walk reveal %q, a repository the caller is not granted", revealing)
			}
		}
	})

	t.Run("(iv) restricted caller, scope on a repository it is not granted: empty", func(t *testing.T) {
		for _, slug := range []string{"acme/secret", "acme/svc"} {
			_, w := walk([]string{"acme/svc"}, []string{"acme/secret"}, slug)
			if len(w.Members) != 0 {
				t.Fatalf("anchor %s under an ungranted scope: members %v, want none", slug, w.Members)
			}
		}
	})
}
