package graphrank

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// E7 case 56: the question's term shares no token with the repository's label,
// alias or search text, and the lexical pool is all pull requests.
const chaos10941Term = "self-hosted CI executor setup repository"

var chaos10941Repo = contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:cd5f742c-f17b-d8b9-5083-1ce784d3817a", Label: "full-chaos/action-runners-local"}

func chaos10941PRs(n int) []CandidateNode {
	nodes := make([]CandidateNode, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, candidateNode(contextfabric.SubjectPullRequest, fmt.Sprintf("pull_request:r:%d", i), fmt.Sprintf("PR %d executor setup", i), 0.55, "*"))
	}
	return nodes
}

func chaos10941Backend(withHook bool, semantic ...CandidateNode) *fakeGraphBackend {
	b := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{chaos10941Term: chaos10941PRs(20)},
		searchTruncated: true,
	}
	if withHook {
		b.enableKindScopedSemantic = true
		b.kindScopedSemanticResults = map[string]map[contextfabric.SubjectKind][]CandidateNode{
			chaos10941Term: {contextfabric.SubjectRepository: semantic},
		}
	}
	return b
}

func chaos10941Request() contextfabric.InvestigationRequest {
	request := testRequest()
	request.ExpectedKinds = []contextfabric.SubjectKind{contextfabric.SubjectRepository}
	return request
}

func chaos10941Resolve(t *testing.T, b *fakeGraphBackend, request contextfabric.InvestigationRequest) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial) {
	t.Helper()
	resolution, offers, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(chaos10941Term), b.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	return resolution, offers
}

func chaos10941Offered(offers contextfabric.StructureOfferMaterial) []string {
	var ids []string
	for _, option := range offers.CandidateOptions {
		ids = append(ids, option.CanonicalID)
	}
	return ids
}

func chaos10941Semantic(subject contextfabric.SubjectRef) CandidateNode {
	n := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.7, "*")
	similarity := 0.7
	n.VectorSimilarity = &similarity
	return n
}

func TestChaos10941_KindScopedSemanticFindIsOfferedNeverCommitted(t *testing.T) {
	t.Parallel()
	resolution, offers := chaos10941Resolve(t, chaos10941Backend(true, chaos10941Semantic(chaos10941Repo)), chaos10941Request())
	if len(resolution.Committed) != 0 {
		t.Fatalf("a semantic-only find committed: %#v", resolution.Committed)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == contextfabric.SubjectRepository {
			t.Fatalf("semantic find entered the ranked pool: %#v", candidate)
		}
	}
	offered := chaos10941Offered(offers)
	if len(offered) == 0 || offered[0] != chaos10941Repo.CanonicalID {
		t.Fatalf("candidate offer = %v, want the repository first", offered)
	}
}

func TestChaos10941_KindScopedSemanticMultiMatchOffersEveryCandidate(t *testing.T) {
	t.Parallel()
	second := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:gitlab-1", Label: "full.chaos/action-runners-local"}
	resolution, offers := chaos10941Resolve(t, chaos10941Backend(true, chaos10941Semantic(chaos10941Repo), chaos10941Semantic(second)), chaos10941Request())
	if len(resolution.Committed) != 0 {
		t.Fatalf("multi-match committed: %#v", resolution.Committed)
	}
	offered := chaos10941Offered(offers)
	if len(offered) < 2 || offered[0] != chaos10941Repo.CanonicalID || offered[1] != second.CanonicalID {
		t.Fatalf("candidate offer = %v, want both repositories listed first", offered)
	}
}

func TestChaos10941_NoKindHintNeverCallsKindScopedSemantic(t *testing.T) {
	t.Parallel()
	b := chaos10941Backend(true, chaos10941Semantic(chaos10941Repo))
	chaos10941Resolve(t, b, testRequest())
	if len(b.kindScopedSemanticCalls) != 0 {
		t.Fatalf("hook called without a kind hint: %v", b.kindScopedSemanticCalls)
	}
}

func TestChaos10941_NothingAboveFloorKeepsTodaysOutcome(t *testing.T) {
	t.Parallel()
	request := chaos10941Request()
	withoutResolution, withoutOffers := chaos10941Resolve(t, chaos10941Backend(false), request)
	hook := chaos10941Backend(true)
	withResolution, withOffers := chaos10941Resolve(t, hook, request)
	if len(hook.kindScopedSemanticCalls) == 0 {
		t.Fatal("hook never called with a kind hint")
	}
	if !reflect.DeepEqual(withoutResolution, withResolution) || !reflect.DeepEqual(withoutOffers, withOffers) {
		t.Fatalf("an empty semantic result changed the outcome:\nwithout=%#v %#v\nwith=%#v %#v", withoutResolution, withoutOffers, withResolution, withOffers)
	}
}

func TestChaos10941_KindScopedSemanticSkippedWhenSomethingCommitted(t *testing.T) {
	t.Parallel()
	committed := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_exact", Label: chaos10941Term}
	b := chaos10941Backend(true, chaos10941Semantic(chaos10941Repo))
	b.searchResults = map[string][]CandidateNode{chaos10941Term: {candidateNode(committed.Kind, committed.CanonicalID, committed.Label, 0.9, "*")}}
	resolution, _ := chaos10941Resolve(t, b, chaos10941Request())
	if len(resolution.Committed) != 1 || resolution.Committed[0] != committed {
		t.Fatalf("exact commit changed: %#v", resolution.Committed)
	}
	if len(b.kindScopedSemanticCalls) != 0 {
		t.Fatalf("semantic arm ran after a commit: %v", b.kindScopedSemanticCalls)
	}
}

func TestChaos10941_KindScopedSemanticDegradedFoldsIntoResolution(t *testing.T) {
	t.Parallel()
	b := chaos10941Backend(true)
	b.kindScopedSemanticDegrade = true
	resolution, _ := chaos10941Resolve(t, b, chaos10941Request())
	if !resolution.RetrievalDegraded {
		t.Fatal("degraded semantic arm did not mark the resolution degraded")
	}
}

func TestChaos10941_KindScopedSemanticFindNeverOutranksANonVectorCandidate(t *testing.T) {
	t.Parallel()
	lexical := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_lexical", Label: "Executor Setup Project"}
	b := chaos10941Backend(true, chaos10941Semantic(chaos10941Repo))
	b.searchResults = map[string][]CandidateNode{chaos10941Term: {
		candidateNode(lexical.Kind, lexical.CanonicalID, lexical.Label, 0.7, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_other", "Other Team", 0.68, "*"),
	}}
	resolution, offers := chaos10941Resolve(t, b, chaos10941Request())
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed: %#v", resolution.Committed)
	}
	offered := chaos10941Offered(offers)
	repoAt, lexicalAt := -1, -1
	for i, id := range offered {
		switch id {
		case chaos10941Repo.CanonicalID:
			repoAt = i
		case lexical.CanonicalID:
			lexicalAt = i
		}
	}
	if lexicalAt < 0 || repoAt < 0 || repoAt < lexicalAt {
		t.Fatalf("offer order = %v: the semantic find must come after the lexical candidate", offered)
	}
}

func chaos10941SemanticAt(subject contextfabric.SubjectRef, relevance float64) CandidateNode {
	n := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, relevance, "*")
	similarity := relevance
	n.VectorSimilarity = &similarity
	return n
}

func TestChaos10941_KindScopedSemanticNeverWidensTheKindOffer(t *testing.T) {
	t.Parallel()
	pool := map[string][]CandidateNode{chaos10941Term: {
		candidateNode(contextfabric.SubjectPullRequest, "pull_request:r:1", "PR one", 0.7, "*"),
	}}
	boundary := func(withHook bool) ([]string, contextfabric.StructureOfferMaterial) {
		b := chaos10941Backend(withHook, chaos10941Semantic(chaos10941Repo))
		b.searchResults = pool
		deps := b.deps()
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		_, offers, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, chaos10941Request(), testInterpreted(chaos10941Term), deps, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range tracer.snapshot() {
			if event.Stage == "kind_offer" {
				return event.KindOfferBoundaryKinds, offers
			}
		}
		t.Fatal("no kind_offer event")
		return nil, offers
	}
	without, withoutOffers := boundary(false)
	with, withOffers := boundary(true)
	if !reflect.DeepEqual(without, with) {
		t.Fatalf("the semantic arm widened the kind offer's input kinds: without=%v with=%v", without, with)
	}
	if !reflect.DeepEqual(withoutOffers.KindOptions, withOffers.KindOptions) || !reflect.DeepEqual(withoutOffers.HandleOptions, withOffers.HandleOptions) {
		t.Fatalf("the semantic arm widened the kind or handle offer")
	}
}

func TestChaos10941_KindScopedSemanticBestSimilaritySurvivesTheOfferCap(t *testing.T) {
	t.Parallel()
	var hits []CandidateNode
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		hits = append(hits, chaos10941SemanticAt(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + id, Label: id}, 0.6))
	}
	best := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:z", Label: "z"}
	hits = append(hits, chaos10941SemanticAt(best, 0.9))
	_, offers := chaos10941Resolve(t, chaos10941Backend(true, hits...), chaos10941Request())
	offered := chaos10941Offered(offers)
	if len(offered) == 0 || offered[0] != best.CanonicalID {
		t.Fatalf("candidate offer = %v, want the strongest semantic find (repository:z) first", offered)
	}
}

func chaos10941SemanticEvents(tracer *captureResolutionTracer) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, event := range tracer.snapshot() {
		if event.Stage == "kind_scoped_semantic" {
			out = append(out, event)
		}
	}
	return out
}

func TestChaos10941_KindScopedSemanticReportsTruncationAndAuthzDrops(t *testing.T) {
	t.Parallel()
	scope := []string{"full-chaos/dev-health-acr"}
	allowed := candidateNode(chaos10941Repo.Kind, chaos10941Repo.CanonicalID, chaos10941Repo.Label, 0.7, scope)
	foreign := candidateNode(chaos10941Repo.Kind, "repository:foreign", "other/private", 0.8, []string{"other/private"})
	b := chaos10941Backend(true, allowed, foreign)
	b.kindScopedSemanticTruncated = true
	deps := b.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scope}
	_, offers, err := ResolveSubjects(context.Background(), principal, chaos10941Request(), testInterpreted(chaos10941Term), deps, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	offered := chaos10941Offered(offers)
	if len(offered) != 1 || offered[0] != chaos10941Repo.CanonicalID {
		t.Fatalf("offer = %v, want only the authorized semantic find", offered)
	}
	events := chaos10941SemanticEvents(tracer)
	if len(events) != 1 || !events[0].KindScopedSemanticTruncated || events[0].KindScopedSemanticAuthzDropped != 1 || events[0].KindScopedSemanticResultCount != 1 {
		t.Fatalf("kind_scoped_semantic events = %+v, want one with result_count=1 truncated=true authz_dropped=1", events)
	}
	if len(b.subjectCandidatesAuthzDropped) == 0 {
		t.Fatalf("authz drops from the semantic arm never reached SubjectCandidatesAuthzDropped telemetry: %v", b.subjectCandidatesAuthzDropped)
	}
}

func TestChaos10941_KindScopedSemanticMissStillEmitsItsEvent(t *testing.T) {
	t.Parallel()
	b := chaos10941Backend(true)
	deps := b.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, chaos10941Request(), testInterpreted(chaos10941Term), deps, nil, nil); err != nil {
		t.Fatal(err)
	}
	events := chaos10941SemanticEvents(tracer)
	if len(events) != 1 || events[0].KindScopedSemanticResultCount != 0 {
		t.Fatalf("kind_scoped_semantic events = %#v, want one with result_count=0", events)
	}
	none := chaos10941Backend(false)
	noneTracer := &captureResolutionTracer{}
	noneDeps := none.deps()
	noneDeps.ResolutionTracer = noneTracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, chaos10941Request(), testInterpreted(chaos10941Term), noneDeps, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(chaos10941SemanticEvents(noneTracer)) != 0 {
		t.Fatal("event emitted although the arm never ran")
	}
}
