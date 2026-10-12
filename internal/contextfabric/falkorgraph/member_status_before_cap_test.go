package falkorgraph

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// pullRequestPool is n pull requests of the team's repository, ids in id
// order, each with the state and reviews state(i) gives (i from 1).
func pullRequestPool(n int, state func(i int) (string, []contextfabric.PullRequestReviewRow)) []pullRequestStatusFixture {
	fixtures := make([]pullRequestStatusFixture, 0, n)
	for i := 1; i <= n; i++ {
		s, reviews := state(i)
		fixtures = append(fixtures, pullRequestStatusFixture{id: fmt.Sprintf("pull_request:alpha:%03d", i), state: s, reviews: reviews})
	}
	return fixtures
}

func pullRequestIDs(from, to int) []string {
	ids := []string{}
	for i := from; i <= to; i++ {
		ids = append(ids, fmt.Sprintf("pull_request:alpha:%03d", i))
	}
	return ids
}

// TestPullRequestStatusIsAppliedBeforeTheMemberCap: the team's first 26 pull
// requests by id are merged and the last four are open. The member read is cut
// at 25, so a status applied after the cut sees only merged pull requests and
// serves none; applied inside the read it serves the four open ones, complete.
// A 31st pull request stores "OPEN", which is not the canonical state and is
// never matched.
func TestPullRequestStatusIsAppliedBeforeTheMemberCap(t *testing.T) {
	fixtures := pullRequestPool(31, func(i int) (string, []contextfabric.PullRequestReviewRow) {
		if i == 31 {
			return "OPEN", nil
		}
		if i <= 26 {
			return "merged", []contextfabric.PullRequestReviewRow{{State: "APPROVED", SubmittedNs: int64(i)}}
		}
		if i == 29 {
			return "open", []contextfabric.PullRequestReviewRow{{State: "APPROVED", SubmittedNs: 100}}
		}
		return "open", nil
	})
	for status, want := range map[string][]string{
		"open":            pullRequestIDs(27, 30),
		"awaiting_review": {"pull_request:alpha:027", "pull_request:alpha:028", "pull_request:alpha:030"},
		"approved":        {"pull_request:alpha:029"},
		"merged":          pullRequestIDs(1, 10),
	} {
		t.Run(status, func(t *testing.T) {
			result, _, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, nil), status)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if got := servedPullRequestIDs(result); !reflect.DeepEqual(got, want) {
				t.Fatalf("served %v, want %v", got, want)
			}
			cut := status == "merged"
			if result.Cohort.Truncated != cut || result.Cohort.Complete == cut {
				t.Fatalf("cohort truncated=%v complete=%v, want truncated=%v: the cut describes the filtered pool", result.Cohort.Truncated, result.Cohort.Complete, cut)
			}
			if result.MemberStatusPoolCut != (contextfabric.MemberStatusPoolCut{}) {
				t.Fatalf("pool cut = %+v, want none: the status was applied inside the read", result.MemberStatusPoolCut)
			}
		})
	}
}

// TestReviewStatusPagesStopAtTheirCapAndSaySo: every open pull request in the
// first eight pages has a review, so none awaits one; the read stops at its
// page cap with an awaiting pull request left unread. The answer is cut, says
// how many pull requests were checked, and the line counts the pool.
func TestReviewStatusPagesStopAtTheirCapAndSaySo(t *testing.T) {
	pages := memberStatusPageCap * 26
	fixtures := pullRequestPool(pages+1, func(i int) (string, []contextfabric.PullRequestReviewRow) {
		if i <= pages {
			return "open", []contextfabric.PullRequestReviewRow{{State: "COMMENTED", SubmittedNs: int64(i)}}
		}
		return "open", nil
	})
	result, telemetry, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, nil), "awaiting_review")
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 0 || !result.Cohort.Truncated || result.Cohort.Complete {
		t.Fatalf("cohort = %+v, want an empty, cut, incomplete pull request set", result.Cohort)
	}
	if want := (contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: pages, Pool: pages + 1}); result.MemberStatusPoolCut != want {
		t.Fatalf("pool cut = %+v, want %+v", result.MemberStatusPoolCut, want)
	}
	want := []PullRequestStatusFilterDecision{{MemberKind: contextfabric.SubjectPullRequest, Status: "awaiting_review", Site: PullRequestStatusFilterSiteWalk,
		Candidates: pages, Kept: 0, PoolCut: true, Pool: pages + 1}}
	if !reflect.DeepEqual(telemetry.pullRequestStatusFilters, want) {
		t.Fatalf("status filter lines = %+v, want %+v", telemetry.pullRequestStatusFilters, want)
	}
}

// TestProjectStatusIsAppliedBeforeTheMemberCap is the same cut for a team's
// projects: the first 26 by id are completed, the last four started.
func TestProjectStatusIsAppliedBeforeTheMemberCap(t *testing.T) {
	fixtures := []memberStatusFixture{}
	want := []string{}
	for i := 1; i <= 30; i++ {
		f := memberStatusFixture{id: fmt.Sprintf("project:alpha-%03d", i), label: fmt.Sprintf("P%03d", i), stored: "completed"}
		if i > 26 {
			f.stored = "started"
			want = append(want, f.id)
		}
		fixtures = append(fixtures, f)
	}
	fixtures = append(fixtures, memberStatusFixture{id: "project:alpha-free-text", label: "Free", stored: "In Progress"})
	result, telemetry, err := discoverMemberStatus(t, memberStatusFake(contextfabric.SubjectProject, fixtures, nil), contextfabric.SubjectProject, "started")
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if got := servedMemberIDs(result); !reflect.DeepEqual(got, want) {
		t.Fatalf("served %v, want %v", got, want)
	}
	if !result.Cohort.Complete || result.Cohort.Truncated {
		t.Fatalf("cohort complete=%v truncated=%v, want a complete set", result.Cohort.Complete, result.Cohort.Truncated)
	}
	if want := (contextfabric.MemberStatusOutsideSet{Kind: contextfabric.SubjectProject, Count: 1}); result.MemberStatusOutsideSet != want {
		t.Fatalf("outside the known set = %+v, want %+v", result.MemberStatusOutsideSet, want)
	}
	if len(telemetry.pullRequestStatusFilters) != 1 || telemetry.pullRequestStatusFilters[0].Site != PullRequestStatusFilterSiteWalk {
		t.Fatalf("status filter lines = %+v, want one walk line", telemetry.pullRequestStatusFilters)
	}
}

// TestIncidentStatusOverACutPoolSaysWhereItWasDecided: incidents are off the
// entity tree, so the status filters the pool after its arm was cut. The
// answer is cut and says within how many incidents the status was decided.
func TestIncidentStatusOverACutPoolSaysWhereItWasDecided(t *testing.T) {
	fixtures := []memberStatusFixture{}
	for i := 1; i <= 30; i++ {
		f := memberStatusFixture{id: fmt.Sprintf("incident:alpha-%03d", i), label: fmt.Sprintf("I%03d", i), stored: "resolved"}
		if i%2 == 0 {
			f.stored = "open"
		}
		fixtures = append(fixtures, f)
	}
	result, telemetry, err := discoverMemberStatus(t, memberStatusFake(contextfabric.SubjectIncident, fixtures, nil), contextfabric.SubjectIncident, "open")
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || !result.Cohort.Truncated || result.Cohort.Complete {
		t.Fatalf("cohort = %+v, want a cut incident set", result.Cohort)
	}
	cut := result.MemberStatusPoolCut
	if cut.Kind != contextfabric.SubjectIncident || cut.Read < 1 || cut.Read >= len(fixtures) {
		t.Fatalf("pool cut = %+v, want the incidents checked before the cut", cut)
	}
	if len(telemetry.pullRequestStatusFilters) != 1 || telemetry.pullRequestStatusFilters[0].Site != PullRequestStatusFilterSitePool || !telemetry.pullRequestStatusFilters[0].PoolCut ||
		telemetry.pullRequestStatusFilters[0].Candidates != cut.Read {
		t.Fatalf("status filter lines = %+v, want one cut pool line over %d incidents", telemetry.pullRequestStatusFilters, cut.Read)
	}
	sentence := contractsv1.ContextFabricMemberStatusPoolCutLimitation(string(cut.Kind), cut.Read, cut.Pool)
	if !strings.Contains(sentence, fmt.Sprintf("first %d incidents", cut.Read)) || !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatalf("pool cut sentence %q is not a service-authored limitation naming the incidents checked", sentence)
	}
}

// TestMemberStatusReadsCarryTheStatusInsideTheRead pins the clause on both
// member reads: the step read and the link read narrow the reached node by its
// stored status before their limit.
func TestMemberStatusReadsCarryTheStatusInsideTheRead(t *testing.T) {
	status := memberStatusPredicate{kind: contextfabric.SubjectPullRequest, status: "awaiting_review"}.clause()
	if !reflect.DeepEqual(status.values, []interface{}{"open"}) {
		t.Fatalf("review status clause values = %v, want open", status.values)
	}
	step := hopOf(entityTree[0], treeRepository).step
	step.status = status
	cypher := walkStepCypher(step, temporalFilter{})
	clause := " AND b." + propPropertyPrefix + "state IN $bstatus"
	if !strings.Contains(cypher, clause) || strings.Index(cypher, clause) > strings.Index(cypher, "LIMIT") {
		t.Fatalf("step read %q does not carry the status before its limit", cypher)
	}
	path, ok := treePath(treeProject, treePullRequest)
	if !ok || len(path) != 2 || !path[1].edge.link {
		t.Fatalf("project -> pull request path = %+v", path)
	}
	link := linkSegmentCypherWith(path[0], path[1], temporalFilter{}, false, true, status)
	if !strings.Contains(link, clause) || strings.Index(link, clause) > strings.Index(link, "SKIP") {
		t.Fatalf("link read %q does not carry the status before its pages", link)
	}
	project := memberStatusPredicate{kind: contextfabric.SubjectProject, status: "started"}
	if got := project.clause().predicate("b"); got != " AND b."+propPropertyPrefix+"state IN $bstatus" {
		t.Fatalf("project clause = %q, want an exact compare: acr holds no synonyms", got)
	}
	if got := project.outsideClause().predicate("b"); got != " AND (b."+propPropertyPrefix+"state IS NULL OR NOT b."+propPropertyPrefix+"state IN $bstatus)" {
		t.Fatalf("outside clause = %q, want an exact compare", got)
	}
	if (memberStatusPredicate{kind: contextfabric.SubjectPullRequest, status: "open"}).outsideClause().set() {
		t.Fatalf("pull requests have no outside count, so no outside clause")
	}
}

// TestStatusFilterLineCarriesWhereTheStatusWasApplied certifies the real slog
// line against its declaration: a walk line with a cut read carries the site,
// the cut and the bounded pool count; a pool line carries no pool count.
func TestStatusFilterLineCarriesWhereTheStatusWasApplied(t *testing.T) {
	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	telemetry.RecordPullRequestStatusFilter(context.Background(), "org-1", PullRequestStatusFilterDecision{
		MemberKind: contextfabric.SubjectPullRequest, Status: "awaiting_review", Site: PullRequestStatusFilterSiteWalk,
		Candidates: 208, Kept: 0, PoolCut: true, Pool: 209,
	})
	telemetry.RecordPullRequestStatusFilter(context.Background(), "org-2", PullRequestStatusFilterDecision{
		MemberKind: contextfabric.SubjectIncident, Status: "open", Site: PullRequestStatusFilterSitePool,
		Candidates: 25, Kept: 12, OutsideSet: 1, PoolCut: true, Pool: -1,
	})
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.PullRequestStatusFilter, Want: map[string]any{
		"org_id": "org-1", "outcome": "applied", "member_kind": "pull_request", "status": "awaiting_review",
		"site": "walk", "candidates": 208, "kept": 0, "pool_cut": true, "pool": 209,
	}}); err != nil {
		t.Fatalf("walk line: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.PullRequestStatusFilter, Want: map[string]any{
		"org_id": "org-2", "outcome": "applied", "member_kind": "incident", "status": "open",
		"site": "pool", "candidates": 25, "kept": 12, "outside_set": 1, "pool_cut": true,
	}}); err != nil {
		t.Fatalf("pool line: %v", err)
	}
	for _, line := range log.LinesWithMsg(eventspec.PullRequestStatusFilter.Msg) {
		if line["org_id"] == "org-2" {
			if _, has := line["pool"]; has {
				t.Fatalf("pool line carries a pool count: %v", line)
			}
		}
	}
}

// TestReviewStatusThatFillsTheCapSaysHowManyWereChecked: 60 open pull
// requests await a review; the pages stop once more than the cap hold the
// status, with open pull requests unread. The set is cut and the answer
// states the status was checked within the first N of the M open pull
// requests the caller may see.
func TestReviewStatusThatFillsTheCapSaysHowManyWereChecked(t *testing.T) {
	fixtures := pullRequestPool(60, func(int) (string, []contextfabric.PullRequestReviewRow) { return "open", nil })
	result, telemetry, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, nil), "awaiting_review")
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || !result.Cohort.Truncated || result.Cohort.Complete || len(result.Cohort.Members) == 0 {
		t.Fatalf("cohort = %+v, want a cut, non-empty pull request set", result.Cohort)
	}
	if want := (contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 26, Pool: 60}); result.MemberStatusPoolCut != want {
		t.Fatalf("pool cut = %+v, want %+v", result.MemberStatusPoolCut, want)
	}
	if len(telemetry.pullRequestStatusFilters) != 1 || !telemetry.pullRequestStatusFilters[0].PoolCut || telemetry.pullRequestStatusFilters[0].Pool != 60 {
		t.Fatalf("status filter lines = %+v, want one cut walk line with pool 60", telemetry.pullRequestStatusFilters)
	}
}

// TestStatusReadIsNotStarvedByAnIntermediateCut: the team owns 30
// repositories, one pull request each; the first 26 repositories by id hold a
// merged one and the last four an open one. The repository frontier is wider
// than the member cap, so the status read reaches every repository and serves
// the four open pull requests, complete.
func TestStatusReadIsNotStarvedByAnIntermediateCut(t *testing.T) {
	var fixtures []pullRequestStatusFixture
	var want []string
	for i := 1; i <= 30; i++ {
		f := pullRequestStatusFixture{id: fmt.Sprintf("pull_request:repo-%03d:1", i), state: "merged", repo: fmt.Sprintf("repository:repo-%03d", i)}
		if i > 26 {
			f.state = "open"
			want = append(want, f.id)
		}
		fixtures = append(fixtures, f)
	}
	for _, status := range []string{"open", "awaiting_review"} {
		result, _, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, nil), status)
		if err != nil {
			t.Fatalf("%s: DiscoverContext() error = %v", status, err)
		}
		if got := servedPullRequestIDs(result); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: served %v, want %v", status, got, want)
		}
		if !result.Cohort.Complete || result.Cohort.Truncated {
			t.Fatalf("%s: cohort complete=%v truncated=%v, want complete", status, result.Cohort.Complete, result.Cohort.Truncated)
		}
	}
}

// TestADeniedPullRequestHoldsTheStatus: two pull requests are outside the
// caller's repository grants, one awaiting a review and one approved. For awaiting_review
// only the first is denied: the approved one does not hold the status, so it
// is filtered, not denied.
func TestADeniedPullRequestHoldsTheStatus(t *testing.T) {
	fixtures := []pullRequestStatusFixture{
		{id: "pull_request:alpha:001", state: "open"},
		{id: "pull_request:alpha:002", state: "open", hidden: true},
		{id: "pull_request:alpha:003", state: "open", hidden: true, reviews: []contextfabric.PullRequestReviewRow{{State: "APPROVED", SubmittedNs: 10}}},
	}
	adapter := newFakeAdapter(t, pullRequestStatusFake(fixtures, nil))
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:alpha", Label: "Alpha"}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/alpha"}}
	status := memberStatusPredicate{kind: contextfabric.SubjectPullRequest, status: "awaiting_review"}
	// The fake's edges carry no authorization lists, so the walk decides by
	// the node alone, the rule of every tree walk but an anchored member read.
	walk, err := adapter.treeMembersOf(context.Background(), "graph", "org-1", principal, contextfabric.RequestedScope{}, team, treePullRequest, 25, temporalFilter{}, false, false, status)
	if err != nil {
		t.Fatalf("teamAnchorMembers() error = %v", err)
	}
	if len(walk.nodes) != 1 || mustSubject(walk.nodes[0]).CanonicalID != "pull_request:alpha:001" {
		t.Fatalf("members = %+v, want pull request 001 only", walk.nodes)
	}
	if walk.denied != 1 {
		t.Fatalf("denied = %d, want 1: only the hidden pull request that holds the status", walk.denied)
	}
}
