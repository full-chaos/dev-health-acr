package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestResolveSubjects_SemanticRepositoryHitKeepsTheDeclaredRepositoryKindOffered(t *testing.T) {
	t.Parallel()
	b := chaos10941Backend(true, chaos10941Semantic(chaos10941Repo))
	b.searchResults = map[string][]CandidateNode{chaos10941Term: {
		candidateNode(contextfabric.SubjectPullRequest, "pull_request:r:1", "PR one executor setup", 0.8, "*"),
		candidateNode(contextfabric.SubjectPullRequest, "pull_request:r:2", "PR two executor setup", 0.79, "*"),
	}}
	tracer := &chaos4120Tracer{}
	deps := b.deps()
	deps.ResolutionTracer = tracer
	frame := namedSubjectFrame(chaos10941Term, kindOf(contractsv1.ContextFabricSubjectRepository))
	_, offer, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(chaos10941Term), deps, nil, nil, frame, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.kindScopedSemanticCalls) == 0 {
		t.Fatal("semantic arm never ran")
	}
	if withheld := tracer.eventsByStage("kind_offer_withheld"); len(withheld) != 0 {
		t.Fatalf("kind_offer_withheld = %+v: the semantic arm returned a repository, the declared kind must not be withheld", withheld)
	}
	var kinds []contractsv1.ContextFabricSubjectKind
	for _, option := range offer.KindOptions {
		kinds = append(kinds, option.Kind)
	}
	if len(kinds) == 0 || kinds[0] != contractsv1.ContextFabricSubjectRepository {
		t.Fatalf("KindOptions kinds = %v, want repository first", kinds)
	}
	_ = contextfabric.SubjectRepository
}

func confirmedRepositoryTurn(t *testing.T, b *fakeGraphBackend, principal storage.Principal) (contextfabric.SubjectResolution, []string) {
	t.Helper()
	frame := namedSubjectFrame(chaos10941Term, kindOf(contractsv1.ContextFabricSubjectRepository))
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectRepository}
	resolution, offer, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, testRequest(), testInterpreted(chaos10941Term), b.deps(), confirmed, nil, frame, "")
	if err != nil {
		t.Fatal(err)
	}
	return resolution, chaos10941Offered(offer)
}

func TestResolveSubjects_ConfirmedRepositoryKindOffersTheSemanticRepositoryHit(t *testing.T) {
	t.Parallel()
	resolution, offered := confirmedRepositoryTurn(t, chaos10941Backend(true, chaos10941Semantic(chaos10941Repo)), storage.Principal{OrgID: "org_1"})
	if len(resolution.Committed) != 0 {
		t.Fatalf("a vector-only find committed: %#v", resolution.Committed)
	}
	if len(offered) != 1 || offered[0] != chaos10941Repo.CanonicalID {
		t.Fatalf("candidate offer = %v, want the semantic repository listed", offered)
	}
}

func TestResolveSubjects_ConfirmedKindSemanticArmSkippedWhenLexicalRescueFoundTheKind(t *testing.T) {
	t.Parallel()
	b := chaos10941Backend(true, chaos10941Semantic(chaos10941Repo))
	lexicalRepo := candidateNode(contextfabric.SubjectRepository, "repository:lexical", "lexical repo", 0.8, "*")
	b.enableSearchKind = true
	b.searchKindResults = map[string]map[contextfabric.SubjectKind][]CandidateNode{chaos10941Term: {contextfabric.SubjectRepository: {lexicalRepo, candidateNode(contextfabric.SubjectRepository, "repository:lexical2", "lexical repo two", 0.8, "*")}}}
	resolution, offered := confirmedRepositoryTurn(t, b, storage.Principal{OrgID: "org_1"})
	if len(b.kindScopedSemanticCalls) != 0 {
		t.Fatalf("semantic arm ran although the lexical rescue found the kind: %v", b.kindScopedSemanticCalls)
	}
	if len(resolution.Candidates)+len(resolution.Committed)+len(offered) == 0 {
		t.Fatal("lexical rescue found nothing; fixture does not exercise the skip")
	}
}

func TestResolveSubjects_ConfirmedKindSemanticMissStaysEmpty(t *testing.T) {
	t.Parallel()
	resolution, offered := confirmedRepositoryTurn(t, chaos10941Backend(true), storage.Principal{OrgID: "org_1"})
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 || len(offered) != 0 {
		t.Fatalf("semantic miss changed the outcome: %#v %v", resolution, offered)
	}
}

func TestResolveSubjects_ConfirmedKindSemanticArmNeverOffersAnUnauthorizedRepository(t *testing.T) {
	t.Parallel()
	scope := []string{"full-chaos/dev-health-acr"}
	foreign := candidateNode(contextfabric.SubjectRepository, "repository:foreign", "other/private", 0.8, []string{"other/private"})
	resolution, offered := confirmedRepositoryTurn(t, chaos10941Backend(true, foreign), storage.Principal{OrgID: "org_1", RepositoryScopes: scope})
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 || len(offered) != 0 {
		t.Fatalf("unauthorized semantic find surfaced: %#v %v", resolution, offered)
	}
}
