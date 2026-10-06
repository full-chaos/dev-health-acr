package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
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
	read     *[][]string
	contract contractsv1.ContextFabricInterpretationContract
	server   *httptest.Server
	token    string
	boot     *acrmcp.Bootstrap
	store    *memoryinvestigation.Store
	graph    *cutWalkGraph
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
	return newCutRigWithBytes(t, walk, 0)
}

func newCutRigWithBytes(t *testing.T, walk contextfabric.TreeWorkItemWalk, maxBytes int) *cutRig {
	t.Helper()
	fixture := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 30, MaxTokens: 16000, MaxBytes: 1 << 20})
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(fixture.client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	read := &[][]string{}
	fixture.model.observe = func(input contextfabric.SynthesisInput) {
		labels := []string{}
		for _, member := range input.Graph.Cohort.Members {
			labels = append(labels, member.Subject.Label)
		}
		*read = append(*read, labels)
	}
	model := cutRepositoryModel{freshTupleModel: *fixture.model}
	graph := &cutWalkGraph{walk: walk}
	store := memoryinvestigation.NewStore()
	dependencies := fixture.dependencies
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	dependencies.Interpreter = contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry, Supplied: supplied}
	dependencies.Graph = cutRepositoryGraph{}
	dependencies.Results = store
	dependencies.TreeWorkItemGraph = graph
	dependencies.TreeWorkItemFilter = nil
	dependencies.TreeWorkItemGate = newResponseOwnerAPITestGate(t)
	options := fixture.engineOptions
	options.MaxItems = 30
	if maxBytes > 0 {
		options.MaxSerializedBytes = int64(maxBytes)
	}
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
	if maxBytes > 0 {
		app.config.MaxSerializedBytes = maxBytes
	}
	server := httptest.NewTLSServer(app.InstrumentedHandler(app.Handler()))
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("real MCP bootstrap: %v", err)
	}
	return &cutRig{read: read, contract: contractsv1.ContextFabricInterpretationContract{ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256}, server: server, token: token, boot: boot, store: store, graph: graph}
}

func (r *cutRig) ask(t *testing.T) servedAnswer { return r.askUpTo(t, 0) }

// askUpTo asks the question with a max_cohort_members of its own; zero is the
// sidecar's default.
func (r *cutRig) askUpTo(t *testing.T, members int) servedAnswer {
	t.Helper()
	return r.askWithin(t, members, 0)
}

// askWithin also sets the client's own max_serialized_bytes; zero is the
// sidecar's default. The client's max_evidence_refs follows its member cap: a
// listed member cites one evidence ref and the projection drops a member whose
// ref does not fit, so the sidecar's default of 25 refs shows at most 25.
func (r *cutRig) askWithin(t *testing.T, members, bytes int) servedAnswer {
	t.Helper()
	request := contractsv1.MCPInvestigateQuestionRequest{
		Question:       "Which issues belong to repository acme/api?",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
	}
	if members > 0 || bytes > 0 {
		request.Budget = &contractsv1.MCPInvestigationBudget{MaxCohortMembers: members, MaxEvidenceRefs: members, MaxSerializedBytes: bytes}
	}
	return callRealMCPTool(t, r.boot, "investigate_question", request)
}

type servedCohort struct {
	Total, Population, Listed, Omitted int
	LowerBound                         bool
	Sentences, Limitations             []string
	Markdown                           string
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
			Budget      struct {
				CohortMembersOmitted int `json:"cohort_members_omitted"`
			} `json:"projection_budget"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	c := node.Structured.Cohort
	served := servedCohort{Limitations: node.Structured.Limitations, Omitted: node.Structured.Budget.CohortMembersOmitted, Total: c.Total, Population: c.Population, Listed: len(c.Members), LowerBound: c.PopulationLowerBound, Markdown: answer.markdown}
	for _, limitation := range node.Structured.Limitations {
		if contractsv1.IsContextFabricWorkItemListedLimitation(limitation) || contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation(limitation) || limitation == contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation {
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

func TestADefaultCallListsEveryMemberAndSaysWhatTheSummaryRead(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative }))
	got := readServedCohort(t, rig.ask(t))
	if got.Listed != 20 || got.Population != 20 || got.Total != 20 || got.listedSentence() != "" || got.tierSentence() {
		t.Fatalf("served cohort = %+v, want 20 listed of 20 with no cut sentence", got)
	}
	if !strings.Contains(got.Markdown, "## Cohort (20 of 20 shown)") {
		t.Fatalf("the cohort heading the client reads is not 20 of 20")
	}
	if !got.synthesisSentence() {
		t.Fatalf("the answer does not say the summary covered only the first members: %+v", got.Sentences)
	}
}

func TestARequestCapCutReachesTheClientWithItsPopulation(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative }))
	got := readServedCohort(t, rig.askUpTo(t, 5))
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(5, 20, false, contractsv1.ContextFabricWorkItemListCutRequest, "")
	if got.Listed != 5 || got.Population != 20 || got.LowerBound || got.listedSentence() != want || got.tierSentence() {
		t.Fatalf("served cohort = %+v, want 5 listed, population 20, sentence %q, no tier sentence", got, want)
	}
	if !strings.Contains(got.Markdown, "## Cohort (5 of 20 shown)") || !strings.Contains(got.Markdown, want) {
		t.Fatalf("the markdown the client receives lacks the heading or %q", want)
	}
}

func TestAMixedTierCutReachesTheClientWithBothSentences(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(i int) string {
		if i < 10 {
			return contextfabric.TreeLinkTierNative
		}
		return contextfabric.TreeLinkTierHeuristic
	}))
	got := readServedCohort(t, rig.askUpTo(t, 5))
	if got.Population != 20 || !got.tierSentence() || got.listedSentence() == "" {
		t.Fatalf("served cohort = %+v, want population 20 with the tier sentence and N of M", got)
	}
}

func TestTwoHundredListedMembersAreServedOnBothRoutes(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 250, 0, func(int) string { return contextfabric.TreeLinkTierNative }))
	fresh := rig.askWithin(t, 200, 250_000)
	got := readServedCohort(t, fresh)
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(200, 250, false, contractsv1.ContextFabricWorkItemListCutServer, "")
	if got.Total != 200 || got.Listed != 200 || got.Population != 250 || got.listedSentence() != want {
		t.Fatalf("served cohort = %+v, want 200 listed of 250 with %q, the members the projection left out counted", got, want)
	}
	stored := rig.byID(t, fresh)
	b := readServedCohort(t, stored)
	if b.Listed != 200 || b.Population != 250 || b.listedSentence() != want {
		t.Fatalf("stored result = %+v, want the same 200 of 250 (the stored result is not projected)", b)
	}
}

func TestTheByteFitCutsTheListAndSaysSo(t *testing.T) {
	rig := newCutRigWithBytes(t, cutWalk(t, 250, 0, func(int) string { return contextfabric.TreeLinkTierNative }), 40_000)
	got := readServedCohort(t, rig.askWithin(t, 200, 40_000))
	want := func(listed int) string {
		sentence, _ := contractsv1.ContextFabricWorkItemListedLimitation(listed, 250, false, contractsv1.ContextFabricWorkItemListCutSize, "")
		return sentence
	}
	if got.Total >= 200 || got.Total < 1 || got.Population != 250 || got.listedSentence() != want(got.Total) || got.Listed+got.Omitted != got.Total {
		t.Fatalf("served cohort = %+v, want a list cut to fit 40000 bytes, population 250 and the size sentence for it", got)
	}
}

func TestTheStoredResultServesTheSameCutAsTheFreshAnswer(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(i int) string {
		if i < 10 {
			return contextfabric.TreeLinkTierNative
		}
		return contextfabric.TreeLinkTierHeuristic
	}))
	fresh := rig.askUpTo(t, 5)
	a, b := readServedCohort(t, fresh), readServedCohort(t, rig.byID(t, fresh))
	if a.Population != 20 || !a.tierSentence() {
		t.Fatalf("fresh = %+v, want population 20 with the tier sentence", a)
	}
	// investigation_result returns the stored result, which has no projected total.
	if a.Population != b.Population || a.Listed != b.Listed || a.LowerBound != b.LowerBound || strings.Join(a.Sentences, "|") != strings.Join(b.Sentences, "|") {
		t.Fatalf("stored differs from fresh:\nfresh  %+v\nstored %+v", a, b)
	}
}

func (r *cutRig) byID(t *testing.T, fresh servedAnswer) servedAnswer {
	t.Helper()
	var id struct {
		Structured struct {
			ResultID string `json:"result_id"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(fresh.structured, &id); err != nil || id.Structured.ResultID == "" {
		t.Fatalf("fresh answer carries no result id: %v %s", err, fresh.structured)
	}
	return callRealMCPTool(t, r.boot, "investigation_result", contractsv1.MCPInvestigationResultRequest{ResultID: id.Structured.ResultID})
}

func (c servedCohort) synthesisSentence() bool {
	for _, s := range c.Sentences {
		if contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation(s) {
			return true
		}
	}
	return false
}

func (cutRepositoryGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range outcomes {
		outcomes[index] = contextfabric.StoredSubjectAdmitted
	}
	return outcomes, nil
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

// What hidden members change in the answer: only the authorization-gap sentence that predates
// this change (it states the denied count to a partially authorized caller). The population, the
// total, the listed count and the N of M sentence are equal.
func TestHiddenMembersDoNotChangeThePopulationOrTheListedSentence(t *testing.T) {
	none := readServedCohort(t, newCutRig(t, cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative })).askUpTo(t, 5))
	hidden := readServedCohort(t, newCutRig(t, cutWalk(t, 20, 7, func(int) string { return contextfabric.TreeLinkTierNative })).askUpTo(t, 5))
	if none.Population != 20 || hidden.Population != 20 || none.listedSentence() != hidden.listedSentence() || none.Total != hidden.Total || none.Listed != hidden.Listed {
		t.Fatalf("hidden members change what this change adds: none=%+v hidden=%+v", none, hidden)
	}
	gap := func(limitations []string) (rest []string, gaps int) {
		for _, l := range limitations {
			if strings.Contains(l, "outside this principal's authorized scope") {
				gaps++
				continue
			}
			rest = append(rest, l)
		}
		return rest, gaps
	}
	noneRest, noneGaps := gap(none.Limitations)
	hiddenRest, hiddenGaps := gap(hidden.Limitations)
	if noneGaps != 0 || hiddenGaps != 1 || !slices.Equal(noneRest, hiddenRest) {
		t.Fatalf("the limitations differ by more than the pre-existing authorization-gap sentence: none=%v hidden=%v", none.Limitations, hidden.Limitations)
	}
}

func TestTheStoredResultKeepsALowerBoundCut(t *testing.T) {
	walk := cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative })
	walk.Truncated = true
	rig := newCutRig(t, walk)
	fresh := rig.askUpTo(t, 5)
	a, b := readServedCohort(t, fresh), readServedCohort(t, rig.byID(t, fresh))
	if !a.LowerBound || !strings.Contains(a.listedSentence(), " of at least 20 members") {
		t.Fatalf("fresh = %+v, want a lower bound stated as at least 20", a)
	}
	if a.LowerBound != b.LowerBound || a.Population != b.Population || strings.Join(a.Sentences, "|") != strings.Join(b.Sentences, "|") {
		t.Fatalf("stored differs from fresh:\nfresh  %+v\nstored %+v", a, b)
	}
}

func TestTheListedSentenceIsStatedForWhatTheResponseLists(t *testing.T) {
	native := func(int) string { return contextfabric.TreeLinkTierNative }
	sentenceFor := func(listed, population int, engine contractsv1.ContextFabricWorkItemListCut, response contractsv1.ContextFabricWorkItemResponseCut) string {
		s, _ := contractsv1.ContextFabricWorkItemListedLimitation(listed, population, false, engine, response)
		return s
	}
	t.Run("the engine cut only", func(t *testing.T) {
		rig := newCutRig(t, cutWalk(t, 20, 0, native))
		got := readServedCohort(t, rig.askUpTo(t, 5))
		if want := sentenceFor(5, 20, contractsv1.ContextFabricWorkItemListCutRequest, ""); got.Listed != 5 || !slices.Equal(got.listedSentences(), []string{want}) {
			t.Fatalf("served %+v, want 5 listed with %q", got, want)
		}
	})
	t.Run("the response cut only", func(t *testing.T) {
		rig := newCutRig(t, cutWalk(t, 12, 0, native))
		fresh := rig.ask(t)
		if f := readServedCohort(t, fresh); f.Listed != 12 || len(f.listedSentences()) != 0 {
			t.Fatalf("fresh %+v, want all 12 listed and no cut sentence", f)
		}
		view := rig.viewByID(t, fresh, 3)
		if want := sentenceFor(3, 12, "", contractsv1.ContextFabricWorkItemResponseCutMembers); view.Listed != 3 || !slices.Equal(view.listedSentences(), []string{want}) {
			t.Fatalf("view with 3 members %+v, want 3 listed with %q", view, want)
		}
	})
	t.Run("both cuts, and a read again with another limit", func(t *testing.T) {
		rig := newCutRig(t, cutWalk(t, 20, 0, native))
		fresh := rig.askUpTo(t, 5)
		same := rig.viewByID(t, fresh, 25)
		if f := readServedCohort(t, fresh); !slices.Equal(same.listedSentences(), f.listedSentences()) || same.Listed != f.Listed {
			t.Fatalf("a read again with an equal limit differs from the fresh answer: fresh %+v view %+v", f, same)
		}
		view := rig.viewByID(t, fresh, 3)
		if want := sentenceFor(3, 20, contractsv1.ContextFabricWorkItemListCutRequest, contractsv1.ContextFabricWorkItemResponseCutMembers); view.Listed != 3 || !slices.Equal(view.listedSentences(), []string{want}) {
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
	fresh := rig.askUpTo(t, 5)
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

// A client that interprets the question on its own model reaches the same walk when its
// interpretation carries the question frame (without one the question is a generic cohort
// question and does not reach the work-item walk).
func TestAClientInterpretedQuestionListsEveryMemberAndStatesTheCut(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(int) string { return contextfabric.TreeLinkTierNative }))
	ask := func(members int) servedCohort {
		answer := callRealMCPTool(t, rig.boot, "investigate_with_interpretation", contractsv1.MCPInvestigateWithInterpretationRequest{
			MCPInvestigateQuestionRequest: contractsv1.MCPInvestigateQuestionRequest{
				Question:       "Which issues belong to repository acme/api?",
				EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
				Budget:         &contractsv1.MCPInvestigationBudget{MaxCohortMembers: members, MaxEvidenceRefs: members},
			},
			Interpretation: json.RawMessage(`{"shape":"explicit_cohort","requested_judgment":"list the work items of the repository","subject_terms":["acme/api"],"scope_anchor_term":"acme/api","scope_anchor_kind":"repository","requested_subject_kind":"work_item","time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"},{"kind":"work"}],"clarification_needed":false,"question_frame":{"goals":["assess_state"],"temporal":"current","subject_expression":{"kind":"children_of_scope","anchor_terms":["acme/api"],"member_kind":"work_item"}}}`),
			Contract:       rig.contract,
			ClientModel:    "claude-test",
		})
		return readServedCohort(t, answer)
	}
	if all := ask(20); all.Listed != 20 || all.Population != 20 || len(all.listedSentences()) != 0 {
		t.Fatalf("a client-interpreted question %+v, want all 20 members listed with no cut sentence", all)
	}
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(5, 20, false, contractsv1.ContextFabricWorkItemListCutRequest, "")
	if cut := ask(5); cut.Listed != 5 || cut.Population != 20 || !slices.Equal(cut.listedSentences(), []string{want}) {
		t.Fatalf("a client-interpreted question with a member limit of 5 %+v, want 5 of 20 with %q", cut, want)
	}
}

// A member row uses one evidence reference. A client that asks for 200 members and keeps the
// tool's default of 25 references gets the members that fit, and the answer names the bound.
func TestTheEvidenceReferenceLimitIsNamedWhenItCutsTheList(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 60, 0, func(int) string { return contextfabric.TreeLinkTierNative }))
	ask := func(members, refs int) servedCohort {
		return readServedCohort(t, callRealMCPTool(t, rig.boot, "investigate_question", contractsv1.MCPInvestigateQuestionRequest{
			Question:       "Which issues belong to repository acme/api?",
			EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
			Budget:         &contractsv1.MCPInvestigationBudget{MaxCohortMembers: members, MaxEvidenceRefs: refs},
		}))
	}
	cut := ask(200, 0)
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(cut.Listed, 60, false, "", contractsv1.ContextFabricWorkItemResponseCutEvidence)
	if cut.Listed >= 60 || cut.Listed < 1 || cut.Population != 60 || !slices.Equal(cut.listedSentences(), []string{want}) || !strings.Contains(want, "max_evidence_refs") {
		t.Fatalf("200 members with the default references %+v, want what fits listed of 60 and the sentence %q naming the evidence reference limit", cut, want)
	}
	if all := ask(200, 200); all.Listed != 60 || all.Population != 60 || len(all.listedSentences()) != 0 {
		t.Fatalf("both limits raised %+v, want all 60 listed and no cut sentence", all)
	}
}

// What the model reads, and the order the served plan names for it, follow link strength: with the
// weaker links on the lowest ids, the 14 members read are the 10 native ones and the 4 lowest-id
// heuristic ones, the served list stays in id order, and the served plan names what its member
// budget bounds (the synthesis input) and the order in use.
func TestTheModelReadsTheStrongestLinksAndThePlanNamesWhatItBounds(t *testing.T) {
	rig := newCutRig(t, cutWalk(t, 20, 0, func(i int) string {
		if i < 10 {
			return contextfabric.TreeLinkTierHeuristic
		}
		return contextfabric.TreeLinkTierNative
	}))
	fresh := rig.ask(t)
	got := readServedCohort(t, fresh)
	if got.Listed != 20 || !got.synthesisSentence() {
		t.Fatalf("served %+v, want all 20 listed and the sentence that the summary read fewer", got)
	}
	if len(*rig.read) != 1 || len((*rig.read)[0]) != 14 {
		t.Fatalf("the model read %v, want one synthesis over 14 members", *rig.read)
	}
	want := map[string]bool{}
	for i := 10; i < 20; i++ {
		want[fmt.Sprintf("ENG-%04d", i)] = true
	}
	for i := 0; i < 4; i++ {
		want[fmt.Sprintf("ENG-%04d", i)] = true
	}
	for _, label := range (*rig.read)[0] {
		if !want[label] {
			t.Fatalf("the model read %q, which is not among the 10 native and the 4 lowest-id heuristic members: %v", label, (*rig.read)[0])
		}
	}
	var stored struct {
		Structured struct {
			AnswerPlan struct {
				Narrowing []struct {
					Stage  string `json:"stage"`
					Basis  string `json:"basis"`
					Before int    `json:"before"`
					After  int    `json:"after"`
				} `json:"narrowing"`
			} `json:"answer_plan"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(rig.byID(t, fresh).structured, &stored); err != nil {
		t.Fatal(err)
	}
	plan := stored.Structured.AnswerPlan
	sawSynthesis := false
	for _, step := range plan.Narrowing {
		if step.Stage == "synthesis_input" && step.Before == 20 && step.After == 14 && step.Basis == "link_strength_then_id" {
			sawSynthesis = true
		}
		if step.Stage == "cardinality" && step.After < 20 {
			t.Fatalf("the served plan says the list was narrowed to %d at stage cardinality while 20 are listed: %+v", step.After, plan.Narrowing)
		}
	}
	if !sawSynthesis {
		t.Fatalf("served plan %+v, want a synthesis_input step 20 to 14 by link_strength_then_id", plan)
	}
}
