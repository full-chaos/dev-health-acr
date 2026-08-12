package zepgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	zep "github.com/getzep/zep-go/v3"
)

type fakeAPI struct {
	graphs           map[string]*zep.Graph
	triples          []*zep.AddTripleRequest
	nodes            map[string]*zep.EntityNode
	edges            map[string]*zep.EntityEdge
	searchResult     *zep.GraphSearchResults
	searches         []*zep.GraphSearchQuery
	deletedGraph     string
	err              error
	createGraphCalls int
	deleteGraphErr   error
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{graphs: map[string]*zep.Graph{}, nodes: map[string]*zep.EntityNode{}, edges: map[string]*zep.EntityEdge{}}
}

func (f *fakeAPI) GetGraph(_ context.Context, graphID string) (*zep.Graph, error) {
	if f.err != nil {
		return nil, f.err
	}
	graph, ok := f.graphs[graphID]
	if !ok {
		return nil, &zep.NotFoundError{}
	}
	return graph, nil
}

func (f *fakeAPI) CreateGraph(_ context.Context, request *zep.CreateGraphRequest) (*zep.Graph, error) {
	f.createGraphCalls++
	graphID := request.GraphID
	graph := &zep.Graph{GraphID: &graphID, Name: request.Name, Description: request.Description}
	f.graphs[graphID] = graph
	return graph, nil
}

func (f *fakeAPI) DeleteGraph(_ context.Context, graphID string) error {
	if f.deleteGraphErr != nil {
		return f.deleteGraphErr
	}
	f.deletedGraph = graphID
	delete(f.graphs, graphID)
	return nil
}

func (f *fakeAPI) AddFactTriple(_ context.Context, request *zep.AddTripleRequest) (*zep.AddTripleResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.triples = append(f.triples, request)
	if request.SourceNodeUUID != nil {
		f.nodes[*request.SourceNodeUUID] = &zep.EntityNode{
			UUID: *request.SourceNodeUUID, Name: deref(request.SourceNodeName), Labels: request.SourceNodeLabels,
			Summary: deref(request.SourceNodeSummary), Attributes: cloneAnyMap(request.SourceNodeAttributes),
		}
	}
	if request.TargetNodeUUID != nil {
		f.nodes[*request.TargetNodeUUID] = &zep.EntityNode{
			UUID: *request.TargetNodeUUID, Name: deref(request.TargetNodeName), Labels: request.TargetNodeLabels,
			Summary: deref(request.TargetNodeSummary), Attributes: cloneAnyMap(request.TargetNodeAttributes),
		}
	}
	if request.FactUUID != nil {
		f.edges[*request.FactUUID] = &zep.EntityEdge{
			UUID: *request.FactUUID, Name: request.FactName, Fact: request.Fact,
			SourceNodeUUID: deref(request.SourceNodeUUID), TargetNodeUUID: deref(request.TargetNodeUUID),
			Attributes: cloneAnyMap(request.EdgeAttributes), CreatedAt: deref(request.CreatedAt),
			ValidAt: request.ValidAt, InvalidAt: request.InvalidAt, ExpiredAt: request.ExpiredAt,
		}
	}
	return &zep.AddTripleResponse{}, nil
}

func (f *fakeAPI) Search(_ context.Context, request *zep.GraphSearchQuery) (*zep.GraphSearchResults, error) {
	f.searches = append(f.searches, request)
	if f.err != nil {
		return nil, f.err
	}
	if f.searchResult == nil {
		return &zep.GraphSearchResults{}, nil
	}
	return f.searchResult, nil
}

func (f *fakeAPI) GetNode(_ context.Context, uuid string) (*zep.EntityNode, error) {
	node, ok := f.nodes[uuid]
	if !ok {
		return nil, &zep.NotFoundError{}
	}
	return node, nil
}

func (f *fakeAPI) DeleteNode(_ context.Context, uuid string) error {
	delete(f.nodes, uuid)
	return nil
}

func (f *fakeAPI) GetNodeEdges(_ context.Context, uuid string) ([]*zep.EntityEdge, error) {
	result := []*zep.EntityEdge{}
	for _, edge := range f.edges {
		if edge.SourceNodeUUID == uuid || edge.TargetNodeUUID == uuid {
			result = append(result, edge)
		}
	}
	return result, nil
}

func (f *fakeAPI) GetEdge(_ context.Context, uuid string) (*zep.EntityEdge, error) {
	edge, ok := f.edges[uuid]
	if !ok {
		return nil, &zep.NotFoundError{}
	}
	return edge, nil
}

func (f *fakeAPI) DeleteEdge(_ context.Context, uuid string) error {
	delete(f.edges, uuid)
	return nil
}

func TestGraphIdentityIsServerDerivedAndOrganizationIsolated(t *testing.T) {
	t.Parallel()
	first := graphID("acr-cf", "org-secret-alpha")
	second := graphID("acr-cf", "org-secret-beta")
	if first == second || strings.Contains(first, "org-secret") || !strings.HasPrefix(first, "acr-cf-") {
		t.Fatalf("graph IDs = %q %q", first, second)
	}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1", Label: "Ask Dev"}
	if nodeUUID("org-a", project) == nodeUUID("org-b", project) {
		t.Fatal("node UUID must include organization identity")
	}
}

func TestProjectionUsesCallerOwnedIDsTemporalTriplesAndWatermark(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	fixed := time.Date(2026, 8, 11, 21, 0, 0, 0, time.UTC)
	adapter.now = func() time.Time { return fixed }
	batch := validBatch()

	receipt, err := adapter.ApplyProjectionBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	if receipt.EntitiesApplied != 1 || receipt.EdgesApplied != 1 || receipt.ContentsApplied != 1 || receipt.EpisodesApplied != 1 || receipt.BackendWatermark == "" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if len(api.triples) != 5 {
		t.Fatalf("triple count = %d", len(api.triples))
	}
	var dependency *zep.AddTripleRequest
	for _, triple := range api.triples {
		if triple.FactName == "DEPENDS_ON" {
			dependency = triple
		}
	}
	if dependency == nil || dependency.FactUUID == nil || dependency.SourceNodeUUID == nil || dependency.TargetNodeUUID == nil || dependency.ValidAt == nil || dependency.InvalidAt == nil {
		t.Fatalf("dependency triple = %#v", dependency)
	}
	watermark, err := adapter.ProjectionWatermark(context.Background(), batch.OrgID, batch.Source)
	if err != nil {
		t.Fatalf("ProjectionWatermark() error = %v", err)
	}
	if watermark.Cursor != batch.NextCursor || watermark.SourceVersion != batch.SourceVersion || watermark.BackendWatermark != receipt.BackendWatermark {
		t.Fatalf("watermark = %#v", watermark)
	}
}

func TestResolveSubjectsFiltersUnauthorizedNodesBeforeCandidates(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	authorized := graphNode("node-authorized", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "|full-chaos/dev-health-acr|", 1)
	foreign := graphNode("node-foreign", contextfabric.SubjectProject, "project_foreign", "Ask Dev Foreign", "|other/private|", 0.99)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{foreign, authorized}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 1 || resolution.Candidates[0].Subject.CanonicalID != "project_ask_dev" || len(resolution.Committed) != 1 {
		t.Fatalf("resolution = %#v", resolution)
	}
}

// TestResolveSubjectsWrongSubjectControlNeverCommitsTheDecoy is the
// negative "wrong subject" control: a name-similar, fully authorized decoy
// subject with higher raw graph relevance than the actual exact match must
// never be the one that gets committed. This is deliberately not an
// authorization test (both subjects are visible to the principal) and not
// an ambiguity test (the confidence gap is wide) -- it isolates plain
// subject discrimination: an exact term match against the canonical label
// must win over a merely relevant, superficially similar distractor.
func TestResolveSubjectsWrongSubjectControlNeverCommitsTheDecoy(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	target := graphNode("node-target", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.4)
	decoy := graphNode("node-decoy", contextfabric.SubjectProject, "project_ask_dev_analytics", "Ask Dev Analytics", "*", 0.6)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{decoy, target}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution.Committed = %#v, want only the exact-match subject committed", resolution.Committed)
	}
	for _, committed := range resolution.Committed {
		if committed.CanonicalID == "project_ask_dev_analytics" {
			t.Fatalf("resolution = %#v, the decoy subject must never be committed", resolution)
		}
	}
}

// TestResolveSubjectsAcceptsPrincipalWildcardRepositoryScope is the direct
// regression for the CHAOS-3752 Reset 0 review must-do: a principal holding
// an org-wide "*" or an "owner/*" repository scope (both valid per
// internal/auth.RepositoryAllowed/validRepositoryScope, e.g. issued to a
// device grant) previously matched no projected node at all, because
// zepgraph's scopeContains only compared exact repository strings. Retrieval
// authorization must accept both wildcard forms.
func TestResolveSubjectsAcceptsPrincipalWildcardRepositoryScope(t *testing.T) {
	t.Parallel()
	narrowlyScoped := graphNode("node-narrow", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "|acme/repo-x|", 1)
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}

	for _, tc := range []struct {
		name   string
		scopes []string
		want   bool
	}{
		{"global wildcard authorizes a narrowly-scoped node", []string{"*"}, true},
		{"owner wildcard authorizes a node under that owner", []string{"acme/*"}, true},
		{"owner wildcard for a different owner still denies (no unsafe widening)", []string{"other/*"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI()
			adapter := mustAdapter(t, api)
			api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{narrowlyScoped}}
			resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}, request, interpreted)
			if err != nil {
				t.Fatalf("ResolveSubjects() error = %v", err)
			}
			got := len(resolution.Candidates) == 1 && resolution.Candidates[0].Subject.CanonicalID == "project_ask_dev"
			if got != tc.want {
				t.Fatalf("resolution = %#v, want candidate present = %v", resolution, tc.want)
			}
		})
	}
}

// TestResolveSubjectsWildcardScopeDoesNotWidenAcrossOrganizations proves the
// wildcard fix stays inside organization isolation: it operates purely on
// the per-node repository authorization tag, never on the search request's
// organization scope, which stays the server-derived per-organization graph
// ID (proved separately by TestGraphIdentityIsServerDerivedAndOrganizationIsolated)
// regardless of the principal's repository scope.
func TestResolveSubjectsWildcardScopeDoesNotWidenAcrossOrganizations(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	node := graphNode("node-org-2", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "|acme/repo-x|", 1)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{node}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	if _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_2", RepositoryScopes: []string{"*"}}, request, interpreted); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(api.searches) != 1 {
		t.Fatalf("searches = %d, want 1", len(api.searches))
	}
	wantGraphID := graphID(adapter.config.GraphPrefix, "org_2")
	if api.searches[0].GraphID == nil || *api.searches[0].GraphID != wantGraphID {
		t.Fatalf("search graph ID = %v, want %q (org isolation is structural, not scope-matching)", api.searches[0].GraphID, wantGraphID)
	}
}

// TestResolveSubjectsTraversesObservationNodeToCanonicalSubject is the
// direct proof of observation-to-entity traversal: a hybrid search hit on a
// document node (the term only matched inside its indexed body/summary
// text) must still resolve back to the canonical subject that document is
// projected against, in addition to proposing the document itself.
func TestResolveSubjectsTraversesObservationNodeToCanonicalSubject(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	// Both the document and its canonical subject are set to a high raw
	// relevance -- deliberately high enough that, before the G1 fix, the
	// document's own (higher, undiscounted) confidence would win the
	// auto-commit comparison against the traversed-and-discounted subject.
	// The subject's UUID must be the real nodeUUID derivation (not an
	// arbitrary fixture string) so the G7 organization-identity
	// verification on the traversal's second-hop GetNode accepts it.
	subjectRef := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	subject := graphNode(nodeUUID("org_1", subjectRef), subjectRef.Kind, subjectRef.CanonicalID, subjectRef.Label, "*", 0.9)
	document := &zep.EntityNode{
		UUID: "node-document", Name: "Ask Dev readiness review", Relevance: ptr(0.95),
		Attributes: map[string]interface{}{
			"canonical_id": "document_1234", "subject_kind": string(contextfabric.SubjectDocument), "label": "Ask Dev readiness review",
			"authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*",
			"evidence_refs": "|evidence_document_1234|",
		},
	}
	api.nodes[subject.UUID] = subject
	api.nodes[document.UUID] = document
	api.edges["edge-documented-by"] = &zep.EntityEdge{
		UUID: "edge-documented-by", Name: "DOCUMENTED_BY", Fact: "Ask Dev is documented by Ask Dev readiness review.",
		SourceNodeUUID: subject.UUID, TargetNodeUUID: document.UUID,
	}
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{document}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"readiness review"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	var sawDocument, sawSubject bool
	var subjectConfidence float64
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "document_1234" {
			sawDocument = true
		}
		if candidate.Subject.CanonicalID == "project_ask_dev" {
			sawSubject = true
			subjectConfidence = candidate.Confidence
		}
	}
	if !sawDocument {
		t.Fatalf("resolution = %#v, want the matched document proposed as a candidate", resolution)
	}
	if !sawSubject {
		t.Fatalf("resolution = %#v, want the traversed canonical subject proposed as a candidate", resolution)
	}
	if subjectConfidence <= 0 || subjectConfidence >= resultConfidence(document.Relevance, document.Score) {
		t.Fatalf("traversed subject confidence = %v, want positive and discounted below the observation's own confidence", subjectConfidence)
	}
	// The canonical subject the document is about must be what gets
	// committed -- never the document itself, even though the document's
	// own raw confidence is higher. Committing the document here would
	// mean a term that only appeared inside a document body silently
	// answered the investigation as if the document were the subject.
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution.Committed = %#v, want only the traversed canonical subject committed, never the document", resolution.Committed)
	}
}

// TestResolveSubjectsTraversalIgnoresUnrelatedRelationEdges proves
// traversal only follows the specific containment/attribution relation
// kinds projectContent/projectEpisode use (DOCUMENTED_BY, HAS_EPISODE) to
// attach a document/episode to its authoritative canonical subject, not
// any edge that happens to point at the observation node. A generic
// MENTIONS/REFERENCES relationship from some unrelated node must never be
// followed as if that unrelated node were the entity the document is
// about.
func TestResolveSubjectsTraversalIgnoresUnrelatedRelationEdges(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	unrelated := graphNode("node-unrelated", contextfabric.SubjectProject, "project_unrelated", "Unrelated Project", "*", 0.99)
	document := &zep.EntityNode{
		UUID: "node-document", Name: "Ask Dev readiness review", Relevance: ptr(0.95),
		Attributes: map[string]interface{}{
			"canonical_id": "document_1234", "subject_kind": string(contextfabric.SubjectDocument), "label": "Ask Dev readiness review",
			"authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*",
			"evidence_refs": "|evidence_document_1234|",
		},
	}
	api.nodes[unrelated.UUID] = unrelated
	api.nodes[document.UUID] = document
	api.edges["edge-mentions"] = &zep.EntityEdge{
		UUID: "edge-mentions", Name: "MENTIONS", Fact: "Unrelated Project mentions Ask Dev readiness review.",
		SourceNodeUUID: unrelated.UUID, TargetNodeUUID: document.UUID,
		Attributes: map[string]interface{}{"authorization_repositories": "*"},
	}
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{document}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"readiness review"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "project_unrelated" {
			t.Fatalf("resolution = %#v, a MENTIONS edge must never be followed as an attribution relation", resolution)
		}
	}
}

// TestResolveSubjectsTraversalRequiresEdgeAuthorization proves traversal
// independently authorizes the attribution edge itself, not just the
// source node it points to. A source node visible to the principal on its
// own must not be traversed to if the specific document-attribution fact
// (the edge) is scoped to a repository the principal cannot see -- the
// edge's own authorization narrows what the *relationship* discloses,
// independent of either endpoint's own visibility.
func TestResolveSubjectsTraversalRequiresEdgeAuthorization(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	subject := graphNode("node-subject", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.9)
	document := &zep.EntityNode{
		UUID: "node-document", Name: "Ask Dev readiness review", Relevance: ptr(0.95),
		Attributes: map[string]interface{}{
			"canonical_id": "document_1234", "subject_kind": string(contextfabric.SubjectDocument), "label": "Ask Dev readiness review",
			"authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*",
			"evidence_refs": "|evidence_document_1234|",
		},
	}
	api.nodes[subject.UUID] = subject
	api.nodes[document.UUID] = document
	api.edges["edge-documented-by"] = &zep.EntityEdge{
		UUID: "edge-documented-by", Name: "DOCUMENTED_BY", Fact: "Ask Dev is documented by Ask Dev readiness review.",
		SourceNodeUUID: subject.UUID, TargetNodeUUID: document.UUID,
		// The attribution fact itself is scoped to a repository the
		// calling principal does not have, even though both the subject
		// and the document nodes are individually unrestricted ("*").
		Attributes: map[string]interface{}{"authorization_repositories": "|other/private|"},
	}
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{document}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"readiness review"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "project_ask_dev" {
			t.Fatalf("resolution = %#v, an unauthorized attribution edge must not be traversed even though the source node is itself unrestricted", resolution)
		}
	}
}

// TestResolveSubjectsExcludesInternalBookkeepingSubjectsFromCandidates
// guards against the adapter's own projection anchor nodes (the
// organization root, projection watermark markers) leaking into a public
// subject resolution -- neither can ever be what a caller meant by name,
// and both carry an unrestricted "*" authorization scope by construction,
// so without this exclusion they could surface as a spurious candidate for
// almost any hybrid search.
func TestResolveSubjectsExcludesInternalBookkeepingSubjectsFromCandidates(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	root := graphNode("node-root", contextfabric.SubjectOrganization, "organization-root", "Organization", "*", 0.99)
	watermark := graphNode("node-watermark", contextfabric.SubjectMetric, "projection-watermark:dev-health-ops", "Projection watermark dev-health-ops", "*", 0.98)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{root, watermark}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"organization"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 {
		t.Fatalf("resolution = %#v, want internal bookkeeping nodes excluded entirely", resolution)
	}
}

// TestIsInternalBookkeepingSubjectIsCaseInsensitive is the probe for Codex
// finding G8(a): isInternalBookkeepingSubject compared canonical_id with
// exact case, so a case-variant value (which the write path never
// legitimately produces today, but which nothing structurally prevents a
// future write path or data-repair script from writing) would not be
// recognized as adapter-internal bookkeeping.
func TestIsInternalBookkeepingSubjectIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	cases := []contextfabric.SubjectRef{
		{Kind: contextfabric.SubjectOrganization, CanonicalID: "Organization-Root"},
		{Kind: contextfabric.SubjectOrganization, CanonicalID: "ORGANIZATION-ROOT"},
		{Kind: contextfabric.SubjectMetric, CanonicalID: "PROJECTION-WATERMARK:dev-health-ops"},
		{Kind: contextfabric.SubjectMetric, CanonicalID: "Projection-Watermark:dev-health-ops"},
	}
	for _, subject := range cases {
		if !isInternalBookkeepingSubject(subject) {
			t.Fatalf("isInternalBookkeepingSubject(%#v) = false, want true regardless of case", subject)
		}
	}
}

// TestDiscoveredCohortExcludesInternalBookkeepingSubjects is the probe for
// Codex finding G8(b): discoveredCohort's membership loop never called
// isInternalBookkeepingSubject, relying entirely on interpretedCohortKind
// only ever returning Team/Project (which a bookkeeping node's real kind,
// Organization/Metric, can never match) to keep bookkeeping nodes out.
// That is an accident of the current cohort-kind range, not a guarantee --
// this constructs the case where a node's reported subject_kind attribute
// coincides with the interpreted cohort kind despite the canonical_id
// still being one of the reserved bookkeeping identifiers, which the
// kind-mismatch alone cannot catch.
func TestDiscoveredCohortExcludesInternalBookkeepingSubjects(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	impostorRoot := &zep.EntityNode{
		UUID: "node-impostor-root", Name: "Organization", Relevance: ptr(0.9),
		Attributes: map[string]interface{}{
			// subject_kind reports "team" -- matching interpretedCohortKind's
			// output below -- while canonical_id still carries the reserved
			// organization-root identifier.
			"canonical_id": "organization-root", "subject_kind": string(contextfabric.SubjectTeam), "label": "Organization",
			"authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*",
			"evidence_refs": "*",
		},
	}
	genuineTeam := graphNode("node-team", contextfabric.SubjectTeam, "team_platform", "Platform", "*", 0.9)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{impostorRoot, genuineTeam}}
	discoveryRequest := contextfabric.GraphDiscoveryRequest{
		Request: validRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
	discoveryRequest.Request.Question = "Which teams are under the most pressure?"
	contextResult, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org_1"}, discoveryRequest)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if contextResult.Cohort == nil {
		t.Fatal("cohort = nil, want the genuine team still discovered")
	}
	for _, member := range contextResult.Cohort.Members {
		if member.Subject.CanonicalID == "organization-root" {
			t.Fatalf("cohort = %#v, an internal bookkeeping identifier must never surface as a cohort member", contextResult.Cohort)
		}
	}
}

// TestResolveSubjectsAndDiscoverContextRejectAlreadyCancelledContext proves
// budget/deadline/cancellation enforcement at the graph retrieval boundary,
// not just inside the outer Engine.
func TestResolveSubjectsAndDiscoverContextRejectAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Ask Dev"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	if _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: "org_1"}, request, interpreted); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveSubjects() error = %v, want context.Canceled", err)
	}
	discovery := contextfabric.GraphDiscoveryRequest{Request: request, Interpretation: interpreted, Resolution: contextfabric.SubjectResolution{}}
	if _, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: "org_1"}, discovery); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverContext() error = %v, want context.Canceled", err)
	}
	if len(api.searches) != 0 {
		t.Fatalf("searches = %d, want zero backend calls for an already-cancelled context", len(api.searches))
	}
}

func TestDiscoverContextReturnsEvidenceClosedDriverAndSubjectlessCohort(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	project := graphNode("project-node", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.95)
	work := graphNode("work-node", contextfabric.SubjectWorkItem, "work_release", "Release acceptance", "*", 0.8)
	team := graphNode("team-node", contextfabric.SubjectTeam, "team_platform", "Platform", "*", 0.9)
	edge := &zep.EntityEdge{
		UUID: "edge-1", Name: "BLOCKS", Fact: "Release acceptance blocks Ask Dev readiness.",
		SourceNodeUUID: project.UUID, TargetNodeUUID: work.UUID, CreatedAt: "2026-08-11T20:00:00Z",
		Attributes: map[string]interface{}{"authorization_repositories": "*", "evidence_refs": "|evidence_release_1234|", "epistemic_status": "observed"},
		Relevance:  ptr(0.91),
	}
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{project, work, team}, Edges: []*zep.EntityEdge{edge}}
	request := contextfabric.GraphDiscoveryRequest{
		Request: validRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
	request.Request.Question = "Which teams are under the most pressure and what is driving it?"
	contextResult, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(contextResult.Paths) != 1 || len(contextResult.DriverCandidates) != 1 || contextResult.DriverCandidates[0].EvidenceRefIDs[0] != "evidence_release_1234" {
		t.Fatalf("graph context = %#v", contextResult)
	}
	if contextResult.Cohort == nil || contextResult.Cohort.Kind != contextfabric.SubjectTeam || len(contextResult.Cohort.Members) != 1 {
		t.Fatalf("cohort = %#v", contextResult.Cohort)
	}
	// Coverage.Source and Coverage.Watermark land verbatim in the public
	// InvestigationResult: the source name must not encode the graph
	// vendor, and the watermark must not leak the adapter's internal graph
	// identifier (config.GraphPrefix + org ID).
	if len(contextResult.Coverage.Sources) != 1 {
		t.Fatalf("coverage sources = %#v", contextResult.Coverage.Sources)
	}
	source := contextResult.Coverage.Sources[0]
	if source.Source != "context-fabric:graph" {
		t.Fatalf("coverage source = %q, want a vendor-neutral source name", source.Source)
	}
	if source.Watermark != "" {
		t.Fatalf("coverage watermark = %q, want empty until a real, non-identifying watermark exists", source.Watermark)
	}
}

// TestDiscoverContextTruncatesEvidenceRefsToBudget proves
// Options.MaxEvidenceRefs is enforced on the aggregated evidence list, not
// just on candidate/path/driver counts.
func TestDiscoverContextTruncatesEvidenceRefsToBudget(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	project := graphNode("project-node", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.95)
	first := graphNode("work-node-1", contextfabric.SubjectWorkItem, "work_1", "Work One", "*", 0.8)
	second := graphNode("work-node-2", contextfabric.SubjectWorkItem, "work_2", "Work Two", "*", 0.8)
	edges := []*zep.EntityEdge{
		{
			UUID: "edge-1", Name: "BLOCKS", Fact: "Work One blocks Ask Dev.", SourceNodeUUID: project.UUID, TargetNodeUUID: first.UUID,
			Attributes: map[string]interface{}{"authorization_repositories": "*", "evidence_refs": "|evidence_one_1234|", "epistemic_status": "observed"}, Relevance: ptr(0.9),
		},
		{
			UUID: "edge-2", Name: "BLOCKS", Fact: "Work Two blocks Ask Dev.", SourceNodeUUID: project.UUID, TargetNodeUUID: second.UUID,
			Attributes: map[string]interface{}{"authorization_repositories": "*", "evidence_refs": "|evidence_two_1234|", "epistemic_status": "observed"}, Relevance: ptr(0.9),
		},
	}
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{project, first, second}, Edges: edges}
	request := validRequest()
	request.Options.MaxEvidenceRefs = 1
	discoveryRequest := contextfabric.GraphDiscoveryRequest{
		Request: request,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "blockers", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
	contextResult, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org_1"}, discoveryRequest)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(contextResult.EvidenceRefIDs) != 1 {
		t.Fatalf("evidence ref IDs = %#v, want truncated to Options.MaxEvidenceRefs=1", contextResult.EvidenceRefIDs)
	}
	// Codex finding G5: the budget must hold across the FINAL result's
	// entire evidence surface, not just the flat aggregated
	// EvidenceRefIDs list -- each path and driver carries its own
	// EvidenceRefIDs too, and those are what actually flow into the
	// public InvestigationResult (Paths, Drivers). Truncating only the
	// aggregate while still admitting every path/driver that produced it
	// leaves the caller's requested budget violated in the parts of the
	// result that matter for serialized size.
	allEvidence := make(map[string]struct{})
	for _, path := range contextResult.Paths {
		for _, id := range path.EvidenceRefIDs {
			allEvidence[id] = struct{}{}
		}
	}
	for _, driver := range contextResult.DriverCandidates {
		for _, id := range driver.EvidenceRefIDs {
			allEvidence[id] = struct{}{}
		}
	}
	for _, id := range contextResult.EvidenceRefIDs {
		allEvidence[id] = struct{}{}
	}
	if len(allEvidence) > 1 {
		t.Fatalf("distinct evidence across paths+drivers+aggregate = %#v, want at most Options.MaxEvidenceRefs=1", allEvidence)
	}
}

// TestDiscoverContextExcludesInternalBookkeepingRelationships guards the
// relationship path against the same organization-root/watermark-marker
// leakage as candidate resolution: even if a bookkeeping edge somehow
// carried evidence, it must never surface as a public relationship path.
func TestDiscoverContextExcludesInternalBookkeepingRelationships(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	root := graphNode("node-root", contextfabric.SubjectOrganization, "organization-root", "Organization", "*", 0.5)
	subject := graphNode("node-subject", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.9)
	edge := &zep.EntityEdge{
		UUID: "edge-bookkeeping", Name: "HAS_SUBJECT", Fact: "Organization contains Ask Dev.",
		SourceNodeUUID: root.UUID, TargetNodeUUID: subject.UUID,
		Attributes: map[string]interface{}{"authorization_repositories": "*", "evidence_refs": "|evidence_leaked_1234|", "epistemic_status": "observed"},
	}
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{root, subject}, Edges: []*zep.EntityEdge{edge}}
	discoveryRequest := contextfabric.GraphDiscoveryRequest{
		Request: validRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
	contextResult, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org_1"}, discoveryRequest)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(contextResult.Paths) != 0 || len(contextResult.EvidenceRefIDs) != 0 {
		t.Fatalf("graph context = %#v, want internal bookkeeping relationship excluded", contextResult)
	}
}

// TestDiscoverContextRejectsSecondHopNodeNotBelongingToCallersOrganization
// is the probe for Codex finding G7: GetNode and GetNodeEdges are
// UUID-only lookups with no per-call graph/organization parameter (unlike
// Search, which is scoped by GraphID), so a second-hop read -- a
// source/target UUID discovered from a search-result edge, not derived by
// this adapter itself -- was trusted without verifying the fetched node
// actually belongs to the caller's organization graph. Since nodeUUID is a
// keyed digest of organization ID + subject kind + canonical ID, a node
// genuinely belonging to the caller's organization always hashes back to
// the UUID it was fetched under; this proves a node that does not is
// rejected rather than silently trusted.
func TestDiscoverContextRejectsSecondHopNodeNotBelongingToCallersOrganization(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	project := graphNode("project-node", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.95)
	// impostorUUID is an arbitrary UUID an edge references as its target.
	// The node GetNode returns for it reports canonical identity
	// "work_release" -- but that identity's real, deterministically-derived
	// UUID under org_1 is nodeUUID("org_1", ...), which is NOT
	// impostorUUID. This is exactly what a compromised/misbehaving second-
	// hop response looks like: a node handed back under a UUID it does not
	// actually correspond to for this organization.
	const impostorUUID = "impostor-uuid-does-not-match-derivation"
	api.nodes[impostorUUID] = graphNode(impostorUUID, contextfabric.SubjectWorkItem, "work_release", "Release acceptance", "*", 0.8)
	edge := &zep.EntityEdge{
		UUID: "edge-1", Name: "BLOCKS", Fact: "Release acceptance blocks Ask Dev.",
		SourceNodeUUID: project.UUID, TargetNodeUUID: impostorUUID,
		Attributes: map[string]interface{}{"authorization_repositories": "*", "evidence_refs": "|evidence_release_1234|", "epistemic_status": "observed"},
	}
	// project is a first-hop result (from Search); impostorUUID is not --
	// it is only reachable through the edge's TargetNodeUUID, forcing the
	// GetNode second-hop fallback.
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{project}, Edges: []*zep.EntityEdge{edge}}
	discoveryRequest := contextfabric.GraphDiscoveryRequest{
		Request: validRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "blockers", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
	contextResult, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org_1"}, discoveryRequest)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(contextResult.Paths) != 0 || len(contextResult.DriverCandidates) != 0 {
		t.Fatalf("graph context = %#v, want the second-hop node rejected for failing organization identity verification", contextResult)
	}
}

// TestApplyProjectionBatchRejectsSeparatorBearingAuthorizationScope is the F1
// end-to-end regression: the v1 contract layer must reject a scope value
// containing the adapter's internal encoding separator ('|') before
// anything is written, so a separator-bearing scope can never be widened
// (via zepgraph's prior "silently fall back to '*'" bug) into an
// unrelated-principal read.
func TestApplyProjectionBatchRejectsSeparatorBearingAuthorizationScope(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	private := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_secret", Label: "Secret Project"}

	batch := validBatch()
	batch.Entities = []contextfabric.EntityProjection{{
		Subject:        private,
		Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/private|leak"}},
		EvidenceRefIDs: []string{"evidence_secret_1234"}, ObservedAt: batch.GeneratedAt, SourceVersion: "ops-v1",
	}}
	batch.Relationships = []contextfabric.RelationshipProjection{}
	batch.Contents = []contextfabric.ContentProjection{}
	batch.Episodes = []contextfabric.EpisodeProjection{}

	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err == nil {
		t.Fatal("ApplyProjectionBatch() accepted a '|'-bearing authorization scope")
	}
	if node := api.nodes[nodeUUID(batch.OrgID, private)]; node != nil {
		t.Fatalf("a rejected batch still wrote a node: %#v", node)
	}
}

func TestPurgeOrganizationDeletesOnlyDerivedGraph(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	id := graphID(adapter.config.GraphPrefix, "org_1")
	api.graphs[id] = &zep.Graph{GraphID: &id}
	if err := adapter.PurgeOrganization(context.Background(), "org_1"); err != nil {
		t.Fatalf("PurgeOrganization() error = %v", err)
	}
	if api.deletedGraph != id {
		t.Fatalf("deleted graph = %q", api.deletedGraph)
	}
}

func TestResolveSubjectsUsesExactCanonicalHintBeforeSemanticSearch(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	api.nodes[nodeUUID("org_1", subject)] = graphNode(nodeUUID("org_1", subject), subject.Kind, subject.CanonicalID, subject.Label, "*", 0.2)
	request := validRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject || len(api.searches) != 0 {
		t.Fatalf("resolution = %#v searches = %#v", resolution, api.searches)
	}
}

// TestResolveSubjectsExactHintPathRespectsMaxSubjectCandidates is the probe
// for Codex finding G4: the exact-hint branch returned every resolved hint
// unconditionally, unlike the hybrid-search branch below it, which
// truncates to Options.MaxSubjectCandidates. A caller supplying more exact
// hints (including Engine's prior-subject-receipt expansion, up to 20) than
// its own configured budget -- or than the contract's absolute
// SubjectResolution.Candidates bound of 50 -- could produce a resolution
// too large for the final InvestigationResult to validate, failing an
// otherwise entirely valid request deep in a later pipeline stage.
func TestResolveSubjectsExactHintPathRespectsMaxSubjectCandidates(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	hints := make([]contextfabric.SubjectHint, 0, 5)
	for i := 0; i < 5; i++ {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("project_%d", i), Label: fmt.Sprintf("Project %d", i)}
		api.nodes[nodeUUID("org_1", subject)] = graphNode(nodeUUID("org_1", subject), subject.Kind, subject.CanonicalID, subject.Label, "*", 0.2)
		hints = append(hints, contextfabric.SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"})
	}
	request := validRequest()
	request.Options.MaxSubjectCandidates = 2
	request.RequestedScope.SubjectHints = hints
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) > 2 || len(resolution.Committed) > 2 {
		t.Fatalf("resolution = %#v, want at most Options.MaxSubjectCandidates=2 candidates/committed", resolution)
	}
}

// TestResolveSubjectsExactHintForUnauthorizedSubjectIsSkippedSilently proves
// the exact-hint path fails closed with no leak and no error when the named
// subject exists but is not authorized for the calling principal. This is
// the path Engine's prior-subject-receipt expansion feeds into (see
// CHAOS-3754 Engine.resolvePriorSubjectHints): a receipt naming a subject
// the principal is not (or no longer) authorized for must degrade exactly
// the same way an ordinary caller-supplied SubjectHint does.
func TestResolveSubjectsExactHintForUnauthorizedSubjectIsSkippedSilently(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_secret", Label: "Secret Project"}
	api.nodes[nodeUUID("org_1", subject)] = graphNode(nodeUUID("org_1", subject), subject.Kind, subject.CanonicalID, subject.Label, "|other/private|", 1)
	request := validRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "prior_subject_receipt"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}, request, contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v, want a silent skip, not an error", err)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 {
		t.Fatalf("resolution = %#v, want the unauthorized subject to never surface", resolution)
	}
}

// TestResolveSubjectsResolvesSubjectMatchedByAliasOrPreviousName proves alias
// and previous-name resolution end to end: ADR 0007 embeds aliases and
// previous names in the node's indexed search summary
// (entitySearchSummary/projectionEntityAttributes) precisely so a term that
// only matches an alias or a former name -- never the current canonical
// label -- still resolves. The adapter cannot make the backend's semantic
// match happen (that is Zep's hybrid search), but it must accept and
// correctly score a node the backend returned this way: high relevance,
// canonical label different from the search term, single strong result.
func TestResolveSubjectsResolvesSubjectMatchedByAliasOrPreviousName(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	// The canonical label is "Ask Dev"; the term below only matches the
	// previous name "Dev Agent" embedded in the node's summary/attributes,
	// never the label itself, so the exact-match fast path in
	// nodeCandidate cannot fire -- this exercises the hybrid-confidence
	// path exclusively.
	node := graphNode("node-previous-name", contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", "*", 0.9)
	node.Attributes["previous_names"] = "|Dev Agent|"
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{node}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Dev Agent"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution = %#v, want the previous-name match resolved to the canonical subject", resolution)
	}
}

func TestProjectionEnrichesEmbeddedEntityTextWithoutLeakingNativeEvidence(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	batch := validBatch()
	batch.Entities[0].PreviousNames = []string{"Dev Agent"}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	node := api.nodes[nodeUUID(batch.OrgID, batch.Entities[0].Subject)]
	if node == nil || !strings.Contains(node.Summary, "Aliases: AskDev") || !strings.Contains(node.Summary, "Previous names: Dev Agent") {
		t.Fatalf("projected node = %#v", node)
	}
	edge := &zep.EntityEdge{
		UUID: "edge-native", Name: "BLOCKS", Fact: "blocks", Episodes: []string{"zep-native-episode"},
		Attributes: map[string]interface{}{"evidence_refs": "|evidence_canonical_1234|"},
	}
	if got := edgeEvidence(edge); len(got) != 1 || got[0] != "evidence_canonical_1234" {
		t.Fatalf("edge evidence = %#v", got)
	}
}

// TestApplyProjectionBatchCreatesGraphOnceAndIsIdempotentUnderReplay checks
// idempotent replay against full node/edge content snapshots, not just
// counts. Matching sizes and receipt counters alone would still pass if
// replay silently overwrote a node's summary or narrowed its authorization
// scope -- e.g. an upsert that recomputed attributes from a mutated input
// and replaced the original. Comparing full snapshots by deep equality is
// what actually proves replay is a true no-op.
func TestApplyProjectionBatchCreatesGraphOnceAndIsIdempotentUnderReplay(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	// Pin the clock: the organization-root watermark node's observed_at is
	// stamped with a.now() on every projection, so without a fixed clock a
	// true no-op replay would still show a legitimately advancing
	// timestamp on that one node, which is not the mutation this test
	// exists to catch.
	adapter.now = func() time.Time { return time.Date(2026, 8, 11, 21, 0, 0, 0, time.UTC) }
	batch := validBatch()

	first, err := adapter.ApplyProjectionBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("first ApplyProjectionBatch() error = %v", err)
	}
	if _, ok := api.graphs[graphID(adapter.config.GraphPrefix, batch.OrgID)]; !ok {
		t.Fatal("first ApplyProjectionBatch() did not create the organization graph")
	}
	if api.createGraphCalls != 1 {
		t.Fatalf("createGraphCalls = %d, want 1", api.createGraphCalls)
	}
	nodeCount, edgeCount := len(api.nodes), len(api.edges)
	firstTriples := len(api.triples)
	beforeNodes := snapshotNodes(api.nodes)
	beforeEdges := snapshotEdges(api.edges)

	second, err := adapter.ApplyProjectionBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("replay ApplyProjectionBatch() error = %v", err)
	}
	if api.createGraphCalls != 1 {
		t.Fatalf("replay recreated the graph: createGraphCalls = %d", api.createGraphCalls)
	}
	if len(api.nodes) != nodeCount || len(api.edges) != edgeCount {
		t.Fatalf("replay grew backend state: nodes %d->%d edges %d->%d", nodeCount, len(api.nodes), edgeCount, len(api.edges))
	}
	// A replay of the identical batch reissues the same set of
	// AddFactTriple calls (deterministic UUIDs converge the backend to the
	// same final state) -- but it must reissue exactly that set, no more
	// and no fewer. Asserting the total exactly doubles catches both a
	// silently skipped write (e.g. an incorrect early-return that drops a
	// relationship on replay) and silently duplicated writes.
	if got, want := len(api.triples), firstTriples*2; got != want {
		t.Fatalf("replay triple call count = %d, want %d (exactly the first apply's %d calls reissued)", got, want, firstTriples)
	}
	if second.EntitiesApplied != first.EntitiesApplied || second.EdgesApplied != first.EdgesApplied ||
		second.ContentsApplied != first.ContentsApplied || second.EpisodesApplied != first.EpisodesApplied {
		t.Fatalf("replay receipt differs: first=%#v second=%#v", first, second)
	}
	if diff := diffNodeSnapshots(beforeNodes, snapshotNodes(api.nodes)); diff != "" {
		t.Fatalf("replay mutated node content: %s", diff)
	}
	if diff := diffEdgeSnapshots(beforeEdges, snapshotEdges(api.edges)); diff != "" {
		t.Fatalf("replay mutated edge content: %s", diff)
	}
}

type nodeSnapshot struct {
	Name       string
	Summary    string
	Labels     []string
	Attributes map[string]interface{}
}

func snapshotNodes(nodes map[string]*zep.EntityNode) map[string]nodeSnapshot {
	snapshot := make(map[string]nodeSnapshot, len(nodes))
	for id, node := range nodes {
		snapshot[id] = nodeSnapshot{
			Name: node.Name, Summary: node.Summary, Labels: append([]string(nil), node.Labels...),
			Attributes: cloneAnyMap(node.Attributes),
		}
	}
	return snapshot
}

func diffNodeSnapshots(before, after map[string]nodeSnapshot) string {
	if reflect.DeepEqual(before, after) {
		return ""
	}
	if len(before) != len(after) {
		return fmt.Sprintf("node count changed: %d -> %d", len(before), len(after))
	}
	for id, want := range before {
		got, ok := after[id]
		if !ok {
			return fmt.Sprintf("node %s disappeared", id)
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("node %s changed: %#v -> %#v", id, want, got)
		}
	}
	return "node set changed"
}

type edgeSnapshot struct {
	Name           string
	Fact           string
	SourceNodeUUID string
	TargetNodeUUID string
	Attributes     map[string]interface{}
	CreatedAt      string
	ValidAt        *string
	InvalidAt      *string
	ExpiredAt      *string
	Episodes       []string
}

func snapshotEdges(edges map[string]*zep.EntityEdge) map[string]edgeSnapshot {
	snapshot := make(map[string]edgeSnapshot, len(edges))
	for id, edge := range edges {
		snapshot[id] = edgeSnapshot{
			Name: edge.Name, Fact: edge.Fact, SourceNodeUUID: edge.SourceNodeUUID, TargetNodeUUID: edge.TargetNodeUUID,
			Attributes: cloneAnyMap(edge.Attributes),
			// String values are immutable, so copying the *string itself
			// (not a fresh pointer to a copied value) is safe: nothing
			// downstream can mutate what it points to out from under this
			// snapshot, and reflect.DeepEqual compares pointee values, not
			// pointer identity.
			CreatedAt: edge.CreatedAt, ValidAt: edge.ValidAt, InvalidAt: edge.InvalidAt, ExpiredAt: edge.ExpiredAt,
			Episodes: append([]string(nil), edge.Episodes...),
		}
	}
	return snapshot
}

func diffEdgeSnapshots(before, after map[string]edgeSnapshot) string {
	if reflect.DeepEqual(before, after) {
		return ""
	}
	if len(before) != len(after) {
		return fmt.Sprintf("edge count changed: %d -> %d", len(before), len(after))
	}
	for id, want := range before {
		got, ok := after[id]
		if !ok {
			return fmt.Sprintf("edge %s disappeared", id)
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("edge %s changed: %#v -> %#v", id, want, got)
		}
	}
	return "edge set changed"
}

func TestApplyProjectionBatchTombstonesDeleteAcrossKindsAndReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	batch := validBatch()
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("seed ApplyProjectionBatch() error = %v", err)
	}
	relationshipEdgeID := relationshipUUID(batch.OrgID, batch.Relationships[0].RelationshipID)
	documentNodeID := contentUUID(batch.OrgID, "document", batch.Contents[0].ContentID)
	episodeNodeID := contentUUID(batch.OrgID, "episode", batch.Episodes[0].EpisodeID)
	workSubject := batch.Relationships[0].To
	workNodeID := nodeUUID(batch.OrgID, workSubject)
	if _, ok := api.edges[relationshipEdgeID]; !ok {
		t.Fatalf("seed missing edge %s", relationshipEdgeID)
	}
	for _, id := range []string{documentNodeID, episodeNodeID, workNodeID} {
		if _, ok := api.nodes[id]; !ok {
			t.Fatalf("seed missing node %s", id)
		}
	}

	now := time.Date(2026, 8, 11, 22, 0, 0, 0, time.UTC)
	tombstoneBatch := batch
	tombstoneBatch.BatchID = "batch_tombstone1"
	tombstoneBatch.Entities = []contextfabric.EntityProjection{}
	tombstoneBatch.Relationships = []contextfabric.RelationshipProjection{}
	tombstoneBatch.Contents = []contextfabric.ContentProjection{}
	tombstoneBatch.Episodes = []contextfabric.EpisodeProjection{}
	tombstoneBatch.Cursor = batch.NextCursor
	tombstoneBatch.NextCursor = "cursor-3"
	tombstoneBatch.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: "relationship", CanonicalID: batch.Relationships[0].RelationshipID, Reason: "superseded", EffectiveAt: now, SourceVersion: "ops-v1"},
		{Kind: "document", CanonicalID: batch.Contents[0].ContentID, Reason: "superseded", EffectiveAt: now, SourceVersion: "ops-v1"},
		{Kind: "episode", CanonicalID: batch.Episodes[0].EpisodeID, Reason: "superseded", EffectiveAt: now, SourceVersion: "ops-v1"},
		{Kind: string(workSubject.Kind), CanonicalID: workSubject.CanonicalID, Reason: "superseded", EffectiveAt: now, SourceVersion: "ops-v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), tombstoneBatch); err != nil {
		t.Fatalf("tombstone ApplyProjectionBatch() error = %v", err)
	}
	if _, ok := api.edges[relationshipEdgeID]; ok {
		t.Fatalf("edge %s survived tombstone", relationshipEdgeID)
	}
	for _, id := range []string{documentNodeID, episodeNodeID, workNodeID} {
		if _, ok := api.nodes[id]; ok {
			t.Fatalf("node %s survived tombstone", id)
		}
	}

	// Replaying tombstones against an already-absent target is a 404 the
	// adapter must treat as success, not an error.
	tombstoneBatch.BatchID = "batch_tombstone2"
	if _, err := adapter.ApplyProjectionBatch(context.Background(), tombstoneBatch); err != nil {
		t.Fatalf("repeat tombstone ApplyProjectionBatch() error = %v", err)
	}
}

// TestApplyProjectionBatchSkipsStaleOutOfOrderTombstone is the F5
// regression: a tombstone that arrives out of order (its EffectiveAt is
// older than the target's already-stored observed_at) must not delete
// state that a later projection has since re-established. Covers both a
// node-kind tombstone (default subject case) and an edge-kind
// (relationship) tombstone, since deleteNodeIfNotNewer/deleteEdgeIfNotNewer
// share the same staleness check.
func TestApplyProjectionBatchSkipsStaleOutOfOrderTombstone(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	newer := time.Date(2026, 8, 11, 23, 0, 0, 0, time.UTC)
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_live", Label: "Live Project"}
	work := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_live", Label: "Live Work"}

	write := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_newwrite", OrgID: "org_1", Source: "dev-health-ops",
		SourceVersion: "ops-v9", Cursor: "c0", NextCursor: "c1", GeneratedAt: newer,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
			EvidenceRefIDs: []string{"evidence_live_1234"}, ObservedAt: newer, SourceVersion: "ops-v9",
		}},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_live", Type: "DEPENDS_ON", From: project, To: work,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
			EvidenceRefIDs: []string{"evidence_live_1234"}, ObservedAt: newer, SourceVersion: "ops-v9",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), write); err != nil {
		t.Fatalf("write ApplyProjectionBatch() error = %v", err)
	}
	projectNodeID := nodeUUID("org_1", project)
	relationshipEdgeID := relationshipUUID("org_1", "relationship_live")
	if _, ok := api.nodes[projectNodeID]; !ok {
		t.Fatal("seed setup wrong: entity was not projected")
	}
	if _, ok := api.edges[relationshipEdgeID]; !ok {
		t.Fatal("seed setup wrong: relationship was not projected")
	}

	tomb := write
	tomb.BatchID = "batch_staletomb"
	tomb.SourceVersion = "ops-v1"
	tomb.GeneratedAt = stale
	tomb.Cursor = "c1"
	tomb.NextCursor = "c2"
	tomb.Entities = []contextfabric.EntityProjection{}
	tomb.Relationships = []contextfabric.RelationshipProjection{}
	tomb.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: string(project.Kind), CanonicalID: project.CanonicalID, Reason: "stale delete delivered out of order", EffectiveAt: stale, SourceVersion: "ops-v1"},
		{Kind: "relationship", CanonicalID: "relationship_live", Reason: "stale delete delivered out of order", EffectiveAt: stale, SourceVersion: "ops-v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), tomb); err != nil {
		t.Fatalf("tombstone ApplyProjectionBatch() error = %v", err)
	}
	if _, ok := api.nodes[projectNodeID]; !ok {
		t.Fatal("STALE TOMBSTONE WON: a node written at 2026-08-11/ops-v9 was deleted by a tombstone effective 2020-01-01/ops-v1")
	}
	if _, ok := api.edges[relationshipEdgeID]; !ok {
		t.Fatal("STALE TOMBSTONE WON: an edge written at 2026-08-11/ops-v9 was deleted by a tombstone effective 2020-01-01/ops-v1")
	}
}

// TestApplyProjectionBatchSkipsStaleOutOfOrderTombstoneForEpisodeAndContent
// is the R2 regression: episode and content/document target nodes must
// carry observed_at just like entity/relationship nodes do, so the same
// staleness guard proven in TestApplyProjectionBatchSkipsStaleOutOfOrderTombstone
// (entity + relationship) also protects episode and content nodes. Without
// observed_at, tombstoneIsStale can never detect staleness for that kind
// and a stale, out-of-order tombstone deletes a newer episode/content
// unconditionally.
func TestApplyProjectionBatchSkipsStaleOutOfOrderTombstoneForEpisodeAndContent(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	newer := time.Date(2026, 8, 11, 23, 0, 0, 0, time.UTC)
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_live", Label: "Live Project"}

	write := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_newwrite", OrgID: "org_1", Source: "dev-health-ops",
		SourceVersion: "ops-v9", Cursor: "c0", NextCursor: "c1", GeneratedAt: newer,
		Entities: []contextfabric.EntityProjection{},
		Contents: []contextfabric.ContentProjection{{
			ContentID: "content_live", Subject: subject, Title: "Live doc", Body: "body", ContentDigest: "digest_live_1234", Untrusted: true,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
			EvidenceRefIDs: []string{"evidence_live_1234"}, ObservedAt: newer, SourceVersion: "ops-v9",
		}},
		Episodes: []contextfabric.EpisodeProjection{{
			EpisodeID: "episode_live", Subject: subject, Goal: "goal", Outcome: "succeeded", Summary: "summary",
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
			EvidenceRefIDs: []string{"evidence_live_1234"}, StartedAt: newer.Add(-time.Minute), EndedAt: newer, SourceVersion: "ops-v9",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), write); err != nil {
		t.Fatalf("write ApplyProjectionBatch() error = %v", err)
	}
	contentNodeID := contentUUID("org_1", "document", "content_live")
	episodeNodeID := contentUUID("org_1", "episode", "episode_live")
	if _, ok := api.nodes[contentNodeID]; !ok {
		t.Fatal("seed setup wrong: content was not projected")
	}
	if _, ok := api.nodes[episodeNodeID]; !ok {
		t.Fatal("seed setup wrong: episode was not projected")
	}
	if got := stringAttribute(api.nodes[episodeNodeID].Attributes, "observed_at"); got == "" {
		t.Fatal("episode node has no observed_at attribute -- the staleness guard cannot protect it")
	}

	tomb := write
	tomb.BatchID = "batch_staletomb"
	tomb.SourceVersion = "ops-v1"
	tomb.GeneratedAt = stale
	tomb.Cursor = "c1"
	tomb.NextCursor = "c2"
	tomb.Contents = []contextfabric.ContentProjection{}
	tomb.Episodes = []contextfabric.EpisodeProjection{}
	tomb.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: "document", CanonicalID: "content_live", Reason: "stale delete delivered out of order", EffectiveAt: stale, SourceVersion: "ops-v1"},
		{Kind: "episode", CanonicalID: "episode_live", Reason: "stale delete delivered out of order", EffectiveAt: stale, SourceVersion: "ops-v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), tomb); err != nil {
		t.Fatalf("tombstone ApplyProjectionBatch() error = %v", err)
	}
	if _, ok := api.nodes[contentNodeID]; !ok {
		t.Fatal("STALE TOMBSTONE WON: content written at 2026-08-11/ops-v9 was deleted by a tombstone effective 2020-01-01/ops-v1")
	}
	if _, ok := api.nodes[episodeNodeID]; !ok {
		t.Fatal("STALE TOMBSTONE WON: episode written at 2026-08-11/ops-v9 was deleted by a tombstone effective 2020-01-01/ops-v1")
	}
}

// nilReturningAPI wraps fakeAPI and overrides GetNode/GetEdge to return
// (nil, nil) -- reproducing the pinned Zep SDK's documented behavior for an
// HTTP 200 response with a null body, which is distinct from the
// *zep.NotFoundError a genuine 404 produces.
type nilReturningAPI struct{ *fakeAPI }

func (nilReturningAPI) GetNode(context.Context, string) (*zep.EntityNode, error) { return nil, nil }
func (nilReturningAPI) GetEdge(context.Context, string) (*zep.EntityEdge, error) { return nil, nil }

// TestApplyProjectionBatchTreatsNilNilTargetAsAbsentDuringTombstone is the
// R4 regression: deleteNodeIfNotNewer/deleteEdgeIfNotNewer checked the
// GetNode/GetEdge error but not whether the returned entity was itself nil
// before dereferencing its Attributes for the staleness check. The pinned
// SDK can return (nil, nil) for an HTTP 200 with a null body, which is not
// an error at all, so the previous code panicked instead of treating the
// target the same way a genuine 404 already is: absent, tombstone is a
// no-op success.
func TestApplyProjectionBatchTreatsNilNilTargetAsAbsentDuringTombstone(t *testing.T) {
	t.Parallel()
	api := nilReturningAPI{fakeAPI: newFakeAPI()}
	adapter := mustAdapter(t, api)
	now := time.Date(2026, 8, 11, 22, 0, 0, 0, time.UTC)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_absent", Label: "Absent Project"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_nilnil_tombstone", OrgID: "org_1", Source: "dev-health-ops",
		SourceVersion: "ops-v1", Cursor: "c0", NextCursor: "c1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Relationships: []contextfabric.RelationshipProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{
			{Kind: string(project.Kind), CanonicalID: project.CanonicalID, Reason: "already absent", EffectiveAt: now, SourceVersion: "ops-v1"},
			{Kind: "relationship", CanonicalID: "relationship_absent", Reason: "already absent", EffectiveAt: now, SourceVersion: "ops-v1"},
		},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() with a nil,nil GetNode/GetEdge target error = %v", err)
	}
}

func TestPurgeOrganizationIsIdempotentWhenGraphAlreadyAbsent(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	api.deleteGraphErr = &zep.NotFoundError{}
	if err := adapter.PurgeOrganization(context.Background(), "org_never_projected"); err != nil {
		t.Fatalf("PurgeOrganization() on an absent graph error = %v", err)
	}
}

func TestProjectionWatermarkReturnsNotFoundWhenUnset(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	_, err := adapter.ProjectionWatermark(context.Background(), "org_1", "dev-health-ops")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ProjectionWatermark() on an unset watermark error = %v, want ErrNotFound", err)
	}
}

func TestResolveSubjectsReturnsSafeNoMatchWithoutCandidates(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Nothing Matches This"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 || resolution.ClarificationPrompt != "" {
		t.Fatalf("resolution = %#v, want a safe no-match result", resolution)
	}
}

func TestResolveSubjectsMarksCloseCandidatesAmbiguousAndOffersClarification(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	alpha := graphNode("node-alpha", contextfabric.SubjectProject, "project_alpha", "Widget Alpha", "*", 0.75)
	beta := graphNode("node-beta", contextfabric.SubjectProject, "project_beta", "Widget Beta", "*", 0.70)
	api.searchResult = &zep.GraphSearchResults{Nodes: []*zep.EntityNode{alpha, beta}}
	request := validRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Which widget"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none for ambiguous candidates", resolution.Committed)
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want two ambiguous candidates", resolution.Candidates)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.State != contextfabric.ResolutionAmbiguous {
			t.Fatalf("candidate state = %q, want ambiguous", candidate.State)
		}
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for an ambiguous, clarification-allowed request")
	}
}

func TestRelationshipProjectionPreservesPriorCanonicalEntityMetadataAcrossBatches(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)
	observed := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	entityOnlyBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_entity_only", OrgID: "org_1", Source: "dev-health-ops",
		SourceVersion: "ops-v1", Cursor: "cursor-0", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Aliases: []string{"AskDev"}, ProviderIDs: map[string]string{"linear": "project-1"},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_project_1234"}, ObservedAt: observed, SourceVersion: "ops-v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), entityOnlyBatch); err != nil {
		t.Fatalf("entity-only ApplyProjectionBatch() error = %v", err)
	}
	subjectNodeID := nodeUUID(entityOnlyBatch.OrgID, project)
	seeded := api.nodes[subjectNodeID]
	if seeded == nil || stringAttribute(seeded.Attributes, "aliases") != "|AskDev|" || seeded.Attributes["provider_linear"] != "project-1" {
		t.Fatalf("seeded node attributes = %#v", seeded)
	}

	later := observed.Add(time.Hour)
	incident := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident_1", Label: "Latency Incident"}
	relationshipOnlyBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_relationship_only", OrgID: "org_1", Source: "dev-health-ops",
		SourceVersion: "ops-v2", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: later,
		Entities: []contextfabric.EntityProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_incident_1", Type: "AFFECTED_BY", From: project, To: incident,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_incident_1234"}, ObservedAt: later, SourceVersion: "ops-v2",
		}},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), relationshipOnlyBatch); err != nil {
		t.Fatalf("relationship-only ApplyProjectionBatch() error = %v", err)
	}
	updated := api.nodes[subjectNodeID]
	if updated == nil {
		t.Fatal("subject node disappeared after relationship-only projection")
	}
	if stringAttribute(updated.Attributes, "aliases") != "|AskDev|" || updated.Attributes["provider_linear"] != "project-1" {
		t.Fatalf("relationship-only upsert erased canonical entity metadata: %#v", updated.Attributes)
	}
	if stringAttribute(updated.Attributes, "observed_at") != later.UTC().Format(time.RFC3339Nano) || stringAttribute(updated.Attributes, "source_version") != "ops-v2" {
		t.Fatalf("relationship-only upsert did not refresh temporal/source fields: %#v", updated.Attributes)
	}
}

// TestSDKAPIGetCallsRetryBoundedAttemptsOnServerErrors proves bounded retry
// for the bodyless read path (GetNode/GetGraph/GetNodeEdges): the SDK's
// injected *http.Client and its Retrier issue up to Config.MaxAttempts
// attempts and succeed once the transient failure clears.
func TestSDKAPIGetCallsRetryBoundedAttemptsOnServerErrors(t *testing.T) {
	t.Parallel()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"transient"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid":"node-1","attributes":{}}`))
	}))
	defer server.Close()
	client, err := newSDKAPI(Config{
		BaseURL: server.URL + "/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: 2 * time.Second, MaxAttempts: 2, MaxResults: 10, AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("newSDKAPI() error = %v", err)
	}
	if _, err := client.GetNode(context.Background(), "node-1"); err != nil {
		t.Fatalf("GetNode() with a bounded retry budget error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want exactly 2 (initial attempt plus one bounded retry)", requests)
	}
}

// TestSDKAPIBodyBearingCallsMakeExactlyOneRequestOnServerError is the F2
// hardening: internal/caller.go's Retrier in the pinned SDK version
// (v3.22.0) re-invokes client.Do with the SAME *http.Request across
// attempts and never explicitly rewinds Request.Body between them, which
// previously made a bounded retry against a transient 5xx racy for a
// body-bearing call -- depending on Go's HTTP transport-level connection
// reuse timing, the retried attempt sometimes reached the server
// successfully and sometimes failed client-side with a net/http transfer
// error ("ContentLength=N with Body length 0") before ever reaching the
// network. Both outcomes were reproduced locally against this pinned SDK
// version. Body-bearing calls (Search here; the same applies to
// AddFactTriple and CreateGraph) now pass a per-call MaxAttempts(1) that
// overrides the client's configured retry budget, so the outcome is
// deterministic: the server is hit exactly once, and no dependency response
// body leaks through safeDependencyError, regardless of how high
// Config.MaxAttempts is set.
func TestSDKAPIBodyBearingCallsMakeExactlyOneRequestOnServerError(t *testing.T) {
	t.Parallel()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"transient-secret-detail"}`))
	}))
	defer server.Close()
	client, err := newSDKAPI(Config{
		BaseURL: server.URL + "/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: 2 * time.Second, MaxAttempts: 3, MaxResults: 10, AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("newSDKAPI() error = %v", err)
	}
	scope := zep.GraphSearchScopeNodes
	graph := graphID("acr-cf", "org-retry")
	_, searchErr := client.Search(context.Background(), &zep.GraphSearchQuery{GraphID: &graph, Query: "Ask Dev", Scope: &scope})
	if searchErr == nil {
		t.Fatal("Search() against a persistently failing server unexpectedly succeeded")
	}
	classified := safeDependencyError("search", searchErr)
	if classified == nil || classified.Error() == "" {
		t.Fatalf("safeDependencyError() produced no safe error for %v", searchErr)
	}
	if strings.Contains(classified.Error(), "transient-secret-detail") {
		t.Fatalf("safeDependencyError() leaked dependency detail: %q", classified.Error())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly 1: a body-bearing call must never retry against this pinned SDK version's non-rewinding request body, regardless of Config.MaxAttempts", requests)
	}
}

func TestSDKAPIPropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	client, err := newSDKAPI(Config{
		BaseURL: "http://127.0.0.1:9999/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 10, AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("newSDKAPI() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scope := zep.GraphSearchScopeNodes
	graph := graphID("acr-cf", "org-cancel")
	_, err = client.Search(ctx, &zep.GraphSearchQuery{GraphID: &graph, Query: "x", Scope: &scope})
	if classified := safeDependencyError("search", err); !errors.Is(classified, context.Canceled) {
		t.Fatalf("Search() with a canceled context error = %v, classified = %v", err, classified)
	}
}

func TestSDKAPIUsesPinnedClientBaseURLAuthenticationAndSafeRateLimitClassification(t *testing.T) {
	t.Parallel()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/api/v2/graph/search" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Api-Key test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body zep.GraphSearchQuery
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.GraphID == nil || !strings.HasPrefix(*body.GraphID, "acr-cf-") || strings.Contains(*body.GraphID, "org-secret") {
			t.Errorf("graph ID = %#v", body.GraphID)
		}
		if requests == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"nodes":[],"edges":[],"episodes":[],"observations":[],"thread_summaries":[]}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer server.Close()
	client, err := newSDKAPI(Config{
		BaseURL: server.URL + "/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 10, AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("newSDKAPI() error = %v", err)
	}
	scope := zep.GraphSearchScopeNodes
	graph := graphID("acr-cf", "org-secret")
	if _, err := client.Search(context.Background(), &zep.GraphSearchQuery{GraphID: &graph, Query: "Ask Dev", Scope: &scope}); err != nil {
		t.Fatalf("first Search() error = %v", err)
	}
	_, err = client.Search(context.Background(), &zep.GraphSearchQuery{GraphID: &graph, Query: "Ask Dev", Scope: &scope})
	if err == nil || !errors.Is(safeDependencyError("search", err), ErrRateLimited) {
		t.Fatalf("second Search() error = %v classified = %v", err, safeDependencyError("search", err))
	}
}

func TestNewSDKAPIIsPinnedAndConstructible(t *testing.T) {
	t.Parallel()
	client, err := newSDKAPI(Config{
		BaseURL: "http://127.0.0.1:9999/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 10, AllowInsecure: true,
	})
	if err != nil || client == nil || SDKVersion != "v3.22.0" {
		t.Fatalf("newSDKAPI() client = %#v err = %v SDKVersion = %q", client, err, SDKVersion)
	}
}

// TestLiveZepContextFabricLifecycle proves the full CHAOS-3752 adapter
// contract against a real Zep endpoint: (1) create an isolated per-org
// graph, (2) project canonical entities/relationships/content/episodes,
// (3) replay the same batch to prove idempotency, (4) retrieve the
// projected subject and its relationship, (5) verify temporal and evidence
// metadata survived the round trip, (6) tombstone the relationship and
// confirm it no longer surfaces, (7) read the projection watermark, (8)
// purge the organization graph (and prove the purge itself is idempotent),
// (9) verify organization isolation against a second live org, and (10)
// clean up both organizations. It is skipped unless ACR_TEST_ZEP_BASE_URL
// and ACR_TEST_ZEP_API_KEY are set; see docs/adr/0007 for what those values
// must be in the absence of a self-hostable Zep server.
func TestLiveZepContextFabricLifecycle(t *testing.T) {
	baseURL := os.Getenv("ACR_TEST_ZEP_BASE_URL")
	apiKey := os.Getenv("ACR_TEST_ZEP_API_KEY")
	if baseURL == "" || apiKey == "" {
		t.Skip("set ACR_TEST_ZEP_BASE_URL and ACR_TEST_ZEP_API_KEY for the live Zep adapter contract")
	}
	adapter, err := New(Config{BaseURL: baseURL, APIKey: apiKey, GraphPrefix: "acr-cf-test", RequestTimeout: 30 * time.Second, MaxAttempts: 2, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stamp := strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")

	// (1) An isolated graph is created implicitly, server-derived from the
	// organization ID, the first time a batch is applied for that org.
	// (2) Project one canonical entity, relationship, content item, and
	// episode in a single batch.
	batch := validBatch()
	batch.OrgID = "live-contract-a-" + stamp
	otherOrgID := "live-contract-b-" + stamp
	principal := storage.Principal{OrgID: batch.OrgID}
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), batch.OrgID) })
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), otherOrgID) })

	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("(1)/(2) live ApplyProjectionBatch() error = %v", err)
	}

	// (3) Replaying the identical batch must be idempotent through
	// deterministic identities: no error, no duplicate state.
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("(3) live idempotent replay ApplyProjectionBatch() error = %v", err)
	}

	// (4) Retrieve the projected subject by exact canonical hint, then
	// discover its projected relationship.
	project := batch.Entities[0].Subject
	request := validRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: project.Kind, ID: project.CanonicalID, Label: project.Label, Source: "live-test"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{project.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted)
	if err != nil {
		t.Fatalf("(4) live ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("(4) live ResolveSubjects() resolution = %#v", resolution)
	}
	request.Question = "What is Ask Dev depending on?"
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: request,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "dependencies", SubjectTerms: []string{project.Label},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
		},
		Resolution: resolution,
	}
	graphContext, err := adapter.DiscoverContext(ctx, principal, discovery)
	if err != nil {
		t.Fatalf("(4) live DiscoverContext() error = %v", err)
	}
	dependencyEdge := findEdgeByType(graphContext.Paths, "DEPENDS_ON")
	if dependencyEdge == nil {
		t.Fatalf("(4) live DiscoverContext() did not surface the projected DEPENDS_ON relationship: %#v", graphContext.Paths)
	}

	// (5) The retrieved relationship must carry its projected temporal
	// bounds and canonical evidence references, not backend-native ones.
	if dependencyEdge.ValidFrom == nil || dependencyEdge.ValidTo == nil || len(dependencyEdge.EvidenceRefIDs) == 0 || dependencyEdge.EvidenceRefIDs[0] != "evidence_dependency_1234" {
		t.Fatalf("(5) live relationship temporal/evidence metadata = %#v", dependencyEdge)
	}

	// (6) Tombstone the relationship and confirm it no longer surfaces.
	tombstoneBatch := batch
	tombstoneBatch.BatchID = "batch_live_tombstone_" + stamp
	tombstoneBatch.Cursor = batch.NextCursor
	tombstoneBatch.NextCursor = "cursor-live-3"
	tombstoneBatch.Entities = []contextfabric.EntityProjection{}
	tombstoneBatch.Relationships = []contextfabric.RelationshipProjection{}
	tombstoneBatch.Contents = []contextfabric.ContentProjection{}
	tombstoneBatch.Episodes = []contextfabric.EpisodeProjection{}
	tombstoneBatch.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: "relationship", CanonicalID: batch.Relationships[0].RelationshipID, Reason: "live contract test cleanup", EffectiveAt: time.Now().UTC(), SourceVersion: "ops-v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, tombstoneBatch); err != nil {
		t.Fatalf("(6) live tombstone ApplyProjectionBatch() error = %v", err)
	}
	afterTombstone, err := adapter.DiscoverContext(ctx, principal, discovery)
	if err != nil {
		t.Fatalf("(6) live DiscoverContext() after tombstone error = %v", err)
	}
	if edge := findEdgeByType(afterTombstone.Paths, "DEPENDS_ON"); edge != nil {
		t.Fatalf("(6) tombstoned relationship still surfaced: %#v", edge)
	}

	// (7) Read the projection watermark; it must reflect the last applied
	// (tombstone) batch, not the original projection batch.
	watermark, err := adapter.ProjectionWatermark(ctx, batch.OrgID, batch.Source)
	if err != nil {
		t.Fatalf("(7) live ProjectionWatermark() error = %v", err)
	}
	if watermark.Cursor != tombstoneBatch.NextCursor || watermark.SourceVersion != tombstoneBatch.SourceVersion {
		t.Fatalf("(7) live watermark = %#v", watermark)
	}

	// (9) Verify organization isolation: a second, live organization
	// project the same canonical labels under its own server-derived graph
	// and must resolve to its own node, proving isolation is structural
	// rather than a coincidental empty result.
	otherBatch := validBatch()
	otherBatch.OrgID = otherOrgID
	if _, err := adapter.ApplyProjectionBatch(ctx, otherBatch); err != nil {
		t.Fatalf("(9) live cross-org ApplyProjectionBatch() error = %v", err)
	}
	crossOrgResolution, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: otherOrgID}, request, interpreted)
	if err != nil {
		t.Fatalf("(9) live cross-org ResolveSubjects() error = %v", err)
	}
	if len(crossOrgResolution.Committed) != 1 || crossOrgResolution.Committed[0] != project {
		t.Fatalf("(9) live cross-org resolution = %#v", crossOrgResolution)
	}

	// (8) Purge the first organization's graph. Purge must be idempotent:
	// calling it again against an already-absent graph must not error.
	if err := adapter.PurgeOrganization(ctx, batch.OrgID); err != nil {
		t.Fatalf("(8) live PurgeOrganization() error = %v", err)
	}
	if err := adapter.PurgeOrganization(ctx, batch.OrgID); err != nil {
		t.Fatalf("(8) live repeat PurgeOrganization() on an absent graph error = %v", err)
	}

	// (9) Purging the first organization must never affect the second: its
	// canonical node, addressed by its own server-derived UUID, must still
	// resolve.
	survivingResolution, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: otherOrgID}, request, interpreted)
	if err != nil {
		t.Fatalf("(9) live surviving-org ResolveSubjects() error = %v", err)
	}
	if len(survivingResolution.Committed) != 1 {
		t.Fatalf("(9) purging one organization affected another: %#v", survivingResolution)
	}

	// (10) Explicit cleanup beyond t.Cleanup, so a failure above still
	// leaves both graphs purged once this line is reached.
	if err := adapter.PurgeOrganization(ctx, otherOrgID); err != nil {
		t.Fatalf("(10) live cleanup PurgeOrganization() error = %v", err)
	}
}

func findEdgeByType(paths []contextfabric.RelationshipPath, relationType string) *contextfabric.RelationshipEdge {
	for pathIndex := range paths {
		for edgeIndex := range paths[pathIndex].Edges {
			if paths[pathIndex].Edges[edgeIndex].Type == relationType {
				return &paths[pathIndex].Edges[edgeIndex]
			}
		}
	}
	return nil
}

func mustAdapter(t *testing.T, api api) *Adapter {
	t.Helper()
	adapter, err := newWithAPI(Config{
		BaseURL: "http://127.0.0.1:9999/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, AllowInsecure: true,
	}, api)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	return adapter
}

func validRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678",
		Question: "What is driving Ask Dev?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
}

func validBatch() contextfabric.ProjectionBatch {
	observed := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	validFrom := observed.Add(-time.Hour)
	validTo := observed.Add(time.Hour)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	work := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_release", Label: "Release acceptance"}
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_12345678", OrgID: "org_1", Source: "dev-health-ops",
		SourceVersion: "ops-v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Aliases: []string{"AskDev"}, ProviderIDs: map[string]string{"linear": "project-1"},
			Properties:     map[string]contextfabric.ScalarValue{"lifecycle": {String: ptr("active")}},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_project_1234"}, ObservedAt: observed, SourceVersion: "ops-v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_1234", Type: "DEPENDS_ON", From: project, To: work,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_dependency_1234"}, ObservedAt: observed, ValidFrom: &validFrom, ValidTo: &validTo, SourceVersion: "ops-v1",
		}},
		Contents: []contextfabric.ContentProjection{{
			ContentID: "document_1234", Subject: project, Title: "Ask Dev readiness", Body: "Release acceptance remains open.", ContentDigest: strings.Repeat("a", 64),
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_document_1234"}, ObservedAt: observed, SourceVersion: "ops-v1", Untrusted: true,
		}},
		Episodes: []contextfabric.EpisodeProjection{{
			EpisodeID: "episode_1234", Subject: project, Goal: "Deliver Ask Dev", Outcome: "partial", Summary: "Implementation completed without product acceptance.",
			Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}}, EvidenceRefIDs: []string{"evidence_episode_1234"},
			StartedAt: validFrom, EndedAt: observed, SourceVersion: "ops-v1",
		}},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

func graphNode(uuid string, kind contextfabric.SubjectKind, canonicalID, label, repositories string, relevance float64) *zep.EntityNode {
	return &zep.EntityNode{
		UUID: uuid, Name: label, Labels: []string{zepLabel(kind)}, Relevance: &relevance,
		Attributes: map[string]interface{}{
			"canonical_id": canonicalID, "subject_kind": string(kind), "label": label,
			"authorization_repositories": repositories, "authorization_projects": "*", "authorization_teams": "*",
			"evidence_refs": "|evidence_identity_1234|",
		},
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneAnyMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	result := make(map[string]interface{}, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

var _ api = (*fakeAPI)(nil)
var _ = errors.Is
