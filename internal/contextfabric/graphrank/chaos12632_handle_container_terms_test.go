package graphrank

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12632: reach turns 3 and 7 ("status of pull request N in R") came back
// from the real model as named_subject with terms ["999"] and ["1557"]: the
// repository the question names was in no field, so the handle census read
// the whole organization and turn 3 was offered another repository's pull
// request 999. Prompt v37 tells the model to keep the container's name as the
// term and leave the number in the question. These runs pin what that shape
// gets on the real resolver: a pull request that exists only in ANOTHER
// repository is never reached, and one that exists in the named repository
// commits beside it.
//
// NOT pinned here: the number-only shape's failure. On this fake the
// question search still finds the repository from the question text, so the
// turn-3 outcome does not reproduce; the executed evidence for it is the
// stored reach run (ticket). Whether a real model emits the container shape
// is measured in reach run part 2.

func handleContainerFrame(terms ...string) *contextfabric.QuestionFrame {
	kind := contextfabric.SubjectPullRequest
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: terms, ExpectedKind: &kind},
		},
	}
}

func handleContainerReaches(turn besideTurn, id string) bool {
	if slices.Contains(besideCommittedIDs(turn.resolution), id) {
		return true
	}
	if slices.ContainsFunc(turn.resolution.Candidates, func(c contextfabric.SubjectCandidate) bool { return c.Subject.CanonicalID == id }) {
		return true
	}
	return slices.ContainsFunc(turn.offers.CandidateOptions, func(o contextfabric.CandidateOption) bool { return o.CanonicalID == id })
}

func TestAHandleQuestionThatKeepsItsContainerStaysInsideIt(t *testing.T) {
	t.Parallel()
	question := besideQuestion("pull request 748")
	elsewhere := besideNode(contextfabric.SubjectPullRequest, besidePRElsewhere, "PR #748")
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			// The container shape (v37): the answer stays inside the named
			// repository and says the handle is absent there.
			container := runBeside(t, besideWorld{principal: principal, question: question, frame: handleContainerFrame(scopeAnchorRepo), census: &besideCensus{rows: besideRows()}, nodes: []CandidateNode{elsewhere}, searches: map[string][]CandidateNode{"748": {elsewhere}}})
			if handleContainerReaches(container, besidePRElsewhere) {
				t.Fatalf("container shape reached the other repository's pull request 748: committed %v candidates %v", besideCommittedIDs(container.resolution), candidateIDs(container.resolution.Candidates))
			}
			if container.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
				t.Fatalf("limitation = %q, want the named repository's absence sentence", container.resolution.CensusLimitation)
			}
		})
	}
}

// The control (reach turn 7): a pull request that exists in the named
// repository commits beside it under the container shape.
func TestAHandleQuestionThatKeepsItsContainerCommitsTheHandleInIt(t *testing.T) {
	t.Parallel()
	for caller, principal := range besideCallers(t) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			turn := runBeside(t, besideWorld{principal: principal, question: besideQuestion("pull request 747"), frame: handleContainerFrame(scopeAnchorRepo), census: &besideCensus{rows: besideRows()}})
			if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, scopeAnchorRepoID) || !slices.Contains(got, besidePRInRepo) {
				t.Fatalf("committed = %v, want the repository and pull request 747", got)
			}
		})
	}
}
