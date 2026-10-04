package contextfabric

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// scopedReuseStore saves what the engine saves, keyed by the time-axis key it
// saves under, and answers a reuse lookup only from a row saved under the same
// key -- the one condition of the production gate this change touches.
type scopedReuseStore struct {
	*mapResultStore
	mu   sync.Mutex
	rows map[string]InvestigationResult
}

func (s *scopedReuseStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, watermark SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, identity ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parent string, semantic SemanticStateWrite) error {
	s.mu.Lock()
	s.rows[timeAxisKey] = result
	s.mu.Unlock()
	return s.mapResultStore.Save(ctx, principal, result, watermark, epoch, timeAxisKey, identity, prompts, authorities, graphEpoch, parent, semantic)
}

func (s *scopedReuseStore) FindReusable(_ context.Context, principal storage.Principal, key ReuseKey) (StoredInvestigationResult, bool, ReuseMissReason, error) {
	s.mu.Lock()
	result, ok := s.rows[key.TimeAxisKey]
	s.mu.Unlock()
	if !ok {
		return StoredInvestigationResult{}, false, ReuseMissNoCandidate, nil
	}
	zero := int64(0)
	return StoredInvestigationResult{Result: result, GrantDigest: StoredResultGrantDigest(principal), SemanticStateRead: SemanticStateReadAbsent, GraphEpoch: &zero}, true, "", nil
}

func TestAnswerReuseFollowsTheCallerRepositoryScope(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	store := &scopedReuseStore{mapResultStore: newMapResultStore(), rows: map[string]InvestigationResult{}}
	graphContext := bootstrapGraphContext(project)
	graphContext.EvidenceRefIDs = []string{"evidence_status_0001", "evidence_readiness_0001"}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    graphContext,
	}
	factReads := 0
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		factReads++
		return bootstrapFactBundle(project), nil
	})
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: bootstrapDraft(project), receipt: acceptanceReceipt()}
	next := 0
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Results:     store,
		ReuseGate:   store,
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { next++; return fmt.Sprintf("result_scope_reuse_%02d", next) },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	ask := func(slugs ...string) InvestigationResult {
		t.Helper()
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestedScope.RepositorySlugs = slugs
		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate(%v) error = %v", slugs, err)
		}
		return result
	}
	served := func(name string, result InvestigationResult, wantReused bool) {
		t.Helper()
		if result.Reused != wantReused {
			t.Fatalf("%s: reused = %v, want %v", name, result.Reused, wantReused)
		}
	}

	served("first scoped ask", ask("acme/repo-25"), false)
	reads := factReads
	served("no scope after a scoped answer", ask(), false)
	if factReads == reads {
		t.Fatalf("the unscoped ask read no facts: it was served from the scoped answer")
	}
	served("another scope after a scoped answer", ask("acme/repo-01"), false)
	served("same scope again", ask("acme/repo-25"), true)
	served("same scope, case and order changed", ask("ACME/Repo-25"), true)
	served("unscoped again", ask(), true)

	served("two scopes", ask("acme/repo-25", "acme/repo-01"), false)
	served("two scopes, order and case changed", ask("ACME/REPO-01", "acme/repo-25"), true)
}
