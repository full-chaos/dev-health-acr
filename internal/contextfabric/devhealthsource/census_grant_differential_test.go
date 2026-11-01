package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
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
	client                                                   contextpacket.ClickHouseQueryClient
	add                                                      func(t *testing.T, repository grantRepository)
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
	nodes := map[string]graphrank.CandidateNode{}
	for _, repository := range []grantRepository{world.insideGitHub, world.insideGitLab, world.outsideGitHub, world.outsideGitLab} {
		nodes[grantCanonicalID(world, repository)] = grantCandidateNode(world, repository)
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
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{grantTerm},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	principal := userprincipal.Narrowed(t, world.org, []string{"acme/*", "group/*"}, []string{world.insideGitHub.slug, world.insideGitLab.slug})
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
	if !reflect.DeepEqual(before.trace, after.trace) {
		t.Fatalf("trace changed:\nbefore %+v\nafter  %+v", before.trace, after.trace)
	}
	if !slices.Equal(before.statements, after.statements) {
		t.Fatalf("statements changed:\nbefore %q\nafter  %q", before.statements, after.statements)
	}
	if !slices.Equal(before.graphReads, after.graphReads) {
		t.Fatalf("graph reads changed:\nbefore %q\nafter  %q", before.graphReads, after.graphReads)
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
		if slug == want || (strings.HasSuffix(want, "/") && strings.HasPrefix(slug, want)) {
			return true
		}
	}
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

func TestAnOutsideSatisfierIsNeverCountedNamedOrOffered(t *testing.T) {
	t.Parallel()
	anOutsideSatisfierIsNeverCountedNamedOrOffered(t, grantModelWorld)
}

func TestAnInsideSatisfierBesideAnOutsideOneCommits(t *testing.T) {
	t.Parallel()
	anInsideSatisfierBesideAnOutsideOneCommits(t, grantModelWorld)
}
