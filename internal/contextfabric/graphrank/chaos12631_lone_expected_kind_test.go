package graphrank

import (
	"context"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12631: a turn states its kind by the frame's expected kind, which the
// model's requested kind fills, or by a lone expected_kinds. A turn that states
// the anchor's kind is untouched (CHAOS-12523, kept by CHAOS-12557): a handle
// beside the anchor is neither committed nor said, whichever way the kind was
// stated. A lone expected_kinds of the handle's own kind still owes it.

func expectedKinds(kinds ...contextfabric.SubjectKind) func(*contextfabric.InvestigationRequest) {
	return func(r *contextfabric.InvestigationRequest) { r.ExpectedKinds = kinds }
}

func TestALoneExpectedKindForTheAnchorLeavesAHandleBesideItUntouched(t *testing.T) {
	t.Parallel()
	kindless := namedScopeAnchorFrame(nil)
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			for _, frame := range map[string]*contextfabric.QuestionFrame{"kindless frame": kindless, "no frame": nil} {
				repository := runBeside(t, besideWorld{principal: principal, question: besideQuestion("pull request 747"), frame: frame, census: &besideCensus{rows: besideRows()}, hinted: true,
					edit: expectedKinds(contextfabric.SubjectRepository)})
				if got := besideCommittedIDs(repository.resolution); !slices.Equal(got, []string{scopeAnchorRepoID}) || repository.resolution.CensusLimitation != "" {
					t.Fatalf("expected_kinds [repository]: committed %v limitation %q, want the repository alone", got, repository.resolution.CensusLimitation)
				}
				team := runTeam(t, teamWorld{principal: principal, question: teamQuestion("pull request 747"), frame: frame, census: &besideCensus{rows: teamRows()}, owned: teamOwned(), ownedOK: true,
					edit: expectedKinds(contextfabric.SubjectTeam)})
				if got := besideCommittedIDs(team.resolution); !slices.Equal(got, []string{teamWorldID}) || team.reads != 0 || team.resolution.CensusLimitation != "" {
					t.Fatalf("expected_kinds [team]: committed %v reads %d limitation %q, want the team alone and no read", got, team.reads, team.resolution.CensusLimitation)
				}
				several := runBeside(t, besideWorld{principal: principal, question: "What is the state of pull request 747 and pull request 999 in " + scopeAnchorRepo + "?", frame: frame, census: &besideCensus{rows: besideRows()}, hinted: true,
					edit: expectedKinds(contextfabric.SubjectRepository)})
				if got := besideCommittedIDs(several.resolution); !slices.Equal(got, []string{scopeAnchorRepoID}) || several.resolution.UnansweredHandlesLimitation != "" || several.resolution.CensusLimitation != "" {
					t.Fatalf("several, expected_kinds [repository]: committed %v sentences %q %q, want untouched", got, several.resolution.UnansweredHandlesLimitation, several.resolution.CensusLimitation)
				}
			}
		})
	}
}

// A lone expected_kinds of the handle's kind owes it, as before: a count of 1
// commits beside the anchor.
func TestALoneExpectedKindForTheHandleStillOwesIt(t *testing.T) {
	t.Parallel()
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			turn := runBeside(t, besideWorld{principal: principal, question: besideQuestion("pull request 747"), frame: namedScopeAnchorFrame(nil), census: &besideCensus{rows: besideRows()}, hinted: true,
				edit: expectedKinds(contextfabric.SubjectPullRequest)})
			if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, besidePRInRepo) {
				t.Fatalf("expected_kinds [pull_request]: committed %v, want the pull request", got)
			}
			// The frame wins over expected_kinds when both state a kind.
			framed := runBeside(t, besideWorld{principal: principal, question: besideQuestion("pull request 747"), frame: statedFrame(contextfabric.SubjectPullRequest), census: &besideCensus{rows: besideRows()}, hinted: true,
				edit: expectedKinds(contextfabric.SubjectRepository)})
			if got := besideCommittedIDs(framed.resolution); !slices.Contains(got, besidePRInRepo) {
				t.Fatalf("frame pull_request, expected_kinds [repository]: committed %v, want the pull request", got)
			}
		})
	}
}

// Several expected_kinds state no kind, in either order: the turn reads as
// mentioned, as on main, and a count of 1 commits the handle beside the anchor. A
// reading that takes the first or the last value fails one of the two orders
// (Codex r1 P3 on #1975).
func TestSeveralExpectedKindsStateNoKindInEitherOrder(t *testing.T) {
	t.Parallel()
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			for _, kinds := range [][]contextfabric.SubjectKind{
				{contextfabric.SubjectRepository, contextfabric.SubjectPullRequest},
				{contextfabric.SubjectPullRequest, contextfabric.SubjectRepository},
			} {
				turn := runBeside(t, besideWorld{principal: principal, question: besideQuestion("pull request 747"), frame: namedScopeAnchorFrame(nil), census: &besideCensus{rows: besideRows()}, hinted: true,
					edit: expectedKinds(kinds...)})
				if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, besidePRInRepo) || !slices.Contains(got, scopeAnchorRepoID) {
					t.Fatalf("expected_kinds %v: committed %v, want the repository and the pull request (mentioned, count 1)", kinds, got)
				}
			}
		})
	}
}

// The hint filter asks for askedKinds' kind: the frame's, else the kind a receipt
// confirmed. With neither it filters nothing, so a lone expected_kinds never decides
// a hint there; it must not overrule a confirmed kind in the anchor clause either. A
// repository on the page says where the confirmed pull request the question names
// is, so it stays (Codex r1 P2 on #1975).
func TestAConfirmedKindKeepsTheAnchorHintWhateverALoneExpectedKindsSays(t *testing.T) {
	t.Parallel()
	pullRequest, repository := contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectRepository
	frame := namedFrameAsking(nil, "PR 532")
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: pullRequest}
	request := testRequest()
	request.Question = "Is PR 532 ready?"
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{askedKindPageHint(repository, hintCanonicalID(repository))}
	for _, kinds := range [][]contextfabric.SubjectKind{nil, {repository}, {pullRequest}} {
		request.ExpectedKinds = kinds
		if _, dropped := withoutHintsOfAnotherKind(context.Background(), request, BindHandles(request.Question), frame, confirmed); len(dropped) != 0 {
			t.Fatalf("expected_kinds %v: dropped %v, want the anchor of the confirmed pull request kept", kinds, dropped)
		}
	}
}
