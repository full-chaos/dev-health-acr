package graphrank

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12523: a question that names a handle is answered about that handle, or
// says why not. These run the real resolver on a repository that holds another
// census kind, as every real repository does: on one that does not, the handle
// used to commit by accident.

const (
	besideOtherRepoID = "repository:r-2"
	besideOtherRepo   = "full-chaos/dev-health-ops"
	besidePRInRepo    = "pull_request:r-1:747"
	besidePRElsewhere = "pull_request:r-2:748"
	besideRunID       = "ci_pipeline_run.v2:r-1:4343"
)

type besideRow struct {
	kind   CensusKind
	value  string
	id     string
	repoID string
	slug   string
}

// besideCensus answers as the real adapter does: inside the bound grant, and
// inside the anchor when one is given. A work item has no repository column.
type besideCensus struct {
	rows  []besideRow
	calls int
	err   error
	shape func(*CensusOutcome)
}

func (c *besideCensus) census(ctx context.Context, _ string, kind CensusKind, value string, handleBound bool, _ contextfabric.SubjectKind, anchorID string, anchorBound bool) (CensusOutcome, error) {
	c.calls++
	grant, bound := contextfabric.CensusGrantFrom(ctx)
	if c.err != nil {
		return CensusOutcome{GrantApplied: bound}, c.err
	}
	var ids []string
	for _, row := range c.rows {
		if row.kind != kind || (handleBound && row.value != value) || (anchorBound && row.repoID != anchorID) || (bound && !slices.Contains(grant, row.slug)) {
			continue
		}
		ids = append(ids, row.id)
	}
	outcome := CensusOutcome{Count: len(ids), CensusReadAt: time.Now().UTC(), GrantApplied: bound}
	if len(ids) == 1 {
		outcome.SatisfierCanonicalID = ids[0]
	} else {
		outcome.SatisfierCanonicalIDs = ids
	}
	if c.shape != nil {
		c.shape(&outcome)
	}
	return outcome, nil
}

func besideRows() []besideRow {
	return []besideRow{
		{contextfabric.SubjectPullRequest, "747", besidePRInRepo, scopeAnchorRepoID, scopeAnchorRepo},
		{contextfabric.SubjectPullRequest, "748", besidePRElsewhere, besideOtherRepoID, besideOtherRepo},
		{contractsv1.ContextFabricSubjectCIRun, "4343", besideRunID, scopeAnchorRepoID, scopeAnchorRepo},
		{contractsv1.ContextFabricSubjectCIRun, "4344", "ci_pipeline_run.v2:r-1:4344", scopeAnchorRepoID, scopeAnchorRepo},
	}
}

func besideNode(kind contextfabric.SubjectKind, id, label string) CandidateNode {
	node := candidateNode(kind, id, label, 0.5, []string{scopeAnchorRepo, besideOtherRepo})
	node.Attributes[authorizationTeamsAttr] = "*"
	return node
}

type besideWorld struct {
	principal storage.Principal
	question  string
	frame     *contextfabric.QuestionFrame
	census    *besideCensus
	hinted    bool
	nodes     []CandidateNode
	pool      []CandidateNode
	edit      func(*contextfabric.InvestigationRequest)
}

type besideTurn struct {
	resolution contextfabric.SubjectResolution
	offers     contextfabric.StructureOfferMaterial
	tracer     *captureResolutionTracer
}

func runBeside(t *testing.T, w besideWorld) besideTurn {
	t.Helper()
	backend := scopeAnchorBackendFor(false, -1, []string{scopeAnchorRepo, besideOtherRepo})
	repoNode := backend.searchResults[scopeAnchorRepo][0]
	repoNode.Attributes[authorizationTeamsAttr] = "*"
	// The pool holds another census kind, as a real repository's does.
	pooledRun := besideNode(contractsv1.ContextFabricSubjectCIRun, besideRunID, scopeAnchorRepo+" CI 4343")
	pooledRun.Relevance = Normalized(0.9)
	pooled := []CandidateNode{repoNode, pooledRun}
	backend.searchResults[scopeAnchorRepo] = append(pooled, w.pool...)
	backend.exactHints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID})] = repoNode
	for _, node := range append(slices.Clone(w.nodes), besideNode(contextfabric.SubjectPullRequest, besidePRInRepo, "PR #747"), besideNode(contractsv1.ContextFabricSubjectCIRun, besideRunID, "CI 4343")) {
		id, _ := node.Attributes["canonical_id"].(string)
		kind, _ := node.Attributes["subject_kind"].(string)
		backend.exactHints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: id})] = node
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = w.census.census
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		if !ValidateHandleGrammar(kind, patternID, value) {
			return "", false
		}
		return HandleSourceColumn(kind, patternID)
	}
	request := testRequest()
	request.Question = w.question
	request.Options.MaxSubjectCandidates = 10
	if w.hinted {
		request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectRepository, ID: scopeAnchorRepoID, Label: scopeAnchorRepo, Source: "workbench"}}
	}
	if w.edit != nil {
		w.edit(&request)
	}
	resolution, offers, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), w.principal, request, testInterpreted(scopeAnchorRepo), deps, nil, nil, w.frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis: %v", err)
	}
	return besideTurn{resolution, offers, tracer}
}

func besideCallers(t *testing.T) map[string]storage.Principal {
	t.Helper()
	return map[string]storage.Principal{
		"restricted":   restrictedCaller(t, scopeAnchorRepo, besideOtherRepo),
		"unrestricted": userprincipal.Authenticated(t, "org_1", nil),
		"universal":    userprincipal.Authenticated(t, "org_1", []string{"*"}),
	}
}

func statedFrame(kind contextfabric.SubjectKind) *contextfabric.QuestionFrame {
	return namedScopeAnchorFrame(&kind)
}

func besideCommittedIDs(resolution contextfabric.SubjectResolution) []string {
	ids := make([]string, 0, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		ids = append(ids, subject.CanonicalID)
	}
	return ids
}

func besideQuestion(noun string) string {
	return "What is the state of " + noun + " in " + scopeAnchorRepo + "?"
}

// Each way a repository comes to be committed: the question's own words, and an
// exact hint, the exit that returns ahead of the search.
func besideExits(t *testing.T, run func(t *testing.T, hinted bool)) {
	t.Helper()
	for name, hinted := range map[string]bool{"from the question": false, "from an exact hint": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, hinted)
		})
	}
}

func TestANamedSubjectCommitsBesideItsRepository(t *testing.T) {
	t.Parallel()
	subjects := map[string]struct {
		noun, want string
		kind       contextfabric.SubjectKind
	}{
		"pull request": {"pull request 747", besidePRInRepo, contextfabric.SubjectPullRequest},
		"ci run":       {"CI run 4343", besideRunID, contractsv1.ContextFabricSubjectCIRun},
	}
	for name, subject := range subjects {
		for caller, principal := range besideCallers(t) {
			for frameName, frame := range map[string]*contextfabric.QuestionFrame{"stated": statedFrame(subject.kind), "unstated": namedScopeAnchorFrame(nil), "no frame": nil} {
				t.Run(name+" "+caller+" "+frameName, func(t *testing.T) {
					t.Parallel()
					besideExits(t, func(t *testing.T, hinted bool) {
						turn := runBeside(t, besideWorld{principal: principal, question: besideQuestion(subject.noun), frame: frame, census: &besideCensus{rows: besideRows()}, hinted: hinted})
						if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, scopeAnchorRepoID) || !slices.Contains(got, subject.want) {
							t.Fatalf("committed = %v, want the repository and %s", got, subject.want)
						}
						if turn.resolution.CensusLimitation != "" {
							t.Fatalf("limitation = %q", turn.resolution.CensusLimitation)
						}
					})
				})
			}
		}
	}
}

// A 0 here proves an absence in this repository only. Pull request 748 lives in the
// other repository, so the row that pins it says "the requested scope", and no
// sentence may read as absent from all the caller can read.
func TestAStatedHandleAbsentFromTheRepositoryIsNotAnsweredByIt(t *testing.T) {
	t.Parallel()
	for name, noun := range map[string]string{"no such subject": "pull request 999", "in the other repository": "pull request 748"} {
		for caller, principal := range besideCallers(t) {
			for _, allowClarification := range []bool{true, false} {
				t.Run(name+" "+caller, func(t *testing.T) {
					t.Parallel()
					besideExits(t, func(t *testing.T, hinted bool) {
						turn := runBeside(t, besideWorld{
							principal: principal, question: besideQuestion(noun), frame: statedFrame(contextfabric.SubjectPullRequest), census: &besideCensus{rows: besideRows()}, hinted: hinted,
							edit: func(r *contextfabric.InvestigationRequest) { r.Options.AllowClarification = allowClarification },
						})
						if got := besideCommittedIDs(turn.resolution); len(got) != 0 {
							t.Fatalf("committed = %v, want the repository demoted", got)
						}
						if turn.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
							t.Fatalf("limitation = %q, want the scope's sentence", turn.resolution.CensusLimitation)
						}
						if !slices.ContainsFunc(turn.resolution.Candidates, func(c contextfabric.SubjectCandidate) bool {
							return c.Subject.CanonicalID == scopeAnchorRepoID && c.State == contextfabric.ResolutionAmbiguous
						}) {
							t.Fatalf("candidates = %v, want the repository offered", candidateIDs(turn.resolution.Candidates))
						}
						_, value, _ := strings.Cut(noun, "request ")
						noHandleOption(t, listingTurn{offers: turn.offers}, value)
						if allowClarification && !strings.Contains(turn.resolution.ClarificationPrompt, scopeAnchorRepo) {
							t.Fatalf("prompt = %q, want the repository named", turn.resolution.ClarificationPrompt)
						}
					})
				})
			}
		}
	}
}

func besideWorkItem(t *testing.T, provider, key string) string {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", provider + ":" + key}, nil)
	if err != nil || omitted {
		t.Fatalf("derive: omitted=%t err=%v", omitted, err)
	}
	return id
}

// A work item has no repository column, so its census is not anchored: its count
// speaks for all the caller can read, and one key names one item whatever the
// repository (ruling 3: no claim about the link).
func TestAStatedWorkItemBesideARepository(t *testing.T) {
	t.Parallel()
	linear, jira := besideWorkItem(t, "linear", "CHAOS-71"), besideWorkItem(t, "jira", "CHAOS-71")
	row := func(id string) besideRow {
		return besideRow{contextfabric.SubjectWorkItem, "CHAOS-71", id, "", scopeAnchorRepo}
	}
	nodes := []CandidateNode{repoLessItem(linear, "Split the job", scopeAnchorRepo), repoLessItem(jira, "Split the job", scopeAnchorRepo)}
	frame := statedFrame(contextfabric.SubjectWorkItem)
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			for provider, id := range map[string]string{"linear": linear, "jira": jira} {
				turn := runBeside(t, besideWorld{principal: principal, question: "Why is CHAOS-71 blocked in " + scopeAnchorRepo + "?", frame: frame, census: &besideCensus{rows: []besideRow{row(id)}}, nodes: nodes})
				if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, id) || !slices.Contains(got, scopeAnchorRepoID) {
					t.Fatalf("%s, count 1: committed = %v", provider, got)
				}
			}
			absent := runBeside(t, besideWorld{principal: principal, question: "Why is CHAOS-999 blocked in " + scopeAnchorRepo + "?", frame: frame, census: &besideCensus{}})
			want := contractsv1.ContextFabricCensusAbsentLimitation
			if caller == "restricted" {
				want = contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation
			}
			if len(absent.resolution.Committed) != 0 || absent.resolution.CensusLimitation != want {
				t.Fatalf("count 0: committed %v limitation %q", absent.resolution.Committed, absent.resolution.CensusLimitation)
			}
			both := runBeside(t, besideWorld{principal: principal, question: "Why is CHAOS-71 blocked in " + scopeAnchorRepo + "?", frame: frame, census: &besideCensus{rows: []besideRow{row(linear), row(jira)}}, nodes: nodes})
			if len(both.resolution.Committed) != 0 {
				t.Fatalf("count 2: committed = %v", both.resolution.Committed)
			}
			got := candidateIDs(both.resolution.Candidates)
			if len(got) < 2 || !slices.Contains(got[:2], linear) || !slices.Contains(got[:2], jira) {
				t.Fatalf("count 2: candidates = %v, want both items first", got)
			}
			if prompt := both.resolution.ClarificationPrompt; strings.Contains(prompt, "could not be matched") || !strings.Contains(prompt, "CHAOS-71") {
				t.Fatalf("count 2: prompt = %q, want the listing's own", prompt)
			}
			noHandleOption(t, listingTurn{offers: both.offers}, "CHAOS-71")
		})
	}
}

// A turn that states no kind may only mention the handle. The repository's answer
// stands: a proved subject joins it, and a proved absence is said.
func TestAnUnstatedTurnKeepsTheRepository(t *testing.T) {
	t.Parallel()
	for frameName, frame := range map[string]*contextfabric.QuestionFrame{"no kind": namedScopeAnchorFrame(nil), "no frame": nil} {
		for caller, principal := range besideCallers(t) {
			t.Run(frameName+" "+caller, func(t *testing.T) {
				t.Parallel()
				besideExits(t, func(t *testing.T, hinted bool) {
					turn := runBeside(t, besideWorld{principal: principal, question: "How is " + scopeAnchorRepo + " doing since pull request 999 merged?", frame: frame, census: &besideCensus{rows: besideRows()}, hinted: hinted})
					if got := besideCommittedIDs(turn.resolution); !slices.Equal(got, []string{scopeAnchorRepoID}) {
						t.Fatalf("committed = %v, want the repository alone", got)
					}
					if turn.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
						t.Fatalf("limitation = %q", turn.resolution.CensusLimitation)
					}
				})
			})
		}
	}
}

// The unstated path never touches a subject of the handle's kind the turn already
// committed: there is no repository's answer to protect from it, and nothing to drop.
func TestAnUnstatedTurnDropsNoCommittedSubject(t *testing.T) {
	t.Parallel()
	deps := (&fakeGraphBackend{}).deps()
	census := &besideCensus{rows: besideRows()}
	deps.CensusFunc = census.census
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		return "git_pull_requests.number", ValidateHandleGrammar(kind, patternID, value)
	}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID, Label: scopeAnchorRepo}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:r-1:5", Label: "Another change"}
	request := testRequest()
	request.Question = "What changed in " + scopeAnchorRepo + " after pull request 747?"
	before := contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repo, other}}
	after, undeciding := commitNamedHandle(context.Background(), storage.Principal{OrgID: "org_1"}, request, namedScopeAnchorFrame(nil), deps, before, contextfabric.CommitBasisSet{}, contextfabric.CommitDecisionDigestSet{})
	if !slices.Equal(after.Committed, before.Committed) || undeciding != nil || census.calls != 0 {
		t.Fatalf("committed %v undeciding %+v census calls %d, want the turn untouched", after.Committed, undeciding, census.calls)
	}
}

// A repository-only question, a question about the repository, and a cohort pay
// nothing and keep their answer.
func TestAQuestionAboutTheRepositoryIsUntouched(t *testing.T) {
	t.Parallel()
	repository := contextfabric.SubjectRepository
	cohort := &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{scopeAnchorRepo}, MemberKind: contextfabric.SubjectPullRequest}},
	}
	expectPullRequests := func(r *contextfabric.InvestigationRequest) {
		r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contextfabric.SubjectPullRequest}
	}
	for name, world := range map[string]besideWorld{
		"no handle":                       {question: "How is " + scopeAnchorRepo + " doing?", frame: namedScopeAnchorFrame(&repository)},
		"no handle, no frame":             {question: "How is " + scopeAnchorRepo + " doing?"},
		"the frame states the repository": {question: besideQuestion("pull request 999"), frame: namedScopeAnchorFrame(&repository)},
		"a false bind":                    {question: "What is the run 2026 plan for " + scopeAnchorRepo + "?", frame: namedScopeAnchorFrame(&repository)},
		"a cohort":                        {question: "How many pull requests in " + scopeAnchorRepo + " came after pull request 999?", frame: cohort},
		"a cohort with a stated kind":     {question: "How many pull requests in " + scopeAnchorRepo + " came after pull request 999?", frame: cohort, edit: expectPullRequests},
		"the request and frame disagree":  {question: besideQuestion("pull request 999"), frame: namedScopeAnchorFrame(&repository), edit: expectPullRequests},
	} {
		for caller, principal := range besideCallers(t) {
			t.Run(name+" "+caller, func(t *testing.T) {
				t.Parallel()
				world.principal, world.census = principal, &besideCensus{rows: besideRows()}
				turn := runBeside(t, world)
				if got := besideCommittedIDs(turn.resolution); !slices.Equal(got, []string{scopeAnchorRepoID}) {
					t.Fatalf("committed = %v, want the repository alone", got)
				}
				if turn.resolution.CensusLimitation != "" {
					t.Fatalf("limitation = %q", turn.resolution.CensusLimitation)
				}
			})
		}
	}
}

// expected_kinds states the kind as a frame does.
func TestTheRequestCanStateTheHandlesKind(t *testing.T) {
	t.Parallel()
	expect := func(r *contextfabric.InvestigationRequest) {
		r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contextfabric.SubjectPullRequest}
	}
	for frameName, frame := range map[string]*contextfabric.QuestionFrame{"no frame": nil, "no kind": namedScopeAnchorFrame(nil)} {
		t.Run(frameName, func(t *testing.T) {
			t.Parallel()
			turn := runBeside(t, besideWorld{principal: userprincipal.Authenticated(t, "org_1", nil), question: besideQuestion("pull request 999"), frame: frame, census: &besideCensus{rows: besideRows()}, edit: expect})
			if len(turn.resolution.Committed) != 0 || turn.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
				t.Fatalf("committed %v limitation %q", turn.resolution.Committed, turn.resolution.CensusLimitation)
			}
		})
	}
	other := func(r *contextfabric.InvestigationRequest) {
		r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectPullRequest}
	}
	turn := runBeside(t, besideWorld{principal: userprincipal.Authenticated(t, "org_1", nil), question: besideQuestion("pull request 999"), census: &besideCensus{rows: besideRows()}, edit: other})
	if got := besideCommittedIDs(turn.resolution); !slices.Equal(got, []string{scopeAnchorRepoID}) {
		t.Fatalf("two expected kinds state neither: committed = %v", got)
	}
}

// A census that proved nothing says nothing. The stated turn still does not answer
// about the repository (the rule at commitNamedHandle), and the unstated turn keeps it.
func TestACensusThatProvedNothingSaysNothing(t *testing.T) {
	t.Parallel()
	failing := func() *besideCensus { return &besideCensus{err: errors.New("census unavailable")} }
	unclosed := func() *besideCensus {
		return &besideCensus{shape: func(o *CensusOutcome) { o.ClosureMismatch = true }}
	}
	unclosedSet := func() *besideCensus {
		return &besideCensus{shape: func(o *CensusOutcome) { o.SatisfierSetClosureMismatch = true }}
	}
	restricted := restrictedCaller(t, scopeAnchorRepo, besideOtherRepo)
	for name, census := range map[string]func() *besideCensus{"an error": failing, "a closure mismatch": unclosed, "a set mismatch": unclosedSet} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stated := runBeside(t, besideWorld{principal: restricted, question: besideQuestion("pull request 999"), frame: statedFrame(contextfabric.SubjectPullRequest), census: census()})
			if len(stated.resolution.Committed) != 0 || stated.resolution.CensusLimitation != "" {
				t.Fatalf("stated: committed %v limitation %q", stated.resolution.Committed, stated.resolution.CensusLimitation)
			}
			if !slices.ContainsFunc(stated.offers.HandleOptions, func(option contractsv1.ContextFabricHandleOption) bool { return option.Value == "999" }) {
				t.Fatalf("stated: options = %+v, want the handle kept: nothing proved it absent", stated.offers.HandleOptions)
			}
			unstated := runBeside(t, besideWorld{principal: restricted, question: besideQuestion("pull request 999"), frame: namedScopeAnchorFrame(nil), census: census()})
			if got := besideCommittedIDs(unstated.resolution); !slices.Equal(got, []string{scopeAnchorRepoID}) || unstated.resolution.CensusLimitation != "" {
				t.Fatalf("unstated: committed %v limitation %q", got, unstated.resolution.CensusLimitation)
			}
		})
	}
}

// The explicit path takes the same sentence: MANY was false on a 0, and its option
// could only veto.
func TestAnExplicitHandleAbsentFromTheRepositoryStatesTheScope(t *testing.T) {
	t.Parallel()
	explicit := func(value string) func(*contextfabric.InvestigationRequest) {
		return func(r *contextfabric.InvestigationRequest) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: value}}
		}
	}
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			turn := runBeside(t, besideWorld{principal: principal, question: besideQuestion("pull request 999"), census: &besideCensus{rows: besideRows()}, edit: explicit("999")})
			if len(turn.resolution.Committed) != 0 || turn.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
				t.Fatalf("committed %v limitation %q", turn.resolution.Committed, turn.resolution.CensusLimitation)
			}
			noHandleOption(t, listingTurn{offers: turn.offers}, "999")
			// The handle the caller sent need not be the one the question names.
			other := runBeside(t, besideWorld{principal: principal, question: "What is the state of that change in " + scopeAnchorRepo + "?", census: &besideCensus{rows: besideRows()}, edit: explicit("999")})
			if len(other.resolution.Committed) != 0 || other.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
				t.Fatalf("question binds no handle: committed %v limitation %q", other.resolution.Committed, other.resolution.CensusLimitation)
			}
			noHandleOption(t, listingTurn{offers: other.offers}, "999")
		})
	}
}

// Two repositories give no anchor. A stated turn asks which, and an unstated one
// keeps both.
func TestTwoRepositoriesAreAskedAboutOnlyWhenTheKindIsStated(t *testing.T) {
	t.Parallel()
	deps := (&fakeGraphBackend{}).deps()
	census := &besideCensus{rows: besideRows()}
	deps.CensusFunc = census.census
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		return "git_pull_requests.number", ValidateHandleGrammar(kind, patternID, value)
	}
	repos := []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID, Label: scopeAnchorRepo}, {Kind: contextfabric.SubjectRepository, CanonicalID: besideOtherRepoID, Label: besideOtherRepo}}
	request := testRequest()
	request.Question = "What is the state of pull request 747 in either repository?"
	principal := storage.Principal{OrgID: "org_1"}
	stated, _ := commitNamedHandle(context.Background(), principal, request, statedFrame(contextfabric.SubjectPullRequest), deps, contextfabric.SubjectResolution{Committed: slices.Clone(repos)}, contextfabric.CommitBasisSet{}, contextfabric.CommitDecisionDigestSet{})
	if len(stated.Committed) != 0 || !strings.Contains(stated.ClarificationPrompt, "Which repository holds") {
		t.Fatalf("stated: committed %v prompt %q", stated.Committed, stated.ClarificationPrompt)
	}
	unstated, _ := commitNamedHandle(context.Background(), principal, request, namedScopeAnchorFrame(nil), deps, contextfabric.SubjectResolution{Committed: slices.Clone(repos)}, contextfabric.CommitBasisSet{}, contextfabric.CommitDecisionDigestSet{})
	if !slices.Equal(unstated.Committed, repos) || census.calls != 0 {
		t.Fatalf("unstated: committed %v census calls %d", unstated.Committed, census.calls)
	}
}
