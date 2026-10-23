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

func TestChaos10941_KindScopedSemanticFindIsOfferedLeadingNeverCommitted(t *testing.T) {
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
