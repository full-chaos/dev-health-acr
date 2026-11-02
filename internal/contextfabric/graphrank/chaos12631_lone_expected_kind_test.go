package graphrank

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
