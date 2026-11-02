package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var hintKindTable = []contextfabric.SubjectKind{
	contractsv1.ContextFabricSubjectOrganization, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject,
	contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectPullRequest,
}

func hintCanonicalID(kind contextfabric.SubjectKind) string { return string(kind) + ":a" }

func namedFrameAsking(kind *contextfabric.SubjectKind, terms ...string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: terms, ExpectedKind: kind},
	}}
}

func hintBackend(kinds ...contextfabric.SubjectKind) *fakeGraphBackend {
	hints := map[string]CandidateNode{}
	for _, kind := range kinds {
		subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: hintCanonicalID(kind)}
		hints[SubjectKey(subject)] = candidateNode(kind, subject.CanonicalID, string(kind)+" a", 1, "*")
	}
	return &fakeGraphBackend{searchResults: map[string][]CandidateNode{}, exactHints: hints}
}

func askedKindPageHint(kind contextfabric.SubjectKind, id string) contextfabric.SubjectHint {
	return contextfabric.SubjectHint{Kind: kind, ID: id, Label: string(kind) + " a", Source: hintsource.AskDevPageContext}
}

func resolveWithHints(t *testing.T, backend *fakeGraphBackend, question string, frame *contextfabric.QuestionFrame, confirmed *contextfabric.ConfirmedExpectedKind, hints ...contextfabric.SubjectHint) contextfabric.SubjectResolution {
	t.Helper()
	request := testRequest()
	request.Question = question
	request.RequestedScope.SubjectHints = hints
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("this subject"), backend.deps(), confirmed, nil, frame, "")
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

// The executed rows of the ticket, at the resolution seam: a frame that asks for a
// repository, with the organization beside a repository hint that is missing or
// forged.
func TestALostRepositoryNeverAnswersAsTheOrganization(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	frame := namedFrameAsking(&repository, "this repository")
	forged := askedKindPageHint(repository, "repository:00000000-0000-4000-8000-000000000001")
	for name, hints := range map[string][]contextfabric.SubjectHint{
		"chip removed":  {askedKindPageHint(organization, hintCanonicalID(organization))},
		"forged beside": {askedKindPageHint(organization, hintCanonicalID(organization)), forged},
		"forged alone":  {forged},
		"forged first":  {forged, askedKindPageHint(organization, hintCanonicalID(organization))},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolveWithHints(t, hintBackend(organization), "Where is engineering effort going in this repository?", frame, nil, hints...)
			if len(resolution.Committed) != 0 {
				t.Fatalf("committed %v, want nothing: the organization answered for a lost repository", resolution.Committed)
			}
		})
	}
}

// A repository that exists outside the caller's grant reads like a forged id: the
// same empty resolution, so the hint says nothing about what exists.
func TestARepositoryOutsideTheGrantReadsLikeAForgedOne(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	frame := namedFrameAsking(&repository, "this repository")
	resolve := func(backend *fakeGraphBackend, id string) contextfabric.SubjectResolution {
		request := testRequest()
		request.Question = "Where is engineering effort going in this repository?"
		request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{askedKindPageHint(organization, hintCanonicalID(organization)), askedKindPageHint(repository, id)}
		resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/inside"}}, request, testInterpreted("this repository"), backend.deps(), nil, nil, frame, "")
		if err != nil {
			t.Fatal(err)
		}
		return resolution
	}
	outside := hintBackend(organization)
	node := candidateNode(repository, hintCanonicalID(repository), "repository a", 1, "acme/outside")
	outside.exactHints[SubjectKey(contextfabric.SubjectRef{Kind: repository, CanonicalID: hintCanonicalID(repository)})] = node
	hidden := resolve(outside, hintCanonicalID(repository))
	forged := resolve(hintBackend(organization), hintCanonicalID(repository))
	if len(hidden.Committed) != 0 || len(forged.Committed) != 0 || len(hidden.Candidates) != len(forged.Candidates) {
		t.Fatalf("outside: committed %v candidates %v; forged: committed %v candidates %v", hidden.Committed, hidden.Candidates, forged.Committed, forged.Candidates)
	}
}

// Every asked kind against every other hint kind. Only the asked kind commits.
func TestOnlyAHintOfTheAskedKindCommits(t *testing.T) {
	t.Parallel()
	for _, asked := range hintKindTable {
		for _, other := range hintKindTable {
			if other == asked {
				continue
			}
			t.Run(string(asked)+"/"+string(other), func(t *testing.T) {
				t.Parallel()
				asked, frame := asked, namedFrameAsking(&asked, "this subject")
				backend := hintBackend(asked, other)
				resolution := resolveWithHints(t, backend, "How is this going?", frame, nil, askedKindPageHint(other, hintCanonicalID(other)))
				if len(resolution.Committed) != 0 {
					t.Fatalf("hint of kind %s committed %v under a frame that asks for %s", other, resolution.Committed, asked)
				}
				resolution = resolveWithHints(t, backend, "How is this going?", frame, nil, askedKindPageHint(other, hintCanonicalID(other)), askedKindPageHint(asked, hintCanonicalID(asked)))
				if len(resolution.Committed) != 1 || resolution.Committed[0].Kind != asked {
					t.Fatalf("committed %v, want the one hint of kind %s", resolution.Committed, asked)
				}
			})
		}
	}
}

// A dropped hint's label must not come back as a search term and commit the same
// subject by its exact label.
func TestADroppedHintsLabelIsNotSearched(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	backend := hintBackend(organization)
	backend.searchResults["organization a"] = []CandidateNode{candidateNode(organization, hintCanonicalID(organization), "organization a", 1, "*")}
	resolution := resolveWithHints(t, backend, "Where is engineering effort going in this repository?", namedFrameAsking(&repository, "this repository"), nil, askedKindPageHint(organization, hintCanonicalID(organization)))
	if len(resolution.Committed) != 0 || len(resolution.Candidates) != 0 {
		t.Fatalf("committed %v candidates %v, want neither", resolution.Committed, resolution.Candidates)
	}
}

// A frame that states no kind keeps today's behavior, and so does a question with
// no frame.
func TestAFrameThatAsksForNoKindKeepsEveryHint(t *testing.T) {
	t.Parallel()
	organization := contractsv1.ContextFabricSubjectOrganization
	for name, frame := range map[string]*contextfabric.QuestionFrame{
		"named, no kind":     namedFrameAsking(nil, "this subject"),
		"no frame":           nil,
		"members of a scope": {SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolveWithHints(t, hintBackend(organization), "How is this going?", frame, nil, askedKindPageHint(organization, hintCanonicalID(organization)))
			if len(resolution.Committed) != 1 {
				t.Fatalf("committed %v, want the hint", resolution.Committed)
			}
		})
	}
}

// A kind the caller confirmed is what the question asks for.
func TestAConfirmedKindDecidesWhichHintCommits(t *testing.T) {
	t.Parallel()
	team, organization := contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectOrganization
	frame := namedFrameAsking(&organization, "this subject")
	backend := hintBackend(team, organization)
	resolution := resolveWithHints(t, backend, "How is this going?", frame, &contextfabric.ConfirmedExpectedKind{Kind: team}, askedKindPageHint(organization, hintCanonicalID(organization)), askedKindPageHint(team, hintCanonicalID(team)))
	if len(resolution.Committed) != 1 || resolution.Committed[0].Kind != team {
		t.Fatalf("committed %v, want the team", resolution.Committed)
	}
}

// The caller's pick from an offer stays, whatever its kind. Any other source takes
// the rule, an engine source string included: the wire carries it, so a caller
// can send it.
func TestOnlyACallersPickEscapesTheKindRule(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	frame := namedFrameAsking(&repository, "this repository")
	for source, commits := range map[hintsource.Source]bool{
		hintsource.ConfirmedCandidateReceipt:       true,
		hintsource.AnswerReuseAuthorizationRecheck: false,
		hintsource.CohortGroupAuthorization:        false,
		hintsource.EngineCommittedAnchorCarry:      false,
		hintsource.PriorSubjectReceipt:             false,
	} {
		hint := askedKindPageHint(organization, hintCanonicalID(organization))
		hint.Source = string(source)
		resolution := resolveWithHints(t, hintBackend(organization), "How is this going?", frame, nil, hint)
		if got := len(resolution.Committed) == 1; got != commits {
			t.Errorf("%s: committed %v, want commit %v", source, resolution.Committed, commits)
		}
	}
}

// The reuse and group rechecks resolve with no frame, so they keep every id they
// hold.
func TestARecheckWithNoFrameKeepsItsHints(t *testing.T) {
	t.Parallel()
	organization := contractsv1.ContextFabricSubjectOrganization
	hint := askedKindPageHint(organization, hintCanonicalID(organization))
	hint.Source = string(hintsource.AnswerReuseAuthorizationRecheck)
	if resolution := resolveWithHints(t, hintBackend(organization), "How is this going?", nil, nil, hint); len(resolution.Committed) != 1 {
		t.Fatalf("committed %v, want the rechecked subject", resolution.Committed)
	}
}

// A repository on a page says where the pull request a question names is
// (CHAOS-8407), so it stays. With no handle in the question it does not.
func TestARepositoryHintStaysOnlyToAnchorANamedHandle(t *testing.T) {
	t.Parallel()
	pullRequest, repository := contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectRepository
	frame := namedFrameAsking(&pullRequest, "PR 532")
	request := testRequest()
	request.Question = "Is PR 532 ready?"
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{askedKindPageHint(repository, hintCanonicalID(repository))}
	if _, dropped := withoutHintsOfAnotherKind(request, frame, nil); len(dropped) != 0 {
		t.Fatalf("dropped %v, want the anchor kept", dropped)
	}
	request.Question = "Is this pull request ready?"
	if _, dropped := withoutHintsOfAnotherKind(request, frame, nil); len(dropped) != 1 {
		t.Fatalf("dropped %v, want the repository dropped: no handle to anchor", dropped)
	}
}

// The trace keeps its total: every page hint takes one outcome.
func TestADroppedPageHintIsCounted(t *testing.T) {
	t.Parallel()
	tally := newPageHintLookupTally(testRequest(), []contextfabric.SubjectHint{askedKindPageHint(contractsv1.ContextFabricSubjectOrganization, "organization:a"), {Kind: contractsv1.ContextFabricSubjectTeam, ID: "team:a", Source: "other"}})
	event, ok := tally.event()
	if !ok || event.PageHintCount != 1 || event.PageHintOtherKind != 1 || event.PageHintOtherKindByKind["organization"] != 1 {
		t.Fatalf("event = %+v, ok = %v", event, ok)
	}
}
