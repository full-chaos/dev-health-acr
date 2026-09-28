package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos7080Engine is mustEngineForPriorReceiptTest's engine with a fact
// reader that records every request, so a test can assert which subjects
// the registry was asked to read.
func chaos7080Engine(t *testing.T, graph GraphReader, reads *[]CanonicalFactRequest) *Engine {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			*reads = append(*reads, request)
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Nominal.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Nominal based on available context.", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
		Results: &staticResultStore{results: map[string]InvestigationResult{}}, Telemetry: &recordingTelemetry{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_99999999" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func chaos7080ProjectGraph(outcome StoredSubjectOutcome, err error) (*storedSubjectGraph, SubjectRef) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "jira:PAY", Label: "Payments"}
	return &storedSubjectGraph{
		capturingGraphReader: &capturingGraphReader{
			// The resolver commits the project -- the baseline behaviour for
			// a restricted caller, because the project node's "*" admitted
			// every caller.
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
				EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): outcome},
		err:      err,
	}, project
}

// CHAOS-7080 fact leg, rule 1: a committed root the restricted caller's grant
// does not admit never reaches the fact registry. On the baseline the
// registry is asked for the project's facts (the red run in the PR body).
func TestChaos7080FactReadRefusesAnUnauthorizedCommittedRoot(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}
	for _, outcome := range []StoredSubjectOutcome{StoredSubjectDenied, StoredSubjectAbsent} {
		graph, project := chaos7080ProjectGraph(outcome, nil)
		var reads []CanonicalFactRequest
		_, err := chaos7080Engine(t, graph, &reads).Investigate(context.Background(), restricted, validInvestigationRequest())
		for _, read := range reads {
			for _, subject := range read.Subjects {
				if subject == project {
					t.Fatalf("%s: the registry was asked for the facts of %v", outcome, project)
				}
			}
		}
		if !errors.Is(err, ErrFactRootNotAuthorized) || !errors.Is(err, ErrNoInvestigationSubjects) {
			t.Fatalf("%s: err = %v, want ErrFactRootNotAuthorized", outcome, err)
		}
	}
	// An authorizer failure fails closed, never reads.
	graph, _ := chaos7080ProjectGraph(StoredSubjectAdmitted, errors.New("graph down"))
	var reads []CanonicalFactRequest
	if _, err := chaos7080Engine(t, graph, &reads).Investigate(context.Background(), restricted, validInvestigationRequest()); !errors.Is(err, ErrUnavailable) || len(reads) != 0 {
		t.Fatalf("authorizer failure: err %v, %d reads", err, len(reads))
	}
}

// Rule 1, the other direction, and the unchanged callers: an admitted root is
// read; an unrestricted or universal caller is never re-checked (no extra
// graph decision) and reads exactly as before.
func TestChaos7080FactReadKeepsAuthorizedAndUnrestrictedReads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		outcome   StoredSubjectOutcome
		wantAsked bool
	}{
		{"restricted, admitted root", storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}, StoredSubjectAdmitted, true},
		{"unrestricted", storage.Principal{OrgID: "org_1"}, StoredSubjectDenied, false},
		{"universal", storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, StoredSubjectDenied, false},
	} {
		graph, project := chaos7080ProjectGraph(tc.outcome, nil)
		var reads []CanonicalFactRequest
		if _, err := chaos7080Engine(t, graph, &reads).Investigate(context.Background(), tc.principal, validInvestigationRequest()); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		read := false
		for _, request := range reads {
			for _, subject := range request.Subjects {
				read = read || subject == project
			}
		}
		if !read {
			t.Errorf("%s: the project's facts were not read", tc.name)
		}
		if asked := len(graph.asked) > 0; asked != tc.wantAsked {
			t.Errorf("%s: re-check consulted the graph = %v, want %v", tc.name, asked, tc.wantAsked)
		}
	}
}
