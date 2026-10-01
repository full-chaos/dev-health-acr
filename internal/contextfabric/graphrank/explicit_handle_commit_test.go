package graphrank

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	explicitHandleRepoID = "repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7"
	explicitHandlePRID   = "pull_request:7b9583ee-4d24-2be7-4d09-34f815bebdd7:747"
)

type explicitHandleCensusCall struct {
	kind        CensusKind
	value       string
	handleBound bool
	anchorKind  contextfabric.SubjectKind
	anchorID    string
	anchorBound bool
}

func explicitHandleFixture(censusCount int, calls *[]explicitHandleCensusCall) (ResolveDeps, contextfabric.InvestigationRequest) {
	repo := candidateNode(contextfabric.SubjectRepository, explicitHandleRepoID, "full-chaos/dev-health-acr", 0.9, "*")
	pr := candidateNode(contextfabric.SubjectPullRequest, explicitHandlePRID, "PR #747", 0.1, "*")
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: explicitHandleRepoID}): repo,
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: explicitHandlePRID}):  pr,
	}}
	deps := backend.deps()
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		return "git_pull_requests.number", ValidateHandleGrammar(kind, patternID, value)
	}
	deps.CensusFunc = func(_ context.Context, _ string, kind CensusKind, value string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorID string, anchorBound bool) (CensusOutcome, error) {
		*calls = append(*calls, explicitHandleCensusCall{kind, value, handleBound, anchorKind, anchorID, anchorBound})
		out := CensusOutcome{Count: censusCount, CensusReadAt: time.Now().UTC()}
		if censusCount == 1 && anchorBound {
			out.SatisfierCanonicalID = explicitHandlePRID
		}
		return out, nil
	}
	request := testRequest()
	request.Question = "Is pull request 747 in the dev-health-acr repository ready to merge?"
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: "747"}}
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectRepository, ID: explicitHandleRepoID, Label: "full-chaos/dev-health-acr", Source: "caller"}}
	return deps, request
}

func TestExplicitPullRequestHandleResolvesInsideCommittedRepository(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	resolution, _, bases, digests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("dev-health-acr"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 2 || resolution.Committed[1].CanonicalID != explicitHandlePRID || resolution.Committed[1].Kind != contextfabric.SubjectPullRequest {
		t.Fatalf("Committed = %#v, want repository then the pull request", resolution.Committed)
	}
	var anchored []explicitHandleCensusCall
	for _, call := range calls {
		if call.anchorBound {
			anchored = append(anchored, call)
		}
	}
	if len(anchored) != 1 || anchored[0].value != "747" || !anchored[0].handleBound || anchored[0].anchorID != explicitHandleRepoID || anchored[0].anchorKind != contextfabric.SubjectRepository {
		t.Fatalf("census calls = %#v, want one handle=747 call anchored on the committed repository", calls)
	}
	events := tracer.eventsForStage("evidence_census_commit")
	if len(events) != 1 || events[0].Outcome != "merged" || !events[0].CensusCommitHandleExplicit || events[0].Subject.CanonicalID != explicitHandlePRID {
		t.Fatalf("evidence_census_commit events = %#v, want one merged handle-explicit event for the pull request", events)
	}
	if bases.For(resolution.Committed[1]) != contextfabric.CommitBasisStatistical || digests.For(resolution.Committed[1]).CommitGate != commitGateExplicitHandleCensus {
		t.Fatalf("basis/digest not recorded for the handle-resolved subject: %v %v", bases, digests)
	}
}

func TestExplicitPullRequestHandleNotUniqueLeavesNoRepositoryAnswer(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(2, &calls)
	resolution := resolveExplicitHandleFixture(t, deps, request)
	if len(resolution.Committed) != 0 {
		t.Fatalf("Committed = %#v, want none: an answer about the repository is not an answer about the pull request", resolution.Committed)
	}
	if !strings.Contains(resolution.ClarificationPrompt, "pull request") || !strings.Contains(resolution.ClarificationPrompt, "full-chaos/dev-health-acr") {
		t.Fatalf("ClarificationPrompt = %q", resolution.ClarificationPrompt)
	}
}

func TestExplicitPullRequestHandleCensusPanicFailsClosed(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	census := deps.CensusFunc
	deps.CensusFunc = func(ctx context.Context, org string, kind CensusKind, value string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorID string, anchorBound bool) (CensusOutcome, error) {
		if anchorBound {
			panic("census exploded")
		}
		return census(ctx, org, kind, value, handleBound, anchorKind, anchorID, anchorBound)
	}
	resolution := resolveExplicitHandleFixture(t, deps, request)
	if len(resolution.Committed) != 0 {
		t.Fatalf("Committed = %#v, want none", resolution.Committed)
	}
}

func TestExplicitPullRequestHandleReplacesACommittedPullRequestWithAnotherNumber(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	other := "pull_request:7b9583ee-4d24-2be7-4d09-34f815bebdd7:748"
	exact := deps.ExactHint
	deps.ExactHint = func(ctx context.Context, s contextfabric.SubjectRef) (CandidateNode, bool, error) {
		if s.CanonicalID == other {
			return candidateNode(s.Kind, other, "PR #748", 0.1, "*"), true, nil
		}
		return exact(ctx, s)
	}
	request.RequestedScope.SubjectHints = append(request.RequestedScope.SubjectHints, contextfabric.SubjectHint{Kind: contextfabric.SubjectPullRequest, ID: other, Label: "PR #748", Source: "caller"})
	resolution := resolveExplicitHandleFixture(t, deps, request)
	var prs []string
	for _, s := range resolution.Committed {
		if s.Kind == contextfabric.SubjectPullRequest {
			prs = append(prs, s.CanonicalID)
		}
	}
	if len(prs) != 1 || prs[0] != explicitHandlePRID {
		t.Fatalf("pull requests committed = %v, want only %s", prs, explicitHandlePRID)
	}
}

func TestSeveralExplicitHandlesLeaveTheResolutionAlone(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	request.SubjectHandles = append(request.SubjectHandles, contractsv1.ContextFabricRequestedHandle{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: "748"})
	resolution := resolveExplicitHandleFixture(t, deps, request)
	if hasCommittedKind(resolution.Committed, contextfabric.SubjectPullRequest) || len(resolution.Committed) != 1 {
		t.Fatalf("Committed = %#v, want the repository untouched: several handles drive offers only", resolution.Committed)
	}
}
func TestExplicitPullRequestHandleNeedsExactlyOneCommittedRepository(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	request.RequestedScope.SubjectHints = nil
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("nothing"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("Committed = %#v, want none: no repository anchor", resolution.Committed)
	}
}

func TestExplicitPullRequestHandleSkipsWhenTwoRepositoriesAreCommitted(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	other := "repository:00000000-0000-0000-0000-000000000002"
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: explicitHandleRepoID}): candidateNode(contextfabric.SubjectRepository, explicitHandleRepoID, "full-chaos/dev-health-acr", 0.9, "*"),
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: other}):                candidateNode(contextfabric.SubjectRepository, other, "full-chaos/other", 0.9, "*"),
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: explicitHandlePRID}):  candidateNode(contextfabric.SubjectPullRequest, explicitHandlePRID, "PR #747", 0.1, "*"),
	}}
	deps.ExactHint = backend.deps().ExactHint
	request.RequestedScope.SubjectHints = append(request.RequestedScope.SubjectHints, contextfabric.SubjectHint{Kind: contextfabric.SubjectRepository, ID: other, Label: "full-chaos/other", Source: "caller"})
	resolution, _, bases, digests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("x"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("Committed = %#v, want none: two repositories can anchor the handle", resolution.Committed)
	}
	if !strings.Contains(resolution.ClarificationPrompt, "full-chaos/dev-health-acr") || !strings.Contains(resolution.ClarificationPrompt, "full-chaos/other") {
		t.Fatalf("ClarificationPrompt = %q, want both candidate repositories named", resolution.ClarificationPrompt)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.State == contextfabric.ResolutionCommitted {
			t.Fatalf("candidate %#v still committed", candidate)
		}
	}
	if len(bases) != 0 || len(digests) != 0 {
		t.Fatalf("bases=%v digests=%v, want none for demoted repositories", bases, digests)
	}
}

func TestExplicitPullRequestHandleDoesNotDuplicateACommittedPullRequest(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	request.RequestedScope.SubjectHints = append(request.RequestedScope.SubjectHints, contextfabric.SubjectHint{Kind: contextfabric.SubjectPullRequest, ID: explicitHandlePRID, Label: "PR #747", Source: "caller"})
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("x"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range resolution.Committed {
		if s.Kind == contextfabric.SubjectPullRequest {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Committed = %#v, want exactly one pull request", resolution.Committed)
	}
}

func resolveExplicitHandleFixture(t *testing.T, deps ResolveDeps, request contextfabric.InvestigationRequest) contextfabric.SubjectResolution {
	t.Helper()
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("x"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

func TestExplicitPullRequestHandleRefusesAnUnsafeCensusOutcome(t *testing.T) {
	t.Parallel()
	cases := map[string]CensusOutcome{
		"two rows but a named satisfier": {Count: 2, SatisfierCanonicalID: explicitHandlePRID},
		"closure mismatch":               {Count: 1, SatisfierCanonicalID: explicitHandlePRID, ClosureMismatch: true},
		"set closure mismatch":           {Count: 1, SatisfierCanonicalID: explicitHandlePRID, SatisfierSetClosureMismatch: true},
		"no satisfier id":                {Count: 1},
		"blank satisfier id":             {Count: 1, SatisfierCanonicalID: "  "},
	}
	for name, outcome := range cases {
		outcome := outcome
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls []explicitHandleCensusCall
			deps, request := explicitHandleFixture(1, &calls)
			deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
				return outcome, nil
			}
			if resolution := resolveExplicitHandleFixture(t, deps, request); len(resolution.Committed) != 0 {
				t.Fatalf("Committed = %#v, want none: neither the pull request nor the repository", resolution.Committed)
			}
		})
	}
}

func TestExplicitPullRequestHandleIsInertWithoutItsInputs(t *testing.T) {
	t.Parallel()
	mutate := map[string]func(*ResolveDeps, *contextfabric.InvestigationRequest){
		"no handles":    func(_ *ResolveDeps, r *contextfabric.InvestigationRequest) { r.SubjectHandles = nil },
		"no census":     func(d *ResolveDeps, _ *contextfabric.InvestigationRequest) { d.CensusFunc = nil },
		"no grammar":    func(d *ResolveDeps, _ *contextfabric.InvestigationRequest) { d.HandleGrammarChecker = nil },
		"invalid value": func(_ *ResolveDeps, r *contextfabric.InvestigationRequest) { r.SubjectHandles[0].Value = "7x" },
		"no anchor kind": func(_ *ResolveDeps, r *contextfabric.InvestigationRequest) {
			r.SubjectHandles[0] = contractsv1.ContextFabricRequestedHandle{Kind: contextfabric.SubjectWorkItem, PatternID: "work_item_ticket_key", Value: "CHAOS-1"}
		},
		"project only": func(_ *ResolveDeps, r *contextfabric.InvestigationRequest) {
			r.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectProject, ID: "project_1", Label: "p", Source: "caller"}}
		},
	}
	for name, fn := range mutate {
		fn := fn
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls []explicitHandleCensusCall
			deps, request := explicitHandleFixture(1, &calls)
			deps.ExactHint = func(ctx context.Context, s contextfabric.SubjectRef) (CandidateNode, bool, error) {
				switch s.Kind {
				case contextfabric.SubjectProject:
					return candidateNode(s.Kind, s.CanonicalID, "p", 0.9, "*"), true, nil
				case contextfabric.SubjectPullRequest:
					return candidateNode(s.Kind, s.CanonicalID, "PR #747", 0.1, "*"), true, nil
				}
				return candidateNode(s.Kind, s.CanonicalID, "full-chaos/dev-health-acr", 0.9, "*"), true, nil
			}
			fn(&deps, &request)
			if resolution := resolveExplicitHandleFixture(t, deps, request); hasCommittedKind(resolution.Committed, contextfabric.SubjectPullRequest) || len(resolution.Committed) > 1 {
				t.Fatalf("Committed = %#v, want at most the hinted subject", resolution.Committed)
			}
		})
	}
}

func hasCommittedKind(committed []contextfabric.SubjectRef, kind contextfabric.SubjectKind) bool {
	for _, subject := range committed {
		if subject.Kind == kind {
			return true
		}
	}
	return false
}

func TestExplicitPullRequestHandleDigestCarriesTheResolutionWideFlags(t *testing.T) {
	t.Parallel()
	var calls []explicitHandleCensusCall
	deps, request := explicitHandleFixture(1, &calls)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: explicitHandleRepoID, Label: "full-chaos/dev-health-acr"}
	bases := contextfabric.CommitBasisSet{}
	digests := contextfabric.CommitDecisionDigestSet{}
	digests.Record(repo, contextfabric.CommitDecisionDigest{CommitGate: "lone_floor", SearchTruncated: true, AliasLookupComplete: true})
	resolution := commitExplicitHandleSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repo}}, bases, digests)
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: explicitHandlePRID}
	if len(resolution.Committed) != 2 {
		t.Fatalf("Committed = %#v", resolution.Committed)
	}
	got := digests.For(pr)
	if got.CommitGate != commitGateExplicitHandleCensus || !got.SearchTruncated || !got.AliasLookupComplete {
		t.Fatalf("digest = %#v, want the evidence_census gate carrying the resolution-wide flags", got)
	}
}
