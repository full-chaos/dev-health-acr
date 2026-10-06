package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// termReadingGraph resolves like the real reader in the one respect this
// test is about: it reads the subject terms through graphrank.SubjectTerms,
// the function the real resolution reads, and finds the project only when a
// term names it and the caller may see it.
type termReadingGraph struct {
	authorizingGraph
	visible bool
	mu      sync.Mutex
	seen    [][]string
}

func (g *termReadingGraph) ResolveSubjects(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	terms := graphrank.SubjectTerms(request, interpreted)
	g.mu.Lock()
	g.seen = append(g.seen, terms)
	g.mu.Unlock()
	resolution := contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}
	bases := contextfabric.CommitBasisSet{}
	if g.visible {
		for _, term := range terms {
			if strings.EqualFold(term, g.project.Label) {
				resolution.Committed = []contextfabric.SubjectRef{g.project}
				bases.Record(g.project, contextfabric.CommitBasisCallerCanonicalID)
			}
		}
	}
	return resolution, contextfabric.StructureOfferMaterial{}, bases, nil, nil
}

func (g *termReadingGraph) lastTerms() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.seen) == 0 {
		return nil
	}
	return g.seen[len(g.seen)-1]
}

func TestSuppliedInterpretationWithoutSubjectTermsIsNeverABareNoMatch(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	project := suppliedRouteProject()

	const frameNamed = `"question_frame":{"goals":["assess_state"],"temporal":"current","subject_expression":{"kind":"named_subject","terms":["Ask Dev"]}}`
	const frameOrg = `"question_frame":{"goals":["assess_state"],"temporal":"current","subject_expression":{"kind":"discovered_kind","member_kind":"team"}}`
	body := func(extra string) string {
		return `{"shape":"single_subject","requested_judgment":"release readiness","time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"}],"clarification_needed":false` + extra + `}`
	}

	run := func(t *testing.T, output string, visible bool) (contractsv1.ContextFabricInvestigationResult, *termReadingGraph, string) {
		t.Helper()
		graph := &termReadingGraph{authorizingGraph: authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: contextfabric.StoredSubjectAdmitted}, visible: visible}
		engine := newTermReadingEngine(t, supplied, graph)
		app, token := newLiveContextFabricTestApp(t, engine)
		request := windowedInvestigationHTTPRequest(t, token, &contractsv1.ContextFabricSuppliedInterpretation{
			Output:             json.RawMessage(output),
			ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
			ClientModel: "claude-test",
		})
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
		}
		var result contractsv1.ContextFabricInvestigationResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result, graph, recorder.Body.String()
	}
	has := func(result contractsv1.ContextFabricInvestigationResult, limitation string) bool {
		for _, got := range result.Limitations {
			if got == limitation {
				return true
			}
		}
		return false
	}

	t.Run("frame names the subject: resolved from the frame and says so", func(t *testing.T) {
		result, graph, _ := run(t, body(`,`+frameNamed), true)
		if got := graph.lastTerms(); !reflect.DeepEqual(got, []string{"Ask Dev"}) {
			t.Fatalf("terms the resolution read = %#v, want the frame's named term", got)
		}
		if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0] != project {
			t.Fatalf("committed = %#v, want the project the frame names", result.SubjectResolution.Committed)
		}
		if !has(result, contractsv1.ContextFabricSubjectTermsFromFrameLimitation) {
			t.Fatalf("limitations = %q, want the sentence naming the frame as the input used", result.Limitations)
		}
	})

	t.Run("nothing names a subject: no_match that names subject_terms", func(t *testing.T) {
		result, graph, _ := run(t, body(`,`+frameOrg), true)
		if got := graph.lastTerms(); len(got) != 0 {
			t.Fatalf("terms the resolution read = %#v, want none", got)
		}
		if result.Status != contractsv1.ContextFabricInvestigationNoMatch {
			t.Fatalf("status = %q, want no_match", result.Status)
		}
		if !has(result, contractsv1.ContextFabricSubjectTermsMissingLimitation) || !strings.Contains(strings.Join(result.Limitations, " "), "subject_terms") {
			t.Fatalf("limitations = %q, want the sentence that names subject_terms as the missing input", result.Limitations)
		}
	})

	t.Run("no frame and no terms: the same reason", func(t *testing.T) {
		result, _, _ := run(t, body(``), true)
		if result.Status != contractsv1.ContextFabricInvestigationNoMatch || !has(result, contractsv1.ContextFabricSubjectTermsMissingLimitation) {
			t.Fatalf("status = %q limitations = %q, want no_match naming subject_terms", result.Status, result.Limitations)
		}
	})

	t.Run("named subject not found keeps the search wording", func(t *testing.T) {
		result, _, _ := run(t, body(`,"subject_terms":["Nothing Like This"]`), true)
		if result.Status != contractsv1.ContextFabricInvestigationNoMatch {
			t.Fatalf("status = %q, want no_match", result.Status)
		}
		if has(result, contractsv1.ContextFabricSubjectTermsMissingLimitation) || has(result, contractsv1.ContextFabricSubjectTermsFromFrameLimitation) {
			t.Fatalf("limitations = %q, want no subject_terms sentence for a name that was searched", result.Limitations)
		}
	})

	t.Run("restricted caller: frame-derived search leaks nothing", func(t *testing.T) {
		result, _, raw := run(t, body(`,`+frameNamed), false)
		if result.Status != contractsv1.ContextFabricInvestigationNoMatch || len(result.SubjectResolution.Committed) != 0 {
			t.Fatalf("status = %q committed = %#v, want no_match with nothing committed", result.Status, result.SubjectResolution.Committed)
		}
		if strings.Contains(raw, project.CanonicalID) {
			t.Fatalf("answer for a caller who cannot read the subject carries its canonical id: %s", raw)
		}
	})

	t.Run("subject_terms present: nothing derived", func(t *testing.T) {
		result, graph, _ := run(t, body(`,"subject_terms":["Ask Dev"],`+frameNamed), true)
		if got := graph.lastTerms(); !reflect.DeepEqual(got, []string{"Ask Dev"}) {
			t.Fatalf("terms = %#v, want the supplied term", got)
		}
		if has(result, contractsv1.ContextFabricSubjectTermsFromFrameLimitation) || has(result, contractsv1.ContextFabricSubjectTermsMissingLimitation) {
			t.Fatalf("limitations = %q, want no derivation sentence", result.Limitations)
		}
	})
}

func newTermReadingEngine(t *testing.T, supplied *genkitruntime.SuppliedInterpreter, graph *termReadingGraph) *contextfabric.Engine {
	t.Helper()
	var synthesized []contextfabric.InterpretedQuestion
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: interpretCountingRuntime{interprets: new(int)}, Supplied: supplied},
		Graph:       graph,
		Facts:       liveFactReader{bundle: liveCanonicalFacts(graph.project)},
		Synthesizer: fixedAnswerSynthesizer{interpretations: &synthesized},
		Results:     memoryinvestigation.NewStore(),
	}, suppliedRouteEngineOptions())
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
