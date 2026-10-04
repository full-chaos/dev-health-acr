package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// routeStore keeps the last saved result and offers it back for reuse.
type routeStore struct {
	mu     sync.Mutex
	saved  *contextfabric.InvestigationResult
	offers int
	// frame and anchorKind, when set, are the reading persisted beside the
	// stored answer, as the production store keeps it.
	frame      *contextfabric.QuestionFrame
	anchorKind contextfabric.SubjectKind
}

func (s *routeStore) Save(_ context.Context, _ storage.Principal, result contextfabric.InvestigationResult, _ contextfabric.SourceWatermarkSnapshot, _ contextfabric.RebuildEpoch, _ string, _ contextfabric.ReuseRetrievalIdentity, _ contextfabric.ReusePromptVersions, _ contextfabric.ReuseVersionAuthorities, _ int64, _ string, _ contextfabric.SemanticStateWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := result
	s.saved = &copied
	return nil
}

func (s *routeStore) Get(context.Context, storage.Principal, string) (contextfabric.StoredInvestigationResult, error) {
	return contextfabric.StoredInvestigationResult{}, nil
}

func (s *routeStore) FindReusable(context.Context, storage.Principal, contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saved == nil {
		return contextfabric.StoredInvestigationResult{}, false, contextfabric.ReuseMissNoCandidate, nil
	}
	s.offers++
	stored := contextfabric.StoredInvestigationResult{Result: *s.saved, SavedAt: time.Now().UTC()}
	if s.frame != nil {
		stored.SemanticState = &contextfabric.PersistedSemanticState{FramePresent: true, Frame: s.frame, ScopeAnchor: contextfabric.SemanticScopeAnchor{Kind: s.anchorKind}}
		stored.SemanticStateRead = contextfabric.SemanticStateReadAvailable
	}
	return stored, true, "", nil
}

// TestAReusedProjectAnswerKeepsItsMembers asks the same project question twice
// with answer reuse on. The reuse recheck runs a discovery that carries no
// frame, so it does not run the walk again; the reused answer must still hold
// every member the first answer held, as a complete cohort.
func TestAReusedProjectAnswerKeepsItsMembers(t *testing.T) {
	s := seedTwoLinkedProjects()
	// The project's deployments do not match the question text, so the
	// recheck's lexical arm cannot return them either.
	for key := range s.text {
		if strings.HasPrefix(key, "deployment|deployment:acme/alpha-service") {
			s.text[key] = "release production"
		}
	}
	want := append([]string(nil), s.deployments[routeProjectAlpha]...)
	sort.Strings(want)
	store := &routeStore{}
	for turn := 1; turn <= 2; turn++ {
		base := s.conn()
		conn := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
			rows, err := base.queryFunc(ctx, key, cypher, params, ro)
			for _, r := range rows {
				for _, value := range r {
					if n, ok := value.(*node); ok && n != nil {
						// A node's ref is a function of the node's own id, the way
						// production mints it (EvidenceRefID over the source row
						// id). Numbering refs by read order gave the same node a
						// different ref on the second turn, which the recheck
						// rightly removed and which read as a recheck defect.
						id, _ := n.Properties[propCanonicalID].(string)
						n.Properties[propEvidenceRefs] = []string{stableNodeRef(id)}
					}
				}
			}
			return rows, err
		}}
		resultID := fmt.Sprintf("result_8300001%d", turn)
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: projectDeploymentsInterpreter{name: "alpha", kind: contextfabric.SubjectProject},
			Graph:       newFakeAdapter(t, conn), Facts: emptyFactReader{}, Synthesizer: countingSynthesizer{},
			Results: store, ReuseGate: store, Requirements: productionRequirementDeriver{},
		}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return resultID }})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: fmt.Sprintf("request_8300001%d", turn),
			Question:    "which deployments belong to project alpha",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
			Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
		if err != nil {
			t.Fatalf("turn %d: Investigate() error = %v", turn, err)
		}
		if reused := turn == 2; result.Reused != reused || result.ResultID != "result_83000011" {
			t.Fatalf("turn %d: reused = %v, result id %s; want the first turn fresh and the second turn served from the stored answer", turn, result.Reused, result.ResultID)
		}
		var got []string
		if result.Cohort != nil {
			for _, m := range result.Cohort.Members {
				got = append(got, m.Subject.CanonicalID)
			}
		}
		sort.Strings(got)
		for _, m := range result.Cohort.Members {
			if len(m.EvidenceRefIDs) != 1 || m.EvidenceRefIDs[0] != stableNodeRef(m.Subject.CanonicalID) {
				t.Fatalf("turn %d: member %s refs = %v, want its own node ref", turn, m.Subject.CanonicalID, m.EvidenceRefIDs)
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") || !result.Cohort.Complete {
			t.Fatalf("turn %d: served %v (complete %v), want the project's own deployments %v as a complete cohort", turn, got, result.Cohort != nil && result.Cohort.Complete, want)
		}
	}
	if store.offers != 1 {
		t.Fatalf("the stored answer was offered %d times, want once", store.offers)
	}
}

// stableNodeRef is the test's stand-in for contractsv1.EvidenceRefID: a pure
// function of the node id.
func stableNodeRef(id string) string {
	return "evidence_" + strings.NewReplacer(":", "_", "/", "_", "#", "_", "-", "_").Replace(id)
}
