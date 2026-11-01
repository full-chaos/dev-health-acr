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
	// Apart from the repository and the team above, so their edge counts hold: a
	// team that holds incidents, an incident for a kind census to find, and a
	// document with two parents.
	incidents := add(subject(contextfabric.SubjectTeam, "team-incidents"))
	for i := 1; i <= 6; i++ {
		relate(fmt.Sprintf("rel_i_incident_%03d", i), contractsv1.ContextFabricRelationshipOwnedByTeam, add(subject(contextfabric.SubjectIncident, fmt.Sprintf("incident-%03d", i))), incidents)
	}
	add(subject(contextfabric.SubjectIncident, "incident-big"))
	document := add(subject(contextfabric.SubjectDocument, "document-big"))
	relate("rel_d_doc_one", contractsv1.ContextFabricRelationshipDocumentedBy, add(subject(contextfabric.SubjectTeam, "team-doc-one")), document)
	relate("rel_d_doc_two", contractsv1.ContextFabricRelationshipDocumentedBy, add(subject(contextfabric.SubjectTeam, "team-doc-two")), document)
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
		return discoverAtLimit(t, ctx, adapter, orgID, limit, question, nil, committed, nil).Coverage
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
			if c := read(t, 10, tc.question, tc.committed); !hasHopWalkCut(c) || !c.Partial {
				t.Fatalf("limit 10: partial %v details %+v, want the cut disclosed", c.Partial, c.Details)
			}
			if c := read(t, 1000, tc.question, tc.committed); hasHopWalkCut(c) {
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

// discoverAtLimit is one DiscoverContext over nodeEdgeBoundFixture at a node read
// limit. A frame makes it a cohort read; an anchor binds the frame's scope to a
// team.
func discoverAtLimit(t *testing.T, ctx context.Context, adapter *Adapter, orgID string, limit int, question string, frame *contextfabric.QuestionFrame, committed []contextfabric.SubjectRef, anchor *contextfabric.SubjectRef) contextfabric.GraphContext {
	t.Helper()
	adapter.config.NodeEdgeReadLimit = limit
	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12451", Question: question, TimeContext: current,
			Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100, MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"}},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{}, TimeContext: current},
		Resolution:     contextfabric.SubjectResolution{Committed: committed, Candidates: []contextfabric.SubjectCandidate{}},
		Binding:        binding,
		Frame:          frame,
		Bases:          contextfabric.CommitBasisSet{},
	}
	if frame != nil {
		request.Interpretation.Shape = contextfabric.ShapeDiscoveredCohort
	}
	if anchor != nil {
		request.ScopeAnchorKind = anchor.Kind
		request.Resolution.Candidates = []contextfabric.SubjectCandidate{{ReceiptID: "receipt_anchor", Subject: *anchor, State: contextfabric.ResolutionCommitted,
			MatchedTerms: frame.SubjectExpression.Scoped.AnchorTerms, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{}}}
		request.Bases.Record(*anchor, contextfabric.CommitBasisAuthoritativeIdentity)
	}
	gc, err := adapter.DiscoverContext(ctx, principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return gc
}

func hasHopWalkCut(c contextfabric.Coverage) bool {
	return slices.ContainsFunc(c.Details, func(d contextfabric.CoverageDetail) bool {
		return d.Code == contractsv1.ContextFabricCoverageDetailGraphHopWalkTruncated
	})
}

func membersOfScopeFrame(term string, kind contextfabric.SubjectKind) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{term}, MemberKind: kind}},
		Temporal:          contextfabric.TemporalIntentCurrent,
		Version:           contextfabric.QuestionFrameVersion,
	}
}

// TestLiveACutHopWalkOnACohortReadFloorsThePopulation (CHAOS-12451, table row
// 3): with no team anchor the members of a cohort come from the hop walk, so a
// cut of that read is a cut of the pool, and the population floor shows it. The
// path row stays away: the floor already said so.
func TestLiveACutHopWalkOnACohortReadFloorsThePopulation(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, _, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	adapter.config.MaxResults = 500
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team-incidents", Label: "team-incidents"}
	frame := membersOfScopeFrame("team-incidents", contextfabric.SubjectIncident)
	committed := []contextfabric.SubjectRef{team}

	cut := discoverAtLimit(t, ctx, adapter, orgID, 3, "how many incidents does the team hold?", frame, committed, &team)
	if cut.Cohort == nil || !cut.CohortPopulationLowerBound {
		t.Fatalf("limit 3: cohort %+v lower bound %v, want the population floored", cut.Cohort, cut.CohortPopulationLowerBound)
	}
	if hasHopWalkCut(cut.Coverage) {
		t.Fatalf("limit 3: details %+v, want the floor alone: it shows this cut", cut.Coverage.Details)
	}
	whole := discoverAtLimit(t, ctx, adapter, orgID, 1000, "how many incidents does the team hold?", frame, committed, &team)
	if whole.Cohort == nil || whole.CohortPopulationLowerBound {
		t.Fatalf("limit 1000: cohort %+v lower bound %v, want a whole population", whole.Cohort, whole.CohortPopulationLowerBound)
	}
}

// TestLiveACutWalkBesideATeamAnchorIsDisclosed (CHAOS-12451, table row 4): the
// anchor's member read is a tree walk, so the population is complete, and a cut
// of another subject's walk or of the text arm is a cut of the answer's paths.
// A walk that only spent its budget is routine and files nothing.
func TestLiveACutWalkBesideATeamAnchorIsDisclosed(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, repository, team := nodeEdgeBoundFixture(t, ctx, adapter)
	adapter.config.MaxResults = 500
	frame := membersOfScopeFrame("team-big", contextfabric.SubjectRepository)
	for _, tc := range []struct {
		name      string
		question  string
		committed []contextfabric.SubjectRef
		limit     int
	}{
		{"the hop walk of the other subject", "which repositories does the team own?", []contextfabric.SubjectRef{team, repository}, 10},
		{"the text arm", "what repositories does team-big own?", []contextfabric.SubjectRef{team}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cut := discoverAtLimit(t, ctx, adapter, orgID, tc.limit, tc.question, frame, tc.committed, &team)
			if !hasHopWalkCut(cut.Coverage) || !cut.Coverage.Partial {
				t.Fatalf("limit %d: partial %v details %+v, want the cut disclosed", tc.limit, cut.Coverage.Partial, cut.Coverage.Details)
			}
			if cut.Cohort == nil || cut.CohortPopulationLowerBound {
				t.Fatalf("limit %d: cohort %+v lower bound %v: the anchor's members are complete", tc.limit, cut.Cohort, cut.CohortPopulationLowerBound)
			}
			if whole := discoverAtLimit(t, ctx, adapter, orgID, 1000, tc.question, frame, tc.committed, &team); hasHopWalkCut(whole.Coverage) {
				t.Fatalf("limit 1000: details %+v, want no cut", whole.Coverage.Details)
			}
		})
	}
	t.Run("a spent budget files nothing", func(t *testing.T) {
		adapter.config.MaxResults = 2
		t.Cleanup(func() { adapter.config.MaxResults = 500 })
		spent := discoverAtLimit(t, ctx, adapter, orgID, 1000, "which repositories does the team own?", frame, []contextfabric.SubjectRef{team, repository}, &team)
		if hasHopWalkCut(spent.Coverage) {
			t.Fatalf("details %+v: a budget is not a cut of a read", spent.Coverage.Details)
		}
	})
}

// TestLiveACutTextArmACensusCoversIsDisclosed (CHAOS-12451, table row 5): a
// complete census covers the members a bounded arm dropped, not the path edges of
// the node the text arm read.
func TestLiveACutTextArmACensusCoversIsDisclosed(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, _, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	adapter.config.MaxResults = 500
	frame := &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind, Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectIncident}},
		Temporal:          contextfabric.TemporalIntentCurrent,
		Version:           contextfabric.QuestionFrameVersion,
	}
	const question = "which incidents touch repository-big?"
	committed := []contextfabric.SubjectRef{}

	cut := discoverAtLimit(t, ctx, adapter, orgID, 10, question, frame, committed, nil)
	if cut.Cohort == nil || cut.CohortPopulationLowerBound {
		t.Fatalf("limit 10: cohort %+v lower bound %v, want the census to cover the pool", cut.Cohort, cut.CohortPopulationLowerBound)
	}
	if !hasHopWalkCut(cut.Coverage) || !cut.Coverage.Partial {
		t.Fatalf("limit 10: partial %v details %+v, want the cut of the paths disclosed", cut.Coverage.Partial, cut.Coverage.Details)
	}
	if whole := discoverAtLimit(t, ctx, adapter, orgID, 1000, question, frame, committed, nil); hasHopWalkCut(whole.Coverage) {
		t.Fatalf("limit 1000: details %+v, want no cut", whole.Coverage.Details)
	}
}

// TestLiveACutObservationReadDegradesRetrieval (CHAOS-12451, table row 6): a
// document's read cut at its limit cannot prove it has no parent. The resolution
// says retrieval saw less than it should, which the answer states as a limitation.
func TestLiveACutObservationReadDegradesRetrieval(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID, _, _ := nodeEdgeBoundFixture(t, ctx, adapter)
	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	request := contextfabric.InvestigationRequest{SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12451", Question: "what does document-big say?", TimeContext: current,
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100, MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"}}
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"document-big"}, TimeContext: current}
	resolve := func(limit int) contextfabric.SubjectResolution {
		t.Helper()
		adapter.config.NodeEdgeReadLimit = limit
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, binding, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		return resolution
	}
	if cut := resolve(1); !cut.RetrievalDegraded {
		t.Fatalf("limit 1: %+v, want retrieval degraded: the read of the document was cut", cut)
	}
	if whole := resolve(1000); whole.RetrievalDegraded {
		t.Fatalf("limit 1000: %+v, want no degradation", whole)
	}
}
