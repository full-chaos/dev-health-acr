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
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12557, lead rulings (a)-(d): a handle named beside a committed team, and
// several handles named beside a committed repository, are answered or said, on
// CHAOS-12523's stated-kind gate.

const (
	teamWorldID    = "team:platform"
	teamWorldLabel = "Platform"
	teamElsewhere  = "repository:r-3"
	prElsewhere    = "pull_request:r-3:749"
)

type teamWorld struct {
	principal storage.Principal
	question  string
	frame     *contextfabric.QuestionFrame
	census    *besideCensus
	owned     []contextfabric.SubjectRef
	ownedOK   bool
	ownedErr  error
	nodes     []CandidateNode
	edit      func(*contextfabric.InvestigationRequest)
}

func teamRows() []besideRow {
	return []besideRow{
		{contextfabric.SubjectPullRequest, "747", besidePRInRepo, scopeAnchorRepoID, scopeAnchorRepo},
		{contextfabric.SubjectPullRequest, "748", besidePRElsewhere, besideOtherRepoID, besideOtherRepo},
		// A pull request in a repository the team does not own: never counted for it.
		{contextfabric.SubjectPullRequest, "749", prElsewhere, teamElsewhere, "acme/elsewhere"},
	}
}

func teamOwned() []contextfabric.SubjectRef {
	return []contextfabric.SubjectRef{
		{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID, Label: scopeAnchorRepo},
		{Kind: contextfabric.SubjectRepository, CanonicalID: besideOtherRepoID, Label: besideOtherRepo},
	}
}

type teamTurn struct {
	resolution contextfabric.SubjectResolution
	reads      int
}

func runTeam(t *testing.T, w teamWorld) teamTurn {
	t.Helper()
	teamNode := aliasCandidateNode(contextfabric.SubjectTeam, teamWorldID, teamWorldLabel, 0.9, []string{teamWorldLabel}, nil, true)
	teamNode.Attributes[authorizationRepositoriesAttr] = []string{scopeAnchorRepo, besideOtherRepo}
	teamNode.Attributes[authorizationTeamsAttr] = []string{"platform"}
	backend := &fakeGraphBackend{
		enableAliasLookup: true, aliasLookupComplete: true,
		aliasLookupClaimants: map[string][]CandidateNode{teamWorldLabel: {teamNode}},
		searchResults:        map[string][]CandidateNode{teamWorldLabel: {teamNode}},
		exactHints:           map[string]CandidateNode{},
	}
	backend.exactHints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamWorldID})] = teamNode
	for _, node := range append(slices.Clone(w.nodes),
		besideNode(contextfabric.SubjectPullRequest, besidePRInRepo, "PR #747"),
		besideNode(contextfabric.SubjectPullRequest, besidePRElsewhere, "PR #748"),
		candidateNode(contextfabric.SubjectPullRequest, prElsewhere, "PR #749", 0.5, []string{"acme/elsewhere"})) {
		id, _ := node.Attributes["canonical_id"].(string)
		kind, _ := node.Attributes["subject_kind"].(string)
		backend.exactHints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: id})] = node
	}
	deps := backend.deps()
	deps.CensusFunc = w.census.census
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		if !ValidateHandleGrammar(kind, patternID, value) {
			return "", false
		}
		return HandleSourceColumn(kind, patternID)
	}
	reads := 0
	deps.TeamOwnedRepositories = func(_ context.Context, team contextfabric.SubjectRef, limit int) ([]contextfabric.SubjectRef, bool, error) {
		reads++
		if team.CanonicalID != teamWorldID || limit != teamOwnedRepositoryLimit {
			return nil, false, fmt.Errorf("read team %q limit %d", team.CanonicalID, limit)
		}
		return w.owned, w.ownedOK, w.ownedErr
	}
	request := testRequest()
	request.Question = w.question
	request.Options.MaxSubjectCandidates = 10
	if w.edit != nil {
		w.edit(&request)
	}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), w.principal, request, testInterpreted(teamWorldLabel), deps, nil, nil, w.frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis: %v", err)
	}
	return teamTurn{resolution, reads}
}

func teamQuestion(handle string) string {
	return "What is the state of " + handle + " in team " + teamWorldLabel + "?"
}

func TestAHandleBesideATeamIsReadInTheRepositoriesTheTeamOwns(t *testing.T) {
	t.Parallel()
	stated, unstated := statedFrame(contextfabric.SubjectPullRequest), namedScopeAnchorFrame(nil)
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			world := func(question string, frame *contextfabric.QuestionFrame) teamWorld {
				return teamWorld{principal: principal, question: question, frame: frame, census: &besideCensus{rows: teamRows()}, owned: teamOwned(), ownedOK: true}
			}
			// A count of 1 in what the team owns commits beside the team, stated or not.
			for name, frame := range map[string]*contextfabric.QuestionFrame{"stated": stated, "unstated": unstated} {
				turn := runTeam(t, world(teamQuestion("pull request 747"), frame))
				if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, teamWorldID) || !slices.Contains(got, besidePRInRepo) {
					t.Fatalf("%s, count 1: committed %v, want the team and the pull request", name, got)
				}
			}
			// A pull request only in a repository the team does not own counts 0 for it.
			for _, number := range []string{"749", "999"} {
				stated0 := runTeam(t, world(teamQuestion("pull request "+number), stated))
				if len(stated0.resolution.Committed) != 0 || stated0.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
					t.Fatalf("stated 0 (%s): committed %v limitation %q, want the team demoted and the in-scope absence", number, stated0.resolution.Committed, stated0.resolution.CensusLimitation)
				}
				if !strings.Contains(stated0.resolution.ClarificationPrompt, "in the repositories the team owns") || !strings.Contains(stated0.resolution.ClarificationPrompt, teamWorldLabel) {
					t.Fatalf("stated 0 (%s): prompt %q", number, stated0.resolution.ClarificationPrompt)
				}
				unstated0 := runTeam(t, world(teamQuestion("pull request "+number), unstated))
				if got := besideCommittedIDs(unstated0.resolution); !slices.Equal(got, []string{teamWorldID}) || unstated0.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
					t.Fatalf("unstated 0 (%s): committed %v limitation %q, want the team's answer and the in-scope absence", number, got, unstated0.resolution.CensusLimitation)
				}
			}
		})
	}
}

// A read of the team's repositories that fails, or is cut at the bound, decides
// nothing: today's answer stands. The cut is logged.
func TestATeamWhoseRepositoriesCannotBeReadWholeChangesNothing(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&lockedWriter{w: &logs}, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	stated := statedFrame(contextfabric.SubjectPullRequest)
	for name, w := range map[string]teamWorld{
		"cut at the bound": {owned: teamOwned(), ownedOK: false},
		"read failed":      {ownedErr: errors.New("graph down")},
	} {
		w.principal, w.question, w.frame, w.census = besideCallers(t)["unrestricted"], teamQuestion("pull request 747"), stated, &besideCensus{rows: teamRows()}
		turn := runTeam(t, w)
		if got := besideCommittedIDs(turn.resolution); !slices.Equal(got, []string{teamWorldID}) || turn.resolution.CensusLimitation != "" {
			t.Fatalf("%s: committed %v limitation %q, want today's answer", name, got, turn.resolution.CensusLimitation)
		}
		if turn.reads != 1 {
			t.Fatalf("%s: %d reads, want one", name, turn.reads)
		}
	}
	if !strings.Contains(logs.String(), `"msg":"context fabric census: the team named beside a handle owns more repositories than one read takes, so the handle was not read"`) ||
		!strings.Contains(logs.String(), `"limit":25`) {
		t.Fatalf("the cut was not logged: %s", logs.String())
	}
}

// A work item has no repository column: beside a team its census reads all the
// caller can read, as beside a repository, and the team's repositories are not read.
func TestAWorkItemBesideATeamIsCountedAcrossWhatTheCallerReads(t *testing.T) {
	t.Parallel()
	id := besideWorkItem(t, "linear", "CHAOS-71")
	turn := runTeam(t, teamWorld{
		principal: besideCallers(t)["unrestricted"], question: "Why is CHAOS-71 blocked in team " + teamWorldLabel + "?",
		frame: statedFrame(contextfabric.SubjectWorkItem), census: &besideCensus{rows: []besideRow{{contextfabric.SubjectWorkItem, "CHAOS-71", id, "", scopeAnchorRepo}}},
		nodes: []CandidateNode{repoLessItem(id, "Split the job", scopeAnchorRepo)}, owned: teamOwned(), ownedOK: true,
	})
	if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, id) || !slices.Contains(got, teamWorldID) {
		t.Fatalf("committed %v, want the item beside the team", got)
	}
	if turn.reads != 0 {
		t.Fatalf("%d reads of the team's repositories, want none for a work item", turn.reads)
	}
}

// A restricted caller's census runs inside its grant: a pull request in an owned
// repository outside the grant is never counted, named or offered (the
// differential: a subject outside the grant changes nothing).
func TestAHandleBesideATeamStaysInsideTheGrant(t *testing.T) {
	t.Parallel()
	restricted := restrictedCaller(t, scopeAnchorRepo)
	turn := runTeam(t, teamWorld{principal: restricted, question: teamQuestion("pull request 748"), frame: statedFrame(contextfabric.SubjectPullRequest),
		census: &besideCensus{rows: teamRows()}, owned: teamOwned(), ownedOK: true})
	if slices.Contains(besideCommittedIDs(turn.resolution), besidePRElsewhere) || slices.Contains(candidateIDs(turn.resolution.Candidates), besidePRElsewhere) {
		t.Fatalf("committed %v candidates %v: a pull request outside the grant reached the turn", besideCommittedIDs(turn.resolution), candidateIDs(turn.resolution.Candidates))
	}
}

// H2: several handles beside one repository.
func TestSeveralHandlesBesideARepository(t *testing.T) {
	t.Parallel()
	question := "What is the state of pull request 747 and pull request 999 in " + scopeAnchorRepo + "?"
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			// Stated: each is counted in the repository; the repository leaves the
			// answer, the satisfier found is offered, and the absent one is said.
			stated := runBeside(t, besideWorld{principal: principal, question: question, frame: statedFrame(contextfabric.SubjectPullRequest), census: &besideCensus{rows: besideRows()}})
			if len(stated.resolution.Committed) != 0 {
				t.Fatalf("stated: committed %v, want none", stated.resolution.Committed)
			}
			if got := candidateIDs(stated.resolution.Candidates); len(got) == 0 || got[0] != besidePRInRepo {
				t.Fatalf("stated: candidates %v, want pull request 747 offered first", got)
			}
			if stated.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
				t.Fatalf("stated: limitation %q, want the in-scope absence for 999", stated.resolution.CensusLimitation)
			}
			if prompt := stated.resolution.ClarificationPrompt; !strings.Contains(prompt, "ask about one at a time") || !strings.Contains(prompt, "747, 999") {
				t.Fatalf("stated: prompt %q", prompt)
			}
			// Unstated: the repository's answer stands, with the sentence.
			unstated := runBeside(t, besideWorld{principal: principal, question: question, frame: namedScopeAnchorFrame(nil), census: &besideCensus{rows: besideRows()}})
			if got := besideCommittedIDs(unstated.resolution); !slices.Contains(got, scopeAnchorRepoID) || unstated.resolution.UnansweredHandlesLimitation != contractsv1.ContextFabricSeveralHandlesUnansweredLimitation {
				t.Fatalf("unstated: committed %v sentence %q", got, unstated.resolution.UnansweredHandlesLimitation)
			}
			// A turn that states the repository is untouched.
			repository := contextfabric.SubjectRepository
			about := runBeside(t, besideWorld{principal: principal, question: question, frame: namedScopeAnchorFrame(&repository), census: &besideCensus{rows: besideRows()}})
			if got := besideCommittedIDs(about.resolution); !slices.Contains(got, scopeAnchorRepoID) || about.resolution.UnansweredHandlesLimitation != "" || about.resolution.CensusLimitation != "" {
				t.Fatalf("stated repository: committed %v, sentences %q %q, want untouched", got, about.resolution.UnansweredHandlesLimitation, about.resolution.CensusLimitation)
			}
		})
	}
}

// lockedWriter serializes writes to the log buffer: handlers may write from
// several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
