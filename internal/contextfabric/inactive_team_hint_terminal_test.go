package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type inactiveHintGraphReader struct{ discoverContextCalls int }

func (*inactiveHintGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "inactive-hint-key", Epoch: 0}, nil
}

func (*inactiveHintGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection, *QuestionFrame, SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
		InactiveHints: []contractsv1.ContextFabricInactiveHint{{
			Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"},
			Answer:  contractsv1.TeamInactiveReason, ActiveCanonicalID: "team:jira:platform",
		}},
	}, StructureOfferMaterial{}, nil, nil, nil
}

func (g *inactiveHintGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverContextCalls++
	return GraphContext{}, nil
}

func TestEngineInvestigate_InactiveTeamHintAnswersNoMatchNamingTheTeamInactive(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	reader := &inactiveHintGraphReader{}
	engine := mustEngineForTerminalReasonTest(t, reader, telemetry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed = %v, want none: the active twin is not served", result.SubjectResolution.Committed)
	}
	hints := result.SubjectResolution.InactiveHints
	if len(hints) != 1 || hints[0].Answer != "team_inactive" || hints[0].ActiveCanonicalID != "team:jira:platform" {
		t.Fatalf("inactive_hints = %#v", hints)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricInactiveTeamHintLimitation {
			found = true
		}
		if limitation == noMatchLimitationUnproven {
			t.Fatalf("limitations = %#v: the unproven-search wording claims a search ran", result.Limitations)
		}
	}
	if !found {
		t.Fatalf("limitations = %#v, want the inactive-team sentence", result.Limitations)
	}
	if want := []string{"team_inactive"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("terminal reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
	}
	if reader.discoverContextCalls != 0 {
		t.Fatalf("DiscoverContext ran %d times after the hint was answered team_inactive: a cohort census there reads the active twin", reader.discoverContextCalls)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result invalid: %v", err)
	}
}

type inactiveHintBesideCommittedReader struct{ inactiveHintGraphReader }

func (r *inactiveHintBesideCommittedReader) ResolveSubjects(ctx context.Context, p storage.Principal, req InvestigationRequest, i InterpretedQuestion, b ResolvedGraphBinding, ck *ConfirmedExpectedKind, ca *ConfirmedAnchorSelection, f *QuestionFrame, k SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	resolution, material, bases, digests, err := r.inactiveHintGraphReader.ResolveSubjects(ctx, p, req, i, b, ck, ca, f, k)
	resolution.Committed = []SubjectRef{{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}}
	return resolution, material, bases, digests, err
}

// A hint answered team_inactive beside a subject that did resolve does not
// end the investigation: the resolved subject is still discovered for.
func TestEngineInvestigate_InactiveTeamHintBesideACommittedSubjectStillDiscovers(t *testing.T) {
	t.Parallel()
	reader := &inactiveHintBesideCommittedReader{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Graph: reader,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, nil
		}),
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_terminal_02" }})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if reader.discoverContextCalls == 0 {
		t.Fatal("DiscoverContext never ran for a resolved subject that stood beside an inactive hint")
	}
}
