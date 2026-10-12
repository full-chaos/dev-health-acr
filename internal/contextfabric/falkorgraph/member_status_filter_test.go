package falkorgraph

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type memberStatusFixture struct {
	id, label, stored string
	// repo is the repository the node's authorization names; empty carries none.
	repo string
}

func memberStatusFrame(kind contextfabric.SubjectKind, value string) *contextfabric.QuestionFrame {
	frame := projectsOfAnchorFrame("Alpha")
	frame.SubjectExpression.Scoped.MemberKind = kind
	frame.SubjectExpression.Scoped.MemberQualifier = contextfabric.MemberQualifierStatus
	frame.SubjectExpression.Scoped.MemberQualifierValue = value
	return frame
}

// memberStatusFake serves a team Alpha. Project members are owned by the team
// (the team's own reach); incident members are matched by text, as incidents
// are off the entity tree. Each fixture's stored status is served by the
// stored-property read the filter issues, never by the pool node.
func memberStatusFake(kind contextfabric.SubjectKind, fixtures []memberStatusFixture, readErr error) *fakeConn {
	stored := map[string]string{}
	for _, f := range fixtures {
		stored[f.id] = f.stored
	}
	property := contextfabric.MemberStatusStoredProperty(kind)
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			if kind != contextfabric.SubjectIncident {
				return nil, nil
			}
			var rows []row
			for _, f := range fixtures {
				properties := map[string]interface{}{propKind: string(kind), propCanonicalID: f.id, propLabel: f.label}
				if f.repo != "" {
					properties["authorization_repositories"] = []string{f.repo}
				}
				rows = append(rows, row{"node": &node{Properties: properties}, "score": 1.0})
			}
			return rows, nil
		case strings.Contains(cypher, "UNION"):
			if kind != contextfabric.SubjectProject || params["id"] != "team:alpha" {
				return nil, nil
			}
			var rows []row
			for _, f := range fixtures {
				rows = append(rows, row{"r": &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_" + f.id}},
					"srcKind": "project", "srcId": f.id, "dstKind": "team", "dstId": "team:alpha"})
			}
			return rows, nil
		}
		id, _ := params["id"].(string)
		if id == "team:alpha" {
			return []row{fakeSubjectNodeRow("team", id, "Alpha")}, nil
		}
		for _, f := range fixtures {
			if f.id == id {
				return []row{fakeSubjectNodeRow(string(kind), id, f.label)}, nil
			}
		}
		return nil, nil
	}}
	fake = withWalkStepReads(fake)
	inner := fake.queryFunc
	fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if cypher != storedNodePropertyCypher(property) {
			return inner(ctx, graphKey, cypher, params, readOnly)
		}
		if readErr != nil {
			return nil, readErr
		}
		var rows []row
		ids, _ := params["ids"].([]interface{})
		for _, raw := range ids {
			id, _ := raw.(string)
			if value, ok := stored[id]; ok {
				rows = append(rows, row{"id": id, "value": value})
			}
		}
		return rows, nil
	}
	return fake
}

func discoverMemberStatus(t *testing.T, fake *fakeConn, kind contextfabric.SubjectKind, status string) (contextfabric.GraphContext, *recordingTelemetry, error) {
	t.Helper()
	return discoverMemberStatusAs(t, fake, kind, status, storage.Principal{OrgID: "org-1"})
}

func discoverMemberStatusAs(t *testing.T, fake *fakeConn, kind contextfabric.SubjectKind, status string, principal storage.Principal) (contextfabric.GraphContext, *recordingTelemetry, error) {
	t.Helper()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:alpha", Label: "Alpha"}
	request := ownershipRoutingRequest(memberStatusFrame(kind, status), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which " + string(kind) + "s of team Alpha are in that state?"
	telemetry := &recordingTelemetry{}
	result, err := newFakeAdapterWithTelemetry(t, fake, telemetry).DiscoverContext(context.Background(), principal, request)
	return result, telemetry, err
}

func servedMemberIDs(result contextfabric.GraphContext) []string {
	ids := []string{}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			ids = append(ids, member.Subject.CanonicalID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Project states as each provider writes them: Linear's closed category set
// maps; the other providers' free strings and an absent state do not.
var projectStatusFixtures = []memberStatusFixture{
	{"project:linear-backlog", "A", "backlog", ""},
	{"project:linear-planned", "B", "planned", ""},
	{"project:linear-started", "C", "started", ""},
	{"project:linear-paused", "D", "paused", ""},
	{"project:linear-completed", "E", "completed", ""},
	{"project:linear-canceled", "F", "canceled", ""},
	{"project:jira-free-text", "G", "In Progress", ""},
	{"project:github-free-text", "H", "OPEN", ""},
	{"project:gitlab-free-text", "I", "opened", ""},
	{"project:no-state", "J", "", ""},
	{"project:cased-set-word", "K", "Started", ""},
}

// Incident statuses: the canonical set ops writes for PagerDuty and Jira
// Service Management incidents, and a raw status no normalization mapped.
var incidentStatusFixtures = []memberStatusFixture{
	{"incident:pagerduty-active", "A", "active", ""},
	{"incident:pagerduty-acknowledged", "B", "acknowledged", ""},
	{"incident:pagerduty-resolved", "C", "resolved", ""},
	{"incident:jsm-open", "D", "open", ""},
	{"incident:jsm-closed", "E", "closed", ""},
	{"incident:suppressed", "F", "suppressed", ""},
	{"incident:raw-unmapped", "G", "Investigating", ""},
	{"incident:no-status", "H", "", ""},
	{"incident:raw-cased", "I", "OPEN", ""},
}

func TestProjectAndIncidentMembersAreFilteredByTheStatedStatus(t *testing.T) {
	cases := []struct {
		kind     contextfabric.SubjectKind
		fixtures []memberStatusFixture
		status   string
		want     []string
		outside  int
	}{
		{contextfabric.SubjectProject, projectStatusFixtures, "started", []string{"project:linear-started"}, 5},
		{contextfabric.SubjectProject, projectStatusFixtures, "completed", []string{"project:linear-completed"}, 5},
		{contextfabric.SubjectProject, projectStatusFixtures, "paused", []string{"project:linear-paused"}, 5},
		{contextfabric.SubjectIncident, incidentStatusFixtures, "resolved", []string{"incident:pagerduty-resolved"}, 3},
		{contextfabric.SubjectIncident, incidentStatusFixtures, "open", []string{"incident:jsm-open"}, 3},
		{contextfabric.SubjectIncident, incidentStatusFixtures, "suppressed", []string{"incident:suppressed"}, 3},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"="+tc.status, func(t *testing.T) {
			result, telemetry, err := discoverMemberStatus(t, memberStatusFake(tc.kind, tc.fixtures, nil), tc.kind, tc.status)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if got := servedMemberIDs(result); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("served %s = %v, want %v", tc.kind, got, tc.want)
			}
			if want := (contextfabric.MemberStatusOutsideSet{Kind: tc.kind, Count: tc.outside}); result.MemberStatusOutsideSet != want {
				t.Fatalf("outside the known set = %+v, want %+v", result.MemberStatusOutsideSet, want)
			}
			want := []PullRequestStatusFilterDecision{{MemberKind: tc.kind, Status: tc.status, Candidates: len(tc.fixtures), Kept: len(tc.want), OutsideSet: tc.outside}}
			if !reflect.DeepEqual(telemetry.pullRequestStatusFilters, want) {
				t.Fatalf("status filter lines = %+v, want %+v", telemetry.pullRequestStatusFilters, want)
			}
		})
	}
}

func TestMemberStatusFilterReadFailureEndsTheCall(t *testing.T) {
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectIncident} {
		fixtures := projectStatusFixtures
		status := "started"
		if kind == contextfabric.SubjectIncident {
			fixtures, status = incidentStatusFixtures, "open"
		}
		_, telemetry, err := discoverMemberStatus(t, memberStatusFake(kind, fixtures, errors.New("graph down")), kind, status)
		if err == nil {
			t.Fatalf("%s: a failed status read must fail the call, not serve an unfiltered set", kind)
		}
		if len(telemetry.pullRequestStatusFilters) != 1 || telemetry.pullRequestStatusFilters[0].Err == nil {
			t.Fatalf("%s: status filter lines = %+v, want one read_failed line", kind, telemetry.pullRequestStatusFilters)
		}
	}
}

func TestNoMemberHoldingTheStatusIsAnEmptyCompleteSet(t *testing.T) {
	fixtures := []memberStatusFixture{{"project:a", "A", "started", ""}, {"project:b", "B", "In Progress", ""}}
	result, _, err := discoverMemberStatus(t, memberStatusFake(contextfabric.SubjectProject, fixtures, nil), contextfabric.SubjectProject, "paused")
	if err != nil {
		t.Fatal(err)
	}
	if result.Cohort == nil || result.Cohort.Kind != contextfabric.SubjectProject || len(result.Cohort.Members) != 0 || !result.Cohort.Complete {
		t.Fatalf("cohort = %+v, want an empty complete project set", result.Cohort)
	}
	if result.MemberStatusOutsideSet.Count != 1 {
		t.Fatalf("outside the known set = %+v, want 1", result.MemberStatusOutsideSet)
	}
}

func TestUnqualifiedProjectQuestionRecordsNoStatusFilter(t *testing.T) {
	result, telemetry, err := discoverMemberStatus(t, memberStatusFake(contextfabric.SubjectProject, projectStatusFixtures, nil), contextfabric.SubjectProject, "")
	if err != nil {
		t.Fatal(err)
	}
	const memberCap = 10
	if len(projectStatusFixtures) <= memberCap {
		t.Fatalf("the fixture holds %d projects, within the member cap %d: it no longer shows the cut", len(projectStatusFixtures), memberCap)
	}
	if got := servedMemberIDs(result); len(got) != memberCap {
		t.Fatalf("served %d projects, want the member cap %d of the %d reached", len(got), memberCap, len(projectStatusFixtures))
	}
	if result.Cohort == nil || !result.Cohort.Truncated {
		t.Fatalf("cohort = %+v, want a truncated set: the cap cut it", result.Cohort)
	}
	if len(telemetry.pullRequestStatusFilters) != 0 || result.MemberStatusOutsideSet.Count != 0 {
		t.Fatalf("an unqualified question recorded a filter: %+v %+v", telemetry.pullRequestStatusFilters, result.MemberStatusOutsideSet)
	}
}

// TestMemberStatusCountsOnlyTheMembersTheCallerMayRead: a caller restricted to
// one repository is told how many readable incidents hold a stored status
// outside the set, never a count that includes incidents of a repository it
// cannot read, and the filter reads and counts only the readable ones.
func TestMemberStatusCountsOnlyTheMembersTheCallerMayRead(t *testing.T) {
	fixtures := []memberStatusFixture{
		{id: "incident:readable-open", label: "A", stored: "open", repo: "acme/granted"},
		{id: "incident:readable-unmapped", label: "B", stored: "Investigating", repo: "acme/granted"},
		{id: "incident:secret-open", label: "C", stored: "open", repo: "acme/secret"},
		{id: "incident:secret-unmapped", label: "D", stored: "Paged", repo: "acme/secret"},
		{id: "incident:secret-unmapped-two", label: "E", stored: "Paged", repo: "acme/secret"},
	}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}
	result, telemetry, err := discoverMemberStatusAs(t, memberStatusFake(contextfabric.SubjectIncident, fixtures, nil), contextfabric.SubjectIncident, "open", principal)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := servedMemberIDs(result), []string{"incident:readable-open"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("served incidents = %v, want %v", got, want)
	}
	if want := (contextfabric.MemberStatusOutsideSet{Kind: contextfabric.SubjectIncident, Count: 1}); result.MemberStatusOutsideSet != want {
		t.Fatalf("outside the known set = %+v, want %+v: the count must not include incidents the caller cannot read", result.MemberStatusOutsideSet, want)
	}
	if len(telemetry.pullRequestStatusFilters) != 1 || telemetry.pullRequestStatusFilters[0].Candidates != 2 {
		t.Fatalf("status filter lines = %+v, want one line over the 2 readable candidates", telemetry.pullRequestStatusFilters)
	}
}

// TestMemberStatusForACallerWhoCanReadNoneOfTheReachedMembers: every reached
// incident is in a repository the caller cannot read. The status filter reads
// and counts none of them, so no member is served, the outside-set count is 0,
// the filter line reports 0 candidates, and the call does not build the
// "no member holds the status" cohort, which would tell the caller that
// members it cannot read exist.
func TestMemberStatusForACallerWhoCanReadNoneOfTheReachedMembers(t *testing.T) {
	fixtures := []memberStatusFixture{
		{id: "incident:secret-open", label: "A", stored: "open", repo: "acme/secret"},
		{id: "incident:secret-unmapped", label: "B", stored: "Paged", repo: "acme/secret"},
		{id: "incident:secret-resolved", label: "C", stored: "resolved", repo: "acme/secret"},
	}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}
	result, telemetry, err := discoverMemberStatusAs(t, memberStatusFake(contextfabric.SubjectIncident, fixtures, nil), contextfabric.SubjectIncident, "open", principal)
	if err != nil {
		t.Fatal(err)
	}
	if got := servedMemberIDs(result); len(got) != 0 {
		t.Fatalf("served incidents = %v, want none", got)
	}
	if result.Cohort != nil {
		t.Fatalf("cohort = %+v, want none: an empty complete cohort would reveal that unreadable members were reached", result.Cohort)
	}
	if result.MemberStatusOutsideSet.Count != 0 {
		t.Fatalf("outside the known set = %+v, want a count of 0", result.MemberStatusOutsideSet)
	}
	want := []PullRequestStatusFilterDecision{{MemberKind: contextfabric.SubjectIncident, Status: "open"}}
	if !reflect.DeepEqual(telemetry.pullRequestStatusFilters, want) {
		t.Fatalf("status filter lines = %+v, want %+v (0 candidates)", telemetry.pullRequestStatusFilters, want)
	}
}
