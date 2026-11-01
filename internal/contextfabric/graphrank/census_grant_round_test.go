package graphrank

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12452: a restricted caller's census runs inside its grant. These tests
// read the principal a real credential reaches the resolver with
// (userprincipal), never a hand-built one.

const (
	grantRepoInside  = "acme/alpha"
	grantRepoBeside  = "acme/beta"
	grantRepoOutside = "other/secret"
	grantPRInside    = "pull_request:r-inside:5063"
	grantPRBeside    = "pull_request:r-beside:5063"
	grantPROutside   = "pull_request:r-outside:5063"
	grantQuestion    = "is pull request 5063 ready to merge?"
)

type grantRow struct {
	kind  CensusKind
	value string
	id    string
	repo  string
}

type grantCensusCall struct {
	kind        CensusKind
	value       string
	handleBound bool
	anchorBound bool
	grant       []string
	grantBound  bool
	filter      []string
}

// grantCensusSpy records what every census call was given and answers as the
// real adapter does: rows inside the bound grant and the requested filter, and
// a refusal for a kind with no repository column.
type grantCensusSpy struct {
	mu    sync.Mutex
	rows  []grantRow
	calls []grantCensusCall
}

func (s *grantCensusSpy) census(ctx context.Context, _ string, kind CensusKind, value string, handleBound bool, _ contextfabric.SubjectKind, _ string, anchorBound bool) (CensusOutcome, error) {
	grant, bound := contextfabric.CensusGrantFrom(ctx)
	filter := CensusRepositoryFilterFrom(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, grantCensusCall{kind, value, handleBound, anchorBound, grant, bound, filter})
	if bound && kind == contextfabric.SubjectWorkItem {
		return CensusOutcome{}, ErrCensusGrantNotApplied
	}
	var ids []string
	for _, row := range s.rows {
		if row.kind != kind || (handleBound && row.value != value) {
			continue
		}
		if (bound && !slices.Contains(grant, row.repo)) || (len(filter) > 0 && !slices.Contains(filter, row.repo)) {
			continue
		}
		ids = append(ids, row.id)
	}
	outcome := CensusOutcome{Count: len(ids), CensusReadAt: time.Now().UTC(), GrantApplied: bound, RepositoryFilterApplied: len(filter) > 0}
	if len(ids) == 1 {
		outcome.SatisfierCanonicalID = ids[0]
	} else {
		outcome.SatisfierCanonicalIDs = ids
	}
	return outcome, nil
}

func grantRows() []grantRow {
	return []grantRow{
		{contextfabric.SubjectPullRequest, "5063", grantPRInside, grantRepoInside},
		{contextfabric.SubjectPullRequest, "5063", grantPROutside, grantRepoOutside},
	}
}

func grantNode(id, repo string, relevance float64) CandidateNode {
	return candidateNode(contextfabric.SubjectPullRequest, id, "PR #5063", relevance, []string{repo})
}

func grantBackend(nodes ...CandidateNode) *fakeGraphBackend {
	hints := map[string]CandidateNode{}
	for _, node := range nodes {
		id, _ := node.Attributes["canonical_id"].(string)
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: id})] = node
	}
	return &fakeGraphBackend{
		enableAliasLookup: true, aliasLookupComplete: true,
		searchResults:   map[string][]CandidateNode{"pull request 5063": nodes},
		searchTruncated: true,
		exactHints:      hints,
	}
}

func defaultGrantBackend() *fakeGraphBackend {
	return grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5), grantNode(grantPROutside, grantRepoOutside, 0.5))
}

func restrictedCaller(t *testing.T, effective ...string) storage.Principal {
	t.Helper()
	return userprincipal.Narrowed(t, "org_1", effective, effective)
}

func resolveGranted(t *testing.T, backend *fakeGraphBackend, principal storage.Principal, census CensusFunc, mutate ...func(*contextfabric.InvestigationRequest, *ResolveDeps)) (contextfabric.SubjectResolution, *captureResolutionTracer) {
	t.Helper()
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = census
	request := testRequest()
	request.Question = grantQuestion
	for _, m := range mutate {
		m(&request, &deps)
	}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted("pull request 5063"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return resolution, tracer
}

func onlyRound(t *testing.T, tracer *captureResolutionTracer) ResolutionTraceEvent {
	t.Helper()
	rounds := tracer.eventsForStage("evidence_round")
	if len(rounds) != 1 {
		t.Fatalf("evidence_round events = %d, want 1", len(rounds))
	}
	return rounds[0]
}

func TestEveryCensusCallOfARoundCarriesTheGrant(t *testing.T) {
	t.Parallel()
	grant := []string{grantRepoInside, grantRepoBeside}
	assertCarried := func(t *testing.T, spy *grantCensusSpy, minimum int) {
		t.Helper()
		if len(spy.calls) < minimum {
			t.Fatalf("census calls = %d, want at least %d: %+v", len(spy.calls), minimum, spy.calls)
		}
		for i, call := range spy.calls {
			if !call.grantBound || !slices.Equal(call.grant, grant) {
				t.Fatalf("census call %d = %+v, want the grant %v on it", i, call, grant)
			}
		}
	}
	t.Run("main loop", func(t *testing.T) {
		t.Parallel()
		spy := &grantCensusSpy{rows: grantRows()}
		_, tracer := resolveGranted(t, defaultGrantBackend(), restrictedCaller(t, grant...), spy.census)
		assertCarried(t, spy, 1)
		if !onlyRound(t, tracer).ShadowGrantScoped {
			t.Fatal("the round did not report a grant-scoped census")
		}
	})
	t.Run("kind proof", func(t *testing.T) {
		t.Parallel()
		// A resolved pool of two kinds is not reachable with this graph fake, so
		// the proof is read at the round, on the context resolve.go binds.
		spy := &grantCensusSpy{rows: []grantRow{{contractsv1.ContextFabricSubjectCIRun, "", "ci_pipeline_run.v2:r-inside:900", grantRepoInside}}}
		input := baseInput()
		input.Question = "how is this repo's CI doing?"
		input.PooledKinds = []CensusKind{contractsv1.ContextFabricSubjectCIRun}
		input.PreNarrowingExplicitKinds = []CensusKind{contextfabric.SubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}
		input.AliasClaimants = map[string][]IdentityMatch{
			"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
		}
		input.Question = "how is this repo's CI doing in dev-health-acr?"
		input.CensusFunc = spy.census
		att := RunShadowEvidenceRound(contextfabric.ScopeCensusToCaller(context.Background(), restrictedCaller(t, grant...)), input, nil)
		if !att.KindInsensitivityEvaluated || att.KindInsensitivityMode != explicitKindNarrowingApplied {
			t.Fatalf("attestation = %+v, want the kind proof run", att)
		}
		assertCarried(t, spy, 2)
	})
	t.Run("handle probe", func(t *testing.T) {
		t.Parallel()
		spy := &grantCensusSpy{rows: grantRows()}
		_, tracer := resolveGranted(t, defaultGrantBackend(), restrictedCaller(t, grant...), spy.census, func(r *contextfabric.InvestigationRequest, d *ResolveDeps) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: "5063"}}
			d.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
				return "git_pull_requests.number", ValidateHandleGrammar(kind, patternID, value)
			}
		})
		if !onlyRound(t, tracer).ShadowHandleInsensitivityEvaluated {
			t.Fatal("the handle probe did not run")
		}
		assertCarried(t, spy, 2)
	})
	t.Run("caller hint and explicit handle", func(t *testing.T) {
		t.Parallel()
		spy := &grantCensusSpy{rows: []grantRow{{contextfabric.SubjectPullRequest, "747", explicitHandlePRID, grantRepoInside}}}
		var ignored []explicitHandleCensusCall
		deps, request := explicitHandleFixtureInside(grantRepoInside, &ignored)
		deps.CensusFunc = spy.census
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), restrictedCaller(t, grant...), request, testInterpreted("dev-health-acr"), deps, nil, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		assertCarried(t, spy, 2)
		anchored := 0
		for _, call := range spy.calls {
			if call.anchorBound {
				anchored++
			}
		}
		if anchored != 1 {
			t.Fatalf("anchored census calls = %d, want the explicit handle's one: %+v", anchored, spy.calls)
		}
		if ids := scopeAnchorCommittedIDs(resolution); !slices.Contains(ids, explicitHandlePRID) {
			t.Fatalf("committed = %v, want the pull request inside the grant", ids)
		}
		if round := onlyRound(t, tracer); !round.ShadowCallerHintShortCircuit || !round.ShadowGrantScoped {
			t.Fatalf("round = %+v, want the caller-hint round scoped to the grant", round)
		}
	})
}

// A census that returns without applying the grant is refused: the wiring
// never trusts a count it cannot prove came from inside the grant.
func TestACensusThatIgnoresTheGrantDecidesNothing(t *testing.T) {
	t.Parallel()
	ignoring := func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: grantPRInside}, nil
	}
	t.Run("round", func(t *testing.T) {
		t.Parallel()
		resolution, tracer := resolveGranted(t, grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5)), restrictedCaller(t, grantRepoInside), ignoring)
		if round := onlyRound(t, tracer); round.ShadowOutcome != string(ShadowWouldClarify) || round.ShadowReason != string(ReasonCensusError) {
			t.Fatalf("round = %q/%q, want would_clarify/census_error", round.ShadowOutcome, round.ShadowReason)
		}
		if len(tracer.eventsForStage("evidence_census_commit")) != 0 {
			t.Fatalf("a commit decision came from a census that ignored the grant")
		}
		if resolution.Committed != nil && len(resolution.Committed) != 0 && tracerCommittedByCensus(tracer) {
			t.Fatalf("committed = %v by the census", resolution.Committed)
		}
	})
	t.Run("explicit handle", func(t *testing.T) {
		t.Parallel()
		var ignored []explicitHandleCensusCall
		deps, request := explicitHandleFixtureInside(grantRepoInside, &ignored)
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), restrictedCaller(t, grantRepoInside), request, testInterpreted("dev-health-acr"), deps, nil, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(ignored) == 0 {
			t.Fatal("the census never ran: the refusal was not exercised")
		}
		if ids := scopeAnchorCommittedIDs(resolution); slices.Contains(ids, explicitHandlePRID) {
			t.Fatalf("committed = %v, want no pull request from a census that ignored the grant", ids)
		}
		var refused bool
		for _, event := range tracer.eventsForStage("evidence_census_commit") {
			refused = refused || (event.Outcome == "refused" && event.CensusCommitHandleExplicit)
		}
		if !refused {
			t.Fatalf("evidence_census_commit = %+v, want the explicit handle refused", tracer.eventsForStage("evidence_census_commit"))
		}
	})
	t.Run("handle verification", func(t *testing.T) {
		t.Parallel()
		ctx := contextfabric.ScopeCensusToCaller(context.Background(), restrictedCaller(t, grantRepoInside))
		ok, reason := VerifyHandle(ctx, "org_1", contextfabric.SubjectPullRequest, "pull_request_number", "5063", ignoring)
		if ok || reason != HandleVerificationCensusUnavailable {
			t.Fatalf("VerifyHandle = %v %q, want the census refused", ok, reason)
		}
		if ok, reason := VerifyHandle(context.Background(), "org_1", contextfabric.SubjectPullRequest, "pull_request_number", "5063", ignoring); !ok || reason != HandleVerificationValid {
			t.Fatalf("control, no grant bound: VerifyHandle = %v %q, want valid", ok, reason)
		}
	})
}

// explicitHandleFixtureInside is explicitHandleFixture with its graph nodes in
// one repository, so a restricted caller of that repository can see them.
func explicitHandleFixtureInside(repo string, calls *[]explicitHandleCensusCall) (ResolveDeps, contextfabric.InvestigationRequest) {
	deps, request := explicitHandleFixture(1, calls)
	inner := deps.ExactHint
	deps.ExactHint = func(ctx context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
		node, ok, err := inner(ctx, subject)
		if ok {
			node.Attributes["authorization_repositories"] = []string{repo}
		}
		return node, ok, err
	}
	return deps, request
}

func tracerCommittedByCensus(tracer *captureResolutionTracer) bool {
	for _, e := range tracer.eventsForStage("decision") {
		if e.Outcome == "committed" && e.CommitGate == "evidence_census" {
			return true
		}
	}
	return false
}

func TestVerifyHandleReadsInsideTheBoundGrant(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{rows: grantRows()[1:]}
	ctx := contextfabric.ScopeCensusToCaller(context.Background(), restrictedCaller(t, grantRepoInside))
	ok, reason := VerifyHandle(ctx, "org_1", contextfabric.SubjectPullRequest, "pull_request_number", "5063", spy.census)
	if ok || reason != HandleVerificationNotFound {
		t.Fatalf("VerifyHandle on a handle only outside the grant = %v %q, want not found", ok, reason)
	}
	if len(spy.calls) != 1 || !spy.calls[0].grantBound {
		t.Fatalf("census calls = %+v, want one with the grant", spy.calls)
	}
}

// The rescue: "pull request 5063" for a restricted caller. The search stalls
// and the census, inside the grant, names one pull request.
func TestTheCensusRescuesAPullRequestHandleForARestrictedCaller(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		backend *fakeGraphBackend
	}{
		{"pooled beside an outside node", defaultGrantBackend()},
		{"lone pooled candidate", grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5))},
		{"pooled beside a rival inside the grant", func() *fakeGraphBackend {
			backend := grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5), grantNode(grantPRBeside, grantRepoBeside, 0.5))
			return backend
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: grantRows()}
			resolution, tracer := resolveGranted(t, tc.backend, restrictedCaller(t, grantRepoInside, grantRepoBeside), spy.census)
			round := onlyRound(t, tracer)
			if round.ShadowOutcome != string(ShadowWouldCommit) || !round.ShadowGrantScoped || round.ShadowUnscopedVisibility {
				t.Fatalf("round = outcome %q reason %q grant %v unscoped %v, want would_commit inside the grant", round.ShadowOutcome, round.ShadowReason, round.ShadowGrantScoped, round.ShadowUnscopedVisibility)
			}
			commits := tracer.eventsForStage("evidence_census_commit")
			if len(commits) != 1 || commits[0].Outcome != "merged" {
				t.Fatalf("evidence_census_commit = %+v, want one merged", commits)
			}
			if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != grantPRInside {
				t.Fatalf("committed = %v, want the pull request inside the grant", ids)
			}
			if !tracerCommittedByCensus(tracer) {
				t.Fatal("no evidence_census commit decision")
			}
		})
	}
}

// The search reached no pull request at all: the census satisfier is the only
// candidate, and it enters the pool through the keyed graph read.
func TestAMergedLoneCandidateCommits(t *testing.T) {
	t.Parallel()
	backend := grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5))
	backend.searchResults = map[string][]CandidateNode{}
	spy := &grantCensusSpy{rows: grantRows()}
	resolution, tracer := resolveGranted(t, backend, restrictedCaller(t, grantRepoInside), spy.census)
	commits := tracer.eventsForStage("evidence_census_commit")
	if len(commits) != 1 || commits[0].Outcome != "merged" {
		t.Fatalf("evidence_census_commit = %+v, want one merged", commits)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != grantPRInside {
		t.Fatalf("committed = %v, want the pull request the census named", ids)
	}
}

func TestAGrantScopedRoundAppliesARequestInsideTheGrant(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		spy := &grantCensusSpy{rows: []grantRow{
			{contextfabric.SubjectPullRequest, "5063", grantPRInside, grantRepoInside},
			{contextfabric.SubjectPullRequest, "5063", grantPRBeside, grantRepoBeside},
		}}
		backend := grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5), grantNode(grantPRBeside, grantRepoBeside, 0.5))
		resolution, tracer := resolveGranted(t, backend, restrictedCaller(t, grantRepoInside, grantRepoBeside), spy.census, func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{grantRepoBeside}
		})
		if len(spy.calls) == 0 || !spy.calls[0].grantBound || !slices.Equal(spy.calls[0].filter, []string{grantRepoBeside}) {
			t.Fatalf("census calls = %+v, want the grant and the requested repository", spy.calls)
		}
		if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != grantPRBeside {
			t.Fatalf("committed = %v, want the pull request of the requested repository", ids)
		}
		if round := onlyRound(t, tracer); !round.ShadowGrantScoped {
			t.Fatalf("round = %+v, want it grant-scoped", round)
		}
	})
	t.Run("a filtered satisfier outside the request is refused", func(t *testing.T) {
		t.Parallel()
		wrong := func(ctx context.Context, _ string, _ CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: grantPRInside, GrantApplied: true, RepositoryFilterApplied: true}, nil
		}
		backend := grantBackend(grantNode(grantPRInside, grantRepoInside, 0.5), grantNode(grantPRBeside, grantRepoBeside, 0.5))
		_, tracer := resolveGranted(t, backend, restrictedCaller(t, grantRepoInside, grantRepoBeside), wrong, func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{grantRepoBeside}
		})
		if round := onlyRound(t, tracer); round.ShadowReason != string(ReasonCensusClosureMismatch) {
			t.Fatalf("round reason = %q, want the cross-check to refuse the satisfier", round.ShadowReason)
		}
	})
	t.Run("team", func(t *testing.T) {
		t.Parallel()
		spy := &grantCensusSpy{rows: []grantRow{
			{contextfabric.SubjectPullRequest, "5063", grantPRInside, grantRepoInside},
			{contextfabric.SubjectPullRequest, "5063", grantPRBeside, grantRepoBeside},
		}}
		inside, beside := grantNode(grantPRInside, grantRepoInside, 0.5), grantNode(grantPRBeside, grantRepoBeside, 0.5)
		inside.Attributes["authorization_teams"] = "*"
		beside.Attributes["authorization_teams"] = "*"
		resolution, tracer := resolveGranted(t, grantBackend(inside, beside), restrictedCaller(t, grantRepoInside, grantRepoBeside), spy.census, func(r *contextfabric.InvestigationRequest, d *ResolveDeps) {
			r.RequestedScope.TeamIDs = []string{"team_1"}
			d.TeamNarrowing = func(_ context.Context, subjects []contextfabric.SubjectRef, _ bool) (TeamNarrowingDecision, error) {
				decision := TeamNarrowingDecision{In: map[string]bool{}, Out: map[string]bool{}}
				for _, subject := range subjects {
					decision.In[subject.CanonicalID] = subject.CanonicalID == grantPRBeside
					decision.Out[subject.CanonicalID] = subject.CanonicalID != grantPRBeside
				}
				return decision, nil
			}
		})
		round := onlyRound(t, tracer)
		if round.ShadowCallerNarrowing != "narrowed_to_one" || !round.ShadowGrantScoped {
			t.Fatalf("round = narrowing %q grant %v, want narrowed_to_one inside the grant", round.ShadowCallerNarrowing, round.ShadowGrantScoped)
		}
		if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != grantPRBeside {
			t.Fatalf("committed = %v, want the requested team's pull request", ids)
		}
		if len(spy.calls) == 0 || !spy.calls[0].grantBound || len(spy.calls[0].filter) != 0 {
			t.Fatalf("census calls = %+v, want the grant and no repository filter", spy.calls)
		}
	})
}

// A narrowing the census cannot apply gets no census, exactly as it does for
// an unrestricted caller: a satisfier outside the request must not decide.
func TestARestrictedCallerWithANarrowingTheCensusCannotApplyGetsNoCensus(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*contextfabric.InvestigationRequest, *ResolveDeps){
		"project": func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.ProjectIDs = []string{"project_1"}
		},
		"repository and project": func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{grantRepoInside}
			r.RequestedScope.ProjectIDs = []string{"project_1"}
		},
		"repository and team": func(r *contextfabric.InvestigationRequest, d *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{grantRepoInside}
			r.RequestedScope.TeamIDs = []string{"team_1"}
			d.TeamNarrowing = func(context.Context, []contextfabric.SubjectRef, bool) (TeamNarrowingDecision, error) {
				return TeamNarrowingDecision{}, nil
			}
		},
		"team with no team narrowing wired": func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.TeamIDs = []string{"team_1"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: grantRows()}
			_, tracer := resolveGranted(t, defaultGrantBackend(), restrictedCaller(t, grantRepoInside), spy.census, mutate)
			if len(spy.calls) != 0 {
				t.Fatalf("census calls = %+v, want none", spy.calls)
			}
			if round := onlyRound(t, tracer); round.ShadowReason != string(ReasonScopedVisibility) || round.ShadowGrantScoped {
				t.Fatalf("round = %q grant %v, want scoped_visibility", round.ShadowReason, round.ShadowGrantScoped)
			}
		})
	}
}

func TestAProjectOnlyCallerGetsNoCensus(t *testing.T) {
	t.Parallel()
	principal := userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"})
	cases := map[string]func(*contextfabric.InvestigationRequest, *ResolveDeps){
		"no request": func(*contextfabric.InvestigationRequest, *ResolveDeps) {},
		"a requested repository": func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.RequestedScope.RepositorySlugs = []string{grantRepoInside}
		},
		"a requested team": func(r *contextfabric.InvestigationRequest, d *ResolveDeps) {
			r.RequestedScope.TeamIDs = []string{"team_1"}
			d.TeamNarrowing = func(context.Context, []contextfabric.SubjectRef, bool) (TeamNarrowingDecision, error) {
				return TeamNarrowingDecision{}, nil
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: grantRows()}
			resolution, tracer := resolveGranted(t, defaultGrantBackend(), principal, spy.census, mutate)
			if len(spy.calls) != 0 {
				t.Fatalf("census calls = %+v, want none", spy.calls)
			}
			if round := onlyRound(t, tracer); round.ShadowReason != string(ReasonScopedVisibility) || round.ShadowGrantScoped {
				t.Fatalf("round = %q grant %v, want scoped_visibility", round.ShadowReason, round.ShadowGrantScoped)
			}
			if len(resolution.Committed) != 0 {
				t.Fatalf("committed = %v, want none", resolution.Committed)
			}
		})
	}
	// Whatever reads the census for this caller reads the reserved value, which
	// no census can apply.
	ctx := contextfabric.ScopeCensusToCaller(context.Background(), principal)
	if grant, bound := contextfabric.CensusGrantFrom(ctx); !bound || len(grant) != 1 || !contractsv1.IsReservedRepositoryScope(grant[0]) {
		t.Fatalf("grant = %v bound %v, want the reserved value", grant, bound)
	}
}

// An unrestricted or universal caller's census is what it was: nothing bound,
// no GrantApplied required, the same trace.
func TestUnrestrictedCensusIsByteIdentical(t *testing.T) {
	t.Parallel()
	principals := map[string]storage.Principal{
		"unrestricted": userprincipal.Authenticated(t, "org_1", nil),
		"universal":    userprincipal.Authenticated(t, "org_1", []string{"*"}),
	}
	for name, principal := range principals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: grantRows()}
			resolution, tracer := resolveGranted(t, defaultGrantBackend(), principal, spy.census)
			if len(spy.calls) == 0 {
				t.Fatal("no census ran")
			}
			for _, call := range spy.calls {
				if call.grantBound {
					t.Fatalf("census call = %+v, want no grant bound", call)
				}
			}
			round := onlyRound(t, tracer)
			if !round.ShadowUnscopedVisibility || round.ShadowGrantScoped {
				t.Fatalf("round = unscoped %v grant %v, want the unscoped census as before", round.ShadowUnscopedVisibility, round.ShadowGrantScoped)
			}
			// Both pull requests satisfy for this caller: no commit, as before.
			if len(resolution.Committed) != 0 {
				t.Fatalf("committed = %v, want a clarification between the two", resolution.Committed)
			}
		})
	}
}

// PR2 owns the work-item read. Until then a work-item census for a restricted
// caller is refused, for every provider's handle: the round stays a
// clarification and nothing reads the organization.
func TestAWorkItemCensusForARestrictedCallerIsRefusedForEveryProvider(t *testing.T) {
	t.Parallel()
	for provider, key := range map[string]string{"linear": "CHAOS-11516", "jira": "CHAOS-204", "github": "CHAOS-77", "gitlab": "CHAOS-78"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			id := "work_item.v2:" + provider + ":" + key
			node := candidateNode(contextfabric.SubjectWorkItem, id, key+" login bug", 0.5, []string{grantRepoInside})
			backend := &fakeGraphBackend{
				searchResults: map[string][]CandidateNode{key: {node}}, searchTruncated: true,
				exactHints: map[string]CandidateNode{SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id}): node},
			}
			spy := &grantCensusSpy{rows: []grantRow{{contextfabric.SubjectWorkItem, key, id, grantRepoInside}}}
			deps := backend.deps()
			tracer := &captureResolutionTracer{}
			deps.ResolutionTracer = tracer
			deps.CensusFunc = spy.census
			request := testRequest()
			request.Question = "why is " + key + " blocked?"
			resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), restrictedCaller(t, grantRepoInside), request, testInterpreted(key), deps, nil, nil, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			round := onlyRound(t, tracer)
			if len(spy.calls) == 0 {
				t.Fatalf("round reason %q: no census call, the refusal was not exercised", round.ShadowReason)
			}
			if round.ShadowOutcome != string(ShadowWouldClarify) || round.ShadowReason != string(ReasonCensusError) {
				t.Fatalf("round = %q/%q, want would_clarify/census_error", round.ShadowOutcome, round.ShadowReason)
			}
			if tracerCommittedByCensus(tracer) || len(tracer.eventsForStage("evidence_census_commit")) != 0 {
				t.Fatalf("committed = %v by the census, want nothing", resolution.Committed)
			}
		})
	}
}

// CHAOS-12446 ruling (a), FINAL: the count of matches outside the requested
// repositories holds what the caller's own grant admits, and only ordinary
// search feeds it. The grant-scoped census reads inside the grant AND the
// request, so it neither adds to that count nor contradicts it.
func TestTheCensusAndTheOutsideCountAgreeForARestrictedCaller(t *testing.T) {
	t.Parallel()
	grant := []string{grantRepoInside, grantRepoBeside}
	narrowed := func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
		r.RequestedScope.RepositorySlugs = []string{grantRepoInside}
	}
	beside := grantRow{contextfabric.SubjectPullRequest, "5063", grantPRBeside, grantRepoBeside}
	inside := grantRow{contextfabric.SubjectPullRequest, "5063", grantPRInside, grantRepoInside}
	outside := grantRow{contextfabric.SubjectPullRequest, "5063", grantPROutside, grantRepoOutside}
	pool := func() *fakeGraphBackend {
		return grantBackend(grantNode(grantPRInside, grantRepoInside, 0.8), grantNode(grantPRBeside, grantRepoBeside, 0.8), grantNode(grantPROutside, grantRepoOutside, 0.8))
	}
	t.Run("a match inside the grant and outside the request", func(t *testing.T) {
		t.Parallel()
		backend := grantBackend(grantNode(grantPRBeside, grantRepoBeside, 0.8), grantNode(grantPROutside, grantRepoOutside, 0.8))
		spy := &grantCensusSpy{rows: []grantRow{beside, outside}}
		resolution, tracer := resolveGranted(t, backend, restrictedCaller(t, grant...), spy.census, narrowed)
		if len(spy.calls) == 0 {
			t.Fatal("no census ran")
		}
		if resolution.OutsideRequestedRepositories != 1 {
			t.Fatalf("outside %d, want 1: the match the grant covers, once, and never the hidden one", resolution.OutsideRequestedRepositories)
		}
		if len(resolution.Committed) != 0 {
			t.Fatalf("committed = %v, want none: the narrowing stands", resolution.Committed)
		}
		if round := onlyRound(t, tracer); round.ShadowCallerNarrowing != narrowedToNone || round.ShadowOutcome == string(ShadowWouldCommit) {
			t.Fatalf("round = narrowing %q outcome %q, want narrowed_to_none and no commit", round.ShadowCallerNarrowing, round.ShadowOutcome)
		}
		// The same turn with no census: the count is search's alone.
		withoutCensus, _ := resolveGranted(t, backend, restrictedCaller(t, grant...), nil, narrowed)
		if withoutCensus.OutsideRequestedRepositories != resolution.OutsideRequestedRepositories {
			t.Fatalf("outside %d with a census, %d without: the census changed the count", resolution.OutsideRequestedRepositories, withoutCensus.OutsideRequestedRepositories)
		}
	})
	t.Run("a match in the request beside one outside it", func(t *testing.T) {
		t.Parallel()
		spy := &grantCensusSpy{rows: []grantRow{inside, beside, outside}}
		resolution, tracer := resolveGranted(t, pool(), restrictedCaller(t, grant...), spy.census, narrowed)
		if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != grantPRInside {
			t.Fatalf("committed = %v, want the match in the request", ids)
		}
		if resolution.OutsideRequestedRepositories != 1 {
			t.Fatalf("outside %d, want 1: the committed subject is not outside, the hidden one never counts", resolution.OutsideRequestedRepositories)
		}
		if probes := tracer.eventsForStage("evidence_probe"); len(probes) != 1 || probes[0].CensusCount != 1 {
			t.Fatalf("evidence_probe = %+v, want count 1 inside the grant and the request", probes)
		}
	})
	t.Run("a census satisfier outside the request never enters the count", func(t *testing.T) {
		t.Parallel()
		// The requested filter could not apply, so the census read the grant
		// alone and named a subject outside the request that search never found.
		fallback := func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: grantPRBeside, GrantApplied: true}, nil
		}
		backend := grantBackend(grantNode(grantPRBeside, grantRepoBeside, 0.8))
		backend.searchResults = map[string][]CandidateNode{}
		resolution, tracer := resolveGranted(t, backend, restrictedCaller(t, grant...), fallback, narrowed)
		if commits := tracer.eventsForStage("evidence_census_commit"); len(commits) != 1 || commits[0].Outcome != "refused" {
			t.Fatalf("evidence_census_commit = %+v, want the satisfier refused by the request", commits)
		}
		if resolution.OutsideRequestedRepositories != 0 || len(resolution.Committed) != 0 {
			t.Fatalf("outside %d committed %v, want 0 and none: search is the only source of the count", resolution.OutsideRequestedRepositories, resolution.Committed)
		}
	})
}

// A handle option is minted from the question alone, so a restricted and a
// project-only caller both hold a handr_ receipt to redeem: the redemption's
// census was reachable for both before CHAOS-12452 bound the grant on it.
func TestAHandleOfferReachesTheCallersARedemptionRefuses(t *testing.T) {
	t.Parallel()
	for name, principal := range map[string]storage.Principal{
		"restricted":   restrictedCaller(t, grantRepoInside),
		"project only": userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeGraphBackend{searchTruncated: true}
			deps := backend.deps()
			deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
				return "work_items.work_item_id", ValidateHandleGrammar(kind, patternID, value)
			}
			spy := &grantCensusSpy{}
			deps.CensusFunc = spy.census
			request := testRequest()
			request.Question = "why is CHAOS-11516 blocked?"
			_, offers, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted("CHAOS-11516"), deps, nil, nil, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(offers.HandleOptions) != 1 || offers.HandleOptions[0].Kind != contextfabric.SubjectWorkItem || offers.HandleOptions[0].Value != "CHAOS-11516" {
				t.Fatalf("handle options = %+v, want the work item the question names", offers.HandleOptions)
			}
		})
	}
}

func TestErrCensusGrantNotAppliedIsACensusError(t *testing.T) {
	t.Parallel()
	refused := func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{}, ErrCensusGrantNotApplied
	}
	_, tracer := resolveGranted(t, defaultGrantBackend(), restrictedCaller(t, grantRepoInside), refused)
	round := onlyRound(t, tracer)
	if round.ShadowOutcome != string(ShadowWouldClarify) || round.ShadowReason != string(ReasonCensusError) {
		t.Fatalf("round = %q/%q, want would_clarify/census_error", round.ShadowOutcome, round.ShadowReason)
	}
	if !errors.Is(ErrCensusGrantNotApplied, ErrCensusGrantNotApplied) || strings.Contains(ErrCensusGrantNotApplied.Error(), "/") {
		t.Fatalf("error text %q must name no repository", ErrCensusGrantNotApplied)
	}
}
