package falkorgraph

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type pullRequestStatusFixture struct {
	id, state string
	reviews   []contextfabric.PullRequestReviewRow
}

// pullRequestStatusFake serves a team that owns one repository whose pull
// requests are fixtures: each pull request node carries its state, and each
// review row is a review node that belongs to its pull request.
func pullRequestStatusFake(fixtures []pullRequestStatusFixture, stateReadErr error) *fakeConn {
	nodes := []scopedAnchorNode{{"team", "team:alpha", "Alpha"}, {"repository", "repository:alpha", "full-chaos/alpha"}}
	edges := []scopedAnchorEdge{{rel: "OWNED_BY_TEAM", srcKind: "repository", srcID: "repository:alpha", dstKind: "team", dstID: "team:alpha"}}
	states := map[string]string{}
	reviews := map[string]contextfabric.PullRequestReviewRow{}
	for _, f := range fixtures {
		nodes = append(nodes, scopedAnchorNode{"pull_request", f.id, f.id})
		edges = append(edges, scopedAnchorEdge{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: f.id, dstKind: "repository", dstID: "repository:alpha"})
		states[f.id] = f.state
		for i, review := range f.reviews {
			reviewID := f.id + ":review:" + string(rune('a'+i))
			nodes = append(nodes, scopedAnchorNode{"pull_request_review", reviewID, reviewID})
			edges = append(edges, scopedAnchorEdge{rel: "BELONGS_TO_PULL_REQUEST", srcKind: "pull_request_review", srcID: reviewID, dstKind: "pull_request", dstID: f.id})
			reviews[reviewID] = review
		}
	}
	fake := scopedAnchorFake(nodes, edges, nil)
	inner := fake.queryFunc
	fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		id, _ := params["id"].(string)
		if review, ok := reviews[id]; ok && !strings.Contains(cypher, "UNION") {
			return []row{{"n": &node{Properties: map[string]interface{}{
				propKind: "pull_request_review", propCanonicalID: id, propLabel: id,
				propPropertyPrefix + "state": review.State, propValidFromNs: review.SubmittedNs,
			}}}}, nil
		}
		return inner(ctx, graphKey, cypher, params, readOnly)
	}
	fake = withWalkStepReads(fake)
	stepped := fake.queryFunc
	fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if cypher != pullRequestStateCypher() {
			return stepped(ctx, graphKey, cypher, params, readOnly)
		}
		if stateReadErr != nil {
			return nil, stateReadErr
		}
		var rows []row
		ids, _ := params["ids"].([]interface{})
		for _, raw := range ids {
			id, _ := raw.(string)
			if state, ok := states[id]; ok {
				rows = append(rows, row{"id": id, "state": state})
			}
		}
		return rows, nil
	}
	return fake
}

func pullRequestStatusFrame(status string) *contextfabric.QuestionFrame {
	frame := projectsOfAnchorFrame("Alpha")
	frame.SubjectExpression.Scoped.MemberKind = contextfabric.SubjectPullRequest
	if status != "" {
		frame.SubjectExpression.Scoped.MemberQualifier = contextfabric.MemberQualifierStatus
		frame.SubjectExpression.Scoped.MemberQualifierValue = status
	}
	return frame
}

func discoverPullRequestMembers(t *testing.T, fake *fakeConn, status string) (contextfabric.GraphContext, *recordingTelemetry, error) {
	t.Helper()
	return discoverPullRequestMembersAt(t, fake, status, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
}

func discoverPullRequestMembersAt(t *testing.T, fake *fakeConn, status string, timeContext contextfabric.TimeContext) (contextfabric.GraphContext, *recordingTelemetry, error) {
	t.Helper()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:alpha", Label: "Alpha"}
	request := ownershipRoutingRequest(pullRequestStatusFrame(status), anchor)
	request.Interpretation.TimeContext = timeContext
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which pull requests of team Alpha are in that state?"
	telemetry := &recordingTelemetry{}
	result, err := newFakeAdapterWithTelemetry(t, fake, telemetry).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	return result, telemetry, err
}

func servedPullRequestIDs(result contextfabric.GraphContext) []string {
	ids := []string{}
	if result.Cohort == nil {
		return ids
	}
	for _, member := range result.Cohort.Members {
		if member.Subject.Kind == contextfabric.SubjectPullRequest {
			ids = append(ids, member.Subject.CanonicalID)
		}
	}
	sort.Strings(ids)
	return ids
}

// TestTeamPullRequestMembersAreFilteredByTheStatedStatus asks for the pull
// requests of a team in each status of the closed set and checks the served
// members are exactly the pull requests that hold it under
// contextfabric.PullRequestStatusMatches.
func TestTeamPullRequestMembersAreFilteredByTheStatedStatus(t *testing.T) {
	fixtures := []pullRequestStatusFixture{
		{id: "pull_request:no-review", state: "open"},
		{id: "pull_request:approved", state: "open", reviews: []contextfabric.PullRequestReviewRow{{State: "CHANGES_REQUESTED", SubmittedNs: 10}, {State: "APPROVED", SubmittedNs: 20}}},
		{id: "pull_request:changes", state: "open", reviews: []contextfabric.PullRequestReviewRow{{State: "APPROVED", SubmittedNs: 10}, {State: "CHANGES_REQUESTED", SubmittedNs: 20}, {State: "COMMENTED", SubmittedNs: 30}}},
		{id: "pull_request:commented", state: "open", reviews: []contextfabric.PullRequestReviewRow{{State: "COMMENTED", SubmittedNs: 10}}},
		{id: "pull_request:pending", state: "open", reviews: []contextfabric.PullRequestReviewRow{{State: "PENDING", SubmittedNs: 10}}},
		{id: "pull_request:merged", state: "merged", reviews: []contextfabric.PullRequestReviewRow{{State: "APPROVED", SubmittedNs: 10}}},
		{id: "pull_request:closed", state: "closed"},
	}
	cases := []struct {
		status string
		want   []string
	}{
		{"", []string{"pull_request:approved", "pull_request:changes", "pull_request:closed", "pull_request:commented", "pull_request:merged", "pull_request:no-review", "pull_request:pending"}},
		{"open", []string{"pull_request:approved", "pull_request:changes", "pull_request:commented", "pull_request:no-review", "pull_request:pending"}},
		{"merged", []string{"pull_request:merged"}},
		{"closed", []string{"pull_request:closed"}},
		{"awaiting_review", []string{"pull_request:no-review", "pull_request:pending"}},
		{"changes_requested", []string{"pull_request:changes"}},
		{"approved", []string{"pull_request:approved"}},
	}
	for _, tc := range cases {
		t.Run("status="+tc.status, func(t *testing.T) {
			result, telemetry, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, nil), tc.status)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if got := servedPullRequestIDs(result); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("served pull requests = %v, want %v", got, tc.want)
			}
			if tc.status == "" {
				if len(telemetry.pullRequestStatusFilters) != 0 {
					t.Fatalf("an unqualified question recorded a status filter: %+v", telemetry.pullRequestStatusFilters)
				}
				return
			}
			want := []PullRequestStatusFilterDecision{{Status: tc.status, Candidates: len(fixtures), Kept: len(tc.want)}}
			if !reflect.DeepEqual(telemetry.pullRequestStatusFilters, want) {
				t.Fatalf("status filter lines = %+v, want %+v", telemetry.pullRequestStatusFilters, want)
			}
		})
	}
}

// TestPullRequestStatusFilterReadFailureEndsTheCall: a failed state read is a
// failed discovery and a read_failed line, never an unfiltered member set.
func TestPullRequestStatusFilterReadFailureEndsTheCall(t *testing.T) {
	fixtures := []pullRequestStatusFixture{{id: "pull_request:no-review", state: "open"}}
	readErr := errors.New("graph unavailable")
	result, telemetry, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, readErr), "awaiting_review")
	if err == nil {
		t.Fatalf("DiscoverContext() served %v with a failed state read", servedPullRequestIDs(result))
	}
	if len(telemetry.pullRequestStatusFilters) != 1 || telemetry.pullRequestStatusFilters[0].outcome() != PullRequestStatusFilterReadFailed {
		t.Fatalf("status filter lines = %+v, want one read_failed line", telemetry.pullRequestStatusFilters)
	}
}

// TestNoPullRequestHoldingTheStatusIsAnEmptyCompleteSet: pull requests were
// reached and none holds the status, so the answer is an empty complete member
// set, never the absence of a member set.
func TestNoPullRequestHoldingTheStatusIsAnEmptyCompleteSet(t *testing.T) {
	fixtures := []pullRequestStatusFixture{{id: "pull_request:merged", state: "merged"}}
	result, _, err := discoverPullRequestMembers(t, pullRequestStatusFake(fixtures, nil), "awaiting_review")
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatalf("no member set served; want an empty complete pull request set")
	}
	if result.Cohort.Kind != contextfabric.SubjectPullRequest || len(result.Cohort.Members) != 0 || !result.Cohort.Complete || result.Cohort.Truncated || result.Cohort.Rationale != pullRequestStatusNoMatchRationale {
		t.Fatalf("cohort = %+v, want an empty complete pull request set with the no-match rationale", *result.Cohort)
	}
}

// TestPullRequestReviewStatusIsReadAsOfNowUnderAStatedWindow: the status is as
// of now whatever window the question states, so a review submitted after the
// window still decides it.
func TestPullRequestReviewStatusIsReadAsOfNowUnderAStatedWindow(t *testing.T) {
	now := time.Now().UTC()
	fixtures := []pullRequestStatusFixture{
		{id: "pull_request:approved", state: "open", reviews: []contextfabric.PullRequestReviewRow{{State: "APPROVED", SubmittedNs: now.UnixNano()}}},
		{id: "pull_request:no-review", state: "open"},
	}
	start, end := now.AddDate(-2, 0, 0), now.AddDate(-1, 0, 0)
	window := contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
	for status, want := range map[string][]string{
		"approved":        {"pull_request:approved"},
		"awaiting_review": {"pull_request:no-review"},
	} {
		result, _, err := discoverPullRequestMembersAt(t, pullRequestStatusFake(fixtures, nil), status, window)
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		if got := servedPullRequestIDs(result); !reflect.DeepEqual(got, want) {
			t.Fatalf("status %s under a past window: served %v, want %v", status, got, want)
		}
	}
}
