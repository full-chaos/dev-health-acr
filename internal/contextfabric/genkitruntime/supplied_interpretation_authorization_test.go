package genkitruntime

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// forbiddenModelRuntime fails the test when a model call is made.
type forbiddenModelRuntime struct{ t *testing.T }

func (f forbiddenModelRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	f.t.Helper()
	f.t.Fatal("InterpretQuestion was called for a request that carries its own interpretation")
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, nil
}

func (f forbiddenModelRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	f.t.Helper()
	f.t.Fatal("SynthesizeAnswer was called by an interpreter")
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, nil
}

const privateRepository = "other/private"

// privateProjectGraph is a graph with one project, visible only to a caller
// whose repository scope holds privateRepository. dropped receives each
// authorization-drop count the resolver reports.
func privateProjectGraph(dropped *[]int) graphrank.ResolveDeps {
	project := graphrank.CandidateNode{
		UUID: "node-project_private", Name: "Ask Dev", Relevance: graphrank.Normalized(0.95),
		Attributes: map[string]interface{}{
			"canonical_id": "project_private", "subject_kind": string(contextfabric.SubjectProject), "label": "Ask Dev",
			"evidence_refs": []string{"evidence_identity_1234"}, "authorization_repositories": []string{privateRepository},
		},
	}
	return graphrank.ResolveDeps{
		ExactHint: func(context.Context, contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
			return graphrank.CandidateNode{}, false, nil
		},
		Search: func(_ context.Context, term string, _ int) ([]graphrank.CandidateNode, bool, bool, error) {
			if term == "Ask Dev" {
				return []graphrank.CandidateNode{project}, false, false, nil
			}
			return nil, false, false, nil
		},
		Traverse: func(context.Context, string, graphrank.CandidateNode, bool) (contextfabric.SubjectCandidate, graphrank.ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, graphrank.ObservationNoParent
		},
		IsInternal:        func(contextfabric.SubjectRef) bool { return false },
		TraversalDegraded: func(context.Context, string, int) {},
		SubjectCandidatesAuthzDropped: func(_ context.Context, _ string, count int) {
			*dropped = append(*dropped, count)
		},
	}
}

func surfacesPrivateProject(resolution contextfabric.SubjectResolution) bool {
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "project_private" {
			return true
		}
	}
	for _, subject := range resolution.Committed {
		if subject.CanonicalID == "project_private" {
			return true
		}
	}
	return false
}

// TestSuppliedInterpretationNamingAnUnreadableRepositoryIsRefusedLikeTheModelPath
// runs one interpretation through both interpret paths and then through the
// real resolver and its real authorization check. A caller that cannot read
// the repository gets the same refusal from both; a caller that can read it
// is served by both, which is what shows the refusal is the authorization
// check and not an empty graph.
func TestSuppliedInterpretationNamingAnUnreadableRepositoryIsRefusedLikeTheModelPath(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	supplied := mustSuppliedInterpreter(t, nil)

	type leg struct {
		name        string
		interpreter contextfabric.RuntimeQuestionInterpreter
		request     contextfabric.InvestigationRequest
	}
	legs := []leg{
		{
			name:        "model",
			interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: mustRuntime(t, &generatorStub{interpretation: output}, Config{})},
			request:     validRequest(),
		},
		{
			name:        "supplied",
			interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: forbiddenModelRuntime{t: t}, Supplied: supplied},
			request:     suppliedRequestFor(supplied, mustMarshalOutput(t, output)),
		},
	}
	resolve := func(t *testing.T, l leg, principal storage.Principal) (contextfabric.SubjectResolution, []int) {
		t.Helper()
		interpreted, _, err := l.interpreter.Interpret(context.Background(), principal, l.request)
		if err != nil {
			t.Fatalf("%s: Interpret() error = %v", l.name, err)
		}
		var dropped []int
		resolution, _, err := graphrank.ResolveSubjects(context.Background(), principal, l.request, interpreted, privateProjectGraph(&dropped), nil, nil)
		if err != nil {
			t.Fatalf("%s: ResolveSubjects() error = %v", l.name, err)
		}
		return resolution, dropped
	}

	denied := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	allowed := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{privateRepository}}

	var deniedResolutions, allowedResolutions []contextfabric.SubjectResolution
	for _, l := range legs {
		resolution, dropped := resolve(t, l, denied)
		if surfacesPrivateProject(resolution) || len(resolution.Committed) != 0 {
			t.Fatalf("%s: resolution = %#v, want the unreadable project never surfaced", l.name, resolution)
		}
		if !reflect.DeepEqual(dropped, []int{1}) {
			t.Fatalf("%s: authorization drops = %v, want exactly one report of one dropped candidate", l.name, dropped)
		}
		deniedResolutions = append(deniedResolutions, resolution)

		resolution, dropped = resolve(t, l, allowed)
		if !surfacesPrivateProject(resolution) {
			t.Fatalf("%s: resolution = %#v, want the project surfaced for a caller that can read its repository", l.name, resolution)
		}
		if len(dropped) != 0 {
			t.Fatalf("%s: authorization drops = %v for a caller that can read the repository, want none", l.name, dropped)
		}
		allowedResolutions = append(allowedResolutions, resolution)
	}
	if !reflect.DeepEqual(deniedResolutions[0], deniedResolutions[1]) {
		t.Fatalf("denied caller: the two paths resolve differently\nmodel:    %#v\nsupplied: %#v", deniedResolutions[0], deniedResolutions[1])
	}
	if !reflect.DeepEqual(allowedResolutions[0], allowedResolutions[1]) {
		t.Fatalf("allowed caller: the two paths resolve differently\nmodel:    %#v\nsupplied: %#v", allowedResolutions[0], allowedResolutions[1])
	}
}
