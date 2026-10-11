package contextfabric

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func setEveryDigestField(t *testing.T) CommitDecisionDigest {
	t.Helper()
	var digest CommitDecisionDigest
	value := reflect.ValueOf(&digest).Elem()
	for i := 0; i < value.NumField(); i++ {
		switch value.Field(i).Kind() {
		case reflect.Bool:
			value.Field(i).SetBool(true)
		case reflect.String:
			value.Field(i).SetString("x")
		default:
			t.Fatalf("digest field %s has kind %s: teach this walk how to fill it", value.Type().Field(i).Name, value.Field(i).Kind())
		}
	}
	digest.CommitGate = "exact_index"
	digest.IdentityProven = false
	digest.IdentityLookup = IdentityLookupTokenGraphLag
	digest.AliasLookupComplete = false
	return digest
}

// Every field a digest carries survives the engine's wire assembly and the
// wire-to-digest read a reused answer decides from.
func TestCommitDigestIdentityLookupSurvivesAssemblyAndTheWireRead(t *testing.T) {
	t.Parallel()
	want := setEveryDigestField(t)
	resolution := workItemTuplePayloadFixture(t).SubjectResolution
	subject := resolution.Committed[0]
	resolution.CommitDecisionDigests = nil
	frame := deploymentScopedFrame(GoalAssessState)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: DecideFrameGate(ValidateFrame(frame, nil, ""), true)}
	graph := graphReaderStub{resolution: resolution, bases: provenCommitBases(subject), digests: CommitDecisionDigestSet{SubjectMapKey(subject): want}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "deployments", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactDeployments}}}, outcome: outcome},
		Graph:       graph,
		Results:     &staticResultStore{},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_digest_lookup_001" }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	served := result.SubjectResolution.CommitDecisionDigests
	if len(served) != 1 {
		t.Fatalf("served %d digests, want 1 (committed %+v)", len(served), result.SubjectResolution.Committed)
	}
	if got := CommitDigestSetFromWire(served).For(subject); got != want {
		t.Fatalf("digest after assembly and wire read = %+v, want %+v", got, want)
	}
}
