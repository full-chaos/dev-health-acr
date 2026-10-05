package graphrank

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const exactLabelCompletenessTerm = "payments"

var exactLabelCompletenessPrincipal = storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/payments", "full-chaos/payments-ledger"}}

func exactLabelNode(kind contextfabric.SubjectKind, id, repository string) CandidateNode {
	return candidateNode(kind, id, exactLabelCompletenessTerm, 0.35, []string{repository})
}

func exactLabelLookupClaimant(id, repository string) CandidateNode {
	node := exactLabelNode(contextfabric.SubjectRepository, id, repository)
	node.Mechanism = contextfabric.MatchExact
	node.FromKeyedIdentityLookup = true
	return node
}

func truncatedPaymentsSearch(extra ...CandidateNode) []CandidateNode {
	nodes := make([]CandidateNode, 0, 10+len(extra))
	for i := 0; i < 10; i++ {
		nodes = append(nodes, candidateNode(contextfabric.SubjectProject, fmt.Sprintf("project_payments_%d", i), fmt.Sprintf("payments rollout %d", i), 0.35, "*"))
	}
	return append(nodes[:5], append(extra, nodes[5:]...)...)
}

type exactLabelLookup struct {
	claimants []CandidateNode
	complete  bool
	err       error
}

func exactLabelBackend(search []CandidateNode, lookup *exactLabelLookup) *fakeGraphBackend {
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{exactLabelCompletenessTerm: search}, searchTruncated: true}
	if lookup != nil {
		backend.enableAliasLookup = true
		backend.aliasLookupClaimants = map[string][]CandidateNode{exactLabelCompletenessTerm: lookup.claimants}
		backend.aliasLookupComplete = lookup.complete
		backend.aliasLookupErr = lookup.err
	}
	return backend
}

func resolvePayments(t *testing.T, backend *fakeGraphBackend) contextfabric.SubjectResolution {
	t.Helper()
	resolution, _, err := ResolveSubjects(context.Background(), exactLabelCompletenessPrincipal, testRequest(), testInterpreted(exactLabelCompletenessTerm), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	return resolution
}

func committedOnly(t *testing.T, resolution contextfabric.SubjectResolution, id string) {
	t.Helper()
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != id || resolution.ClarificationPrompt != "" {
		t.Fatalf("Committed = %#v prompt = %q, want %s committed", resolution.Committed, resolution.ClarificationPrompt, id)
	}
}

func clarified(t *testing.T, resolution contextfabric.SubjectResolution) {
	t.Helper()
	if len(resolution.Committed) != 0 || resolution.ClarificationPrompt == "" {
		t.Fatalf("Committed = %#v prompt = %q, want a clarification", resolution.Committed, resolution.ClarificationPrompt)
	}
}

func TestExactLabelRepositoryClarifiesWhenTheIdentityLookupRanIncomplete(t *testing.T) {
	t.Parallel()
	named := exactLabelNode(contextfabric.SubjectRepository, "repo_payments", "full-chaos/payments")
	resolution := resolvePayments(t, exactLabelBackend(truncatedPaymentsSearch(named), &exactLabelLookup{
		claimants: []CandidateNode{exactLabelLookupClaimant("repo_payments", "full-chaos/payments")},
		complete:  false,
	}))
	clarified(t, resolution)
}

func TestExactLabelCommitStaysWhereNoCompletenessProofExists(t *testing.T) {
	t.Parallel()
	repository := exactLabelNode(contextfabric.SubjectRepository, "repo_payments", "full-chaos/payments")
	workItem := exactLabelNode(contextfabric.SubjectWorkItem, "work_item_payments", "full-chaos/payments")
	cases := map[string]struct {
		backend *fakeGraphBackend
		want    string
	}{
		"repository, lookup not run for the time axis": {
			backend: exactLabelBackend(truncatedPaymentsSearch(repository), &exactLabelLookup{err: ErrIdentityLookupNotRunForTimeAxis}),
			want:    "repo_payments",
		},
		"repository, no lookup wired": {
			backend: exactLabelBackend(truncatedPaymentsSearch(repository), nil),
			want:    "repo_payments",
		},
		"work item, lookup ran incomplete": {
			backend: exactLabelBackend(truncatedPaymentsSearch(workItem), &exactLabelLookup{complete: false}),
			want:    "work_item_payments",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			committedOnly(t, resolvePayments(t, tc.backend), tc.want)
		})
	}
}

func TestExactLabelRepositoryCommitsOnACompleteLookupAndClarifiesOnAVisibleRival(t *testing.T) {
	t.Parallel()
	named := exactLabelNode(contextfabric.SubjectRepository, "repo_payments", "full-chaos/payments")
	committedOnly(t, resolvePayments(t, exactLabelBackend(truncatedPaymentsSearch(named), &exactLabelLookup{
		claimants: []CandidateNode{exactLabelLookupClaimant("repo_payments", "full-chaos/payments")},
		complete:  true,
	})), "repo_payments")
	for _, complete := range []bool{true, false} {
		clarified(t, resolvePayments(t, exactLabelBackend(truncatedPaymentsSearch(named), &exactLabelLookup{
			claimants: []CandidateNode{exactLabelLookupClaimant("repo_payments", "full-chaos/payments"), exactLabelLookupClaimant("repo_payments_ledger", "full-chaos/payments-ledger")},
			complete:  complete,
		})))
	}
}

func TestExactLabelDecisionIgnoresARivalTheCallerCannotRead(t *testing.T) {
	t.Parallel()
	named := exactLabelNode(contextfabric.SubjectRepository, "repo_payments", "full-chaos/payments")
	ownClaimant := exactLabelLookupClaimant("repo_payments", "full-chaos/payments")
	hiddenNode := exactLabelNode(contextfabric.SubjectRepository, "repo_payments_private", "other/private")
	hiddenClaimant := exactLabelLookupClaimant("repo_payments_private", "other/private")
	lookups := map[string]func() *exactLabelLookup{
		"lookup complete":            func() *exactLabelLookup { return &exactLabelLookup{complete: true} },
		"lookup incomplete":          func() *exactLabelLookup { return &exactLabelLookup{complete: false} },
		"lookup not run (time axis)": func() *exactLabelLookup { return &exactLabelLookup{err: ErrIdentityLookupNotRunForTimeAxis} },
	}
	for lookupName, lookup := range lookups {
		noRivalLookup := lookup()
		if noRivalLookup.err == nil {
			noRivalLookup.claimants = []CandidateNode{ownClaimant}
		}
		noRival := resolvePayments(t, exactLabelBackend(truncatedPaymentsSearch(named), noRivalLookup))
		rivals := map[string]struct {
			search    []CandidateNode
			claimants []CandidateNode
		}{
			"rival in the search and the lookup": {truncatedPaymentsSearch(named, hiddenNode), []CandidateNode{ownClaimant, hiddenClaimant}},
			"rival cut from the search":          {truncatedPaymentsSearch(named), []CandidateNode{ownClaimant, hiddenClaimant}},
			"rival in the search only":           {truncatedPaymentsSearch(named, hiddenNode), []CandidateNode{ownClaimant}},
		}
		for rivalName, rival := range rivals {
			t.Run(lookupName+", "+rivalName, func(t *testing.T) {
				withRivalLookup := lookup()
				if withRivalLookup.err == nil {
					withRivalLookup.claimants = rival.claimants
				}
				hidden := resolvePayments(t, exactLabelBackend(rival.search, withRivalLookup))
				if !reflect.DeepEqual(noRival, hidden) {
					t.Fatalf("resolution with an unreadable rival differs from the one without it:\nwithout: %#v\nwith:    %#v", noRival, hidden)
				}
			})
		}
	}
}

func TestExactLabelTraceNamesTheLookupStateAndTheRefusal(t *testing.T) {
	t.Parallel()
	named := exactLabelNode(contextfabric.SubjectRepository, "repo_payments", "full-chaos/payments")
	cases := map[string]struct {
		lookup     *exactLabelLookup
		wantState  string
		wantGate   string
		wantStatus string
	}{
		"complete":          {&exactLabelLookup{claimants: []CandidateNode{exactLabelLookupClaimant("repo_payments", "full-chaos/payments")}, complete: true}, "complete", "exact_index", "committed"},
		"incomplete":        {&exactLabelLookup{claimants: []CandidateNode{exactLabelLookupClaimant("repo_payments", "full-chaos/payments")}}, "incomplete", "exact_index_unproven", "ambiguous"},
		"not_run_time_axis": {&exactLabelLookup{err: ErrIdentityLookupNotRunForTimeAxis}, "not_run_time_axis", "exact_index", "committed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracer := &recordingTracer{}
			backend := exactLabelBackend(truncatedPaymentsSearch(named), tc.lookup)
			deps := backend.deps()
			deps.ResolutionTracer = tracer
			if _, _, err := ResolveSubjects(context.Background(), exactLabelCompletenessPrincipal, testRequest(), testInterpreted(exactLabelCompletenessTerm), deps, nil, nil); err != nil {
				t.Fatalf("ResolveSubjects error = %v", err)
			}
			lookups := tracedStage(tracer.events, "alias_lookup")
			if len(lookups) != 1 || lookups[0].IdentityLookup != tc.wantState {
				t.Fatalf("alias_lookup events = %#v, want one with identity_lookup %q", lookups, tc.wantState)
			}
			decisions := tracedStage(tracer.events, "decision")
			if len(decisions) == 0 || decisions[0].Outcome != tc.wantStatus || decisions[0].CommitGate != tc.wantGate {
				t.Fatalf("decision events = %#v, want outcome %q commit_gate %q", decisions, tc.wantStatus, tc.wantGate)
			}
		})
	}
}

func tracedStage(events []ResolutionTraceEvent, stage string) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, event := range events {
		if event.Stage == stage {
			out = append(out, event)
		}
	}
	return out
}

func TestAnUnprovenExactLabelHandsNoCommitToTheVectorMarginRescue(t *testing.T) {
	t.Parallel()
	exact := corroborationCandidate("project_payments", 1, contextfabric.MatchExact, contextfabric.MatchVector)
	exact.MatchedTerms = []string{exactLabelCompletenessTerm}
	neighbour := corroborationCandidate("project_payments_rollout", 0.5, contextfabric.MatchLexical, contextfabric.MatchVector)
	similarities := map[string]float64{SubjectKey(exact.Subject): 0.40, SubjectKey(neighbour.Subject): 0.95}
	bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(exact.Subject): exact, SubjectKey(neighbour.Subject): neighbour}
	resolve := func(lookup IdentityLookupState) contextfabric.SubjectResolution {
		resolution, _, _ := resolveFromMergedCandidatesWithAnchorSlot(
			bySubject, map[string]string{}, map[string]bool{}, 10, true, true,
			similarities, 0.25, false, 10, 20, true,
			DefaultCommitGatePolicy(), nil, nil, lookup, nil, "", "", false, false, nil, anchorReservedSlot{}, nil, 1)
		return resolution
	}
	if committed := resolve(IdentityLookupComplete).Committed; len(committed) != 1 || committed[0].CanonicalID != "project_payments" {
		t.Fatalf("complete lookup: Committed = %#v, want project_payments on the exact-label tier", committed)
	}
	if committed := resolve(IdentityLookupIncomplete).Committed; len(committed) != 0 {
		t.Fatalf("incomplete lookup: Committed = %#v, want nothing: the refused exact label must not hand the commit to a neighbour by vector margin", committed)
	}
}

func TestTheEvidenceCensusPassNeverCommitsARefusedExactLabel(t *testing.T) {
	t.Parallel()
	const prID = "pull_request:repo-1:532"
	named := exactLabelNode(contextfabric.SubjectRepository, "repo_payments", "full-chaos/payments")
	pr := candidateNode(contextfabric.SubjectPullRequest, prID, "PR #532", 0.50, "*")
	cases := map[string]struct {
		question   string
		terms      []string
		search     map[string][]CandidateNode
		censusRead bool
		roundCheck func(ResolutionTraceEvent) bool
	}{
		"label only": {
			question: "who owns payments?",
			terms:    []string{exactLabelCompletenessTerm},
			search:   map[string][]CandidateNode{exactLabelCompletenessTerm: truncatedPaymentsSearch(named)},
			roundCheck: func(event ResolutionTraceEvent) bool {
				return event.ShadowOutcome == "would_clarify" && event.ShadowReason == "no_discriminators"
			},
		},
		"label and a pull request handle": {
			question:   "why did PR 532 fail in payments?",
			terms:      []string{exactLabelCompletenessTerm, "PR 532"},
			search:     map[string][]CandidateNode{exactLabelCompletenessTerm: truncatedPaymentsSearch(named), "PR 532": {pr}},
			censusRead: true,
			roundCheck: func(event ResolutionTraceEvent) bool {
				return event.ShadowOutcome == "would_clarify" && event.ShadowNonCensusedSurvivor
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := &fakeGraphBackend{
				searchResults: tc.search, searchTruncated: true,
				enableAliasLookup:    true,
				aliasLookupClaimants: map[string][]CandidateNode{exactLabelCompletenessTerm: {exactLabelLookupClaimant("repo_payments", "full-chaos/payments")}},
				aliasLookupComplete:  false,
				exactHints: map[string]CandidateNode{
					SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: prID}):           pr,
					SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_payments"}): named,
				},
			}
			deps := backend.deps()
			tracer := &recordingTracer{}
			deps.ResolutionTracer = tracer
			var censusKinds []CensusKind
			deps.CensusFunc = func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
				censusKinds = append(censusKinds, kind)
				if kind == contextfabric.SubjectPullRequest {
					return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: prID}, nil
				}
				return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "repo_payments"}, nil
			}
			request := testRequest()
			request.Question = tc.question

			resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(tc.terms...), deps, nil, nil)

			if err != nil {
				t.Fatalf("ResolveSubjects error = %v", err)
			}
			for _, subject := range resolution.Committed {
				if isAliasLookupScopedKind(subject.Kind) {
					t.Fatalf("Committed = %#v, want no repository, project or team: the exact label was refused for an incomplete read", resolution.Committed)
				}
			}
			if (len(censusKinds) > 0) != tc.censusRead {
				t.Fatalf("census kinds read = %v, want read=%t: the row must reach the census pass it names", censusKinds, tc.censusRead)
			}
			rounds := tracedStage(tracer.events, "evidence_round")
			if len(rounds) != 1 || !tc.roundCheck(rounds[0]) {
				t.Fatalf("evidence_round events = %#v, want the census pass to clarify", rounds)
			}
			if len(resolution.Committed) == 0 && resolution.ClarificationPrompt == "" {
				t.Fatal("nothing committed and no clarification")
			}
			for _, subject := range resolution.Committed {
				if subject.CanonicalID != prID {
					t.Fatalf("Committed = %#v, want at most the handle-named pull request", resolution.Committed)
				}
			}
		})
	}
}
