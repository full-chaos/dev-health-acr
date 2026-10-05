package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A repository's work items are served from the linked pull request walk. When
// the answer item ceiling cuts the list, these tests read what an MCP client
// receives -- the structured JSON and the markdown -- from the real engine
// behind the real hosted route and the real MCP server; only the walk's rows
// and the model's draft are controlled.

var cutRepository = contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:3f1c9a52-6a1e-4f0b-9d55-1c2b7e9d4a10", Label: "acme/api"}

type cutWalkGraph struct {
	walk contextfabric.TreeWorkItemWalk
}

func (g *cutWalkGraph) TreeWorkItemMembers(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, contextfabric.RequestedScope, contextfabric.SubjectRef, int) (contextfabric.TreeWorkItemWalk, error) {
	return g.walk, nil
}

type cutRepositoryGraph struct{}

func (cutRepositoryGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "member-cut", Epoch: 1}, nil
}

func (cutRepositoryGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_member_cut_repository", Subject: cutRepository, State: contextfabric.ResolutionCommitted,
		MatchedTerms: []string{"acme/api"}, MatchReasons: []string{"exact"}, Confidence: 1,
		EvidenceRefIDs: []string{"evidence_identity_1234"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias},
	}
	bases := contextfabric.CommitBasisSet{}
	bases.Record(cutRepository, contextfabric.CommitBasisAuthoritativeIdentity)
	digests := contextfabric.CommitDecisionDigestSet{}
	digests.Record(cutRepository, contextfabric.CommitDecisionDigest{CommitGate: "identity_fast_path", IdentityProven: true})
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{cutRepository}}, contextfabric.StructureOfferMaterial{}, bases, digests, nil
}

func (cutRepositoryGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{}, fmt.Errorf("a repository work-item question reached graph discovery")
}

type cutRepositoryModel struct{ freshTupleModel }

func (m cutRepositoryModel) InterpretQuestion(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	interpreted, receipt, err := m.freshTupleModel.InterpretQuestion(ctx, p, r)
	receipt.ScopeAnchorKind = contextfabric.SubjectRepository
	receipt.ScopeAnchorTerm = "acme/api"
	interpreted.SubjectTerms = []string{"acme/api"}
	return interpreted, receipt, err
}

type cutRig struct {
	server *httptest.Server
	token  string
	boot   *acrmcp.Bootstrap
	store  *memoryinvestigation.Store
	graph  *cutWalkGraph
}

func cutWalk(t *testing.T, count, denied int, tier func(i int) string) contextfabric.TreeWorkItemWalk {
	t.Helper()
	walk := contextfabric.TreeWorkItemWalk{PullRequests: 1, LinkedIssues: count + denied, Denied: denied}
	for i := 0; i < count; i++ {
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", fmt.Sprintf("ENG-%04d", i)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		walk.Members = append(walk.Members, contextfabric.TreeWorkItemMember{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: fmt.Sprintf("ENG-%04d", i)}, Tier: tier(i)})
	}
	return walk
}

func newCutRig(t *testing.T, walk contextfabric.TreeWorkItemWalk) *cutRig {
	t.Helper()
	fixture := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 30, MaxTokens: 16000, MaxBytes: 1 << 20})
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(fixture.client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model := cutRepositoryModel{freshTupleModel: *fixture.model}
	graph := &cutWalkGraph{walk: walk}
	store := memoryinvestigation.NewStore()
	dependencies := fixture.dependencies
	dependencies.Interpreter = contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry}
	dependencies.Graph = cutRepositoryGraph{}
	dependencies.Results = store
	dependencies.TreeWorkItemGraph = graph
	dependencies.TreeWorkItemFilter = nil
	dependencies.TreeWorkItemGate = newResponseOwnerAPITestGate(t)
	options := fixture.engineOptions
	options.MaxItems = 30
	nextID := 0
	options.NewResultID = func() string { nextID++; return fmt.Sprintf("result_member_cut_%02d", nextID) }
	options.Now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	engine, err := contextfabric.NewEngine(dependencies, options)
	if err != nil {
		t.Fatal(err)
	}
	app, token := newParityHostedAppWithBudget(t, investigatorFunc(func(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		result, err := engine.Investigate(ctx, p, r)
		if err != nil {
			t.Logf("engine error: %v", err)
		}
		return result, err
	}), store, limits.ResourceBudget{MaxItems: 30, MaxTokens: 500_000, MaxBytes: 8 << 20})
	app.config.RequestTimeout = 30 * time.Second
	app.config.MaxItems = 30
	server := httptest.NewTLSServer(app.InstrumentedHandler(app.Handler()))
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("real MCP bootstrap: %v", err)
	}
	return &cutRig{server: server, token: token, boot: boot, store: store, graph: graph}
}

func (r *cutRig) ask(t *testing.T) servedAnswer {
	t.Helper()
	return callRealMCPTool(t, r.boot, "investigate_question", contractsv1.MCPInvestigateQuestionRequest{
		Question:       "Which issues belong to repository acme/api?",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
	})
}

type servedCohort struct {
	Total, Population, Listed int
	LowerBound                bool
	Sentences                 []string
	Markdown                  string
}

func readServedCohort(t *testing.T, answer servedAnswer) servedCohort {
	t.Helper()
	var node struct {
		Structured struct {
			Cohort struct {
				Total                int  `json:"total"`
				Population           int  `json:"population"`
				PopulationLowerBound bool `json:"population_lower_bound"`
				Members              []json.RawMessage
			} `json:"cohort"`
			Limitations []string `json:"limitations"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	c := node.Structured.Cohort
	served := servedCohort{Total: c.Total, Population: c.Population, Listed: len(c.Members), LowerBound: c.PopulationLowerBound, Markdown: answer.markdown}
	for _, limitation := range node.Structured.Limitations {
		if contractsv1.IsContextFabricWorkItemListedLimitation(limitation) || limitation == contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation {
			served.Sentences = append(served.Sentences, limitation)
		}
	}
	return served
}

func (c servedCohort) tierSentence() bool {
	for _, s := range c.Sentences {
		if s == contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation {
			return true
		}
	}
	return false
}

func (c servedCohort) listedSentence() string {
	for _, s := range c.Sentences {
		if contractsv1.IsContextFabricWorkItemListedLimitation(s) {
			return s
		}
	}
	return ""
}

func TestACutRepositoryListReachesTheClientWithItsPopulation(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative }))
	got := readServedCohort(t, rig.ask(t))
	if got.Listed >= 20 || got.Listed == 0 {
		t.Fatalf("listed = %d, want a list cut by the item ceiling", got.Listed)
	}
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(got.Listed, 20, false, contractsv1.ContextFabricWorkItemListCutServer)
	if got.Population != 20 || got.LowerBound || got.listedSentence() != want || got.tierSentence() {
		t.Fatalf("served cohort = %+v, want population 20, sentence %q, no tier sentence", got, want)
	}
	if !strings.Contains(got.Markdown, "## Cohort ("+strconv.Itoa(got.Listed)+" of 20 shown)") {
		t.Fatalf("the cohort heading the client reads does not state the population")
	}
	if !strings.Contains(got.Markdown, want) {
		t.Fatalf("the markdown the client receives lacks %q", want)
	}
}

func TestAMixedTierCutReachesTheClientWithBothSentences(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(i int) string {
		if i < 10 {
			return contextfabric.TreeLinkTierNative
		}
		return contextfabric.TreeLinkTierHeuristic
	}))
	got := readServedCohort(t, rig.ask(t))
	if got.Population != 20 || !got.tierSentence() || got.listedSentence() == "" {
		t.Fatalf("served cohort = %+v, want population 20 with the tier sentence and N of M", got)
	}
}

func TestHiddenMembersChangeNothingTheClientReceives(t *testing.T) {
	none := readServedCohort(t, newCutRig(t, cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative })).ask(t))
	hidden := readServedCohort(t, newCutRig(t, cutWalk(t, 20, 7, func(int) string { return contextfabric.TreeLinkTierNative })).ask(t))
	if none.Population != 20 || hidden.Population != 20 || none.listedSentence() != hidden.listedSentence() || none.Total != hidden.Total || none.Listed != hidden.Listed {
		t.Fatalf("hidden members change the served answer: none=%+v hidden=%+v", none, hidden)
	}
}

func TestTheStoredResultServesTheSameCutAsTheFreshAnswer(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(i int) string {
		if i < 10 {
			return contextfabric.TreeLinkTierNative
		}
		return contextfabric.TreeLinkTierHeuristic
	}))
	fresh := rig.ask(t)
	var id struct {
		Structured struct {
			ResultID string `json:"result_id"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(fresh.structured, &id); err != nil || id.Structured.ResultID == "" {
		t.Fatalf("fresh answer carries no result id: %v %s", err, fresh.structured)
	}
	stored := callRealMCPTool(t, rig.boot, "investigation_result", contractsv1.MCPInvestigationResultRequest{ResultID: id.Structured.ResultID})
	a, b := readServedCohort(t, fresh), readServedCohort(t, stored)
	if a.Population != 20 || !a.tierSentence() {
		t.Fatalf("fresh = %+v, want population 20 with the tier sentence", a)
	}
	// investigation_result returns the stored result, which has no projected total.
	if a.Population != b.Population || a.Listed != b.Listed || a.LowerBound != b.LowerBound || strings.Join(a.Sentences, "|") != strings.Join(b.Sentences, "|") {
		t.Fatalf("stored differs from fresh:\nfresh  %+v\nstored %+v", a, b)
	}
}

func (cutRepositoryGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range outcomes {
		outcomes[index] = contextfabric.StoredSubjectAdmitted
	}
	return outcomes, nil
}

func TestTheStoredResultKeepsALowerBoundCut(t *testing.T) {
	walk := cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative })
	walk.Truncated = true
	rig := newCutRig(t, walk)
	fresh := rig.ask(t)
	var id struct {
		Structured struct {
			ResultID string `json:"result_id"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(fresh.structured, &id); err != nil || id.Structured.ResultID == "" {
		t.Fatalf("fresh answer carries no result id: %v %s", err, fresh.structured)
	}
	stored := callRealMCPTool(t, rig.boot, "investigation_result", contractsv1.MCPInvestigationResultRequest{ResultID: id.Structured.ResultID})
	a, b := readServedCohort(t, fresh), readServedCohort(t, stored)
	if !a.LowerBound || !strings.Contains(a.listedSentence(), " of at least 20 members") {
		t.Fatalf("fresh = %+v, want a lower bound stated as at least 20", a)
	}
	if a.LowerBound != b.LowerBound || a.Population != b.Population || strings.Join(a.Sentences, "|") != strings.Join(b.Sentences, "|") {
		t.Fatalf("stored differs from fresh:\nfresh  %+v\nstored %+v", a, b)
	}
}

// viewByID reads the stored result through the hosted route's projection view with a
// member limit of its own, the way a client reads a result again with another budget.
func (r *cutRig) viewByID(t *testing.T, fresh servedAnswer, members int) servedCohort {
	t.Helper()
	var id struct {
		Structured struct {
			ResultID string `json:"result_id"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(fresh.structured, &id); err != nil || id.Structured.ResultID == "" {
		t.Fatalf("fresh answer carries no result id: %v %s", err, fresh.structured)
	}
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/context-fabric/investigations/%s?view=projection&max_cohort_members=%d&max_evidence_refs=500", r.server.URL, id.Structured.ResultID, members), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.2.5")
	response, err := r.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("projection view: status %d err %v body %.500s", response.StatusCode, err, body)
	}
	return readServedCohort(t, servedAnswer{structured: json.RawMessage(`{"structured":` + string(body) + `}`)})
}

func (c servedCohort) listedSentences() []string {
	var out []string
	for _, s := range c.Sentences {
		if contractsv1.IsContextFabricWorkItemListedLimitation(s) {
			out = append(out, s)
		}
	}
	return out
}

func TestTheListedSentenceIsStatedForWhatTheResponseLists(t *testing.T) {
	native := func(int) string { return contextfabric.TreeLinkTierNative }
	sentenceFor := func(listed, population int, cut contractsv1.ContextFabricWorkItemListCut) string {
		s, _ := contractsv1.ContextFabricWorkItemListedLimitation(listed, population, false, cut)
		return s
	}
	t.Run("the engine cut only", func(t *testing.T) {
		rig := newCutRig(t, cutWalk(t, 20, 0, native))
		got := readServedCohort(t, rig.ask(t))
		if want := sentenceFor(got.Listed, 20, contractsv1.ContextFabricWorkItemListCutServer); got.Listed != 14 || !slices.Equal(got.listedSentences(), []string{want}) {
			t.Fatalf("served %+v, want 14 listed with %q", got, want)
		}
	})
	t.Run("the response cut only", func(t *testing.T) {
		rig := newCutRig(t, cutWalk(t, 12, 0, native))
		fresh := rig.ask(t)
		if f := readServedCohort(t, fresh); f.Listed != 12 || len(f.listedSentences()) != 0 {
			t.Fatalf("fresh %+v, want all 12 listed and no cut sentence", f)
		}
		view := rig.viewByID(t, fresh, 3)
		if want := sentenceFor(3, 12, contractsv1.ContextFabricWorkItemListCutResponse); view.Listed != 3 || !slices.Equal(view.listedSentences(), []string{want}) {
			t.Fatalf("view with 3 members %+v, want 3 listed with %q", view, want)
		}
	})
	t.Run("both cuts, and a read again with another limit", func(t *testing.T) {
		rig := newCutRig(t, cutWalk(t, 20, 0, native))
		fresh := rig.ask(t)
		same := rig.viewByID(t, fresh, 25)
		if f := readServedCohort(t, fresh); !slices.Equal(same.listedSentences(), f.listedSentences()) || same.Listed != f.Listed {
			t.Fatalf("a read again with an equal limit differs from the fresh answer: fresh %+v view %+v", f, same)
		}
		view := rig.viewByID(t, fresh, 3)
		if want := sentenceFor(3, 20, contractsv1.ContextFabricWorkItemListCutBoth); view.Listed != 3 || !slices.Equal(view.listedSentences(), []string{want}) {
			t.Fatalf("view with 3 members %+v, want 3 listed with %q", view, want)
		}
	})
}

func TestTheTierSentenceIsOnlyStatedForTheListTheEngineBuilt(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(i int) string {
		if i < 10 {
			return contextfabric.TreeLinkTierNative
		}
		return contextfabric.TreeLinkTierHeuristic
	}))
	fresh := rig.ask(t)
	if f := readServedCohort(t, fresh); !f.tierSentence() {
		t.Fatalf("fresh %+v, want the tier sentence for the engine's cut", f)
	}
	if same := rig.viewByID(t, fresh, 25); !same.tierSentence() {
		t.Fatalf("a read again with the same list %+v lost the tier sentence", same)
	}
	if narrower := rig.viewByID(t, fresh, 3); narrower.tierSentence() || narrower.Listed != 3 {
		t.Fatalf("a read again that cuts the list by id order %+v, want 3 listed and no tier sentence", narrower)
	}
}
