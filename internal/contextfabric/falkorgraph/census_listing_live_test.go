//go:build sizeproof

package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12467, on real stores: seeded ClickHouse rows, projected by the real
// producers into a real FalkorDB, read by the real Engine, resolver and census.
// The turn stops at a stub synthesizer: reaching it is what "committed" means
// here, and no model is called.

// listingStageTracer keeps the census lines of a turn.
type listingStageTracer struct {
	mu     sync.Mutex
	events []graphrank.ResolutionTraceEvent
}

func (r *listingStageTracer) Trace(event graphrank.ResolutionTraceEvent) {
	if event.Stage != "evidence_round" && event.Stage != "evidence_probe" && event.Stage != "evidence_census_listing" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *listingStageTracer) take() []graphrank.ResolutionTraceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

// listingStore is the memory store with the graph epoch the Postgres store
// returns, so a receipt of an earlier turn is honoured.
type listingStore struct {
	*memoryinvestigation.Store
	epoch int64
}

func (s listingStore) Get(ctx context.Context, principal storage.Principal, id string) (contextfabric.StoredInvestigationResult, error) {
	stored, err := s.Store.Get(ctx, principal, id)
	stored.GraphEpoch = &s.epoch
	return stored, err
}

type listingLiveTurn struct {
	result    contextfabric.InvestigationResult
	committed []string
	trace     []graphrank.ResolutionTraceEvent
}

func (l listingLiveTurn) count() int {
	for _, event := range l.trace {
		if event.Stage == "evidence_probe" {
			return event.CensusCount
		}
	}
	return -1
}

func (l listingLiveTurn) ran() bool {
	return slices.ContainsFunc(l.trace, func(event graphrank.ResolutionTraceEvent) bool { return event.Stage == "evidence_round" })
}

var rawIDInText = regexp.MustCompile(`pull_request:|work_item\.v2|ci_pipeline_run|[0-9a-f]{8}-[0-9a-f]{4}|%3A|\[census witness\]`)

func TestACensusCountIsListedOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := scopedClickHouse(t, ctx)
	tracer := &listingStageTracer{}
	adapter := scopedFalkor(t, ctx, query, tracer)
	org := scopedUUID("org-12467")
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-20 * 24 * time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	repo := map[string]string{}
	addRepo := func(slug, provider string) {
		repo[slug] = scopedUUID("listing repo " + slug)
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo[slug], org, slug, provider, now)
	}
	addPR := func(slug string, number uint32, title string) {
		exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`, repo[slug], org, number, title, "merged", created, now)
	}
	var inside []string
	for i := range 10 {
		slug, provider := fmt.Sprintf("acme/r%02d", i), "github"
		if i >= 5 {
			provider = "gitlab"
		}
		addRepo(slug, provider)
		inside = append(inside, slug)
		addPR(slug, 12, "Refactor the widget cache")
	}
	addRepo("other/secret", "github")
	addPR("other/secret", 12, "Hidden change")
	addPR("acme/r00", 77, "Lone change")
	addPR("other/secret", 78, "Lone change outside")
	addPR("acme/r00", 79, "The link's own pull request")
	addPR("acme/r01", 79, "The link's own second pull request")
	for i := range 3 {
		exec("ci run", `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`, repo[inside[i]], "4242", "success", created, now, org)
	}
	for _, item := range [][3]string{{"linear:CHAOS-70", "linear", "issue"}, {"jira:CHAOS-70", "jira", "story"}} {
		exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item[0], scopedZeroRepo, org, "Split the job", item[2], "open", item[1], "", created, now, now)
	}
	for i, link := range [][2]string{{"linear:CHAOS-70", "acme/r00"}, {"jira:CHAOS-70", "acme/r01"}} {
		exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repo[link[1]], link[0], uint32(79), float32(0.9), "native", "", now.Add(time.Duration(i)*time.Millisecond), org)
	}
	own := func(team string, slugs ...string) {
		exec("team", `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			team, "team "+team, "", created, created, org, "linear", team, []string{}, uint8(1))
		for _, slug := range slugs {
			exec("ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				org, "github", team, repo[slug], slug, "exact", "native", uint8(1), uint16(1), int32(1), created, nil, created)
		}
	}
	own("TTHREE", inside[:3]...)
	own("TONE", inside[3])
	own("TREST", append(append([]string{}, inside[4:]...), "other/secret")...)
	own("TONLY", "other/secret")
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	scopedDrain(t, ctx, main, devhealthsource.SourceName, org, adapter)
	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	scopedDrain(t, ctx, teams, devhealthsource.TeamsProjectsSourceName, org, adapter)
	// After the drain: in the census, not in the graph.
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	prBatch, err := direct.PrepareBatch(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range graphrank.CensusSatisfierSetBudget + 2 {
		slug := fmt.Sprintf("acme/big%04d", i)
		id := scopedUUID("listing repo " + slug)
		if err := batch.Append(id, org, slug, "github", now); err != nil {
			t.Fatal(err)
		}
		if err := prBatch.Append(id, org, uint32(13), "Bulk change", "merged", created, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
	if err := prBatch.Send(); err != nil {
		t.Fatal(err)
	}

	unrestricted := storage.Principal{OrgID: org, Subject: "u", CredentialID: "c"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, unrestricted)
	if err != nil {
		t.Fatal(err)
	}
	store := listingStore{Store: memoryinvestigation.NewStore(), epoch: binding.Epoch}
	results := 0
	turn := func(t *testing.T, principal storage.Principal, question string, terms []string, edit func(*contextfabric.InvestigationRequest)) listingLiveTurn {
		t.Helper()
		var committed []string
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: fixedInterpreter{interpretation: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: terms,
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
			}},
			Graph: adapter, Facts: emptyFactReader{},
			Synthesizer: synthesizeFunc(func(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
				for _, subject := range input.Graph.Resolution.Committed {
					committed = append(committed, subject.CanonicalID)
				}
				return contextfabric.InvestigationResult{}, errors.New("no model in this test")
			}),
			Results: store, Requirements: productionRequirementDeriver{},
		}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string {
			results++
			return fmt.Sprintf("result_12467%03d", results)
		}})
		if err != nil {
			t.Fatal(err)
		}
		tracer.take()
		request := contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12467001", Question: question,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		}
		if edit != nil {
			edit(&request)
		}
		runCtx, _ := contextfabric.WithSynthesisInputCollector(ctx)
		result, err := engine.Investigate(runCtx, principal, request)
		if err != nil && len(committed) == 0 {
			t.Fatalf("Investigate: %v", err)
		}
		return listingLiveTurn{result, committed, tracer.take()}
	}
	false1 := "Retrieval found no candidate"
	saysNothingFound := func(t *testing.T, result contextfabric.InvestigationResult) {
		t.Helper()
		for _, limitation := range result.Limitations {
			if strings.HasPrefix(limitation, false1) || strings.HasPrefix(limitation, "One authorized subject matched") {
				t.Fatalf("limitations %q read as nothing found", result.Limitations)
			}
		}
	}
	// lists is a clarification naming the candidates, with no raw id on show.
	lists := func(t *testing.T, got listingLiveTurn, status contextfabric.InvestigationStatus, want int) {
		t.Helper()
		if got.result.Status != status || len(got.result.SubjectResolution.Candidates) != want {
			t.Fatalf("status %q candidates %d, want %q and %d", got.result.Status, len(got.result.SubjectResolution.Candidates), status, want)
		}
		saysNothingFound(t, got.result)
		shown := got.result.DeterministicAnswer + " " + strings.Join(got.result.Limitations, " ")
		for _, candidate := range got.result.SubjectResolution.Candidates {
			shown += " " + candidate.Subject.Label + " " + strings.Join(candidate.MatchedTerms, " ")
		}
		if got.result.StructureNeeds != nil {
			for _, option := range got.result.StructureNeeds.CandidateOptions {
				shown += " " + option.Label
			}
			for _, option := range got.result.StructureNeeds.HandleOptions {
				if option.Value == "12" || option.Value == "CHAOS-70" {
					t.Fatalf("handle options = %+v: a handle several subjects satisfy cannot decide", got.result.StructureNeeds.HandleOptions)
				}
			}
		}
		if raw := rawIDInText.FindString(shown); raw != "" {
			t.Fatalf("%q is on show: %s", raw, shown)
		}
	}
	const pr12 = "What is the state of pull request 12?"
	term := []string{"pull request 12"}
	restricted := userprincipal.Narrowed(t, org, []string{"acme/*"}, inside)
	team := func(id string) func(*contextfabric.InvestigationRequest) {
		return func(r *contextfabric.InvestigationRequest) { r.RequestedScope.TeamIDs = []string{id} }
	}
	repos := func(slug string) func(*contextfabric.InvestigationRequest) {
		return func(r *contextfabric.InvestigationRequest) { r.RequestedScope.RepositorySlugs = []string{slug} }
	}
	noClarification := func(r *contextfabric.InvestigationRequest) { r.Options.AllowClarification = false }

	t.Run("restricted: the count is listed, whatever the terms", func(t *testing.T) {
		for _, terms := range [][]string{term, {"12"}, {}, {"state"}} {
			got := turn(t, restricted, pr12, terms, nil)
			if got.count() != 10 {
				t.Fatalf("terms %q: census count %d, want 10: the outside row is not counted", terms, got.count())
			}
			lists(t, got, contextfabric.InvestigationClarificationRequired, 10)
			if strings.Contains(fmt.Sprintf("%+v", got.result.SubjectResolution.Candidates), "other/secret") {
				t.Fatalf("a candidate names the repository outside the grant: %+v", got.result.SubjectResolution.Candidates)
			}
		}
	})
	t.Run("clarification not allowed: no_match, with the candidates and a true sentence", func(t *testing.T) {
		lists(t, turn(t, restricted, pr12, term, noClarification), contextfabric.InvestigationNoMatch, 10)
	})
	t.Run("unrestricted: the same, with the round on a search that was not truncated", func(t *testing.T) {
		for _, terms := range [][]string{term, {}, {"state"}} {
			got := turn(t, unrestricted, pr12, terms, nil)
			if got.count() != 11 {
				t.Fatalf("terms %q: census count %d, want 11", terms, got.count())
			}
			lists(t, got, contextfabric.InvestigationClarificationRequired, 10)
			if want := contractsv1.ContextFabricCensusListedLimitation(11, 10); !slices.Contains(got.result.Limitations, want) {
				t.Fatalf("limitations %q lack %q", got.result.Limitations, want)
			}
		}
		lists(t, turn(t, unrestricted, pr12, term, noClarification), contextfabric.InvestigationNoMatch, 10)
	})
	t.Run("unrestricted: a lone satisfier commits, and repos= still decides", func(t *testing.T) {
		if got := turn(t, unrestricted, "What is the state of pull request 77?", []string{}, nil); len(got.committed) != 1 {
			t.Fatalf("committed = %v, want the lone satisfier", got.committed)
		}
		outside := turn(t, unrestricted, "What is the state of pull request 78?", []string{"pull request 78"}, repos("acme/r00"))
		if len(outside.committed) != 0 || len(outside.result.SubjectResolution.Candidates) != 0 || outside.result.SubjectResolution.CensusLimitation != "" {
			t.Fatalf("committed %v candidates %+v: a satisfier outside the request decided", outside.committed, outside.result.SubjectResolution.Candidates)
		}
		if got := turn(t, unrestricted, pr12, term, repos("acme/r03")); len(got.committed) != 1 {
			t.Fatalf("committed = %v, want the one inside the request", got.committed)
		}
	})
	t.Run("the next turn: a receipt, or a narrowing", func(t *testing.T) {
		first := turn(t, restricted, pr12, term, nil)
		chosen := first.result.SubjectResolution.Candidates[1]
		got := turn(t, restricted, pr12, term, func(r *contextfabric.InvestigationRequest) {
			r.PriorSubjectReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: first.result.ResultID, ReceiptID: chosen.ReceiptID}}
		})
		if len(got.committed) != 1 || got.committed[0] != chosen.Subject.CanonicalID {
			t.Fatalf("committed = %v, want the receipt's %s", got.committed, chosen.Subject.CanonicalID)
		}
		if got := turn(t, restricted, pr12, term, repos("acme/r03")); len(got.committed) != 1 {
			t.Fatalf("repos=: committed = %v, want one", got.committed)
		}
	})
	t.Run("team narrowing", func(t *testing.T) {
		lists(t, turn(t, restricted, pr12, term, team("TTHREE")), contextfabric.InvestigationClarificationRequired, 3)
		if got := turn(t, restricted, pr12, term, team("TONE")); len(got.committed) != 1 {
			t.Fatalf("narrowed_to_one: committed = %v", got.committed)
		}
		lists(t, turn(t, restricted, pr12, term, team("TONLY")), contextfabric.InvestigationClarificationRequired, 10)
	})
	t.Run("a CI run and a work item take the same path", func(t *testing.T) {
		if got := turn(t, restricted, "What is the state of CI run 4242?", []string{}, nil); got.count() != 3 || len(got.result.SubjectResolution.Candidates) != 3 {
			t.Fatalf("CI run: count %d candidates %d", got.count(), len(got.result.SubjectResolution.Candidates))
		}
		for name, principal := range map[string]storage.Principal{"restricted": restricted, "unrestricted": unrestricted} {
			got := turn(t, principal, "Why is CHAOS-70 blocked?", []string{}, nil)
			lists(t, got, contextfabric.InvestigationClarificationRequired, 2)
			labels := got.result.SubjectResolution.Candidates[0].Subject.Label + got.result.SubjectResolution.Candidates[1].Subject.Label
			if !strings.Contains(labels, "(linear)") || !strings.Contains(labels, "(jira)") {
				t.Fatalf("%s: labels %q do not tell the trackers apart", name, labels)
			}
		}
	})
	t.Run("over the set budget the count is stated", func(t *testing.T) {
		const question = "What is the state of pull request 13?"
		got := turn(t, userprincipal.Authenticated(t, org, []string{"acme/*"}), question, []string{"pull request 13"}, nil)
		want := contractsv1.ContextFabricCensusUnlistedLimitation(graphrank.CensusSatisfierSetBudget + 2)
		if got.result.Status != contextfabric.InvestigationNoMatch || !slices.Contains(got.result.Limitations, want) {
			t.Fatalf("status %q limitations %q, want no_match and %q", got.result.Status, got.result.Limitations, want)
		}
		saysNothingFound(t, got.result)
	})
	t.Run("project-only stays as it is", func(t *testing.T) {
		got := turn(t, userprincipal.ProjectOnly(t, org, []string{"project.v2:github:P-1"}), pr12, []string{}, nil)
		if got.ran() || got.result.Status != contextfabric.InvestigationNoMatch || len(got.result.SubjectResolution.Candidates) != 0 {
			t.Fatalf("round %t status %q candidates %d, want no round", got.ran(), got.result.Status, len(got.result.SubjectResolution.Candidates))
		}
	})
}
