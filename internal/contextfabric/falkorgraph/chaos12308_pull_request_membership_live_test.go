package falkorgraph

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveAPullRequestMembershipFollowsItsLinks is CHAOS-12308's write proof
// on a real FalkorDB:
//
//  1. a stated set and its tier land on the pull request node;
//  2. a write that does not state it removes both, so no stale tier survives;
//  3. a pull request whose LAST link is retracted goes back to unstated: no
//     row names it again, so the projection is the only place that can know;
//  4. a native-tier set stays when a lower-tier link of that pull request is
//     retracted, also when it was written in the same batch: it stands on
//     native links alone, and the retraction runs after the entity writes.
func TestLiveAPullRequestMembershipFollowsItsLinks(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	org := "live-12308-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })

	t0 := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	ref := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	issueA, issueB := ref(contextfabric.SubjectWorkItem, "work_item:A"), ref(contextfabric.SubjectWorkItem, "work_item:B")
	pullP, pullQ, pullR, pullS := ref(contextfabric.SubjectPullRequest, "pull_request:r:1"), ref(contextfabric.SubjectPullRequest, "pull_request:r:2"), ref(contextfabric.SubjectPullRequest, "pull_request:r:3"), ref(contextfabric.SubjectPullRequest, "pull_request:r:4")
	issueC, issueD := ref(contextfabric.SubjectWorkItem, "work_item:C"), ref(contextfabric.SubjectWorkItem, "work_item:D")
	stated := func(subject contextfabric.SubjectRef, tier string, identities ...string) contextfabric.EntityProjection {
		entity := contextfabric.EntityProjection{
			Subject: subject, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}},
			EvidenceRefIDs: []string{"evidence_12308"}, ObservedAt: t0, SourceVersion: "v1",
		}
		if tier != "" {
			entity.ProjectIdentities = &identities
			entity.Properties = map[string]contractsv1.ContextFabricScalarValue{contractsv1.ContextFabricProjectMembershipTierProperty: {String: &tier}}
		}
		return entity
	}
	step := 0
	apply := func(entities []contextfabric.EntityProjection, rels ...contextfabric.RelationshipProjection) {
		t.Helper()
		step++
		if rels == nil {
			rels = []contextfabric.RelationshipProjection{}
		}
		batch := columnEdgeBatch(org, fmt.Sprintf("batch-%04d", step), t0, rels, []contextfabric.ProjectionTombstone{})
		if entities != nil {
			batch.Entities = entities
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	require := func(name string, subject contextfabric.SubjectRef, wantTier string, wantSet ...string) {
		t.Helper()
		n, err := adapter.nodeByKindID(ctx, key, org, string(subject.Kind), subject.CanonicalID, temporalFilter{})
		if err != nil || n == nil {
			t.Fatalf("%s: read %s: %v %v", name, subject.CanonicalID, n, err)
		}
		tier, _ := n.Properties[propProjectMembershipTier].(string)
		if tier != wantTier {
			t.Fatalf("%s: %s tier = %q, want %q", name, subject.CanonicalID, tier, wantTier)
		}
		value, present := n.Properties[propAuthzProjectIdentities]
		if wantSet == nil {
			if present && value != nil {
				t.Fatalf("%s: %s states %v, want unstated", name, subject.CanonicalID, value)
			}
			return
		}
		if fmt.Sprint(value) != fmt.Sprint(wantSet) {
			t.Fatalf("%s: %s states %v, want %v", name, subject.CanonicalID, value, wantSet)
		}
	}

	const project = "project.v2:github:P-1"
	apply([]contextfabric.EntityProjection{stated(pullP, "heuristic", project), stated(pullQ, "native", project), stated(pullR, "native", project), stated(pullS, "explicit_text", project)},
		tieredLink("link-C-S", issueC, pullS, "explicit_text", 2, t0),
		tieredLink("link-A-P", issueA, pullP, "heuristic", 1, t0),
		tieredLink("link-B-Q", issueB, pullQ, "heuristic", 1, t0),
	)
	require("written", pullP, "heuristic", project)
	require("written", pullQ, "native", project)

	apply([]contextfabric.EntityProjection{stated(pullR, "")})
	require("a write that states nothing", pullR, "")

	// A native link of issue A to another pull request retracts A -> P, the
	// LAST link of P. P's own row is gone: nothing else will tell it.
	apply(nil, tieredLink("link-A-R", issueA, pullR, "native", 3, t0))
	require("the last link went", pullP, "")

	// The same from the pull request's side: a native link INTO S retracts the
	// lower-tier link its set stood on, and no statement of S came with it.
	apply(nil, tieredLink("link-D-S", issueD, pullS, "native", 3, t0))
	require("a lower-tier link retracted from the pull request's side", pullS, "")

	// Q gains a native link in the same batch that states its native set; the
	// lower-tier edge into Q goes and the set stays.
	apply([]contextfabric.EntityProjection{stated(pullQ, "native", project)}, tieredLink("link-A-Q", issueA, pullQ, "native", 3, t0))
	require("a native set in the retraction's batch", pullQ, "native", project)
	if got := linkEdges(t, ctx, adapter, key); !reflect.DeepEqual(got, []string{"link-A-Q=native", "link-A-R=native", "link-D-S=native"}) {
		t.Fatalf("links = %v", got)
	}
}

// A walk reads a pull request as a projection map of walkNodeProperties, and
// that map carries the membership AND its tier, so the tier decides walked
// pull requests too.
func TestLiveAWalkReadsAPullRequestsMembershipAndTier(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	org := "live-12308-walk-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })

	observed := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	const wanted, other = "project.v2:linear:proj-1", "project.v2:linear:proj-2"
	pullRequest := func(number, tier string, identities ...string) contextfabric.EntityProjection {
		entity := contextfabric.EntityProjection{
			Subject:        contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:r:" + number, Label: number},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}},
			EvidenceRefIDs: []string{"evidence_12308_" + number}, ObservedAt: observed, SourceVersion: "v1",
		}
		if tier != "" {
			entity.ProjectIdentities = &identities
			entity.Properties = map[string]contractsv1.ContextFabricScalarValue{contractsv1.ContextFabricProjectMembershipTierProperty: {String: &tier}}
		}
		return entity
	}
	batch := columnEdgeBatch(org, "batch-0001", observed, []contextfabric.RelationshipProjection{}, []contextfabric.ProjectionTombstone{})
	batch.Entities = []contextfabric.EntityProjection{pullRequest("1", "native", wanted), pullRequest("2", "native", other), pullRequest("3", "heuristic", wanted), pullRequest("4", "")}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) RETURN %s", labelSubject, propOrgID, propKind, walkProjection("n", walkNodeProperties))
	rows, err := adapter.api.query(ctx, key, cypher, map[string]interface{}{"org": org, "kind": string(contractsv1.ContextFabricSubjectPullRequest)}, true)
	if err != nil {
		t.Fatalf("walk projection read error = %v", err)
	}
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{wanted}, Complete: true}}
	repositoryCaller := storage.Principal{OrgID: org, RepositoryScopes: []string{"acme/api"}}
	projectOnly := storage.Principal{OrgID: org, ProjectScopes: []string{wanted}}
	got := map[string][3]bool{}
	for _, r := range rows {
		n := walkNode(r["n"])
		if n == nil {
			t.Fatalf("a walk row holds no node: %#v", r)
		}
		attributes := toCandidateNode(n).Attributes
		got[propStringValue(n.Properties[propLabel])] = [3]bool{
			graphrank.AuthorizedAttributes(repositoryCaller, narrowed, attributes),
			graphrank.ProjectNarrowingUnproven(narrowed, attributes),
			graphrank.AuthorizedAttributes(projectOnly, contextfabric.RequestedScope{}, attributes),
		}
	}
	want := map[string][3]bool{
		"1": {true, false, true},  // native, a member: proves, and admits a project-only caller
		"2": {false, true, false}, // native, another project: excluded
		"3": {true, true, false},  // lower tier: neither proves nor denies
		"4": {true, true, false},  // unstated
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("admitted, unproven, project-only = %v, want %v", got, want)
	}
}

// A repository walk's fused link read counts the pull requests it crossed on a
// lower tier. The issues here state no membership, so each decides for itself,
// and a pull request of another project is crossed too: it stays admitted.
func TestLiveARepositoryWalkCountsThePullRequestsItCrossedOnALowerTier(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	org := "live-12308-crossed-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })
	const wanted, other = "project.v2:linear:proj-1", "project.v2:linear:proj-2"
	issue := func(id string) string { return "work_item.v2:00000000-0000-0000-0000-000000000000:" + id }
	links := []liveMemberLink{
		{issue: issue("A"), pullRequest: "pull_request:acme/svc:1", slug: memberAnchorSlug, tier: "explicit_text"},
		{issue: issue("B"), pullRequest: "pull_request:acme/svc:2", slug: memberAnchorSlug, tier: "heuristic"},
		{issue: issue("C"), pullRequest: "pull_request:acme/svc:3", slug: memberAnchorSlug, tier: "native"},
		{issue: issue("D"), pullRequest: "pull_request:acme/svc:4", slug: memberAnchorSlug, tier: "explicit_text"},
	}
	entities, relationships := newLiveMemberFixture(org, time.Now().UTC(), nil, links)
	stated := map[string]struct {
		tier, identity string
	}{
		"pull_request:acme/svc:1": {"explicit_text", wanted},
		"pull_request:acme/svc:2": {"heuristic", other},
		"pull_request:acme/svc:3": {"native", wanted},
	}
	for i := range entities.Entities {
		statement, found := stated[entities.Entities[i].Subject.CanonicalID]
		if !found {
			continue
		}
		tier, set := statement.tier, []string{statement.identity}
		entities.Entities[i].ProjectIdentities = &set
		entities.Entities[i].Properties = map[string]contextfabric.ScalarValue{contractsv1.ContextFabricProjectMembershipTierProperty: {String: &tier}}
	}
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	principal := storage.Principal{OrgID: org}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{wanted}, Complete: true}}
	walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, narrowed, anchor, 25)
	if err != nil {
		t.Fatalf("TreeWorkItemMembers() error = %v", err)
	}
	if want := (contextfabric.CrossedPullRequests{ExplicitText: 1, Heuristic: 1}); walk.Crossed != want {
		t.Fatalf("crossed = %+v, want %+v (members %d)", walk.Crossed, want, len(walk.Members))
	}
	plain, err := adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{}, anchor, 25)
	if err != nil {
		t.Fatalf("TreeWorkItemMembers() error = %v", err)
	}
	if plain.Crossed != (contextfabric.CrossedPullRequests{}) {
		t.Fatalf("no narrowing crossed %+v, want none", plain.Crossed)
	}
}

// A walk from a project reaches pull requests as the far side of the link
// read: those count too.
func TestLiveAProjectWalkCountsThePullRequestsItReachedOnALowerTier(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	org := "live-12308-far-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })
	const wanted = "project.v2:linear:proj-1"
	issueID := "work_item.v2:00000000-0000-0000-0000-000000000000:A"
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: wanted, Label: "proj-1"}
	links := []liveMemberLink{{issue: issueID, pullRequest: "pull_request:acme/svc:1", slug: memberAnchorSlug, tier: "explicit_text"}}
	entities, relationships := newLiveMemberFixture(org, time.Now().UTC(), func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		add(anchor, contextfabric.AuthorizationScope{ProjectIDs: []string{"proj-1"}}, "")
		relate("issue_project", contractsv1.ContextFabricRelationshipBelongsToProject, contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: issueID, Label: "issue"}, anchor,
			contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}}, "")
	}, links)
	for i := range entities.Entities {
		if entities.Entities[i].Subject.Kind != contextfabric.SubjectPullRequest {
			continue
		}
		tier, set := "explicit_text", []string{wanted}
		entities.Entities[i].ProjectIdentities = &set
		entities.Entities[i].Properties = map[string]contextfabric.ScalarValue{contractsv1.ContextFabricProjectMembershipTierProperty: {String: &tier}}
	}
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{wanted}, Complete: true}}
	walk, err := adapter.treeMembers(ctx, key, org, storage.Principal{OrgID: org}, narrowed, anchor, treePullRequest, 25, temporalFilter{})
	if err != nil {
		t.Fatalf("treeMembers() error = %v", err)
	}
	if len(walk.linkSubjects) != 1 {
		t.Fatalf("premise: the walk reached %d pull requests, want 1", len(walk.linkSubjects))
	}
	if got := crossedPullRequests(walk.crossedLowerTier); got != (contextfabric.CrossedPullRequests{ExplicitText: 1}) {
		t.Fatalf("crossed = %+v, want the far-side pull request", got)
	}
}
