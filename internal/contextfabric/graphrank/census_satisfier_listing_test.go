package graphrank

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12467: a census that counted satisfiers lists them, on the real resolver
// and a real principal shape. The search in these tests finds nothing, so the
// census is the only way in, as on the live rows of the ticket.

const listingQuestion = "What is the state of pull request 12?"

func listingRepo(i int) string { return fmt.Sprintf("acme/r%02d", i) }

func listingID(i int) string { return fmt.Sprintf("pull_request:repo-%02d:12", i) }

// listingRows is one satisfier of pull request 12 in each of n repositories.
func listingRows(n int) []grantRow {
	rows := make([]grantRow, 0, n)
	for i := range n {
		rows = append(rows, grantRow{contextfabric.SubjectPullRequest, "12", listingID(i), listingRepo(i)})
	}
	return rows
}

// listingNode is a pull request node as projected: no team of its own, which the
// projection writes as "*".
func listingNode(i int) CandidateNode {
	node := candidateNode(contextfabric.SubjectPullRequest, listingID(i), "Refactor the widget cache", 0.5, []string{listingRepo(i)})
	node.Attributes[authorizationTeamsAttr] = "*"
	return node
}

func listingScopes(n int) []string {
	scopes := make([]string, 0, n)
	for i := range n {
		scopes = append(scopes, listingRepo(i))
	}
	return scopes
}

type listingWorld struct {
	principal storage.Principal
	census    CensusFunc
	nodes     []CandidateNode
	pool      []CandidateNode
	truncated bool
	edit      func(*contextfabric.InvestigationRequest, *ResolveDeps)
	question  string
	terms     []string
}

type listingTurn struct {
	resolution contextfabric.SubjectResolution
	offers     contextfabric.StructureOfferMaterial
	tracer     *captureResolutionTracer
	reads      *[]string
}

func runListing(t *testing.T, w listingWorld) listingTurn {
	t.Helper()
	hints := map[string]CandidateNode{}
	for _, node := range w.nodes {
		id, _ := node.Attributes["canonical_id"].(string)
		kind, _ := node.Attributes["subject_kind"].(string)
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: id})] = node
	}
	terms := w.terms
	if terms == nil {
		terms = []string{"pull request 12"}
	}
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{terms[0]: w.pool}, searchTruncated: w.truncated, exactHints: hints}
	deps := backend.deps()
	reads := &[]string{}
	exact := deps.ExactHint
	deps.ExactHint = func(ctx context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
		*reads = append(*reads, subject.CanonicalID)
		return exact(ctx, subject)
	}
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = w.census
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		if !ValidateHandleGrammar(kind, patternID, value) {
			return "", false
		}
		return HandleSourceColumn(kind, patternID)
	}
	request := testRequest()
	request.Question = listingQuestion
	if w.question != "" {
		request.Question = w.question
	}
	if w.edit != nil {
		w.edit(&request, &deps)
	}
	resolution, offers, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), w.principal, request, testInterpreted(terms...), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis: %v", err)
	}
	return listingTurn{resolution, offers, tracer, reads}
}

func candidateIDs(candidates []contextfabric.SubjectCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.Subject.CanonicalID)
	}
	return ids
}

func listingNodes(n int) []CandidateNode {
	nodes := make([]CandidateNode, 0, n)
	for i := range n {
		nodes = append(nodes, listingNode(i))
	}
	return nodes
}

func listingCallers(t *testing.T, n int) map[string]storage.Principal {
	t.Helper()
	return map[string]storage.Principal{
		"restricted":   restrictedCaller(t, listingScopes(n)...),
		"unrestricted": userprincipal.Authenticated(t, "org_1", nil),
		"universal":    userprincipal.Authenticated(t, "org_1", []string{"*"}),
	}
}

func TestSeveralSatisfiersAreListedOnAnEmptyPool(t *testing.T) {
	t.Parallel()
	for name, principal := range listingCallers(t, 10) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: listingRows(10)}
			turn := runListing(t, listingWorld{principal: principal, census: spy.census, nodes: listingNodes(10)})
			want := make([]string, 0, 10)
			for i := range 10 {
				want = append(want, listingID(i))
			}
			if got := candidateIDs(turn.resolution.Candidates); !slices.Equal(got, want) {
				t.Fatalf("candidates = %v, want the 10 satisfiers in id order", got)
			}
			for _, candidate := range turn.resolution.Candidates {
				if candidate.State != contextfabric.ResolutionAmbiguous {
					t.Fatalf("candidate %+v is not ambiguous", candidate)
				}
			}
			if len(turn.resolution.Committed) != 0 || turn.resolution.CensusLimitation != "" {
				t.Fatalf("committed %v limitation %q, want neither", turn.resolution.Committed, turn.resolution.CensusLimitation)
			}
			if !strings.Contains(turn.resolution.ClarificationPrompt, "acme/r00") {
				t.Fatalf("prompt = %q", turn.resolution.ClarificationPrompt)
			}
			if len(*turn.reads) != 10 {
				t.Fatalf("keyed reads = %d, want one for each listed id", len(*turn.reads))
			}
			if len(turn.offers.CandidateOptions) == 0 {
				t.Fatal("no candidate option was offered for a listed satisfier")
			}
		})
	}
}

// The set arrives in no order the statement promises. The list is sorted by
// canonical id, so one census gives one answer.
func TestTheListIsInCanonicalIDOrder(t *testing.T) {
	t.Parallel()
	rows := listingRows(4)
	slices.Reverse(rows)
	spy := &grantCensusSpy{rows: rows}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(4)...), census: spy.census, nodes: listingNodes(4)})
	got := candidateIDs(turn.resolution.Candidates)
	if !slices.IsSorted(got) || len(got) != 4 {
		t.Fatalf("candidates = %v, want 4 sorted by canonical id", got)
	}
}

// A noisy pool cannot fill the budget ahead of what the census proved.
func TestSatisfiersGoAheadOfThePool(t *testing.T) {
	t.Parallel()
	noise := []CandidateNode{
		candidateNode(contextfabric.SubjectRepository, "repository:noise", "noise one", 0.8, []string{listingRepo(0)}),
		candidateNode(contextfabric.SubjectRepository, "repository:noise-2", "noise two", 0.8, []string{listingRepo(0)}),
		candidateNode(contextfabric.SubjectRepository, "repository:noise-3", "noise three", 0.8, []string{listingRepo(0)}),
	}
	spy := &grantCensusSpy{rows: listingRows(3)}
	turn := runListing(t, listingWorld{
		principal: restrictedCaller(t, listingScopes(3)...), census: spy.census, nodes: listingNodes(3), pool: noise,
		edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) { r.Options.MaxSubjectCandidates = 4 },
	})
	got := candidateIDs(turn.resolution.Candidates)
	if len(got) != 4 || !slices.Equal(got[:3], []string{listingID(0), listingID(1), listingID(2)}) {
		t.Fatalf("candidates = %v, want the 3 satisfiers first, cut at the budget of 4", got)
	}
}

// Listing cannot commit: a node at full relevance, with the exact term as its
// label, stays a candidate.
func TestAListingNeverCommits(t *testing.T) {
	t.Parallel()
	nodes := []CandidateNode{
		candidateNode(contextfabric.SubjectPullRequest, listingID(0), "pull request 12", 1, []string{listingRepo(0)}),
		candidateNode(contextfabric.SubjectPullRequest, listingID(1), "something else", 0.1, []string{listingRepo(1)}),
	}
	spy := &grantCensusSpy{rows: listingRows(2)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(2)...), census: spy.census, nodes: nodes})
	if len(turn.resolution.Committed) != 0 || len(turn.resolution.Candidates) != 2 {
		t.Fatalf("committed %v candidates %v, want 2 candidates and no commit", turn.resolution.Committed, candidateIDs(turn.resolution.Candidates))
	}
}

func TestAListingIsCutAtTheBoundAndSaysSo(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ satisfiers, budget, want int }{
		"the request's budget":   {10, 3, 3},
		"the bound on the reads": {30, 50, 25},
		"past the bound by one":  {26, 50, 25},
		"exactly the budget":     {3, 3, 3},
		"a budget of two":        {5, 2, 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: listingRows(tc.satisfiers)}
			turn := runListing(t, listingWorld{
				principal: restrictedCaller(t, listingScopes(tc.satisfiers)...), census: spy.census, nodes: listingNodes(tc.satisfiers),
				edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
					r.Options.MaxSubjectCandidates = tc.budget
				},
			})
			if len(turn.resolution.Candidates) != tc.want || len(*turn.reads) != tc.want {
				t.Fatalf("candidates %d reads %d, want %d of each", len(turn.resolution.Candidates), len(*turn.reads), tc.want)
			}
			want := contractsv1.ContextFabricCensusListedLimitation(tc.satisfiers, tc.want)
			if turn.resolution.CensusLimitation != want {
				t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
			}
		})
	}
}

// A budget of one cannot show a choice, so it lists nothing and says the count.
func TestABudgetBelowTwoListsNothing(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(4)}
	turn := runListing(t, listingWorld{
		principal: restrictedCaller(t, listingScopes(4)...), census: spy.census, nodes: listingNodes(4),
		edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) { r.Options.MaxSubjectCandidates = 1 },
	})
	if len(turn.resolution.Candidates) != 0 || len(*turn.reads) != 0 {
		t.Fatalf("candidates %v reads %v, want none", candidateIDs(turn.resolution.Candidates), *turn.reads)
	}
	if want := contractsv1.ContextFabricCensusUnlistedLimitation(4); turn.resolution.CensusLimitation != want {
		t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
	}
}

func countOnlyCensus(outcome CensusOutcome) CensusFunc {
	return func(ctx context.Context, _ string, _ CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
		_, bound := contextfabric.CensusGrantFrom(ctx)
		outcome.GrantApplied = bound
		return outcome, nil
	}
}

// Over the budget no id is held: no read, and the count is stated.
func TestOverTheBudgetSaysTheCount(t *testing.T) {
	t.Parallel()
	for name, principal := range listingCallers(t, 2) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			turn := runListing(t, listingWorld{principal: principal, census: countOnlyCensus(CensusOutcome{Count: 1001})})
			if len(turn.resolution.Candidates) != 0 || len(*turn.reads) != 0 {
				t.Fatalf("candidates %v reads %v, want none", candidateIDs(turn.resolution.Candidates), *turn.reads)
			}
			if want := contractsv1.ContextFabricCensusUnlistedLimitation(1001); turn.resolution.CensusLimitation != want {
				t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
			}
		})
	}
}

// A set that did not close is not trusted. The count is stated and no id is read.
func TestAClosureMismatchListsNothing(t *testing.T) {
	t.Parallel()
	ids := []string{listingID(0), listingID(1)}
	for name, outcome := range map[string]CensusOutcome{
		"the set":     {Count: 2, SatisfierCanonicalIDs: ids, SatisfierSetClosureMismatch: true},
		"the witness": {Count: 1, SatisfierCanonicalID: ids[0], ClosureMismatch: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(2)...), census: countOnlyCensus(outcome), nodes: listingNodes(2)})
			if len(turn.resolution.Candidates) != 0 || len(*turn.reads) != 0 {
				t.Fatalf("candidates %v reads %v, want none", candidateIDs(turn.resolution.Candidates), *turn.reads)
			}
			if want := contractsv1.ContextFabricCensusUnlistedLimitation(outcome.Count); turn.resolution.CensusLimitation != want {
				t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
			}
		})
	}
}

// Every id missing from the graph, or refused by the gate: the census still
// counted them. The reads stay inside the bound.
func TestEveryRefusedIdSaysTheCount(t *testing.T) {
	t.Parallel()
	refused := make([]CandidateNode, 0, 30)
	for i := range 30 {
		refused = append(refused, candidateNode(contextfabric.SubjectPullRequest, listingID(i), "elsewhere", 0.5, []string{"other/secret"}))
	}
	for name, nodes := range map[string][]CandidateNode{"missing from the graph": nil, "refused by the gate": refused} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: listingRows(30)}
			turn := runListing(t, listingWorld{
				principal: restrictedCaller(t, listingScopes(30)...), census: spy.census, nodes: nodes,
				edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) { r.Options.MaxSubjectCandidates = 50 },
			})
			if len(turn.resolution.Candidates) != 0 {
				t.Fatalf("candidates = %v", candidateIDs(turn.resolution.Candidates))
			}
			if len(*turn.reads) != censusNarrowingMaxSatisfiers {
				t.Fatalf("keyed reads = %d, want the bound", len(*turn.reads))
			}
			if want := contractsv1.ContextFabricCensusUnlistedLimitation(30); turn.resolution.CensusLimitation != want {
				t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
			}
		})
	}
}

// A lone satisfier the graph does not hold: the census counted it. A work item
// keeps the sentence it already has.
func TestALoneRefusedSatisfierSaysTheCount(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(1)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(1)...), census: spy.census})
	if want := contractsv1.ContextFabricCensusUnlistedLimitation(1); turn.resolution.CensusLimitation != want {
		t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
	}
	id := "work_item.v2:00000000-0000-0000-0000-000000000000:linear:CHAOS-70"
	spy = &grantCensusSpy{rows: []grantRow{{contextfabric.SubjectWorkItem, "CHAOS-70", id, grantRepoInside}}}
	turn = runListing(t, listingWorld{principal: restrictedCaller(t, grantRepoInside), census: spy.census, question: "Why is CHAOS-70 blocked?", terms: []string{"CHAOS-70"}})
	if turn.resolution.CensusLimitation != "" {
		t.Fatalf("limitation = %q beside the work item's own sentence", turn.resolution.CensusLimitation)
	}
	if turn.resolution.WorkItemLinkLimitation != contractsv1.ContextFabricWorkItemLinksUnreadLimitation {
		t.Fatalf("work item limitation = %q", turn.resolution.WorkItemLinkLimitation)
	}
}

// A lone satisfier beside a hypothesis the census cannot speak to is not
// committed. It is listed all the same.
func TestALoneSatisfierBesideASurvivorIsListed(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(1)}
	survivor := candidateNode(contextfabric.SubjectRepository, "repository:noise", "noise", 0.5, []string{listingRepo(0)})
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(1)...), census: spy.census, nodes: listingNodes(1), pool: []CandidateNode{survivor}})
	if len(turn.resolution.Committed) == 0 && !slices.Contains(candidateIDs(turn.resolution.Candidates), listingID(0)) {
		t.Fatalf("candidates = %v, want the satisfier in the list", candidateIDs(turn.resolution.Candidates))
	}
}

func teamDecision(in map[string]bool, out map[string]bool, err error) TeamNarrowingFunc {
	return func(context.Context, []contextfabric.SubjectRef, bool) (TeamNarrowingDecision, error) {
		return TeamNarrowingDecision{In: in, Out: out}, err
	}
}

func teamEdit(narrow TeamNarrowingFunc) func(*contextfabric.InvestigationRequest, *ResolveDeps) {
	return func(r *contextfabric.InvestigationRequest, deps *ResolveDeps) {
		r.RequestedScope.TeamIDs = []string{"team-t"}
		deps.TeamNarrowing = narrow
	}
}

// narrowed_to_many: the list is the narrowing's, for both callers.
func TestTheNarrowingKeepsItsOwnList(t *testing.T) {
	t.Parallel()
	in, out := map[string]bool{}, map[string]bool{}
	for i := range 10 {
		if i < 3 {
			in[listingID(i)] = true
		} else {
			out[listingID(i)] = true
		}
	}
	for name, principal := range map[string]storage.Principal{"restricted": restrictedCaller(t, listingScopes(10)...), "unrestricted": userprincipal.Authenticated(t, "org_1", nil)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: listingRows(10)}
			turn := runListing(t, listingWorld{principal: principal, census: spy.census, nodes: listingNodes(10), edit: teamEdit(teamDecision(in, out, nil))})
			if got := candidateIDs(turn.resolution.Candidates); !slices.Equal(got, []string{listingID(0), listingID(1), listingID(2)}) {
				t.Fatalf("candidates = %v, want the 3 inside the team", got)
			}
			if turn.resolution.CensusLimitation != "" {
				t.Fatalf("limitation = %q: all that the narrowing kept is listed", turn.resolution.CensusLimitation)
			}
		})
	}
}

// Anchor B: with no in-team offer, today's offers stand, so the conversation
// never dead-ends. The satisfiers are what the question names.
func TestNoInTeamSatisfierListsAll(t *testing.T) {
	t.Parallel()
	out := map[string]bool{}
	for i := range 4 {
		out[listingID(i)] = true
	}
	spy := &grantCensusSpy{rows: listingRows(4)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(4)...), census: spy.census, nodes: listingNodes(4), edit: teamEdit(teamDecision(nil, out, nil))})
	if len(turn.resolution.Candidates) != 4 || turn.resolution.CensusLimitation != "" {
		t.Fatalf("candidates %v limitation %q, want the 4 satisfiers", candidateIDs(turn.resolution.Candidates), turn.resolution.CensusLimitation)
	}
}

// Ruling 3: a narrowing the caller asked for is never dropped in silence. When it
// cannot tell which satisfiers it keeps, nothing is listed and the count is said.
func TestAnUndecidedNarrowingListsNothing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		satisfiers int
		narrow     TeamNarrowingFunc
	}{
		"satisfier_read_failed": {4, teamDecision(nil, nil, errors.New("read failed"))},
		"census_truncated":      {30, teamDecision(nil, nil, nil)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: listingRows(tc.satisfiers)}
			turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(tc.satisfiers)...), census: spy.census, nodes: listingNodes(tc.satisfiers), edit: teamEdit(tc.narrow)})
			if len(turn.resolution.Candidates) != 0 || len(*turn.reads) != 0 {
				t.Fatalf("candidates %v reads %v, want none", candidateIDs(turn.resolution.Candidates), *turn.reads)
			}
			if want := contractsv1.ContextFabricCensusUnlistedLimitation(tc.satisfiers); turn.resolution.CensusLimitation != want {
				t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
			}
		})
	}
}

// A repository narrowing that refuses every satisfier keeps its own sentence: the
// count of a census read inside the request is zero, and nothing is listed.
func TestARepositoryNarrowingThatRefusesEverySatisfierStaysAsItIs(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(4)}
	turn := runListing(t, listingWorld{
		principal: restrictedCaller(t, listingScopes(5)...), census: spy.census, nodes: listingNodes(4),
		edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{listingRepo(4)}
		},
	})
	if len(turn.resolution.Candidates) != 0 || turn.resolution.CensusLimitation != "" {
		t.Fatalf("candidates %v limitation %q, want neither", candidateIDs(turn.resolution.Candidates), turn.resolution.CensusLimitation)
	}
}

// The label is the server's. A node labelled with its own canonical id shows no
// raw id, no synthetic marker, and no id suffix in the prompt.
func TestACensusLabelNeverShowsARawID(t *testing.T) {
	t.Parallel()
	nodes := make([]CandidateNode, 0, 3)
	for i := range 3 {
		nodes = append(nodes, candidateNode(contextfabric.SubjectPullRequest, listingID(i), listingID(i), 0.5, []string{listingRepo(i)}))
	}
	spy := &grantCensusSpy{rows: listingRows(3)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(3)...), census: spy.census, nodes: nodes})
	if len(turn.resolution.Candidates) != 3 {
		t.Fatalf("candidates = %v", candidateIDs(turn.resolution.Candidates))
	}
	shown := turn.resolution.ClarificationPrompt
	for _, candidate := range turn.resolution.Candidates {
		shown += " " + candidate.Subject.Label + " " + strings.Join(candidate.MatchedTerms, " ")
		if !slices.Contains(candidate.MatchedTerms, "pull request 12") {
			t.Fatalf("matched terms = %v, want the handle as the question wrote it", candidate.MatchedTerms)
		}
	}
	for _, option := range turn.offers.CandidateOptions {
		shown += " " + option.Label
	}
	for _, raw := range []string{"pull_request:", "repo-0", censusProvenanceMarker, "%3A"} {
		if strings.Contains(shown, raw) {
			t.Fatalf("%q is shown: %s", raw, shown)
		}
	}
	if want := "pull request #12 in acme/r00"; turn.resolution.Candidates[0].Subject.Label != want {
		t.Fatalf("label = %q, want %q", turn.resolution.Candidates[0].Subject.Label, want)
	}
}

func TestACensusLabelKeepsARealTitle(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(2)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(2)...), census: spy.census, nodes: listingNodes(2)})
	if want := "pull request #12 in acme/r00: Refactor the widget cache"; turn.resolution.Candidates[0].Subject.Label != want {
		t.Fatalf("label = %q, want %q", turn.resolution.Candidates[0].Subject.Label, want)
	}
}

// A node can list a slug beside the one that admits it. The label names only
// what the caller holds.
func TestALabelNamesOnlyAGrantedRepository(t *testing.T) {
	t.Parallel()
	nodes := []CandidateNode{
		candidateNode(contextfabric.SubjectPullRequest, listingID(0), "Title", 0.5, []string{"other/secret", listingRepo(0)}),
		listingNode(1),
	}
	spy := &grantCensusSpy{rows: listingRows(2)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(2)...), census: spy.census, nodes: nodes})
	if len(turn.resolution.Candidates) != 2 {
		t.Fatalf("candidates = %v", candidateIDs(turn.resolution.Candidates))
	}
	if strings.Contains(turn.resolution.Candidates[0].Subject.Label+turn.resolution.ClarificationPrompt, "other/secret") {
		t.Fatalf("label %q prompt %q name a repository outside the grant", turn.resolution.Candidates[0].Subject.Label, turn.resolution.ClarificationPrompt)
	}
}

// One key in two trackers, one title: the provider tells them apart, so the
// prompt never falls to its id suffix cue.
func TestTwoWorkItemsOfOneKeyAreToldApart(t *testing.T) {
	t.Parallel()
	ids := make([]string, 0, 2)
	nodes := make([]CandidateNode, 0, 2)
	for _, provider := range []string{"linear", "jira"} {
		id, omitted, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", provider + ":CHAOS-70"}, nil)
		if err != nil || omitted {
			t.Fatalf("derive: omitted=%t err=%v", omitted, err)
		}
		ids = append(ids, id)
		nodes = append(nodes, repoLessItem(id, "Split the job", grantRepoInside))
	}
	spy := &grantCensusSpy{rows: []grantRow{{contextfabric.SubjectWorkItem, "CHAOS-70", ids[0], grantRepoInside}, {contextfabric.SubjectWorkItem, "CHAOS-70", ids[1], grantRepoInside}}}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, grantRepoInside), census: spy.census, nodes: nodes, question: "Why is CHAOS-70 blocked?", terms: []string{"CHAOS-70"}})
	if len(turn.resolution.Candidates) != 2 {
		t.Fatalf("candidates = %v", candidateIDs(turn.resolution.Candidates))
	}
	first, second := turn.resolution.Candidates[0].Subject.Label, turn.resolution.Candidates[1].Subject.Label
	if first == second || strings.Contains(turn.resolution.ClarificationPrompt, "%3A") {
		t.Fatalf("labels %q %q prompt %q", first, second, turn.resolution.ClarificationPrompt)
	}
	if !strings.Contains(first+second, "(linear)") || !strings.Contains(first+second, "(jira)") {
		t.Fatalf("labels %q %q do not name the providers", first, second)
	}
}

// Project-only stays as it is: no census, no read, no option, no sentence.
func TestAProjectOnlyCallerStaysAsItIs(t *testing.T) {
	t.Parallel()
	census := func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		t.Error("a project-only caller reached the census")
		return CensusOutcome{}, nil
	}
	turn := runListing(t, listingWorld{principal: userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"}), census: census, nodes: listingNodes(2)})
	if len(turn.resolution.Candidates) != 0 || turn.resolution.CensusLimitation != "" || len(*turn.reads) != 0 || len(turn.offers.HandleOptions) != 0 {
		t.Fatalf("resolution %+v offers %+v reads %v, want nothing", turn.resolution, turn.offers, *turn.reads)
	}
	if rounds := turn.tracer.eventsForStage("evidence_round"); len(rounds) != 0 {
		t.Fatalf("evidence_round = %+v on a search that was not truncated, want no round", rounds)
	}
}

// Ruling 1: any caller but project-only, with one bound handle and nothing
// committed, runs the round, whatever the search says.
func TestUnrestrictedRunsTheRoundOnABoundHandle(t *testing.T) {
	t.Parallel()
	for name, principal := range map[string]storage.Principal{"no scopes": userprincipal.Authenticated(t, "org_1", nil), "universal": userprincipal.Authenticated(t, "org_1", []string{"*"})} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: listingRows(1)}
			turn := runListing(t, listingWorld{principal: principal, census: spy.census, nodes: listingNodes(1)})
			if len(turn.resolution.Committed) != 1 || turn.resolution.Committed[0].CanonicalID != listingID(0) {
				t.Fatalf("committed = %v, want the lone satisfier", turn.resolution.Committed)
			}
		})
	}
	// Two handles are a multi-subject question: an unrestricted caller gets no
	// round for it on a search that was not truncated, as before.
	spy := &grantCensusSpy{rows: listingRows(1)}
	turn := runListing(t, listingWorld{principal: userprincipal.Authenticated(t, "org_1", nil), census: spy.census, nodes: listingNodes(1), question: "Compare pull request 12 and pull request 13"})
	if rounds := turn.tracer.eventsForStage("evidence_round"); len(rounds) != 0 {
		t.Fatalf("evidence_round = %+v, want no round on two handles", rounds)
	}
}

// Ruling 1, the line that stands: for an unrestricted principal, a satisfier
// outside the request must not decide. The round now runs on a search that was
// not truncated, and repos= still decides.
func TestARequestedRepositoryStillDecidesForAnUnrestrictedCaller(t *testing.T) {
	t.Parallel()
	unrestricted := userprincipal.Authenticated(t, "org_1", nil)
	narrow := func(slug string) func(*contextfabric.InvestigationRequest, *ResolveDeps) {
		return func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{slug}
		}
	}
	// The only satisfier is outside the request: it neither commits nor lists.
	spy := &grantCensusSpy{rows: listingRows(1)}
	turn := runListing(t, listingWorld{principal: unrestricted, census: spy.census, nodes: listingNodes(1), edit: narrow("acme/elsewhere")})
	if len(turn.resolution.Committed) != 0 || len(turn.resolution.Candidates) != 0 || turn.resolution.CensusLimitation != "" {
		t.Fatalf("committed %v candidates %v limitation %q, want none: the request decides", turn.resolution.Committed, candidateIDs(turn.resolution.Candidates), turn.resolution.CensusLimitation)
	}
	// Two satisfiers, one inside the request: that one commits.
	spy = &grantCensusSpy{rows: listingRows(2)}
	turn = runListing(t, listingWorld{principal: unrestricted, census: spy.census, nodes: listingNodes(2), edit: narrow(listingRepo(1))})
	if len(turn.resolution.Committed) != 1 || turn.resolution.Committed[0].CanonicalID != listingID(1) {
		t.Fatalf("committed = %v, want the satisfier inside the request", turn.resolution.Committed)
	}
}

// A handle that several subjects satisfy cannot be redeemed into one of them, so
// it is not offered. A candidate is.
func TestAHandleOptionThatCannotDecideIsDropped(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(3)}
	turn := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(3)...), census: spy.census, nodes: listingNodes(3)})
	for _, option := range turn.offers.HandleOptions {
		if option.Kind == contextfabric.SubjectPullRequest && option.Value == "12" {
			t.Fatalf("handle options = %+v, want the undeciding handle dropped", turn.offers.HandleOptions)
		}
	}
	over := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(3)...), census: countOnlyCensus(CensusOutcome{Count: 1001})})
	if len(over.offers.HandleOptions) != 0 {
		t.Fatalf("over the budget: handle options = %+v", over.offers.HandleOptions)
	}
	// With nothing counted the handle may still name a real subject.
	none := runListing(t, listingWorld{principal: restrictedCaller(t, listingScopes(3)...), census: (&grantCensusSpy{}).census})
	if len(none.offers.HandleOptions) != 1 {
		t.Fatalf("count 0: handle options = %+v, want it kept", none.offers.HandleOptions)
	}
}

// One line per round, with counts only.
func TestTheListingLineCarriesCountsAndNoID(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: listingRows(10)}
	turn := runListing(t, listingWorld{
		principal: restrictedCaller(t, listingScopes(10)...), census: spy.census, nodes: listingNodes(10),
		edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) { r.Options.MaxSubjectCandidates = 4 },
	})
	lines := turn.tracer.eventsForStage("evidence_census_listing")
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want one", lines)
	}
	line := lines[0]
	if line.CensusCount != 10 || line.CensusListed != 4 || line.CensusListingReads != 4 || line.CensusListingOutcome != "partly_listed" || line.CensusListingReason != "" {
		t.Fatalf("line = %+v", line)
	}
	if strings.Contains(fmt.Sprintf("%+v", line), "repo-0") {
		t.Fatalf("line names an id: %+v", line)
	}
}

// logBuffer is a slog sink the race detector accepts: a turn's reads may write
// from more than one goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.buf.Bytes())
}

// The real producer into a real JSON handler, against the spec, for a line of
// each outcome. No id on any line.
func TestTheListingLineCertifiesFromTheRealProducer(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		census          CensusFunc
		nodes           []CandidateNode
		edit            func(*contextfabric.InvestigationRequest, *ResolveDeps)
		outcome, reason string
		count, listed   int
		reads, refused  int
	}{
		"listed": {(&grantCensusSpy{rows: listingRows(3)}).census, listingNodes(3), nil, "listed", "", 3, 3, 3, 0},
		"partly listed": {(&grantCensusSpy{rows: listingRows(3)}).census, listingNodes(3), func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.Options.MaxSubjectCandidates = 2
		}, "partly_listed", "", 3, 2, 2, 0},
		"over the budget": {countOnlyCensus(CensusOutcome{Count: 1001}), nil, nil, "unlisted", "over_budget", 1001, 0, 0, 0},
		"refused":         {(&grantCensusSpy{rows: listingRows(2)}).census, nil, nil, "unlisted", "refused", 2, 0, 2, 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var sink logBuffer
			turn := runListing(t, listingWorld{
				principal: restrictedCaller(t, listingScopes(3)...), census: tc.census, nodes: tc.nodes,
				edit: func(r *contextfabric.InvestigationRequest, deps *ResolveDeps) {
					if tc.edit != nil {
						tc.edit(r, deps)
					}
					deps.ResolutionTracer = NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelInfo})))
				},
			})
			_ = turn
			log, err := certify.Parse(sink.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse() error = %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.EvidenceCensusListing, Want: map[string]any{
				"request_id": "request_12345678", "stage": "evidence_census_listing", "census_kind": "pull_request", "census_count": tc.count,
				"census_listed": tc.listed, "census_listing_reads": tc.reads, "census_listing_refused": tc.refused,
				"outcome": tc.outcome, "census_listing_reason": tc.reason,
			}}); err != nil {
				t.Fatalf("certify.Certify() error = %v", err)
			}
			if strings.Contains(string(sink.Bytes()), `census_count`) && strings.Contains(listingLine(sink.Bytes()), "repo-0") {
				t.Fatalf("the line names an id: %s", listingLine(sink.Bytes()))
			}
		})
	}
}

func listingLine(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"stage":"evidence_census_listing"`) {
			return line
		}
	}
	return ""
}

func TestTheListingVocabulariesAreTheProducers(t *testing.T) {
	t.Parallel()
	for _, field := range eventspec.EvidenceCensusListing.Fields {
		switch field.Key {
		case "outcome":
			if !slices.Equal(field.ClosedVocabulary, CensusListingOutcomeVocabulary()) {
				t.Fatalf("spec %v, producer %v", field.ClosedVocabulary, CensusListingOutcomeVocabulary())
			}
		case "census_listing_reason":
			if !slices.Equal(field.ClosedVocabulary, CensusListingReasonVocabulary()) {
				t.Fatalf("spec %v, producer %v", field.ClosedVocabulary, CensusListingReasonVocabulary())
			}
		}
	}
}

// An anchor-only census counts a population, not a named subject. A kind the
// handle was not applied to is never listed, whatever it counted.
func TestAnAnchorOnlyCensusIsNotListed(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{}
	deps := backend.deps()
	reads := 0
	deps.ExactHint = func(context.Context, contextfabric.SubjectRef) (CandidateNode, bool, error) {
		reads++
		return CandidateNode{}, false, nil
	}
	request := testRequest()
	request.Question = listingQuestion
	attestation := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{{
		Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 3, AnchorApplied: true,
		SatisfierCanonicalIDs: []string{listingID(0), listingID(1), listingID(2)},
	}}}
	listed, undeciding := listCensusSatisfiers(context.Background(), restrictedCaller(t, listingRepo(0)), request, deps, attestation, contextfabric.SubjectResolution{})
	if len(listed.Candidates) != 0 || listed.CensusLimitation != "" || undeciding != nil || reads != 0 {
		t.Fatalf("listed %+v undeciding %v reads %d, want nothing", listed, undeciding, reads)
	}
}

// A round that did not read inside the caller's visibility is not trusted, even
// if it holds ids. Same belt as attestedSatisfier.
func TestAListingNeedsAVisibleRound(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{}
	deps := backend.deps()
	request := testRequest()
	request.Question = listingQuestion
	attestation := Attestation{Kinds: []KindAttestation{{
		Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 2, HandleApplied: true,
		SatisfierCanonicalIDs: []string{listingID(0), listingID(1)},
	}}}
	listed, undeciding := listCensusSatisfiers(context.Background(), restrictedCaller(t, listingRepo(0)), request, deps, attestation, contextfabric.SubjectResolution{})
	if len(listed.Candidates) != 0 || listed.CensusLimitation != "" || undeciding != nil {
		t.Fatalf("listed %+v undeciding %v, want nothing", listed, undeciding)
	}
}

// A work item a link scope reached is tested on the issue side of the link, never
// by its own repository, as a merge is.
func TestALinkScopedWorkItemIsListedThroughItsLink(t *testing.T) {
	t.Parallel()
	first, second := "work_item.v2:00000000-0000-0000-0000-000000000000:linear:CHAOS-70", "work_item.v2:00000000-0000-0000-0000-000000000000:jira:CHAOS-70"
	nodes := map[string]CandidateNode{first: repoLessItem(first, "Split the job", grantRepoInside), second: repoLessItem(second, "Split the job")}
	backend := &fakeGraphBackend{}
	deps := backend.deps()
	deps.ExactHint = func(_ context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
		node, ok := nodes[subject.CanonicalID]
		return node, ok, nil
	}
	request := testRequest()
	request.Question = "Why is CHAOS-70 blocked?"
	request.RequestedScope.RepositorySlugs = []string{grantRepoInside}
	attestation := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{{
		Kind: contextfabric.SubjectWorkItem, Complete: true, Count: 2, HandleApplied: true, RepositoryFilterApplied: true,
		SatisfierCanonicalIDs: []string{first, second},
	}}}
	listed, _ := listCensusSatisfiers(context.Background(), restrictedCaller(t, grantRepoInside), request, deps, attestation, contextfabric.SubjectResolution{})
	if got := candidateIDs(listed.Candidates); !slices.Equal(got, []string{first}) {
		t.Fatalf("candidates = %v, want the item a link joins to the scope", got)
	}
	if want := contractsv1.ContextFabricCensusListedLimitation(2, 1); listed.CensusLimitation != want {
		t.Fatalf("limitation = %q, want %q", listed.CensusLimitation, want)
	}
}
