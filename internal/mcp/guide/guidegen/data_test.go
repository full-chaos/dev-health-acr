package guidegen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The acr://guide/data resource (CHAOS-7072). The operation table is the
// catalogue the runner loads; the worked examples are executed through the
// REAL operation runner and policy, so an example the runner would refuse
// cannot ship.

func TestDataGuideOperationTableIsTheCatalogue(t *testing.T) {
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	served := map[string][2]bool{}
	for _, op := range catalogue.Operations(directread.CallerUnrestricted) {
		served[op.Name] = [2]bool{true, false}
	}
	for _, op := range catalogue.Operations(directread.CallerRestricted) {
		entry := served[op.Name]
		entry[1] = true
		served[op.Name] = entry
	}
	if len(served) != 16 {
		t.Fatalf("the catalogue serves %d operations, expected 16", len(served))
	}
	text := embeddedFiles(t)[FileData]
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 6 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[1]), "`")
		want, ok := served[name]
		if !ok {
			t.Errorf("the guide lists %q, which the runner does not serve", name)
			continue
		}
		rows++
		if got := strings.TrimSpace(cells[3]); got != yesNoCell(want[0]) {
			t.Errorf("%s unrestricted: guide %q, catalogue %v", name, got, want[0])
		}
		if got := strings.TrimSpace(cells[4]); got != yesNoCell(want[1]) {
			t.Errorf("%s restricted: guide %q, catalogue %v", name, got, want[1])
		}
		if directread.OperationPurpose(name) == "" || !strings.Contains(line, directread.OperationPurpose(name)) {
			t.Errorf("%s: purpose missing from the guide row", name)
		}
	}
	if rows != len(served) {
		t.Fatalf("the guide table has %d operation rows, the catalogue serves %d", rows, len(served))
	}
	for _, ns := range catalogue.NotServed() {
		if !strings.Contains(text, "`"+ns.Name+"` ("+string(ns.Code)+")") {
			t.Errorf("not-served %s (%s) is missing from the guide", ns.Name, ns.Code)
		}
	}
}

func TestDataGuideSaysWhichWayAndCarriesTheRules(t *testing.T) {
	text := embeddedFiles(t)[FileData]
	for _, rule := range DataRules {
		if !strings.Contains(text, rule) {
			t.Errorf("the guide lacks the rule %q", rule)
		}
	}
	for _, want := range []string{
		"`investigate_question` is for our own engine's narrative answers",
		"If you are a model, plan the reads yourself",
		"`run_operation` needs the `data:read` scope",
		"More data tools are planned; none is named here until it ships.",
		"use `read_facts` when it ships (it is not in this release)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the guide lacks %q", want)
		}
	}
	// Only what exists is a callable: read_facts / read_relationships appear
	// only in the "when it ships" sentence.
	for _, tool := range []string{"read_relationships", "graphql_query", "read_rows", "plan_investigation"} {
		if strings.Contains(text, tool) {
			t.Errorf("the guide names %s, which does not exist in this release", tool)
		}
	}
	if strings.Count(text, "read_facts") != 1 {
		t.Errorf("read_facts appears %d times; it may appear once, as not yet shipped", strings.Count(text, "read_facts"))
	}
	if !strings.Contains(embeddedFiles(t)[FileQuestions], "`acr://guide/data`") {
		t.Error("the questions guide does not point to acr://guide/data")
	}
	if len(DataExamples) != 4 {
		t.Fatalf("%d worked examples, want 4", len(DataExamples))
	}
}

// --- the examples, executed through the real runner ---

type exGraph struct{}

const exOrg = "11111111-1111-4111-8111-111111111111"

func (exGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "ex", Epoch: 1}, nil
}

func (exGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	nodes := map[string][]graphrank.CandidateNode{}
	for _, subject := range subjects {
		nodes[graphrank.SubjectKey(subject)] = []graphrank.CandidateNode{{Attributes: map[string]interface{}{
			"subject_kind": string(subject.Kind), "canonical_id": subject.CanonicalID, "authorization_repositories": []string{"acme/payments"},
		}}}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

func (exGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

type exQuery struct{ calls int }

func (q *exQuery) Execute(_ context.Context, _ directread.QueryCall) (directread.QueryResult, error) {
	q.calls++
	return directread.QueryResult{StatusCode: 200, Body: []byte(`{"data":{}}`)}, nil
}

func exampleRunner(t *testing.T) (*directread.OperationRunner, *exQuery) {
	t.Helper()
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	query := &exQuery{}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{Catalogue: catalogue, Gate: directread.NewSubjectGate(exGraph{}, nil), Client: query})
	if err != nil {
		t.Fatal(err)
	}
	return runner, query
}

func runExample(t *testing.T, runner *directread.OperationRunner, args string) directread.OperationResponse {
	t.Helper()
	var request directread.OperationRequest
	if err := json.Unmarshal([]byte(args), &request); err != nil {
		t.Fatalf("example arguments are not a run_operation request: %v: %s", err, args)
	}
	principal := storage.Principal{OrgID: exOrg, Subject: "user-x", CredentialID: "cred-x"}
	response, err := runner.Run(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("%s: %v", args, err)
	}
	return response
}

func TestDataGuideExamplesRunThroughTheRealPolicy(t *testing.T) {
	runner, query := exampleRunner(t)
	checked, served, refused := 0, 0, 0
	for i, example := range DataExamples {
		for j, call := range example.Calls {
			switch call.Tool {
			case "find_subjects":
				var request struct {
					Kind  string   `json:"kind"`
					Query string   `json:"query"`
					Kinds []string `json:"kinds"`
				}
				if err := json.Unmarshal([]byte(call.Args), &request); err != nil || (request.Kind == "" && request.Query == "") {
					t.Errorf("example %d call %d: not a find_subjects request: %s", i+1, j+1, call.Args)
				}
			case "run_operation":
				response := runExample(t, runner, call.Args)
				checked++
				if call.Refused == "" {
					served++
					if response.Call != directread.CallServed {
						t.Errorf("example %d call %d (%s) is not served: call=%s refusal=%+v", i+1, j+1, call.Args, response.Call, response.Refusal)
					}
				} else {
					refused++
					if response.Call != directread.CallRefused || response.Refusal == nil || string(response.Refusal.Code) != call.Refused {
						t.Errorf("example %d call %d should be refused with %s: call=%s refusal=%+v", i+1, j+1, call.Refused, response.Call, response.Refusal)
					}
				}
			default:
				t.Errorf("example %d call %d: unknown tool %q", i+1, j+1, call.Tool)
			}
		}
	}
	if checked < 7 || served < 6 || refused < 1 {
		t.Fatalf("executed %d run_operation examples (%d served, %d refused); the measurement did not happen", checked, served, refused)
	}
	if query.calls != served {
		t.Errorf("the query service saw %d calls for %d served examples: a refused example reached upstream", query.calls, served)
	}
}

// Guard: an example with a variable the policy does not allow is caught.
func TestDataGuideExampleCheckFailsOnAWrongVariable(t *testing.T) {
	runner, _ := exampleRunner(t)
	bad := `{"operation":"hotspots","variables":{"input":{"repoIds":["` + sampleRepoID + `"],"since":"2026-08-29","untilUtc":"2026-09-28T00:00:00Z"}}}`
	if response := runExample(t, runner, bad); response.Call == directread.CallServed {
		t.Fatal("a hotspots call with an unknown variable was served; the example check would pass a wrong example")
	}
}

func TestBuildRefusesAnEmptyOperationsCatalogue(t *testing.T) {
	in := FromRegistries()
	in.DataOperations = nil
	if _, err := Build(in); err == nil {
		t.Fatal("Build accepted an empty operations catalogue")
	}
}
