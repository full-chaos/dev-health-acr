package falkorgraph

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12348 (North Star check 12, missing is not healthy): a non-cohort graph
// read is served as GraphContext.Paths, so it has no cohort to carry a
// population floor. A candidate set it lost must reach its coverage instead:
//
//   - graph_hop_walk_truncated: a committed subject's hop walk spent its edge
//     budget with candidates left;
//   - graph_team_narrowing_undecided_paths: under scope.team_ids a path edge was
//     dropped because an endpoint's ownership was absent, not current, or cut.
//
// A cohort read keeps its population floor and gains neither. "Non-cohort" is
// the reader's own test: the frame gives no servable member kind
// (CohortMemberKindForFrame), the test DiscoveredCohort gates a cohort on.

const (
	hopWalkTruncatedCode    = "graph_hop_walk_truncated"
	undecidedPathsCode      = "graph_team_narrowing_undecided_paths"
	hopWalkTruncatedRaw     = "hop_walk_truncated"
	undecidedPathsRawPrefix = "team_narrowing_undecided_paths:"
)

func undecidedPathsRaw(count int) string {
	return fmt.Sprintf("%s%d", undecidedPathsRawPrefix, count)
}

func cutReadDetails(gc contextfabric.GraphContext, code string) []contextfabric.CoverageDetail {
	var out []contextfabric.CoverageDetail
	for _, d := range gc.Coverage.Details {
		if string(d.Code) == code {
			out = append(out, d)
		}
	}
	return out
}

// assertCutReadDisclosed asserts one degrading graph row of code, with its
// count (nil: none), paired 1:1 and in order with DegradedReasons, as
// validateCoverageDetails requires.
func assertCutReadDisclosed(t *testing.T, gc contextfabric.GraphContext, code, raw string, count *int) {
	t.Helper()
	if !gc.Coverage.Partial {
		t.Errorf("Coverage.Partial = false, want true: the read lost candidates (%s)", code)
	}
	details := cutReadDetails(gc, code)
	if len(details) != 1 {
		t.Fatalf("%s details = %d, want 1, in %+v", code, len(details), gc.Coverage.Details)
	}
	d := details[0]
	if !d.Degrading || d.Source != "context-fabric:graph" || d.Raw != raw {
		t.Fatalf("detail = %+v, want a degrading context-fabric:graph row with raw %q", d, raw)
	}
	if (d.Count == nil) != (count == nil) || (count != nil && *d.Count != *count) {
		t.Fatalf("detail count = %v, want %v", d.Count, count)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("detail does not validate: %v", err)
	}
	if d.Label == "" || d.Label == "Coverage was limited" {
		t.Fatalf("label = %q, want the label of this code", d.Label)
	}
	var degrading []string
	for _, row := range gc.Coverage.Details {
		if row.Degrading {
			degrading = append(degrading, row.Raw)
		}
	}
	if !slices.Equal(degrading, gc.Coverage.DegradedReasons) {
		t.Fatalf("degrading rows %v do not pair with degraded_reasons %v", degrading, gc.Coverage.DegradedReasons)
	}
}

func assertNoCutReadDisclosure(t *testing.T, gc contextfabric.GraphContext) {
	t.Helper()
	for _, code := range []string{hopWalkTruncatedCode, undecidedPathsCode} {
		if details := cutReadDetails(gc, code); len(details) != 0 {
			t.Fatalf("%s details = %+v, want none", code, details)
		}
	}
	for _, reason := range gc.Coverage.DegradedReasons {
		if reason == hopWalkTruncatedRaw || strings.HasPrefix(reason, undecidedPathsRawPrefix) {
			t.Fatalf("degraded_reasons = %v, want no cut read reason", gc.Coverage.DegradedReasons)
		}
	}
}

// assertCoverageClean is what a cohort read and a whole read must both keep.
func assertCoverageClean(t *testing.T, gc contextfabric.GraphContext) {
	t.Helper()
	assertNoCutReadDisclosure(t, gc)
	if gc.Coverage.Partial || len(gc.Coverage.DegradedReasons) != 0 || len(gc.Coverage.Details) != 0 {
		t.Fatalf("coverage = %+v, want not partial, with no reason and no detail", gc.Coverage)
	}
}

func namedSubjectFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed},
		Temporal:          contextfabric.TemporalIntentCurrent,
		Version:           contextfabric.QuestionFrameVersion,
	}
}

// unservableMemberFrame declares a member kind no cohort is served for.
func unservableMemberFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Origin"}, MemberKind: contextfabric.SubjectDocument}},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

func repositoryMemberFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectRepository}},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

// nonCohortFrames holds every way the reader decides a read is not a cohort
// read.
func nonCohortFrames(t *testing.T) map[string]*contextfabric.QuestionFrame {
	t.Helper()
	frames := map[string]*contextfabric.QuestionFrame{
		"no frame":                nil,
		"a named subject":         namedSubjectFrame(),
		"an unservable member":    unservableMemberFrame(),
		"a repository-less frame": {Version: contextfabric.QuestionFrameVersion},
	}
	for name, frame := range frames {
		if frame == nil {
			continue
		}
		if kind, _, _ := contextfabric.CohortMemberKindForFrame(*frame); kind != "" {
			t.Fatalf("%s: the frame declares the servable kind %q: not a non-cohort read", name, kind)
		}
	}
	return frames
}

func nonCohortHopWalkRequest(frame *contextfabric.QuestionFrame) contextfabric.GraphDiscoveryRequest {
	request := hopWalkTruncationRequest(poolTruncationFulltextCollectLimit + 100)
	request.Interpretation.Shape = contextfabric.ShapeSingleSubject
	request.Frame = frame
	return request
}

// The harm: one more neighbour than the edge budget. The read serves the budget
// and, before this, read as complete.
func TestANonCohortReadDisclosesAClippedHopWalk(t *testing.T) {
	t.Parallel()
	for name, frame := range nonCohortFrames(t) {
		t.Run(name, func(t *testing.T) {
			adapter := hopWalkTruncationAdapter(t, poolTruncationFulltextCollectLimit+1, nil)
			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, nonCohortHopWalkRequest(frame))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if result.Cohort != nil {
				t.Fatalf("Cohort = %+v, want none: this read is served as paths", result.Cohort)
			}
			if len(result.Paths) == 0 {
				t.Fatal("Paths empty: the fixture served no neighbourhood")
			}
			assertCutReadDisclosed(t, result, hopWalkTruncatedCode, hopWalkTruncatedRaw, nil)
		})
	}
}

// Any committed subject's cut clips the read: an OR across subjects, whichever
// came last.
func TestANonCohortReadDisclosesACutOfAnyCommittedSubject(t *testing.T) {
	t.Parallel()
	clipped := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	whole := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p2", Label: "Other"}
	for name, committed := range map[string][]contextfabric.SubjectRef{
		"the cut subject first": {clipped, whole},
		"the cut subject last":  {whole, clipped},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := hopWalkTruncationAdapter(t, poolTruncationFulltextCollectLimit+1, nil)
			request := nonCohortHopWalkRequest(nil)
			request.Resolution.Committed = committed
			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			assertCutReadDisclosed(t, result, hopWalkTruncatedCode, hopWalkTruncatedRaw, nil)
		})
	}
}

func TestANonCohortReadWithAWholeHopWalkDisclosesNothing(t *testing.T) {
	t.Parallel()
	adapter := hopWalkTruncationAdapter(t, poolTruncationFulltextCollectLimit-1, nil)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, nonCohortHopWalkRequest(nil))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if got := len(result.Paths); got != poolTruncationFulltextCollectLimit-1 {
		t.Fatalf("paths = %d, want %d", got, poolTruncationFulltextCollectLimit-1)
	}
	assertCoverageClean(t, result)
}

// A cohort read keeps its floor (the cohort's own truncation and the population
// lower bound), and its coverage stays as it was.
func TestACohortReadOverAClippedHopWalkKeepsItsFloorAndItsCoverage(t *testing.T) {
	t.Parallel()
	adapter := hopWalkTruncationAdapter(t, poolTruncationFulltextCollectLimit+1, nil)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, hopWalkTruncationRequest(poolTruncationFulltextCollectLimit+100))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || !result.Cohort.Truncated || result.Cohort.Complete || !result.CohortPopulationLowerBound {
		t.Fatalf("cohort = %+v, lower bound = %v, want the truncated cohort and the floor", result.Cohort, result.CohortPopulationLowerBound)
	}
	assertCoverageClean(t, result)
}

// "Cohort read" is what the frame asks for, never whether a cohort came back: a
// cut read that found no member of the kind is still a cohort read.
func TestACohortReadWithNoMemberOverAClippedHopWalkGainsNoCutDetail(t *testing.T) {
	t.Parallel()
	nodes := []seededNode{{kind: "project", id: "p1", label: "Origin"}}
	var edges []seededEdge
	for i := 0; i <= poolTruncationFulltextCollectLimit; i++ {
		id := fmt.Sprintf("proj_%03d", i)
		nodes = append(nodes, seededNode{kind: "project", id: id, label: id})
		edges = append(edges, seededEdge{"BLOCKS", "project", "p1", "project", id, ""})
	}
	adapter := newFakeAdapter(t, seededGraphConn(nodes, edges))
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, hopWalkTruncationRequest(poolTruncationFulltextCollectLimit+100))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil || !result.CohortPopulationLowerBound {
		t.Fatalf("cohort = %+v, lower bound = %v, want no cohort and the floor", result.Cohort, result.CohortPopulationLowerBound)
	}
	assertCoverageClean(t, result)
}

// The team narrowing twin of the live fixture, over the seeded fake graph:
// origin is the committed project, owned by team-c. A narrowing to team-b keeps
// what team-b owns. A repository with no owner row has undecided ownership (an
// ended owner reads the same way: the ownership read returns no current row).
const (
	narrowedOrigin = "project:origin"
	narrowedTeam   = "team-b"
)

type narrowedSeed struct {
	nodes []seededNode
	edges []seededEdge
}

func newNarrowedSeed() *narrowedSeed {
	s := &narrowedSeed{}
	s.add("project", narrowedOrigin)
	s.nodes = append(s.nodes, teamNode("team:team-b", narrowedTeam), teamNode("team:team-c", "team-c"))
	s.own("project", narrowedOrigin, "team:team-c")
	return s
}

// add seeds a node with the "*" team list a projected node carries.
func (s *narrowedSeed) add(kind, id string) {
	s.nodes = append(s.nodes, seededNode{kind: kind, id: id, label: id, props: map[string]interface{}{propAuthzTeams: "*"}})
}

func (s *narrowedSeed) own(kind, id, team string) {
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", kind, id, "team", team, ""})
}

// repository seeds a repository with its owner; "" seeds no owner row.
func (s *narrowedSeed) repository(id, team string) {
	s.add("repository", id)
	if team != "" {
		s.own("repository", id, team)
	}
}

func (s *narrowedSeed) pathEdge(fromKind, fromID, toKind, toID string) {
	s.edges = append(s.edges, seededEdge{"BLOCKS", fromKind, fromID, toKind, toID, ""})
}

// conn gives every edge the "*" team list a projected edge carries, and every
// walked link of record its tier.
func (s *narrowedSeed) conn() *fakeConn {
	conn := seededGraphConn(s.nodes, s.edges)
	inner := conn.queryFunc
	conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		rows, err := inner(ctx, key, cypher, params, readOnly)
		if err != nil {
			return rows, err
		}
		for _, r := range rows {
			e, ok := r["r"].(*edge)
			if !ok {
				continue
			}
			e.Properties[propAuthzTeams] = "*"
			if params["rel"] == "LINKS_PULL_REQUEST" {
				e.Properties[propPropertyPrefix+linkTierProperty] = "native"
			}
		}
		return rows, err
	}
	return conn
}

func narrowedRequest(frame *contextfabric.QuestionFrame, teams ...string) contextfabric.GraphDiscoveryRequest {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: narrowedOrigin, Label: "Origin"}
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "what is going on with Origin", RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{origin}},
		Frame:      frame,
	}
}

func discoverNarrowed(t *testing.T, s *narrowedSeed, request contextfabric.GraphDiscoveryRequest) contextfabric.GraphContext {
	t.Helper()
	result, err := newFakeAdapter(t, s.conn()).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return result
}

func servedSubjects(gc contextfabric.GraphContext) map[string]bool {
	served := map[string]bool{}
	for _, p := range gc.Paths {
		for _, n := range p.Nodes {
			served[n.CanonicalID] = true
		}
	}
	return served
}

// Each kind the narrowing decides, with the endpoint count its undecided
// ownership removes. A pull request of an ownerless repository and an issue of
// one reach the repository at the next hop, whose edge has two undecided ends.
func TestATeamNarrowingDisclosesUndecidedPathEndpointsOfEveryKind(t *testing.T) {
	t.Parallel()
	const repo, pr, issue, project = "repository:orphan", "pull_request:github:orphan:1", "work_item.v2:github:1", "project.v2:linear:orphan"
	for name, tc := range map[string]struct {
		seed  func(*narrowedSeed)
		count int
	}{
		"a repository with no owner": {func(s *narrowedSeed) {
			s.repository(repo, "")
			s.pathEdge("project", narrowedOrigin, "repository", repo)
		}, 1},
		"a project with no owner": {func(s *narrowedSeed) {
			s.add("project", project)
			s.pathEdge("project", narrowedOrigin, "project", project)
		}, 1},
		"a pull request with no repository edge": {func(s *narrowedSeed) {
			s.add("pull_request", pr)
			s.pathEdge("project", narrowedOrigin, "pull_request", pr)
		}, 1},
		"a pull request of an ownerless repository": {func(s *narrowedSeed) {
			s.repository(repo, "")
			s.add("pull_request", pr)
			s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "pull_request", pr, "repository", repo, ""})
			s.pathEdge("project", narrowedOrigin, "pull_request", pr)
		}, 2},
		"an issue linked to a pull request of an ownerless repository": {func(s *narrowedSeed) {
			s.repository(repo, "")
			s.add("pull_request", pr)
			s.add("work_item", issue)
			s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "pull_request", pr, "repository", repo, ""}, linkEdge(issue, pr, "native"))
			s.pathEdge("project", narrowedOrigin, "work_item", issue)
		}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			s := newNarrowedSeed()
			tc.seed(s)
			result := discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam))
			count := tc.count
			assertCutReadDisclosed(t, result, undecidedPathsCode, undecidedPathsRaw(count), &count)
			assertNoHopWalkCode(t, result)
			if served := servedSubjects(result); len(served) != 0 {
				t.Fatalf("served %v, want no undecided endpoint", served)
			}
		})
	}
}

func assertNoHopWalkCode(t *testing.T, gc contextfabric.GraphContext) {
	t.Helper()
	if details := cutReadDetails(gc, hopWalkTruncatedCode); len(details) != 0 {
		t.Fatalf("hop walk details = %+v, want none: the walk was not cut", details)
	}
}

// Every way the reader decides a read is not a cohort read.
func TestATeamNarrowingDisclosesUndecidedPathEndpointsOnEveryNonCohortRead(t *testing.T) {
	t.Parallel()
	for name, frame := range nonCohortFrames(t) {
		t.Run(name, func(t *testing.T) {
			s := newNarrowedSeed()
			s.repository("repository:orphan", "")
			s.pathEdge("project", narrowedOrigin, "repository", "repository:orphan")
			count := 1
			assertCutReadDisclosed(t, discoverNarrowed(t, s, narrowedRequest(frame, narrowedTeam)), undecidedPathsCode, undecidedPathsRaw(1), &count)
		})
	}
}

// The count is of distinct endpoints, not of edges: two edges of one endpoint
// count once, and an endpoint that stays in scope never counts.
func TestATeamNarrowingCountsDistinctUndecidedEndpoints(t *testing.T) {
	t.Parallel()
	s := newNarrowedSeed()
	s.repository("repository:owned", "team:team-b")
	s.repository("repository:orphan-1", "")
	s.repository("repository:orphan-2", "")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:owned")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:orphan-1")
	s.pathEdge("repository", "repository:orphan-1", "project", narrowedOrigin)
	s.pathEdge("repository", "repository:owned", "repository", "repository:orphan-1")
	s.pathEdge("repository", "repository:owned", "repository", "repository:orphan-2")
	result := discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam))
	count := 2
	assertCutReadDisclosed(t, result, undecidedPathsCode, undecidedPathsRaw(2), &count)
	if served := servedSubjects(result); !served["repository:owned"] || served["repository:orphan-1"] || served["repository:orphan-2"] {
		t.Fatalf("served %v, want the owned repository and no undecided endpoint", served)
	}
}

// Two undecided ends: no decided end removes the edge, so both count. The end
// the walk reached only through this edge is either end of it.
func TestATeamNarrowingCountsBothUndecidedEndsOfOneEdge(t *testing.T) {
	t.Parallel()
	const reached, farther = "repository:orphan-reached", "repository:orphan-farther"
	for name, ends := range map[string][2]string{
		"the farther end is the edge's To":   {reached, farther},
		"the farther end is the edge's From": {farther, reached},
	} {
		t.Run(name, func(t *testing.T) {
			s := newNarrowedSeed()
			s.repository(reached, "")
			s.repository(farther, "")
			s.pathEdge("project", narrowedOrigin, "repository", reached)
			s.pathEdge("repository", ends[0], "repository", ends[1])
			count := 2
			assertCutReadDisclosed(t, discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam)), undecidedPathsCode, undecidedPathsRaw(2), &count)
		})
	}
}

// A decided out-of-scope drop is correct narrowing: scope, not missing data.
func TestATeamNarrowingDropOfADecidedEndpointDisclosesNothing(t *testing.T) {
	t.Parallel()
	s := newNarrowedSeed()
	s.repository("repository:owned", "team:team-b")
	s.repository("repository:other", "team:team-c")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:owned")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:other")
	result := discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam))
	assertCoverageClean(t, result)
	if served := servedSubjects(result); !served["repository:owned"] || served["repository:other"] {
		t.Fatalf("served %v, want the owned repository alone", served)
	}
}

// One undecided end and one decided out-of-scope end: the decided end alone
// removes the edge, so nothing was lost to indecision and it does not count.
func TestATeamNarrowingEdgeWithADecidedOutEndCountsNoUndecidedEnd(t *testing.T) {
	t.Parallel()
	s := newNarrowedSeed()
	s.repository("repository:other", "team:team-c")
	s.repository("repository:orphan", "")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:other")
	s.pathEdge("repository", "repository:other", "repository", "repository:orphan")
	assertCoverageClean(t, discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam)))
}

// An endpoint counts when any one of its edges was lost to its indecision, and
// a second endpoint held only by a decided out-of-scope neighbour still does
// not.
func TestATeamNarrowingCountsAnEndpointByTheEdgeItAloneRemoved(t *testing.T) {
	t.Parallel()
	s := newNarrowedSeed()
	s.repository("repository:other", "team:team-c")
	s.repository("repository:orphan-lost", "")
	s.repository("repository:orphan-mixed", "")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:other")
	s.pathEdge("project", narrowedOrigin, "repository", "repository:orphan-lost")
	s.pathEdge("repository", "repository:other", "repository", "repository:orphan-lost")
	s.pathEdge("repository", "repository:other", "repository", "repository:orphan-mixed")
	count := 1
	assertCutReadDisclosed(t, discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam)), undecidedPathsCode, undecidedPathsRaw(1), &count)
}

func TestNoTeamNarrowingDisclosesNoUndecidedPaths(t *testing.T) {
	t.Parallel()
	for name, teams := range map[string][]string{"no team": nil, "a requested star": {"*"}} {
		t.Run(name, func(t *testing.T) {
			s := newNarrowedSeed()
			s.repository("repository:orphan", "")
			s.pathEdge("project", narrowedOrigin, "repository", "repository:orphan")
			result := discoverNarrowed(t, s, narrowedRequest(nil, teams...))
			assertCoverageClean(t, result)
			if !servedSubjects(result)["repository:orphan"] {
				t.Fatalf("served %v, want the whole neighbourhood", servedSubjects(result))
			}
		})
	}
}

// A cohort read gains no row. A dropped path edge of another kind leaves it
// unfloored, and a candidate of its member kind floors it, as before.
func TestACohortReadUnderATeamNarrowingKeepsItsFloorAndItsCoverage(t *testing.T) {
	t.Parallel()
	seed := func() *narrowedSeed {
		s := newNarrowedSeed()
		s.repository("repository:orphan", "")
		s.pathEdge("project", narrowedOrigin, "repository", "repository:orphan")
		return s
	}
	t.Run("a candidate of another kind", func(t *testing.T) {
		result := discoverNarrowed(t, seed(), narrowedRequest(discoveredTeamCohortFrame(), narrowedTeam))
		if result.CohortPopulationLowerBound {
			t.Fatal("lower bound = true, want the read unfloored: no member was lost")
		}
		// The one row a team cohort has here, as before: team-c's node is
		// outside the narrowing.
		assertNoCutReadDisclosure(t, result)
		if want := []string{"cohort_denied_by_authorization:1"}; !slices.Equal(result.Coverage.DegradedReasons, want) || len(result.Coverage.Details) != 1 {
			t.Fatalf("coverage = %+v, want only %v", result.Coverage, want)
		}
	})
	t.Run("a candidate of the member kind", func(t *testing.T) {
		result := discoverNarrowed(t, seed(), narrowedRequest(repositoryMemberFrame(), narrowedTeam))
		if !result.CohortPopulationLowerBound {
			t.Fatal("lower bound = false, want the population floor: a member's ownership is missing")
		}
		assertCoverageClean(t, result)
	})
}

// Both causes on one read: two rows, each with its own reason, in reason order.
func TestANonCohortReadDisclosesBothCausesPairedInOrder(t *testing.T) {
	t.Parallel()
	s := newNarrowedSeed()
	for i := 0; i <= poolTruncationFulltextCollectLimit; i++ {
		id := fmt.Sprintf("repository:orphan-%03d", i)
		s.repository(id, "")
		s.pathEdge("project", narrowedOrigin, "repository", id)
	}
	result := discoverNarrowed(t, s, narrowedRequest(nil, narrowedTeam))
	assertCutReadDisclosed(t, result, hopWalkTruncatedCode, hopWalkTruncatedRaw, nil)
	count := poolTruncationFulltextCollectLimit
	assertCutReadDisclosed(t, result, undecidedPathsCode, undecidedPathsRaw(count), &count)
}
