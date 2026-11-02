package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12452: the real resolver and the real census adapter, for a real
// principal, over a census source and a fake graph. The body runs twice: on a
// statement model that evaluates what the statement and its bindings carry
// (always), and on real ClickHouse (ACR_CONTAINER_TESTS=1).

const (
	grantNumber   = uint32(5063)
	grantQuestion = "is pull request 5063 ready to merge?"
	grantTerm     = "pull request 5063"
)

// grantRepository is one repository of the world. provider is not in any
// census statement: the rows of two providers must behave the same.
type grantRepository struct {
	id, slug, provider string
}

// grantWorld is one organization with its repositories. add writes one pull
// request row; client is what the census reads.
type grantWorld struct {
	org                                                      string
	insideGitHub, insideGitLab, outsideGitHub, outsideGitLab grantRepository
	// nestedGitLab and malformed sit under the insideGitHub owner, and the
	// graph gate refuses both slugs: they are outside an owner grant.
	nestedGitLab, malformed grantRepository
	client                  contextpacket.ClickHouseQueryClient
	add                     func(t *testing.T, repository grantRepository)
}

type grantStatementLog struct {
	inner contextpacket.ClickHouseQueryClient
	lines []string
}

func (l *grantStatementLog) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	rendered := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		rendered = append(rendered, fmt.Sprintf("%s=%v", binding.Name, binding.Value))
	}
	sort.Strings(rendered)
	l.lines = append(l.lines, statement+" | "+strings.Join(rendered, ","))
	return l.inner.Query(ctx, statement, bindings)
}

type grantTracer struct {
	events []graphrank.ResolutionTraceEvent
}

func (r *grantTracer) Trace(event graphrank.ResolutionTraceEvent) {
	event.CensusReadAtUnix = 0
	r.events = append(r.events, event)
}

func (r *grantTracer) stage(stage string) []graphrank.ResolutionTraceEvent {
	var kept []graphrank.ResolutionTraceEvent
	for _, event := range r.events {
		if event.Stage == stage {
			kept = append(kept, event)
		}
	}
	return kept
}

// grantObservation is everything a caller or an operator can read of one turn:
// the resolution with its candidates and offers, the commit bases, the whole
// trace, each statement with its bindings, and the graph reads.
type grantObservation struct {
	resolution contextfabric.SubjectResolution
	offers     contextfabric.StructureOfferMaterial
	bases      contextfabric.CommitBasisSet
	digests    contextfabric.CommitDecisionDigestSet
	trace      []graphrank.ResolutionTraceEvent
	statements []string
	graphReads []string
}

func grantCanonicalID(world grantWorld, repository grantRepository) string {
	return fmt.Sprintf("pull_request:%s:%d", repository.id, grantNumber)
}

func grantCandidateNode(world grantWorld, repository grantRepository) graphrank.CandidateNode {
	id := grantCanonicalID(world, repository)
	return graphrank.CandidateNode{UUID: "node-" + id, Name: "PR #5063", Relevance: graphrank.Normalized(0.5), Attributes: map[string]interface{}{
		"canonical_id": id, "subject_kind": string(contextfabric.SubjectPullRequest), "label": "PR #5063",
		"evidence_refs": []string{"evidence_identity_1234"}, "authorization_repositories": []string{repository.slug},
	}}
}

// observeGrantTurn runs one turn. The graph is the same in every turn of a
// test: every pull request is a node whether or not its row exists yet, so a
// difference between two turns can only come from the census.
func observeGrantTurn(t *testing.T, world grantWorld) grantObservation {
	t.Helper()
	principal := userprincipal.Narrowed(t, world.org, []string{"acme/*", "group/*"}, []string{world.insideGitHub.slug, world.insideGitLab.slug})
	return observeTurnAs(t, world, principal, nil)
}

// observeTurnAs is observeGrantTurn for one principal, with extra graph nodes.
func observeTurnAs(t *testing.T, world grantWorld, principal storage.Principal, extra map[string]graphrank.CandidateNode) grantObservation {
	t.Helper()
	return observeTurnWith(t, world, principal, extra, 10)
}

// observeTurnWith is observeTurnAs with the request's candidate budget.
func observeTurnWith(t *testing.T, world grantWorld, principal storage.Principal, extra map[string]graphrank.CandidateNode, budget int) grantObservation {
	t.Helper()
	nodes := map[string]graphrank.CandidateNode{}
	for _, repository := range []grantRepository{world.insideGitHub, world.insideGitLab, world.outsideGitHub, world.outsideGitLab} {
		nodes[grantCanonicalID(world, repository)] = grantCandidateNode(world, repository)
	}
	for id, node := range extra {
		nodes[id] = node
	}
	log := &grantStatementLog{inner: world.client}
	tracer := &grantTracer{}
	var graphReads []string
	deps := graphrank.ResolveDeps{
		ExactHint: func(_ context.Context, subject contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
			graphReads = append(graphReads, "exact "+subject.CanonicalID)
			node, ok := nodes[subject.CanonicalID]
			return node, ok, nil
		},
		Search: func(_ context.Context, term string, _ int) ([]graphrank.CandidateNode, bool, bool, error) {
			graphReads = append(graphReads, "search "+term)
			return nil, true, false, nil
		},
		Traverse: func(context.Context, string, graphrank.CandidateNode, bool) (contextfabric.SubjectCandidate, graphrank.ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, graphrank.ObservationNoParent
		},
		IsInternal:       func(contextfabric.SubjectRef) bool { return false },
		ResolutionTracer: tracer,
		CensusFunc:       devhealthsource.NewCensusFunc(log),
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: grantQuestion,
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: budget, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{grantTerm},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, offers, bases, digests, err := graphrank.ResolveSubjectsWithCommitBasis(context.Background(), principal, request, interpreted, deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis: %v", err)
	}
	return grantObservation{resolution, offers, bases, digests, tracer.events, log.lines, graphReads}
}

func assertSameTurn(t *testing.T, before, after grantObservation) {
	t.Helper()
	if !reflect.DeepEqual(before.resolution, after.resolution) {
		t.Fatalf("resolution changed:\nbefore %+v\nafter  %+v", before.resolution, after.resolution)
	}
	if !reflect.DeepEqual(before.offers, after.offers) {
		t.Fatalf("offers changed:\nbefore %+v\nafter  %+v", before.offers, after.offers)
	}
	if !reflect.DeepEqual(before.bases, after.bases) || !reflect.DeepEqual(before.digests, after.digests) {
		t.Fatalf("commit decision changed: %+v %+v / %+v %+v", before.bases, before.digests, after.bases, after.digests)
	}
	if !sameTrace(before.trace, after.trace) {
		t.Fatalf("trace changed:\nbefore %+v\nafter  %+v", before.trace, after.trace)
	}
	if !slices.Equal(before.statements, after.statements) {
		t.Fatalf("statements changed:\nbefore %q\nafter  %q", before.statements, after.statements)
	}
	if !slices.Equal(before.graphReads, after.graphReads) {
		t.Fatalf("graph reads changed:\nbefore %q\nafter  %q", before.graphReads, after.graphReads)
	}
}

// sameTrace compares two traces field by field, but for one: the listing's elapsed
// time is the clock of this run, and no row decides it. The keyed reads that take
// that time are compared here (CensusListingReads) and in graphReads, id by id, so
// rows outside the grant that changed the work would fail on those. Any other
// field that differs fails, a new timing field too: name it here only with that
// same argument.
func sameTrace(before, after []graphrank.ResolutionTraceEvent) bool {
	withoutClock := func(trace []graphrank.ResolutionTraceEvent) []graphrank.ResolutionTraceEvent {
		events := slices.Clone(trace)
		for index := range events {
			events[index].CensusListingElapsedMs = 0
		}
		return events
	}
	return reflect.DeepEqual(withoutClock(before), withoutClock(after))
}

// The comparison leaves out the clock and nothing else: a change in the work a
// listing did still fails it.
func TestTheTraceComparisonLeavesOutOnlyTheClock(t *testing.T) {
	t.Parallel()
	line := graphrank.ResolutionTraceEvent{Stage: "evidence_census_listing", CensusCount: 3, CensusListed: 3, CensusListingReads: 3, CensusListingElapsedMs: 2}
	slower := line
	slower.CensusListingElapsedMs = 90
	if !sameTrace([]graphrank.ResolutionTraceEvent{line}, []graphrank.ResolutionTraceEvent{slower}) {
		t.Fatal("two runs of one turn differ by the clock alone, and the comparison failed them")
	}
	for name, edit := range map[string]func(*graphrank.ResolutionTraceEvent){
		"reads":   func(e *graphrank.ResolutionTraceEvent) { e.CensusListingReads = 4 },
		"count":   func(e *graphrank.ResolutionTraceEvent) { e.CensusCount = 4 },
		"listed":  func(e *graphrank.ResolutionTraceEvent) { e.CensusListed = 2 },
		"refused": func(e *graphrank.ResolutionTraceEvent) { e.CensusListingRefused = 1 },
		"outcome": func(e *graphrank.ResolutionTraceEvent) { e.CensusListingOutcome = "partly_listed" },
	} {
		changed := line
		edit(&changed)
		if sameTrace([]graphrank.ResolutionTraceEvent{line}, []graphrank.ResolutionTraceEvent{changed}) {
			t.Fatalf("%s changed and the comparison passed", name)
		}
	}
	if sameTrace([]graphrank.ResolutionTraceEvent{line}, nil) {
		t.Fatal("a line went missing and the comparison passed")
	}
}

type grantWorldBuilder func(t *testing.T, name string) grantWorld

// outsideRowsChangeNothing adds rows outside the grant, of two providers, to an
// organization and reads the same turn before and after: nothing a caller or
// an operator can read may differ.
func outsideRowsChangeNothing(t *testing.T, build grantWorldBuilder) {
	cases := []struct {
		name    string
		inside  func(grantWorld) []grantRepository
		outcome string
	}{
		{"no row inside the grant", func(grantWorld) []grantRepository { return nil }, string(graphrank.ShadowWouldNoMatch)},
		{"one row inside, github", func(w grantWorld) []grantRepository { return []grantRepository{w.insideGitHub} }, string(graphrank.ShadowWouldCommit)},
		{"one row inside, gitlab", func(w grantWorld) []grantRepository { return []grantRepository{w.insideGitLab} }, string(graphrank.ShadowWouldCommit)},
		{"two rows inside", func(w grantWorld) []grantRepository { return []grantRepository{w.insideGitHub, w.insideGitLab} }, string(graphrank.ShadowWouldClarify)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := build(t, tc.name)
			for _, repository := range tc.inside(world) {
				world.add(t, repository)
			}
			before := observeGrantTurn(t, world)
			rounds := (&grantTracer{events: before.trace}).stage("evidence_round")
			if len(rounds) != 1 || rounds[0].ShadowOutcome != tc.outcome || !rounds[0].ShadowGrantScoped {
				t.Fatalf("evidence_round = %+v, want %s inside the grant", rounds, tc.outcome)
			}
			world.add(t, world.outsideGitHub)
			world.add(t, world.outsideGitLab)
			assertSameTurn(t, before, observeGrantTurn(t, world))
		})
	}
}

// aListingChangesNothingOutsideTheGrant is outsideRowsChangeNothing for each way a
// census listing can end, in one world: outside rows change no candidate, label,
// offer, sentence, trace, statement or graph read. CHAOS-12467.
func aListingChangesNothingOutsideTheGrant(t *testing.T, build grantWorldBuilder) {
	world := build(t, "census listing")
	world.add(t, world.insideGitHub)
	world.add(t, world.insideGitLab)
	principal := userprincipal.Narrowed(t, world.org, []string{"acme/*", "group/*"}, []string{world.insideGitHub.slug, world.insideGitLab.slug})
	refused := map[string]graphrank.CandidateNode{}
	for _, repository := range []grantRepository{world.insideGitHub, world.insideGitLab} {
		node := grantCandidateNode(world, repository)
		node.Attributes["authorization_repositories"] = []string{"other/secret"}
		refused[grantCanonicalID(world, repository)] = node
	}
	besideOutside := grantCandidateNode(world, world.insideGitHub)
	besideOutside.Attributes["authorization_repositories"] = []string{world.outsideGitHub.slug, world.insideGitHub.slug}
	turns := []struct {
		name   string
		budget int
		extra  map[string]graphrank.CandidateNode
		want   int
		says   string
	}{
		{"all listed", 10, nil, 2, ""},
		{"a budget below two", 1, nil, 0, contractsv1.ContextFabricCensusUnlistedLimitation(2)},
		{"every id refused", 10, refused, 0, contractsv1.ContextFabricCensusUnlistedLimitation(2)},
		{"a node beside an outside slug", 10, map[string]graphrank.CandidateNode{grantCanonicalID(world, world.insideGitHub): besideOutside}, 2, ""},
	}
	before := make([]grantObservation, 0, len(turns))
	for _, turn := range turns {
		observed := observeTurnWith(t, world, principal, turn.extra, turn.budget)
		if got := len(observed.resolution.Candidates); got != turn.want || observed.resolution.CensusLimitation != turn.says {
			t.Fatalf("%s: candidates %d limitation %q, want %d and %q", turn.name, got, observed.resolution.CensusLimitation, turn.want, turn.says)
		}
		before = append(before, observed)
	}
	world.add(t, world.outsideGitHub)
	world.add(t, world.outsideGitLab)
	for index, turn := range turns {
		after := observeTurnWith(t, world, principal, turn.extra, turn.budget)
		assertSameTurn(t, before[index], after)
		rendered := fmt.Sprintf("%+v %+v", after.resolution, after.offers)
		for _, slug := range []string{world.outsideGitHub.slug, world.outsideGitLab.slug} {
			if strings.Contains(rendered, slug) {
				t.Fatalf("%s: %s is in what the turn shows: %s", turn.name, slug, rendered)
			}
		}
	}
}

func anOutsideSatisfierIsNeverCountedNamedOrOffered(t *testing.T, build grantWorldBuilder) {
	world := build(t, "only outside")
	none := observeGrantTurn(t, world)
	world.add(t, world.outsideGitHub)
	world.add(t, world.outsideGitLab)
	outside := observeGrantTurn(t, world)
	assertSameTurn(t, none, outside)
	tracer := &grantTracer{events: outside.trace}
	for _, probe := range tracer.stage("evidence_probe") {
		if probe.CensusCount != 0 {
			t.Fatalf("evidence_probe = %+v, want count 0", probe)
		}
	}
	rendered := fmt.Sprintf("%+v %+v %+v %q %q", outside.resolution, outside.offers, outside.trace, outside.statements, outside.graphReads)
	for _, repository := range []grantRepository{world.outsideGitHub, world.outsideGitLab} {
		if strings.Contains(rendered, repository.id) || strings.Contains(rendered, repository.slug) {
			t.Fatalf("%s is in what the turn exposes: %s", repository.slug, rendered)
		}
	}
	if rounds := tracer.stage("evidence_round"); len(rounds) != 1 || rounds[0].ShadowOutcome != string(graphrank.ShadowWouldNoMatch) {
		t.Fatalf("evidence_round = %+v, want would_no_match", rounds)
	}
}

func anInsideSatisfierBesideAnOutsideOneCommits(t *testing.T, build grantWorldBuilder) {
	world := build(t, "inside beside outside")
	world.add(t, world.insideGitHub)
	world.add(t, world.outsideGitHub)
	turn := observeGrantTurn(t, world)
	tracer := &grantTracer{events: turn.trace}
	probes := tracer.stage("evidence_probe")
	if len(probes) != 1 || probes[0].CensusCount != 1 {
		t.Fatalf("evidence_probe = %+v, want one kind with count 1, not 2", probes)
	}
	if rounds := tracer.stage("evidence_round"); len(rounds) != 1 || rounds[0].ShadowOutcome != string(graphrank.ShadowWouldCommit) {
		t.Fatalf("evidence_round = %+v, want would_commit", rounds)
	}
	if commits := tracer.stage("evidence_census_commit"); len(commits) != 1 || commits[0].Outcome != "merged" {
		t.Fatalf("evidence_census_commit = %+v, want one merged", commits)
	}
	want := grantCanonicalID(world, world.insideGitHub)
	if len(turn.resolution.Committed) != 1 || turn.resolution.Committed[0].CanonicalID != want {
		t.Fatalf("committed = %+v, want %s", turn.resolution.Committed, want)
	}
}

// anOwnerGrantReadsOnlyWhatTheGateAdmits: an owner wildcard admits exactly the
// two-part slugs the graph gate admits, through the census count and through a
// turn. The turn is the caller-visible half (codex r2, CHAOS-12455): the node
// of a repository now at acme/group/secret still lists an old in-grant slug, so
// the gate alone would have committed the row a prefix let through.
func anOwnerGrantReadsOnlyWhatTheGateAdmits(t *testing.T, build grantWorldBuilder) {
	world := build(t, "owner wildcard")
	world.add(t, world.insideGitHub)
	world.add(t, world.nestedGitLab)
	world.add(t, world.malformed)
	ctx := contextfabric.WithCensusGrant(context.Background(), []string{"acme/*"})
	outcome, err := devhealthsource.NewCensusFunc(world.client)(ctx, world.org, contextfabric.SubjectPullRequest, fmt.Sprint(grantNumber), true, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := grantCanonicalID(world, world.insideGitHub); outcome.Count != 1 || outcome.SatisfierCanonicalID != want {
		t.Fatalf("census = %+v, want only %s", outcome, want)
	}

	alone := build(t, "owner wildcard turn")
	stale := map[string]graphrank.CandidateNode{}
	for _, repository := range []grantRepository{alone.nestedGitLab, alone.malformed} {
		node := grantCandidateNode(alone, repository)
		node.Attributes["authorization_repositories"] = []string{"acme/old-name"}
		stale[grantCanonicalID(alone, repository)] = node
	}
	principal := userprincipal.Authenticated(t, alone.org, []string{"acme/*"})
	none := observeTurnAs(t, alone, principal, stale)
	alone.add(t, alone.nestedGitLab)
	alone.add(t, alone.malformed)
	assertSameTurn(t, none, observeTurnAs(t, alone, principal, stale))
}

// grantStatementModel answers a census statement from rows, evaluating only
// what the statement and its bindings carry: the organization, the number, and
// each repository filter whose clause is in the text. A statement with no grant
// reads every row of the organization, as the real one would.
type grantStatementModel struct {
	rows   []grantModelRow
	readAt time.Time
}

type grantModelRow struct {
	org        string
	repository grantRepository
}

func filterAdmits(statement string, values map[string]any, prefix, slug string) bool {
	seen := false
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s_%d", prefix, i)
		value, bound := values[name]
		if !bound || !strings.Contains(statement, "{"+name+":String}") {
			return !seen
		}
		seen = true
		want := fmt.Sprint(value)
		if slug == want || (strings.HasSuffix(want, "/") && strings.HasPrefix(slug, want) && slugShapeAdmits(statement, name, slug)) {
			return true
		}
	}
}

// slugShapeAdmits runs the regular expression of the named clause, when the
// clause has one. It is the statement's own text, so the model reads the rule
// the statement carries: both engines are RE2.
func slugShapeAdmits(statement, name, slug string) bool {
	_, after, found := strings.Cut(statement, "{"+name+":String}) AND match(lower(trimBoth(repo)), '")
	if !found {
		return true
	}
	expression, _, _ := strings.Cut(after, "'")
	return regexp.MustCompile(expression).MatchString(slug)
}

func (m *grantStatementModel) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	values := bindingValues(bindings)
	var keys []string
	for _, row := range m.rows {
		if values["census_org_id"] != row.org || values["census_handle_pr_number"] != grantNumber {
			continue
		}
		if !filterAdmits(statement, values, "census_grant", row.repository.slug) || !filterAdmits(statement, values, "census_repo", row.repository.slug) {
			continue
		}
		keys = append(keys, fmt.Sprintf("%s:%s:%d", row.org, row.repository.id, grantNumber))
	}
	sort.Strings(keys)
	fake := &censusFakeClient{aggregateCount: uint64(len(keys)), aggregateReadAt: m.readAt, rowKeys: keys}
	return fake.Query(ctx, statement, bindings)
}

func grantModelWorld(_ *testing.T, _ string) grantWorld {
	model := &grantStatementModel{readAt: time.Unix(1_800_000_000, 0).UTC()}
	world := grantWorld{
		org:           "org_grant",
		insideGitHub:  grantRepository{"11111111-0000-4000-8000-000000000001", "acme/alpha", "github"},
		insideGitLab:  grantRepository{"11111111-0000-4000-8000-000000000002", "group/beta", "gitlab"},
		outsideGitHub: grantRepository{"22222222-0000-4000-8000-000000000001", "other/secret", "github"},
		outsideGitLab: grantRepository{"22222222-0000-4000-8000-000000000002", "elsewhere/hidden", "gitlab"},
		nestedGitLab:  grantRepository{"22222222-0000-4000-8000-000000000003", "acme/group/secret", "gitlab"},
		malformed:     grantRepository{"22222222-0000-4000-8000-000000000004", "acme/trailing-", "github"},
		client:        model,
	}
	world.add = func(_ *testing.T, repository grantRepository) {
		model.rows = append(model.rows, grantModelRow{world.org, repository})
	}
	return world
}

func TestOutsideRowsChangeNothingOnTheStatementModel(t *testing.T) {
	t.Parallel()
	outsideRowsChangeNothing(t, grantModelWorld)
}

func TestAListingChangesNothingOutsideTheGrant(t *testing.T) {
	t.Parallel()
	aListingChangesNothingOutsideTheGrant(t, grantModelWorld)
}

// Over the set budget no id is held. The model counts rows, so the rows of one
// repository stand for many. Outside rows change nothing here either.
func TestOverTheBudgetChangesNothingOutsideTheGrant(t *testing.T) {
	t.Parallel()
	world := grantModelWorld(t, "over the budget")
	for range graphrank.CensusSatisfierSetBudget + 2 {
		world.add(t, world.insideGitHub)
	}
	before := observeGrantTurn(t, world)
	if want := contractsv1.ContextFabricCensusUnlistedLimitation(graphrank.CensusSatisfierSetBudget + 2); before.resolution.CensusLimitation != want {
		t.Fatalf("limitation = %q, want %q", before.resolution.CensusLimitation, want)
	}
	if len(before.resolution.Candidates) != 0 || len(before.graphReads) != 1 {
		t.Fatalf("candidates %v graph reads %q, want none past the search", before.resolution.Candidates, before.graphReads)
	}
	world.add(t, world.outsideGitHub)
	world.add(t, world.outsideGitLab)
	assertSameTurn(t, before, observeGrantTurn(t, world))
}

// A project-only caller reads no statement, whatever the organization holds.
func TestAProjectOnlyTurnDoesNotFollowTheRows(t *testing.T) {
	t.Parallel()
	world := grantModelWorld(t, "project only")
	principal := userprincipal.ProjectOnly(t, world.org, []string{"project.v2:github:P-1"})
	before := observeTurnAs(t, world, principal, nil)
	if len(before.statements) != 0 || before.resolution.CensusLimitation != "" {
		t.Fatalf("statements %q limitation %q, want neither", before.statements, before.resolution.CensusLimitation)
	}
	world.add(t, world.insideGitHub)
	world.add(t, world.insideGitLab)
	world.add(t, world.outsideGitHub)
	assertSameTurn(t, before, observeTurnAs(t, world, principal, nil))
}

func TestAnOutsideSatisfierIsNeverCountedNamedOrOffered(t *testing.T) {
	t.Parallel()
	anOutsideSatisfierIsNeverCountedNamedOrOffered(t, grantModelWorld)
}

func TestAnInsideSatisfierBesideAnOutsideOneCommits(t *testing.T) {
	t.Parallel()
	anInsideSatisfierBesideAnOutsideOneCommits(t, grantModelWorld)
}

func TestAnOwnerGrantReadsOnlyWhatTheGateAdmits(t *testing.T) {
	t.Parallel()
	anOwnerGrantReadsOnlyWhatTheGateAdmits(t, grantModelWorld)
}
