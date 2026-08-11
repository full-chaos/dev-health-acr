package zepgraph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	zep "github.com/getzep/zep-go/v3"
)

type fakeAPI struct {
	graphs       map[string]*zep.Graph
	triples      []*zep.AddTripleRequest
	nodes        map[string]*zep.EntityNode
	edges        map[string]*zep.EntityEdge
	searchResult *zep.GraphSearchResults
	searches     []*zep.GraphSearchQuery
	deletedGraph string
	err          error
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
	graphID := request.GraphID
	graph := &zep.Graph{GraphID: &graphID, Name: request.Name, Description: request.Description}
	f.graphs[graphID] = graph
	return graph, nil
}

func (f *fakeAPI) DeleteGraph(_ context.Context, graphID string) error {
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

func TestLiveZepProjectionAndPurge(t *testing.T) {
	baseURL := os.Getenv("ACR_TEST_ZEP_BASE_URL")
	apiKey := os.Getenv("ACR_TEST_ZEP_API_KEY")
	if baseURL == "" || apiKey == "" {
		t.Skip("set ACR_TEST_ZEP_BASE_URL and ACR_TEST_ZEP_API_KEY for the live Zep adapter contract")
	}
	adapter, err := New(Config{BaseURL: baseURL, APIKey: apiKey, GraphPrefix: "acr-cf-test", RequestTimeout: 30 * time.Second, MaxAttempts: 2, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	batch := validBatch()
	batch.OrgID = "live-contract-" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), batch.OrgID) })
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("live ApplyProjectionBatch() error = %v", err)
	}
	if _, err := adapter.ProjectionWatermark(context.Background(), batch.OrgID, batch.Source); err != nil {
		t.Fatalf("live ProjectionWatermark() error = %v", err)
	}
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
