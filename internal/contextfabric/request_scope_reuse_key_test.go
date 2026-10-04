package contextfabric

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type axisRecordingStore struct {
	*mapResultStore
	mu   sync.Mutex
	keys []string
}

func (s *axisRecordingStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, watermark SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, identity ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parent string, semantic SemanticStateWrite) error {
	s.mu.Lock()
	s.keys = append(s.keys, timeAxisKey)
	s.mu.Unlock()
	return s.mapResultStore.Save(ctx, principal, result, watermark, epoch, timeAxisKey, identity, prompts, authorities, graphEpoch, parent, semantic)
}

func TestSavedAnswerAndReuseLookupAreKeyedByTheCallerRepositoryScope(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	saved := func(slugs []string) string {
		store := &axisRecordingStore{mapResultStore: newMapResultStore()}
		graph := &acceptanceGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context:    bootstrapGraphContext(project),
		}
		facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return bootstrapFactBundle(project), nil
		})
		engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), store)
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestedScope.RepositorySlugs = slugs
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if len(store.keys) != 1 {
			t.Fatalf("saves = %d, want 1", len(store.keys))
		}
		return store.keys[0]
	}
	unscoped := saved(nil)
	scopedA := saved([]string{"acme/repo-25"})
	scopedPair := saved([]string{"acme/repo-25", "acme/repo-01"})
	scopedPairReordered := saved([]string{"acme/repo-01", "acme/repo-25"})
	scopedB := saved([]string{"acme/repo-01"})
	if strings.Contains(unscoped, "+s:") {
		t.Fatalf("unscoped key %q carries a scope component", unscoped)
	}
	if !strings.Contains(scopedA, unscoped+"+s:") {
		t.Fatalf("scoped key %q, want the unscoped key %q plus a scope component", scopedA, unscoped)
	}
	if scopedA == scopedB {
		t.Fatalf("two different scopes saved under one key %q", scopedA)
	}
	if scopedPair != scopedPairReordered {
		t.Fatalf("the same scope keyed differently: %q vs %q", scopedPair, scopedPairReordered)
	}

	terminalSaved := func(slugs []string) string {
		store := &axisRecordingStore{mapResultStore: newMapResultStore()}
		graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
		engine := buildTerminalEngine(t, graph, store)
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestedScope.RepositorySlugs = slugs
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if len(store.keys) != 1 {
			t.Fatalf("terminal saves = %d, want 1", len(store.keys))
		}
		return store.keys[0]
	}
	if a, b := terminalSaved(nil), terminalSaved([]string{"acme/repo-25"}); strings.Contains(a, "+s:") || !strings.HasPrefix(b, a+"+s:") {
		t.Fatalf("terminal save keys unscoped %q scoped %q, want the scoped key to extend the unscoped one", a, b)
	}

	lookup := func(slugs []string) string {
		var got string
		candidateProject, candidate := outcomeAuthorityCandidate(InvestigationComplete, "complete")
		_ = candidateProject
		engine := completenessAuthorityTestEngine(t, EngineDependencies{
			Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{candidateProject}}},
			ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
				got = key.TimeAxisKey
				return candidate, true, nil
			}),
		}, false, false)
		request := validInvestigationRequest()
		request.RequestedScope.RepositorySlugs = slugs
		if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return got
	}
	if a, b := lookup(nil), lookup([]string{"acme/repo-25"}); a == "" || a == b || !strings.HasPrefix(b, a+"+s:") {
		t.Fatalf("lookup keys unscoped %q scoped %q, want the scoped key to extend the unscoped one", a, b)
	}
}

func TestRequestScopeTimeAxisKeyFailsClosed(t *testing.T) {
	t.Parallel()
	scoped := InvestigationRequest{}
	scoped.RequestedScope.RepositorySlugs = []string{"acme/repo-25"}
	if got := RequestScopeTimeAxisKey(scoped, ""); got != "" {
		t.Fatalf("empty axis key with a scope = %q, want it left empty (never reusable)", got)
	}
	if got := RequestScopeTimeAxisKey(scoped, strings.Repeat("a", reuseTimeAxisKeyMaxLength)); got != "" {
		t.Fatalf("over-long key = %q, want empty (never reusable)", got)
	}
	if got := RequestScopeTimeAxisKey(InvestigationRequest{}, "current"); got != "current" {
		t.Fatalf("unscoped key = %q, want it unchanged", got)
	}
}

func TestRequestScopeTimeAxisKeyIgnoresPaddingOrderAndCase(t *testing.T) {
	t.Parallel()
	key := func(slugs ...string) string {
		request := InvestigationRequest{}
		request.RequestedScope.RepositorySlugs = slugs
		return RequestScopeTimeAxisKey(request, "current")
	}
	want := key("acme/repo-25", "acme/repo-01")
	for name, got := range map[string]string{
		"padding": key(" acme/repo-25 ", "acme/repo-01\t"),
		"order":   key("acme/repo-01", "acme/repo-25"),
		"case":    key("ACME/Repo-25", "acme/REPO-01"),
	} {
		if got != want {
			t.Errorf("%s: key %q, want %q", name, got, want)
		}
	}
	if key("acme/repo-25") == want {
		t.Errorf("a different scope shares the key %q", want)
	}
}
