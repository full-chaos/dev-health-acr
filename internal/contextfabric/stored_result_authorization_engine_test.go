package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// storedSubjectGraph is capturingGraphReader plus a stored-subject decision
// the test states per subject key. A key with no entry is absent.
type storedSubjectGraph struct {
	*capturingGraphReader
	outcomes map[string]StoredSubjectOutcome
	err      error
	asked    [][]SubjectRef
}

func (g *storedSubjectGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	g.asked = append(g.asked, append([]SubjectRef(nil), subjects...))
	if g.err != nil {
		return nil, g.err
	}
	out := make([]StoredSubjectOutcome, len(subjects))
	for index, subject := range subjects {
		outcome, ok := g.outcomes[SubjectMapKey(subject)]
		if !ok {
			outcome = StoredSubjectAbsent
		}
		out[index] = outcome
	}
	return out, nil
}

func priorReceiptFixture() (SubjectRef, InvestigationResult) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_1"
	prior.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_abc12345", Subject: project, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{project},
	}
	return project, prior
}

// A prior result whose subject the caller's CURRENT grant refuses is, to the
// engine, a prior result that does not exist: the receipt resolves exactly as
// it does when the row is missing, and no subject of the refused result
// reaches the graph as a hint.
func TestAPriorResultTheCallerMayNotReadBindsLikeAMissingOne(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}
	run := func(t *testing.T, stored bool, outcome StoredSubjectOutcome) (InvestigationResult, *storedSubjectGraph, *recordingTelemetry) {
		t.Helper()
		project, prior := priorReceiptFixture()
		results := map[string]InvestigationResult{}
		if stored {
			results[prior.ResultID] = prior
		}
		graph := &storedSubjectGraph{
			capturingGraphReader: &capturingGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				context: GraphContext{
					Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
					EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				},
			},
			outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): outcome},
		}
		telemetry := &recordingTelemetry{}
		engine := mustEngineForPriorReceiptTest(t, graph, &staticResultStore{results: results}, telemetry)
		request := validInvestigationRequest()
		request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_abc12345"}}
		result, err := engine.Investigate(context.Background(), principal, request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return result, graph, telemetry
	}

	missing, _, _ := run(t, false, StoredSubjectAdmitted)
	for _, outcome := range []StoredSubjectOutcome{StoredSubjectDenied, StoredSubjectAbsent} {
		t.Run(string(outcome), func(t *testing.T) {
			refused, graph, telemetry := run(t, true, outcome)
			if !reflect.DeepEqual(refused.SubjectResolution.PriorSubjectReceiptDispositions, missing.SubjectResolution.PriorSubjectReceiptDispositions) {
				t.Fatalf("refused parent dispositions = %#v, missing parent = %#v", refused.SubjectResolution.PriorSubjectReceiptDispositions, missing.SubjectResolution.PriorSubjectReceiptDispositions)
			}
			for _, request := range graph.resolveRequests {
				for _, hint := range request.RequestedScope.SubjectHints {
					if hint.Source == "prior_subject_receipt" {
						t.Fatalf("a refused parent's subject reached the graph as a receipt hint: %#v", hint)
					}
				}
			}
			if len(graph.asked) == 0 {
				t.Fatal("the stored-subject decision was never consulted")
			}
			decisions := telemetry.storedResultAuthorizations
			if len(decisions) == 0 {
				t.Fatal("no stored-result decision reached the trace")
			}
			last := decisions[len(decisions)-1]
			wantReason := StoredResultReasonSubjectDenied
			if outcome == StoredSubjectAbsent {
				wantReason = StoredResultReasonSubjectAbsent
			}
			if last.Surface != StoredResultSurfacePriorResult || last.Decision != StoredResultDenied || last.Reason != wantReason || last.PrincipalScope != StoredResultScopeRestricted {
				t.Fatalf("decision = %+v", last)
			}
		})
	}

	t.Run("admitted", func(t *testing.T) {
		admitted, _, telemetry := run(t, true, StoredSubjectAdmitted)
		want := []contractsv1.ContextFabricPriorSubjectReceiptEntry{{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptApplied}}
		if !reflect.DeepEqual(admitted.SubjectResolution.PriorSubjectReceiptDispositions, want) {
			t.Fatalf("admitted parent dispositions = %#v", admitted.SubjectResolution.PriorSubjectReceiptDispositions)
		}
		if reflect.DeepEqual(admitted.SubjectResolution.PriorSubjectReceiptDispositions, missing.SubjectResolution.PriorSubjectReceiptDispositions) {
			t.Fatal("control: an admitted parent must bind differently from a missing one")
		}
		if n := len(telemetry.storedResultAuthorizations); n == 0 || telemetry.storedResultAuthorizations[n-1].Decision != StoredResultAdmitted {
			t.Fatalf("decisions = %+v", telemetry.storedResultAuthorizations)
		}
	})
}

// Every engine read of a stored result goes through the gate: the Engine's
// results store is wrapped, so the raw store is never what a reader holds.
func TestEveryEngineStoredResultReadIsDecided(t *testing.T) {
	t.Parallel()
	project, prior := priorReceiptFixture()
	graph := &storedSubjectGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectDenied}}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, telemetry)
	if _, ok := engine.results.(authorizedResultStore); !ok {
		t.Fatalf("engine.results = %T, want the authorizing wrapper", engine.results)
	}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}
	if _, err := engine.results.Get(context.Background(), principal, prior.ResultID); !errors.Is(err, ErrInvestigationResultNotFound) {
		t.Fatalf("Get() error = %v, want ErrInvestigationResultNotFound", err)
	}
	// carryLoadResult is how every carry, window and ledger path reads.
	if _, err := carryLoadResult(context.Background(), engine.results, principal, prior.ResultID); !errors.Is(err, ErrInvestigationResultNotFound) {
		t.Fatalf("carryLoadResult() error = %v, want ErrInvestigationResultNotFound", err)
	}
	graph.err = errors.New("graph down")
	if _, err := engine.results.Get(context.Background(), principal, prior.ResultID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get() on a failed graph read error = %v, want ErrUnavailable", err)
	}
	if got := telemetry.storedResultAuthorizations; len(got) != 3 || got[2].Decision != StoredResultUnavailable || got[2].Reason != StoredResultReasonGraphReadFailed {
		t.Fatalf("decisions = %+v", got)
	}
}

// reuseSubjectGraph is graphReaderStub with a stated stored-subject decision.
type reuseSubjectGraph struct {
	graphReaderStub
	outcome StoredSubjectOutcome
}

func (g reuseSubjectGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	out := make([]StoredSubjectOutcome, len(subjects))
	for index := range out {
		out[index] = g.outcome
	}
	return out, nil
}

// A reuse candidate is a stored answer served in place of a fresh one, so it
// takes the same decision: a candidate the caller's grant refuses is a miss,
// even when the reuse subject recheck alone would pass it.
func TestAReuseCandidateTheCallerMayNotReadIsAMiss(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"acme/tools"}}
	for _, tc := range []struct {
		outcome StoredSubjectOutcome
		reused  bool
		miss    AnswerReuseOutcome
	}{
		{StoredSubjectAdmitted, true, AnswerReuseHit},
		{StoredSubjectDenied, false, AnswerReuseMissAuthorization},
		{StoredSubjectAbsent, false, AnswerReuseMissAuthorization},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			project, candidate := reusableCandidate()
			telemetry := &recordingTelemetry{}
			freshPath := errors.New("fresh investigation reached")
			engine := mustReuseTestEngine(t, EngineDependencies{
				Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					return InterpretedQuestion{}, freshPath
				}),
				Graph:     reuseSubjectGraph{graphReaderStub: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}}, outcome: tc.outcome},
				Results:   &resultStoreStub{},
				Telemetry: telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			})
			result, err := engine.Investigate(context.Background(), principal, validInvestigationRequest())
			if tc.reused {
				if err != nil || !result.Reused {
					t.Fatalf("Investigate() = reused %t, error %v; want the candidate served", result.Reused, err)
				}
			} else if err == nil || result.Reused {
				t.Fatalf("Investigate() = reused %t, error %v; want the fresh path taken", result.Reused, err)
			}
			if len(telemetry.answerReuseOutcomes) == 0 || telemetry.answerReuseOutcomes[0] != tc.miss {
				t.Fatalf("reuse outcomes = %v, want first %s", telemetry.answerReuseOutcomes, tc.miss)
			}
			var reuseDecisions []StoredResultAuthorization
			for _, decision := range telemetry.storedResultAuthorizations {
				if decision.Surface == StoredResultSurfaceAnswerReuse {
					reuseDecisions = append(reuseDecisions, decision)
				}
			}
			if len(reuseDecisions) != 1 || reuseDecisions[0].Admitted() != tc.reused {
				t.Fatalf("reuse decisions = %+v", reuseDecisions)
			}
		})
	}
}
