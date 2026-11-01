package falkorgraph

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// nodeEdgeBoundFixture is one repository with 3 owners, 5 pull requests and 30 CI
// runs, and one team that owns it beside 12 work items. Relationship ids are
// chosen so the id order alone puts the rare edges LAST: a limit with no tier
// would keep the CI runs and lose the owners.
func nodeEdgeBoundFixture(t *testing.T, ctx context.Context, adapter *Adapter) (orgID string, repository, team contextfabric.SubjectRef) {
	t.Helper()
	orgID = "live-12451-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	created := now.Add(-48 * time.Hour)
	subject := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/big"}}
	entities := []contextfabric.EntityProjection{}
	add := func(s contextfabric.SubjectRef) contextfabric.SubjectRef {
		entities = append(entities, contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_" + s.CanonicalID}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"})
		return s
	}
	relationships := []contextfabric.RelationshipProjection{}
	relate := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef) {
		relationships = append(relationships, contextfabric.RelationshipProjection{RelationshipID: id, Type: typ, From: from, To: to, Derivation: contextfabric.DerivationCanonicalStructured,
			EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"})
	}
	repository = add(subject(contextfabric.SubjectRepository, "repository-big"))
	team = add(subject(contextfabric.SubjectTeam, "team-big"))
	for i := 1; i <= 30; i++ {
		relate(fmt.Sprintf("rel_a_ci_%03d", i), contractsv1.ContextFabricRelationshipBelongsToRepository, add(subject(contractsv1.ContextFabricSubjectCIRun, fmt.Sprintf("ci-run-%03d", i))), repository)
	}
	for i := 1; i <= 5; i++ {
		relate(fmt.Sprintf("rel_m_pr_%03d", i), contractsv1.ContextFabricRelationshipBelongsToRepository, add(subject(contextfabric.SubjectPullRequest, fmt.Sprintf("pull_request-%03d", i))), repository)
	}
	relate("rel_z_owner_1", contractsv1.ContextFabricRelationshipOwnedByTeam, repository, team)
	for _, name := range []string{"team-two", "team-three"} {
		relate("rel_z_owner_"+name, contractsv1.ContextFabricRelationshipOwnedByTeam, repository, add(subject(contextfabric.SubjectTeam, name)))
	}
	for i := 1; i <= 12; i++ {
		relate(fmt.Sprintf("rel_b_wi_%03d", i), contractsv1.ContextFabricRelationshipOwnedByTeam, add(subject(contextfabric.SubjectWorkItem, fmt.Sprintf("work_item-%03d", i))), team)
	}
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor, e.Entities = "batch_12451_0001", "", "cursor-1", entities
	r.BatchID, r.Cursor, r.NextCursor, r.Relationships = "batch_12451_0002", "cursor-1", "cursor-2", relationships
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	return orgID, repository, team
}

func edgeIDs(t *testing.T, adapter *Adapter, ctx context.Context, orgID string, subject contextfabric.SubjectRef) ([]string, bool) {
	t.Helper()
	key := graphKey(adapter.config.GraphPrefix, orgID)
	edges, cut, err := adapter.edgesOfNode(ctx, key, orgID, subjectUUID(string(subject.Kind), subject.CanonicalID), temporalFilter{})
	if err != nil {
		t.Fatalf("edgesOfNode() error = %v", err)
	}
	ids := make([]string, 0, len(edges))
	for _, e := range edges {
		ids = append(ids, e.UUID)
	}
	return ids, cut
}

func countPrefix(ids []string, prefix string) int {
	n := 0
	for _, id := range ids {
		if strings.Contains(id, prefix) {
			n++
		}
	}
	return n
}

// TestLiveANodeEdgeReadIsBoundedAndKeepsTheRareEdges (CHAOS-12451): the read
// stops at its limit, says so, and the limit cannot cost a node its owners or
// its repositories, whatever the id order.
func TestLiveANodeEdgeReadIsBoundedAndKeepsTheRareEdges(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, repository, team := nodeEdgeBoundFixture(t, ctx, adapter)

	adapter.config.NodeEdgeReadLimit = 10
	ids, cut := edgeIDs(t, adapter, ctx, orgID, repository)
	if len(ids) != 10 || !cut {
		t.Fatalf("read %d edges, cut %v; want 10 and cut: 38 edges exist", len(ids), cut)
	}
	if got := countPrefix(ids, "rel_z_owner"); got != 3 {
		t.Fatalf("owner edges %d in %v, want all 3: they sort last by id and must survive the cut", got, ids)
	}
	if got := countPrefix(ids, "rel_m_pr"); got != 5 {
		t.Fatalf("pull request edges %d in %v, want all 5: a CI run only fills what is left", got, ids)
	}
	if got := countPrefix(ids, "rel_a_ci"); got != 2 {
		t.Fatalf("CI run edges %d in %v, want the 2 left", got, ids)
	}
	again, _ := edgeIDs(t, adapter, ctx, orgID, repository)
	if !slices.Equal(ids, again) {
		t.Fatalf("two reads differ: %v and %v", ids, again)
	}

	// The bound is in the query, not only in the code that reads the rows: that
	// is what keeps the rows out of FalkorDB's memory.
	raw, err := adapter.api.query(ctx, graphKey(adapter.config.GraphPrefix, orgID), nodeEdgesCypher(temporalFilter{}),
		map[string]interface{}{"org": orgID, "kind": string(repository.Kind), "id": repository.CanonicalID, "lim": 11}, true)
	if err != nil {
		t.Fatalf("query() error = %v", err)
	}
	if len(raw) != 11 {
		t.Fatalf("the statement returned %d rows, want 11: it must stop at $lim", len(raw))
	}

	adapter.config.NodeEdgeReadLimit = 4
	teamIDs, teamCut := edgeIDs(t, adapter, ctx, orgID, team)
	if !teamCut || len(teamIDs) != 4 || !slices.ContainsFunc(teamIDs, func(id string) bool { return strings.Contains(id, "rel_z_owner_1") }) {
		t.Fatalf("team read %v, cut %v: the repository the team owns must survive its work items", teamIDs, teamCut)
	}
}

// TestLiveANodeWithExactlyTheLimitIsNotCut: the read asks for one row past the
// limit, so a node that fits is complete and says nothing.
func TestLiveANodeWithExactlyTheLimitIsNotCut(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, repository, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	for limit, want := range map[int]bool{38: false, 37: true} {
		adapter.config.NodeEdgeReadLimit = limit
		ids, cut := edgeIDs(t, adapter, ctx, orgID, repository)
		if cut != want || len(ids) != limit {
			t.Fatalf("limit %d: %d edges, cut %v; want %d and %v", limit, len(ids), cut, limit, want)
		}
	}
}

// TestLiveACutNodeReadIsDisclosed (CHAOS-12451, lead ruling): a read that hits
// the limit is a cut read, disclosed through graph_hop_walk_truncated, even with
// walk budget left. The walk budget here is far above the limit, so nothing else
// can report it. A read that fits discloses nothing. The two readers are proved
// apart: a committed subject the question text does not name reaches only the
// hop walk, and a subject found by text with nothing committed reaches only the
// text arm, so neither can hide the other.
func TestLiveACutNodeReadIsDisclosed(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, repository, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	adapter.config.MaxResults = 500
	read := func(t *testing.T, limit int, question string, committed []contextfabric.SubjectRef) contextfabric.Coverage {
		t.Helper()
		adapter.config.NodeEdgeReadLimit = limit
		principal := storage.Principal{OrgID: orgID}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatalf("ResolveInvestigationBinding() error = %v", err)
		}
		current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		gc, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
			Request: contextfabric.InvestigationRequest{SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12451", Question: question, TimeContext: current,
				Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100, MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144},
				Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"}},
			Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{}, TimeContext: current},
			Resolution:     contextfabric.SubjectResolution{Committed: committed, Candidates: []contextfabric.SubjectCandidate{}},
			Binding:        binding,
		})
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		return gc.Coverage
	}
	hasCut := func(c contextfabric.Coverage) bool {
		return slices.ContainsFunc(c.Details, func(d contextfabric.CoverageDetail) bool {
			return d.Code == contractsv1.ContextFabricCoverageDetailGraphHopWalkTruncated
		})
	}
	for _, tc := range []struct {
		name      string
		question  string
		committed []contextfabric.SubjectRef
	}{
		{"the hop walk alone", "how are things?", []contextfabric.SubjectRef{repository}},
		{"the text arm alone", "what about repository-big?", []contextfabric.SubjectRef{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if c := read(t, 10, tc.question, tc.committed); !hasCut(c) || !c.Partial {
				t.Fatalf("limit 10: partial %v details %+v, want the cut disclosed", c.Partial, c.Details)
			}
			if c := read(t, 1000, tc.question, tc.committed); hasCut(c) {
				t.Fatalf("limit 1000: details %+v, want no cut: every edge was read and admitted", c.Details)
			}
		})
	}
}

// TestAnObservationReadCutAtItsLimitIsAFailedTraversal: the traversal has no
// other way to say its parent may be past the cut, and it must not lose it in
// silence.
func TestAnObservationReadCutAtItsLimitIsAFailedTraversal(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, repository, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	key := graphKey(adapter.config.GraphPrefix, orgID)
	uuid := subjectUUID(string(repository.Kind), repository.CanonicalID)
	adapter.config.NodeEdgeReadLimit = 10
	if _, err := adapter.observationEdges(ctx, key, orgID, uuid, temporalFilter{}); err == nil {
		t.Fatal("a cut read returned no error")
	}
	adapter.config.NodeEdgeReadLimit = 1000
	if edges, err := adapter.observationEdges(ctx, key, orgID, uuid, temporalFilter{}); err != nil || len(edges) != 38 {
		t.Fatalf("a read that fits: %d edges, error %v; want 38 and none", len(edges), err)
	}
}

// TestLiveACutPastTheServerResultCapIsDisclosed: FalkorDB drops the rows past
// RESULTSET_SIZE (10,000 in the image) with no error. With a default limit at
// that cap, a read of this node gets 10,000 rows, sees none past its limit, and
// says nothing about the rest.
func TestLiveACutPastTheServerResultCapIsDisclosed(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, repository, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	key := graphKey(adapter.config.GraphPrefix, orgID)

	// One write statement: projecting this many rows through batches adds
	// minutes and nothing the read depends on.
	seed := fmt.Sprintf("MATCH (n:%[1]s {%[2]s:$org, %[3]s:$kind, %[4]s:$id}) UNWIND range(1, $count) AS i "+
		"CREATE (:%[1]s {%[2]s:$org, %[3]s:$ciKind, %[4]s:'ci-cap-' + toString(i)})-[:%[5]s {%[6]s:'rel_cap_' + toString(i)}]->(n)",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, propRelationshipID)
	if _, err := adapter.api.query(ctx, key, seed, map[string]interface{}{"org": orgID, "kind": string(repository.Kind), "id": repository.CanonicalID,
		"ciKind": string(contractsv1.ContextFabricSubjectCIRun), "count": 10050}, false); err != nil {
		t.Fatalf("seed error = %v", err)
	}

	ids, cut := edgeIDs(t, adapter, ctx, orgID, repository)
	if !cut || len(ids) != defaultNodeEdgeReadLimit {
		t.Fatalf("read %d edges, cut %v; want %d and cut: 10,088 edges exist", len(ids), cut, defaultNodeEdgeReadLimit)
	}
	if got := countPrefix(ids, "rel_z_owner"); got != 3 {
		t.Fatalf("owner edges %d, want all 3", got)
	}
}
