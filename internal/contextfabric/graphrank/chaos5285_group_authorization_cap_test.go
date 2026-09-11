package graphrank

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupAuthorizationHintRequest is the grouped-cohort authorization call shape
// exactly: `count` team hints of source cohort_group_authorization, every one
// of which the backend can resolve, and the given candidate cap.
func groupAuthorizationHintRequest(count, maxSubjectCandidates int) (contextfabric.InvestigationRequest, *fakeGraphBackend) {
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{}}
	req := testRequest()
	req.Options.MaxSubjectCandidates = maxSubjectCandidates
	hints := make([]contextfabric.SubjectHint, 0, count)
	for index := 0; index < count; index++ {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: fmt.Sprintf("team:team_%03d", index), Label: fmt.Sprintf("Team %03d", index)}
		backend.exactHints[SubjectKey(subject)] = candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")
		hints = append(hints, contextfabric.SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: string(hintsource.CohortGroupAuthorization)})
	}
	req.RequestedScope.SubjectHints = hints
	return req, backend
}

// TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap is the GROUNDING for
// the engine's group-authorization batching, and it is a measurement of the
// real resolver rather than of a double.
//
// The grouped path authorizes its constructed groups by resolving them as
// canonical-id hints, and a group the resolver does not commit is counted as
// DENIED. So the one property of this resolver the authorization step depends
// on is: does it commit every resolvable hint it is handed? It does not. The
// caller-hint exit finalizes through FinalizeExactResolutionWithBasis at
// request.Options.MaxSubjectCandidates, and every hint past that cap is
// dropped exactly as an unauthorized one would be -- indistinguishable, from
// the caller's side, from "the principal may not see this group".
//
// That is what made groups 51..250 of a legal 250-group cohort read as denied
// and never reach the group fact read. The engine's double models this cap
// (groupAuthorizingGraph), and this test is what licenses that model: if the
// resolver ever stopped capping here, the double would be modelling a
// behaviour that no longer exists, and this test would say so.
//
// The control row is the cap itself: exactly MaxSubjectCandidates hints all
// commit, so the 51-hint row's shortfall is the cap and nothing else.
func TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		hints, maxCandidates, wantCommitted int
	}{
		{hints: 50, maxCandidates: 50, wantCommitted: 50}, // control: at the cap, everything commits
		{hints: 51, maxCandidates: 50, wantCommitted: 50}, // one past it: one resolvable group is lost
		{hints: 250, maxCandidates: 50, wantCommitted: 50},
	} {
		t.Run(fmt.Sprintf("hints=%d/cap=%d", row.hints, row.maxCandidates), func(t *testing.T) {
			t.Parallel()
			req, backend := groupAuthorizationHintRequest(row.hints, row.maxCandidates)
			res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
				storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("teams"),
				backend.deps(), nil, nil, nil, "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			t.Logf("hints=%d max_subject_candidates=%d committed=%d", row.hints, row.maxCandidates, len(res.Committed))
			if len(res.Committed) != row.wantCommitted {
				t.Fatalf("committed = %d, want %d -- the resolver's caller-hint cap changed, so the engine double that models it (groupAuthorizingGraph) no longer describes the resolver it stands in for",
					len(res.Committed), row.wantCommitted)
			}
		})
	}
}

// unresolvableTeamHints returns count team hints of the given source, none of
// which the backend can resolve by id.
func unresolvableTeamHints(count int, source string) []contextfabric.SubjectHint {
	hints := make([]contextfabric.SubjectHint, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("team:unresolvable_%03d", index)
		hints = append(hints, contextfabric.SubjectHint{Kind: contextfabric.SubjectTeam, ID: id, Label: id, Source: source})
	}
	return hints
}

// searchesFor runs one resolution over hints against a backend whose search
// WOULD return candidates, and reports how many search calls it made and what
// it committed.
func searchesFor(t *testing.T, hints []contextfabric.SubjectHint) (int, []contextfabric.SubjectRef) {
	t.Helper()
	found := candidateNode(contextfabric.SubjectTeam, "team:found_by_search", "Teams", 1, "*")
	backend := &fakeGraphBackend{
		exactHints:            map[string]CandidateNode{},
		searchResults:         map[string][]CandidateNode{"teams": {found}},
		enableSearchQuestion:  true,
		searchQuestionResults: map[string][]CandidateNode{},
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 50
	req.RequestedScope.SubjectHints = hints
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("teams"),
		backend.deps(), nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return len(backend.searchCalls) + len(backend.searchQuestionCalls), res.Committed
}

// TestAnIdentityQuestionThatResolvesNothingNeverSearches is the pin for the
// cost the group-authorization batching exposed, and for its sibling on the
// answer-reuse recheck.
//
// Both callers ask one question -- "may this principal see these ids" -- and
// keep only the ids they asked about. When none of the ids resolves, nothing
// reaches the caller-hint short circuit and the resolver used to fall through
// to hybrid search: on the trial venue 18-20 s per 50 group ids, embeddings
// included, to return an answer that admits nothing. With the group read
// authorizing in batches of 50, a 250-group cohort paid that four times over.
//
// The backend here WOULD answer a search (a team is findable by the term), so
// a resolver that still searched is seen doing it, and the committed set shows
// the search could not have served the question anyway: it finds an id nobody
// asked about.
func TestAnIdentityQuestionThatResolvesNothingNeverSearches(t *testing.T) {
	t.Parallel()
	for _, source := range []hintsource.Source{hintsource.CohortGroupAuthorization, hintsource.AnswerReuseAuthorizationRecheck} {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			searches, committed := searchesFor(t, unresolvableTeamHints(50, string(source)))
			t.Logf("source=%s unresolvable_hints=50 searches=%d committed=%v", source, searches, committed)
			if searches != 0 {
				t.Errorf("searches = %d, want 0 -- an identity question that resolved nothing widened into hybrid search, which cannot admit an id the keyed lookup refused and costs the turn 18-20 s per batch on the trial venue", searches)
			}
			if len(committed) != 0 {
				t.Errorf("committed = %v, want nothing -- no id this question asked about resolved", committed)
			}
		})
	}
}

// TestAQuestionASearchCanAnswerStillSearches is the CONTROL, and it passes on
// both sides: the same unresolvable ids from a caller's own hint, and from a
// set mixing one caller hint into an authorization set, still fall through to
// search. Without it, a resolver that never searched on ANY hinted request
// would satisfy the pin above.
func TestAQuestionASearchCanAnswerStillSearches(t *testing.T) {
	t.Parallel()
	callerOnly := unresolvableTeamHints(3, "workbench")
	mixed := append(unresolvableTeamHints(3, string(hintsource.CohortGroupAuthorization)), unresolvableTeamHints(1, "workbench")...)
	receipt := unresolvableTeamHints(3, string(hintsource.PriorSubjectReceipt))
	for name, hints := range map[string][]contextfabric.SubjectHint{"caller hints": callerOnly, "mixed authorization + caller": mixed, "prior receipts": receipt} {
		searches, _ := searchesFor(t, hints)
		t.Logf("%s: searches=%d", name, searches)
		if searches == 0 {
			t.Errorf("CONTROL BROKEN: %s made no search -- a question a search can answer must still reach it", name)
		}
	}
}

// TestTheNoFallbackPredicateOverItsWholeDomain drives hintsForbidSearchFallback
// over every shape a hint set can take: empty, each enumerated source alone,
// the two authorization sources together, and every mix with a source that
// permits the fallback. Only a set made ENTIRELY of no-fallback sources may
// skip the search.
func TestTheNoFallbackPredicateOverItsWholeDomain(t *testing.T) {
	t.Parallel()
	hint := func(source string) contextfabric.SubjectHint {
		return contextfabric.SubjectHint{Kind: contextfabric.SubjectTeam, ID: "team:x", Label: "x", Source: source}
	}
	group, recheck, receipt := string(hintsource.CohortGroupAuthorization), string(hintsource.AnswerReuseAuthorizationRecheck), string(hintsource.PriorSubjectReceipt)
	for _, cell := range []struct {
		name  string
		hints []contextfabric.SubjectHint
		want  bool
	}{
		{"null (nil set: a search by definition)", nil, false},
		{"empty container", []contextfabric.SubjectHint{}, false},
		{"one group-authorization hint", []contextfabric.SubjectHint{hint(group)}, true},
		{"one reuse-recheck hint", []contextfabric.SubjectHint{hint(recheck)}, true},
		{"both authorization sources", []contextfabric.SubjectHint{hint(group), hint(recheck)}, true},
		{"duplicate authorization hints", []contextfabric.SubjectHint{hint(group), hint(group)}, true},
		{"a prior receipt alone", []contextfabric.SubjectHint{hint(receipt)}, false},
		{"a caller hint alone", []contextfabric.SubjectHint{hint("workbench")}, false},
		{"authorization + one caller hint", []contextfabric.SubjectHint{hint(group), hint("workbench")}, false},
		{"authorization + one receipt", []contextfabric.SubjectHint{hint(recheck), hint(receipt)}, false},
		{"out-of-vocabulary source (caller-authored)", []contextfabric.SubjectHint{hint("cohort_group_authorization_x")}, false},
		{"case variant of an authorization source", []contextfabric.SubjectHint{hint("COHORT_GROUP_AUTHORIZATION")}, false},
	} {
		if got := hintsForbidSearchFallback(cell.hints); got != cell.want {
			t.Errorf("%s: forbid = %v, want %v", cell.name, got, cell.want)
		}
		t.Logf("| no-fallback predicate | hints | %s | forbid=%v |", cell.name, cell.want)
	}
}
