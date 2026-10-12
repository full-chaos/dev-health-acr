package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// versionedRouteStore offers a stored answer back only to a lookup under the
// prompt versions it was saved under, as the production store's key does.
type versionedRouteStore struct {
	routeStore
	savedPrompts contextfabric.ReusePromptVersions
}

func (s *versionedRouteStore) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult, snapshot contextfabric.SourceWatermarkSnapshot, epoch contextfabric.RebuildEpoch, axis string, identity contextfabric.ReuseRetrievalIdentity, prompts contextfabric.ReusePromptVersions, authorities contextfabric.ReuseVersionAuthorities, graphEpoch int64, parent string, semantic contextfabric.SemanticStateWrite) error {
	s.savedPrompts = prompts
	return s.routeStore.Save(ctx, principal, result, snapshot, epoch, axis, identity, prompts, authorities, graphEpoch, parent, semantic)
}

func (s *versionedRouteStore) FindReusable(ctx context.Context, principal storage.Principal, key contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	if key.SynthesisPromptVersion != s.savedPrompts.SynthesisPromptVersion || key.InterpretationPromptVersion != s.savedPrompts.InterpretationPromptVersion {
		return contextfabric.StoredInvestigationResult{}, false, contextfabric.ReuseMissNoCandidate, nil
	}
	return s.routeStore.FindReusable(ctx, principal, key)
}

// mainSynthesisPromptVersion is the synthesis prompt version main served before
// the cohort population entered the model input.
const mainSynthesisPromptVersion = "context-fabric-synthesis.v21"

func investigateTwiceUnderPromptVersions(t *testing.T, secondSynthesisVersion string) (reused bool, offers int) {
	t.Helper()
	s := seedTwoLinkedProjects()
	for key := range s.text {
		if strings.HasPrefix(key, "deployment|deployment:acme/alpha-service") {
			s.text[key] = "release production"
		}
	}
	store := &versionedRouteStore{}
	for turn := 1; turn <= 2; turn++ {
		prompts := contextfabric.ReusePromptVersions{
			InterpretationPromptVersion: genkitruntime.DefaultInterpretationPromptVersion,
			SynthesisPromptVersion:      mainSynthesisPromptVersion,
		}
		if turn == 2 {
			prompts.SynthesisPromptVersion = secondSynthesisVersion
		}
		resultID := fmt.Sprintf("result_9427000%d", turn)
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: projectDeploymentsInterpreter{name: "alpha", kind: contextfabric.SubjectProject},
			Graph:       newFakeAdapter(t, s.conn()), Facts: emptyFactReader{}, Synthesizer: countingSynthesizer{},
			Results: store, ReuseGate: store, Requirements: productionRequirementDeriver{},
		}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return resultID }, ReusePromptVersions: prompts})
		if err != nil {
			t.Fatal(err)
		}
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: fmt.Sprintf("request_9427000%d", turn),
			Question:    "which deployments belong to project alpha",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
			Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if turn == 2 {
			reused = result.Reused
		}
	}
	return reused, store.offers
}

func TestAnAnswerStoredUnderTheEarlierSynthesisPromptIsNotReused(t *testing.T) {
	if reused, _ := investigateTwiceUnderPromptVersions(t, mainSynthesisPromptVersion); !reused {
		t.Fatal("control: the same prompt version did not reuse the stored answer, so this row cannot show a miss")
	}
	if reused, offers := investigateTwiceUnderPromptVersions(t, genkitruntime.DefaultSynthesisPromptVersion); reused || offers != 0 {
		t.Fatalf("an answer stored under %s was reused (reused=%v offers=%d) while the current synthesis prompt is %s", mainSynthesisPromptVersion, reused, offers, genkitruntime.DefaultSynthesisPromptVersion)
	}
}
