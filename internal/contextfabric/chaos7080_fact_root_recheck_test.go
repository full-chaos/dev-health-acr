package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos7080Engine is mustEngineForPriorReceiptTest's engine with a fact
// reader that records every request, so a test can assert which subjects
// the registry was asked to read.
func chaos7080Engine(t *testing.T, graph GraphReader, reads *[]CanonicalFactRequest, telemetry EngineTelemetry) *Engine {
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
		Results: &staticResultStore{results: map[string]InvestigationResult{}}, Telemetry: telemetry,
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

// refusalTelemetry is recordingTelemetry plus the optional Warn recorder.
type refusalTelemetry struct {
	*recordingTelemetry
	refused []int
}

func (r *refusalTelemetry) RecordFactRootRefused(_ context.Context, _ storage.Principal, _ StoredResultAuthorization, refused int) {
	r.refused = append(r.refused, refused)
}

// CHAOS-7080 fact leg, rule 1: a committed root the restricted caller's grant
// does not admit never reaches the fact registry, and the caller cannot tell
// it from a subject that does not exist: the turn ends exactly as it does
// when resolution commits nothing. On the baseline the registry is asked for
// the project's facts (the red run in the PR body). The refusal is loud: an
// Info decision line (surface fact_root_recheck) and a Warn line.
func TestChaos7080FactReadRefusesAnUnauthorizedCommittedRoot(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}

	// The reference: a turn whose resolution commits nothing at all.
	nothing, _ := chaos7080ProjectGraph(StoredSubjectAdmitted, nil)
	nothing.resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	var none []CanonicalFactRequest
	want, err := chaos7080Engine(t, nothing, &none, &recordingTelemetry{}).Investigate(context.Background(), restricted, validInvestigationRequest())
	if err != nil {
		t.Fatalf("reference turn: %v", err)
	}

	for _, outcome := range []StoredSubjectOutcome{StoredSubjectDenied, StoredSubjectAbsent} {
		graph, project := chaos7080ProjectGraph(outcome, nil)
		graph.resolution.Candidates = []SubjectCandidate{{ReceiptID: "receipt_pay00001", Subject: project, State: ResolutionCommitted, MatchReasons: []string{"exact"}, Confidence: 1}}
		var reads []CanonicalFactRequest
		telemetry := &refusalTelemetry{recordingTelemetry: &recordingTelemetry{}}
		got, err := chaos7080Engine(t, graph, &reads, telemetry).Investigate(context.Background(), restricted, validInvestigationRequest())
		if err != nil {
			t.Fatalf("%s: err = %v, want the ordinary zero-subject terminal", outcome, err)
		}
		for _, read := range reads {
			for _, subject := range read.Subjects {
				if subject == project {
					t.Fatalf("%s: the registry was asked for the facts of %v", outcome, project)
				}
			}
		}
		if got.Status != want.Status || len(got.SubjectResolution.Committed) != 0 || len(got.SubjectResolution.Candidates) != len(want.SubjectResolution.Candidates) {
			t.Fatalf("%s: status %s committed %v candidates %d, want the no-subject turn (status %s, %d candidates)", outcome, got.Status, got.SubjectResolution.Committed, len(got.SubjectResolution.Candidates), want.Status, len(want.SubjectResolution.Candidates))
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), project.CanonicalID) || strings.Contains(string(encoded), project.Label) {
			t.Fatalf("%s: the served result names the refused subject: %s", outcome, encoded)
		}
		if len(telemetry.refused) != 1 || telemetry.refused[0] != 1 {
			t.Fatalf("%s: Warn refusals recorded %v, want [1]", outcome, telemetry.refused)
		}
		sawDecision := false
		for _, decision := range telemetry.storedResultAuthorizations {
			sawDecision = sawDecision || (decision.Surface == StoredResultSurfaceFactRootRecheck && decision.Decision == StoredResultDenied)
		}
		if !sawDecision {
			t.Fatalf("%s: no fact_root_recheck denied decision line", outcome)
		}
	}
	// An authorizer failure fails closed, never reads.
	graph, _ := chaos7080ProjectGraph(StoredSubjectAdmitted, errors.New("graph down"))
	var reads []CanonicalFactRequest
	if _, err := chaos7080Engine(t, graph, &reads, &recordingTelemetry{}).Investigate(context.Background(), restricted, validInvestigationRequest()); !errors.Is(err, ErrUnavailable) || len(reads) != 0 {
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
		if _, err := chaos7080Engine(t, graph, &reads, &recordingTelemetry{}).Investigate(context.Background(), tc.principal, validInvestigationRequest()); err != nil {
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

// The Warn line: counts and kinds only, never the refused subject's id or
// label, at Warn.
func TestChaos7080FactRootRefusedLineIsWarnAndCarriesNoIdentity(t *testing.T) {
	var buffer bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buffer, nil)))
	var recorder FactRootRefusalRecorder = telemetry
	recorder.RecordFactRootRefused(context.Background(), storage.Principal{OrgID: "org_1"}, StoredResultAuthorization{Reason: StoredResultReasonSubjectDenied, DeniedCount: 1, RefusedKinds: []string{"project"}}, 1)
	var line map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &line); err != nil {
		t.Fatalf("line %q: %v", buffer.String(), err)
	}
	if line["level"] != "WARN" || line["msg"] != FactRootRefusedLogMessage || line["refused_count"] != float64(1) || line["reason"] != "subject_denied" {
		t.Fatalf("line %v", line)
	}
	if strings.Contains(buffer.String(), "jira:PAY") || strings.Contains(buffer.String(), "Payments") {
		t.Fatalf("line carries an identity: %s", buffer.String())
	}
}

// flakyForOneRootGraph answers every stored-subject decision from its
// outcomes, except a decision asked for failRoot ALONE, which fails: the
// aggregate decision (all roots) succeeds and refuses, the per-root decision
// for the refused root succeeds, the per-root decision for failRoot cannot be
// taken.
type flakyForOneRootGraph struct {
	*storedSubjectGraph
	failRoot SubjectRef
}

func (g *flakyForOneRootGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	if len(subjects) == 1 && SubjectMapKey(subjects[0]) == SubjectMapKey(g.failRoot) {
		return nil, errors.New("graph down for this root")
	}
	return g.storedSubjectGraph.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

// A per-root decision that cannot be taken fails the turn closed; it never
// lets that root through. Two roots: the project is refused (its per-root
// decision succeeds), the other root's per-root decision cannot be taken.
func TestChaos7080PerRootDecisionFailureFailsClosed(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}
	inner, project := chaos7080ProjectGraph(StoredSubjectDenied, nil)
	other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:allowed", Label: "allowed"}
	inner.resolution.Committed = []SubjectRef{project, other}
	inner.outcomes[SubjectMapKey(other)] = StoredSubjectAdmitted
	var reads []CanonicalFactRequest
	_, err := chaos7080Engine(t, &flakyForOneRootGraph{storedSubjectGraph: inner, failRoot: other}, &reads, &recordingTelemetry{}).Investigate(context.Background(), restricted, validInvestigationRequest())
	if !errors.Is(err, ErrUnavailable) || len(reads) != 0 {
		t.Fatalf("per-root failure: err %v, %d reads, want unavailable and no read", err, len(reads))
	}
}
