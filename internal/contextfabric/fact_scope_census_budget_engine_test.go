package contextfabric

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// censusBudgetEngine serves a small answer over a REAL registry and the
// production eligibility table, with a project and a team as roots and every
// requirement kind the table names, at a given byte budget.
func censusBudgetEngine(t *testing.T, maxSerializedBytes int64, maxItems, claims int) (*Engine, int) {
	t.Helper()
	var providers []FactProvider
	var requirements []FactRequirement
	for kind := range factScopePolicies {
		providers = append(providers, &factProviderStub{
			capability: planCapability(kind, string(kind), SubjectRepository),
			result:     FactProviderResult{State: SourceAvailable},
		})
		requirements = append(requirements, FactRequirement{Kind: kind})
	}
	sort.Slice(requirements, func(i, j int) bool { return requirements[i].Kind < requirements[j].Kind })
	registry, err := NewFactCapabilityRegistry(providers, FactRegistryOptions{
		ScopeExpander: &recordingScopeExpander{counts: FactScopeExpansionCounts{CensusComplete: true, AuthorizedCount: 200}},
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	subjects := []SubjectRef{scopeProject, scopeTeam}
	candidate := affirmationCandidate(ResolutionCommitted)
	candidate.Subject = subjects[0]
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "release_readiness_and_drivers",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: requirements,
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{candidate}, Committed: subjects},
			context:    emptyAffirmationGraph(),
			bases:      provenCommitBases(subjects...),
		},
		Facts: registry,
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			draft := affirmationResult()
			draft.SubjectResolution = SubjectResolution{}
			draft.Status = InvestigationNoMatch
			draft.Coverage = Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
			for i := 0; i < claims; i++ {
				draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{
					ClaimID: "claim_" + string(rune('a'+i)), Kind: FactStatus, Subject: scopeProject, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
			draft.Versions = VersionSet{
				Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
				InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			}
			return draft, nil
		}),
		Telemetry: &recordingTelemetry{},
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxSerializedBytes: maxSerializedBytes, MaxItems: maxItems,
		Now:         func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID: func() string { return "result_12345678" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine, len(requirements)
}

// TestTheCensusGivesWayInsideTheBudgetFitBeforeAnAnswerIsRefused drives the
// whole engine at the smallest budget a caller may request. The composition-
// time ceiling reserves room for the smallest valid answer only, so this
// answer plus its allowed census overruns on bytes; stage 3 must cut census
// rows and say how many, never refuse an answer over its own diagnostics.
func TestTheCensusGivesWayInsideTheBudgetFitBeforeAnAnswerIsRefused(t *testing.T) {
	t.Parallel()
	engine, _ := censusBudgetEngine(t, contractsv1.ContextFabricSerializedBytesMin, 0, 0)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v -- an answer must not be refused for its census", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("served %d bytes, census rows=%d omitted=%d", len(encoded), len(result.FactScopeCensus), result.FactScopeCensusOmitted)
	if len(encoded) > contractsv1.ContextFabricSerializedBytesMin {
		t.Fatalf("served %d bytes against a %d-byte budget", len(encoded), contractsv1.ContextFabricSerializedBytesMin)
	}
	if result.FactScopeCensusOmitted == 0 {
		t.Fatal("nothing was shed at the smallest budget")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("a shed census must still be servable: %v", err)
	}
}

// TestTheCensusGivesWayWhenItemsAndBytesAreBothExceeded: the items axis takes
// the overrun label when both are exceeded, and the census must still give
// way on the bytes -- the answer is served or cut by the item lever, never
// refused over census rows it could have shed.
func TestTheCensusGivesWayWhenItemsAndBytesAreBothExceeded(t *testing.T) {
	t.Parallel()
	engine, _ := censusBudgetEngine(t, contractsv1.ContextFabricSerializedBytesMin, 1, 3)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v -- an answer over on items and bytes must not be refused for its census", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("served %d bytes, claims=%d census rows=%d omitted=%d", len(encoded), len(result.ClaimedFacts), len(result.FactScopeCensus), result.FactScopeCensusOmitted)
	if len(encoded) > contractsv1.ContextFabricSerializedBytesMin {
		t.Fatalf("served %d bytes against a %d-byte budget", len(encoded), contractsv1.ContextFabricSerializedBytesMin)
	}
}
