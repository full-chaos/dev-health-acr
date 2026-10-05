package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	outsidePathRepoID  = "repo_payments"
	outsidePathOtherID = "repo_billing"
	outsidePathPRID    = "pull_request:repo_payments:747"
)

type outsidePathRun struct {
	resolution contextfabric.SubjectResolution
	digests    contextfabric.CommitDecisionDigestSet
	decisions  []ResolutionTraceEvent
	census     []ResolutionTraceEvent
}

func runOutsidePath(t *testing.T, complete bool, question string, terms []string, edit func(*contextfabric.InvestigationRequest, *ResolveDeps)) outsidePathRun {
	t.Helper()
	repo := exactLabelNode(contextfabric.SubjectRepository, outsidePathRepoID, "full-chaos/payments")
	other := candidateNode(contextfabric.SubjectRepository, outsidePathOtherID, "full-chaos/billing", 0.9, "*")
	pr := candidateNode(contextfabric.SubjectPullRequest, outsidePathPRID, "PR #747", 0.5, "*")
	backend := &fakeGraphBackend{
		searchResults:        map[string][]CandidateNode{exactLabelCompletenessTerm: truncatedPaymentsSearch(repo), "PR 747": {pr}},
		searchTruncated:      true,
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{exactLabelCompletenessTerm: {exactLabelLookupClaimant(outsidePathRepoID, "full-chaos/payments")}},
		aliasLookupComplete:  complete,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: outsidePathRepoID}):  repo,
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: outsidePathOtherID}): other,
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: outsidePathPRID}):   pr,
		},
	}
	deps := backend.deps()
	tracer := &recordingTracer{}
	deps.ResolutionTracer = tracer
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		return "git_pull_requests.number", ValidateHandleGrammar(kind, patternID, value)
	}
	deps.CensusFunc = func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
		if kind == contextfabric.SubjectPullRequest {
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: outsidePathPRID}, nil
		}
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: outsidePathRepoID}, nil
	}
	request := testRequest()
	request.Question = question
	if edit != nil {
		edit(&request, &deps)
	}
	resolution, _, _, digests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(terms...), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	return outsidePathRun{resolution: resolution, digests: digests, decisions: tracedStage(tracer.events, "decision"), census: tracedStage(tracer.events, "evidence_census_commit")}
}

func outsideCommittedIDs(run outsidePathRun) []string {
	ids := []string{}
	for _, subject := range run.resolution.Committed {
		ids = append(ids, subject.CanonicalID+"@"+run.digests.For(subject).CommitGate)
	}
	return ids
}

func refusedExactStep(run outsidePathRun) bool {
	for _, event := range run.decisions {
		if event.Outcome == "ambiguous" && event.CommitGate == exactLabelRefusalCommitGate {
			return true
		}
	}
	return false
}

func TestTheOutsideCommitPathsAfterAnExactLabelRefusal(t *testing.T) {
	t.Parallel()
	terms := []string{exactLabelCompletenessTerm}
	t.Run("caller hints run before the exact-label step and commit only by canonical id", func(t *testing.T) {
		for _, source := range []string{"caller", string(hintsource.AnswerReuseAuthorizationRecheck), string(hintsource.CohortGroupAuthorization), string(hintsource.EngineCommittedAnchorCarry), string(hintsource.PriorSubjectReceipt)} {
			run := runOutsidePath(t, false, "who owns payments?", terms, func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
				r.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectRepository, ID: outsidePathOtherID, Label: "full-chaos/billing", Source: source}}
			})
			ids := outsideCommittedIDs(run)
			if len(ids) != 1 || run.resolution.Committed[0].CanonicalID != outsidePathOtherID {
				t.Fatalf("source %s: committed %v, want only the hinted repository by its canonical id", source, ids)
			}
			if refusedExactStep(run) {
				t.Fatalf("source %s: the exact-label step ran and refused; a caller canonical id must be decided before any label is read", source)
			}
			t.Logf("source %s: committed %v", source, ids)
		}
	})
	t.Run("explicit handle needs a committed repository", func(t *testing.T) {
		handle := func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: "747"}}
		}
		control := runOutsidePath(t, true, "Is the change in payments ready to merge?", terms, handle)
		refused := runOutsidePath(t, false, "Is the change in payments ready to merge?", terms, handle)
		t.Logf("complete read: committed %v; incomplete read: committed %v prompt %q", outsideCommittedIDs(control), outsideCommittedIDs(refused), refused.resolution.ClarificationPrompt)
		if !containsID(control, outsidePathPRID) || len(control.census) != 1 || !control.census[0].CensusCommitHandleExplicit {
			t.Fatalf("control: committed %v, want the handle-named pull request committed by the explicit-handle path beside the repository (the row must reach that path)", outsideCommittedIDs(control))
		}
		if len(refused.resolution.Committed) != 0 || !refusedExactStep(refused) {
			t.Fatalf("refused: committed %v, want nothing after the exact-label refusal", outsideCommittedIDs(refused))
		}
	})
	t.Run("census beside a scope anchor needs a committed anchor", func(t *testing.T) {
		control := runOutsidePath(t, true, "why did PR 747 fail in payments?", []string{exactLabelCompletenessTerm, "PR 747"}, nil)
		refused := runOutsidePath(t, false, "why did PR 747 fail in payments?", []string{exactLabelCompletenessTerm, "PR 747"}, nil)
		t.Logf("complete read: committed %v census %d; incomplete read: committed %v census %d", outsideCommittedIDs(control), len(control.census), outsideCommittedIDs(refused), len(refused.census))
		if !containsID(control, outsidePathRepoID) || !containsID(control, outsidePathPRID) || len(control.census) != 1 || control.census[0].CensusCommitHandleExplicit {
			t.Fatalf("control: committed %v census events %d, want the repository and the pull request committed beside it by the census (the row must reach the beside-anchor path)", outsideCommittedIDs(control), len(control.census))
		}
		if len(refused.resolution.Committed) != 0 || !refusedExactStep(refused) {
			t.Fatalf("refused: committed %v, want nothing after the exact-label refusal", outsideCommittedIDs(refused))
		}
	})
}

func containsID(run outsidePathRun, id string) bool {
	for _, subject := range run.resolution.Committed {
		if subject.CanonicalID == id {
			return true
		}
	}
	return false
}

func TestAConfirmedKindReDecisionCommitsNoNeighbourAfterAnExactLabelRefusal(t *testing.T) {
	t.Parallel()
	repo := exactLabelNode(contextfabric.SubjectRepository, outsidePathRepoID, "full-chaos/payments")
	neighbour := candidateNode(contextfabric.SubjectRepository, "repo_payments_ledger_tool", "payments ledger tool", 0.9, []string{"full-chaos/payments"})
	resolve := func(complete bool) (contextfabric.SubjectResolution, contextfabric.CommitDecisionDigestSet) {
		backend := &fakeGraphBackend{
			searchResults:        map[string][]CandidateNode{exactLabelCompletenessTerm: truncatedPaymentsSearch(repo)},
			searchTruncated:      true,
			enableAliasLookup:    true,
			aliasLookupClaimants: map[string][]CandidateNode{exactLabelCompletenessTerm: {exactLabelLookupClaimant(outsidePathRepoID, "full-chaos/payments")}},
			aliasLookupComplete:  complete,
			enableSearchKind:     true,
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				exactLabelCompletenessTerm: {contextfabric.SubjectRepository: {neighbour}},
			},
		}
		resolution, _, _, digests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(exactLabelCompletenessTerm), backend.deps(), &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectRepository}, nil, nil, "")
		if err != nil {
			t.Fatalf("ResolveSubjects error = %v", err)
		}
		return resolution, digests
	}
	refused, digests := resolve(false)
	t.Logf("incomplete read: committed %v prompt %q", outsideCommittedIDs(outsidePathRun{resolution: refused, digests: digests}), refused.ClarificationPrompt)
	if len(refused.Committed) != 0 {
		t.Fatalf("incomplete read: committed %v, want nothing: the confirmed-kind re-decision must not commit a neighbour after the exact label was refused", outsideCommittedIDs(outsidePathRun{resolution: refused, digests: digests}))
	}
}
