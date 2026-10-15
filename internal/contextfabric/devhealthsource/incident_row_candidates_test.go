package devhealthsource

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestIncidentRowCandidatesProjectWhatAFromZeroReadProjects(t *testing.T) {
	observed := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	changed := observed.Add(48 * time.Hour)
	const (
		incidentID = "8e4b1d70-2c9a-4f63-b815-000000000001"
		repoA      = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a01"
		repoB      = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a02"
		repoC      = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a03"
	)
	base := incidentRow{id: incidentID, title: "checkout timeouts", observedAt: observed, cursorAt: changed}
	edgeTo := func(repoID string) string { return incidentRepositoryRelationshipID("incident:"+incidentID, repoID) }

	two := base
	two.activeRepos = parseIncidentRepos([]string{repoA + "|acme/checkout", repoB + "|acme/ledger"})
	two.endedRepos = []string{repoB, repoC}
	var stats incidentPageStats
	out := incidentRowCandidates(two, &stats)
	var entities, edges, retracted []string
	var authorization []string
	for _, c := range out {
		switch {
		case c.entity != nil:
			entities = append(entities, c.entity.Subject.CanonicalID)
			authorization = c.entity.Authorization.RepositorySlugs
		case c.relationship != nil:
			edges = append(edges, c.relationship.RelationshipID+">"+c.relationship.To.CanonicalID)
		case c.tombstone != nil:
			if c.tombstone.Kind != "relationship" || !c.tombstone.EffectiveAt.Equal(changed) {
				t.Fatalf("tombstone %+v, want a relationship retraction effective at the change", c.tombstone)
			}
			retracted = append(retracted, c.tombstone.CanonicalID)
		}
		if c.sortKey != incidentID {
			t.Fatalf("candidate sort key %q, want the incident id", c.sortKey)
		}
	}
	if len(entities) != 1 {
		t.Fatalf("entities %v, want the incident once", entities)
	}
	if !slices.Equal(authorization, []string{"acme/checkout", "acme/ledger"}) {
		t.Fatalf("authorization %v, want every repository an active mapping names", authorization)
	}
	if want := []string{edgeTo(repoA) + ">repository:" + repoA, edgeTo(repoB) + ">repository:" + repoB}; !slices.Equal(edges, want) {
		t.Fatalf("edges %v, want %v", edges, want)
	}
	if edgeTo(repoA) == edgeTo(repoB) {
		t.Fatal("two repositories of one incident share an edge id")
	}
	if !slices.Equal(retracted, []string{edgeTo(repoC)}) || stats.edgesRetracted != 1 {
		t.Fatalf("retracted %v (counted %d), want only the edge to repository:%s, which no active mapping names", retracted, stats.edgesRetracted, repoC)
	}

	unmapped := base
	unmapped.endedRepos = []string{repoA}
	stats = incidentPageStats{}
	out = incidentRowCandidates(unmapped, &stats)
	if len(out) != 1 || out[0].tombstone == nil || out[0].tombstone.Kind != "incident" || out[0].tombstone.CanonicalID != "incident:"+incidentID || out[0].tombstone.Reason != "unmapped" || stats.unmapped != 1 {
		t.Fatalf("no active mapping names a known repository: %+v (counted %d), want the incident retracted as unmapped", out, stats.unmapped)
	}

	late := unmapped
	late.observedAt = changed.Add(time.Hour)
	if out := incidentRowCandidates(late, &stats); len(out) != 1 || out[0].tombstone == nil || !out[0].tombstone.EffectiveAt.Equal(late.observedAt) {
		t.Fatalf("an incident observed after the change: %+v, want the retraction effective at its observed time, so the backend does not refuse it", out)
	}
	lateEdge := two
	lateEdge.observedAt = changed.Add(time.Hour)
	for _, c := range incidentRowCandidates(lateEdge, &stats) {
		if c.tombstone != nil && !c.tombstone.EffectiveAt.Equal(lateEdge.observedAt) {
			t.Fatalf("edge retraction effective %v, want the incident's later observed time %v", c.tombstone.EffectiveAt, lateEdge.observedAt)
		}
	}

	deleted := two
	deleted.deleted = true
	if out := incidentRowCandidates(deleted, &stats); len(out) != 1 || out[0].tombstone == nil || out[0].tombstone.Reason != "source_deleted" {
		t.Fatalf("a deleted incident: %+v, want only the source_deleted tombstone", out)
	}

	if got := parseIncidentRepos([]string{"|acme/x", repoA + "|", repoA + "|acme/checkout"}); len(got) != 1 || got[0].id != repoA || got[0].slug != "acme/checkout" {
		t.Fatalf("parseIncidentRepos kept %+v, want only the complete entry", got)
	}
}

func TestIncidentRowMemoKeyChangesWithTheMappingSet(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	a := candidate{table: "operational_incidents", observedAt: at, sortKey: "incident-1", memoVersion: "111"}
	b := a
	b.memoVersion = "222"
	if rowMemoKey(a) == rowMemoKey(b) {
		t.Fatal("two mapping sets of one incident at one keyset place share a memo key: a late mapping write would be dropped")
	}
	plain := candidate{table: "repos", observedAt: at, sortKey: "repo-1"}
	if rowMemoKey(plain) != "repos\x00"+strconv.FormatInt(at.UnixNano(), 10)+"|repo-1" {
		t.Fatalf("memo key of a row without a version changed: %q", rowMemoKey(plain))
	}
}

// An incident two active mappings tie to two repositories is read by a caller
// scoped to either one, under the one existing rule for a multi-repository
// authorization (graphrank.AuthorizedAttributes: any of the caller's
// repositories in the list), and by no caller scoped to neither.
func TestIncidentOfTwoRepositoriesIsReadByACallerOfEither(t *testing.T) {
	row := incidentRow{
		id: "8e4b1d70-2c9a-4f63-b815-000000000002", title: "ledger drift", observedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		activeRepos: parseIncidentRepos([]string{"6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a01|acme/checkout", "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a02|acme/ledger"}),
	}
	var stats incidentPageStats
	var slugs []string
	for _, c := range incidentRowCandidates(row, &stats) {
		if c.entity != nil {
			slugs = c.entity.Authorization.RepositorySlugs
		}
	}
	attributes := map[string]interface{}{"authorization_repositories": slugs}
	for scope, want := range map[string]bool{"acme/checkout": true, "acme/ledger": true, "acme/payments": false} {
		principal := storage.Principal{OrgID: "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0b09", RepositoryScopes: []string{scope}}
		if got := graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, attributes); got != want {
			t.Errorf("a caller scoped to %s reads the incident authorized for %v: %v, want %v", scope, slugs, got, want)
		}
	}
}
