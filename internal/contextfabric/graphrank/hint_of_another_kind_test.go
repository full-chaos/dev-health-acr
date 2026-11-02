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
	return resolveWithHintsIn(t, context.Background(), backend, question, frame, confirmed, hints...)
}

func resolveWithHintsIn(t *testing.T, ctx context.Context, backend *fakeGraphBackend, question string, frame *contextfabric.QuestionFrame, confirmed *contextfabric.ConfirmedExpectedKind, hints ...contextfabric.SubjectHint) contextfabric.SubjectResolution {
	t.Helper()
	request := testRequest()
	request.Question = question
	request.RequestedScope.SubjectHints = hints
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(ctx, storage.Principal{OrgID: "org_1"}, request, testInterpreted("this subject"), backend.deps(), confirmed, nil, frame, "")
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

// The caller's pick from an offer stays, whatever its kind: the engine records it
// on ctx after redeeming the receipt. No source string escapes the rule, the
// pick's own included, because the wire carries the source.
func TestOnlyTheRedeemedPickEscapesTheKindRule(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	frame := namedFrameAsking(&repository, "this repository")
	for _, source := range append(hintsource.All(), hintsource.Source(hintsource.AskDevPageContext), "") {
		hint := askedKindPageHint(organization, hintCanonicalID(organization))
		hint.Source = string(source)
		if resolution := resolveWithHints(t, hintBackend(organization), "How is this going?", frame, nil, hint); len(resolution.Committed) != 0 {
			t.Errorf("source %q: committed %v, want nothing", source, resolution.Committed)
		}
	}
	hint := askedKindPageHint(organization, hintCanonicalID(organization))
	hint.Source = string(hintsource.ConfirmedCandidateReceipt)
	picked := contextfabric.WithConfirmedCandidatePick(context.Background(), contextfabric.SubjectRef{Kind: organization, CanonicalID: hintCanonicalID(organization)})
	if resolution := resolveWithHintsIn(t, picked, hintBackend(organization), "How is this going?", frame, nil, hint); len(resolution.Committed) != 1 {
		t.Fatalf("the redeemed pick: committed %v, want it", resolution.Committed)
	}
	other := contextfabric.WithConfirmedCandidatePick(context.Background(), contextfabric.SubjectRef{Kind: organization, CanonicalID: "organization:other"})
	if resolution := resolveWithHintsIn(t, other, hintBackend(organization), "How is this going?", frame, nil, hint); len(resolution.Committed) != 0 {
		t.Fatalf("a pick of another subject: committed %v, want nothing", resolution.Committed)
	}
}

// A dropped hint may have been all the turn had to search with: the frame's own
// terms then say what the question names.
func TestADroppedHintLeavesTheFrameTermsToSearch(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	backend := hintBackend(organization)
	backend.searchResults["repo-marker"] = []CandidateNode{candidateNode(repository, "repository:target", "repo-marker", 1, "*")}
	request := testRequest()
	request.Question = "Where is effort going in repo-marker?"
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{askedKindPageHint(organization, hintCanonicalID(organization))}
	interpreted := testInterpreted()
	interpreted.SubjectTerms = nil
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted, backend.deps(), nil, nil, namedFrameAsking(&repository, "repo-marker"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "repository:target" {
		t.Fatalf("committed %v, searched %v, want the repository the frame names", resolution.Committed, backend.searchCalls)
	}
}

// A page hint of a kind neither side asks for does not hold a comparison whose
// sides both resolve.
func TestAnUnrelatedHintDoesNotHoldAComparison(t *testing.T) {
	t.Parallel()
	repository, team := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam
	backend := hintBackend(team)
	backend.searchResults["alpha"] = []CandidateNode{candidateNode(repository, "repository:alpha", "alpha", 1, "*")}
	backend.searchResults["beta"] = []CandidateNode{candidateNode(repository, "repository:beta", "beta", 1, "*")}
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"alpha"}, ExpectedKind: slotGateKindPointer(repository)}},
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"beta"}, ExpectedKind: slotGateKindPointer(repository)}},
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	without := resolveWithHints(t, backend, "Compare alpha and beta", &frame, nil)
	with := resolveWithHints(t, backend, "Compare alpha and beta", &frame, nil, askedKindPageHint(team, hintCanonicalID(team)))
	if len(without.Committed) != 2 || len(with.Committed) != len(without.Committed) {
		t.Fatalf("committed without the hint %v, with it %v: want both sides either way", without.Committed, with.Committed)
	}
}

// An explicit set the comparison cut does not take falls through to the ordinary
// resolver. It still asks for its own operands' kinds, so a hint of another kind
// commits nothing there, in the decisive pass and in the offers-only pass.
func TestAnOutOfCutComparisonKeepsItsOperandsKinds(t *testing.T) {
	t.Parallel()
	repository, organization := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectOrganization
	operand := func(term string, kind *contextfabric.SubjectKind) contextfabric.SubjectOperand {
		return contextfabric.SubjectOperand{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: kind}}
	}
	frame := func(kind *contextfabric.SubjectKind) *contextfabric.QuestionFrame {
		derived := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
			Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
			SubjectExpression: contextfabric.SubjectExpression{
				Kind:     contextfabric.SubjectExpressionExplicitSet,
				Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{operand("alpha", kind), operand("beta", kind), operand("gamma", kind)}},
			},
			Temporal: contextfabric.TemporalIntentCurrent,
			Version:  contextfabric.QuestionFrameVersion,
		}, nil)
		return &derived
	}
	if got := contextfabric.ClassifyComparisonOperands(frame(&repository)).Admission; got != contextfabric.ComparisonOutOfCutOperandCount {
		t.Fatalf("fixture: admission %s, want out_of_cut_operand_count", got)
	}
	hint := askedKindPageHint(organization, hintCanonicalID(organization))
	for name, ctx := range map[string]context.Context{"decisive": context.Background(), "offers only": contextfabric.WithOffersOnlyResolution(context.Background())} {
		resolution := resolveWithHintsIn(t, ctx, hintBackend(organization), "Compare alpha, beta and gamma", frame(&repository), nil, hint)
		if len(resolution.Committed) != 0 {
			t.Errorf("%s: committed %v, want nothing", name, resolution.Committed)
		}
		for _, candidate := range resolution.Candidates {
			if candidate.Subject.Kind == organization {
				t.Errorf("%s: the organization hint reached the pool: %v", name, resolution.Candidates)
			}
		}
	}
	// An operand with no stated kind leaves the set asking for no kind, as today.
	if resolution := resolveWithHints(t, hintBackend(organization), "Compare alpha, beta and gamma", frame(nil), nil, hint); len(resolution.Committed) != 1 {
		t.Fatalf("unstated kinds: committed %v, want today's behaviour", resolution.Committed)
	}
}

// An inactive team hint answers for that team. On a comparison of other kinds it
// must not answer in place of both sides; on a comparison of teams it still does.
func TestAnInactiveTeamHintAnswersOnlyAComparisonOfTeams(t *testing.T) {
	t.Parallel()
	team := contractsv1.ContextFabricSubjectTeam
	hint := askedKindPageHint(team, "team:platform")
	inactive := map[string]string{SubjectKey(contextfabric.SubjectRef{Kind: team, CanonicalID: "team:platform"}): ""}
	frame := func(kind contextfabric.SubjectKind) *contextfabric.QuestionFrame {
		derived := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
			Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
			SubjectExpression: contextfabric.SubjectExpression{
				Kind: contextfabric.SubjectExpressionExplicitSet,
				Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
					{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"alpha"}, ExpectedKind: slotGateKindPointer(kind)}},
					{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"beta"}, ExpectedKind: slotGateKindPointer(kind)}},
				}},
			},
			Temporal: contextfabric.TemporalIntentCurrent,
			Version:  contextfabric.QuestionFrameVersion,
		}, nil)
		return &derived
	}
	for kind, wantInactive := range map[contextfabric.SubjectKind]bool{contractsv1.ContextFabricSubjectRepository: false, team: true} {
		backend := hintBackend()
		backend.exactInactive = inactive
		resolution := resolveWithHints(t, backend, "Compare alpha and beta", frame(kind), nil, hint)
		if got := len(resolution.InactiveHints) > 0; got != wantInactive {
			t.Errorf("comparison of %s: inactive answers %v, want answered %v", kind, resolution.InactiveHints, wantInactive)
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
	if _, dropped := withoutHintsOfAnotherKind(context.Background(), request, BindHandles(request.Question), frame, nil); len(dropped) != 0 {
		t.Fatalf("dropped %v, want the anchor kept", dropped)
	}
	request.Question = "Is this pull request ready?"
	if _, dropped := withoutHintsOfAnotherKind(context.Background(), request, BindHandles(request.Question), frame, nil); len(dropped) != 1 {
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

// A project on a page says where the ticket a question names is, but only a key
// the caller holds is a handle (CHAOS-12463). A key whose prefix the caller does
// not hold binds nothing, so the project anchors nothing and is dropped. The
// filter reads the handles after the key gate, not the raw question.
func TestAProjectHintAnchorsOnlyAKeyTheCallerHolds(t *testing.T) {
	t.Parallel()
	workItem, project := contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectProject
	for held, wantDropped := range map[bool]int{true: 0, false: 1} {
		deps := hintBackend(project).deps()
		tracer := &recordingTracer{}
		deps.ResolutionTracer = tracer
		deps.WorkItemKeyPrefix = (&prefixReads{held: map[string]bool{"OPS-": held}}).read
		request := testRequest()
		request.Question = "what blocks OPS-1?"
		request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{askedKindPageHint(project, hintCanonicalID(project))}
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("OPS-1"), deps, nil, nil, namedFrameAsking(&workItem, "OPS-1"), ""); err != nil {
			t.Fatal(err)
		}
		var event ResolutionTraceEvent
		for _, traced := range tracer.events {
			if traced.Stage == "page_hint_lookup" {
				event = traced
			}
		}
		if event.Stage == "" || event.PageHintOtherKind != wantDropped {
			t.Errorf("prefix held %v: page hint event %+v, want %d dropped", held, event, wantDropped)
		}
	}
}
