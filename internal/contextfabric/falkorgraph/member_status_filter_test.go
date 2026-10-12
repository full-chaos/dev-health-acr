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
				rows = append(rows, row{"node": &node{Properties: map[string]interface{}{propKind: string(kind), propCanonicalID: f.id, propLabel: f.label}}, "score": 1.0})
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
				r := fakeSubjectNodeRow(string(kind), id, f.label)
				if n, ok := r["n"].(*node); ok && f.stored != "" {
					n.Properties[propPropertyPrefix+property] = f.stored
				}
				return []row{r}, nil
			}
		}
		return nil, nil
	}}
	fake = withWalkStepReads(fake)
	inner := fake.queryFunc
	fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if readErr != nil && strings.Contains(cypher, "$bstatus") {
			return nil, readErr
		}
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
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:alpha", Label: "Alpha"}
	request := ownershipRoutingRequest(memberStatusFrame(kind, status), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which " + string(kind) + "s of team Alpha are in that state?"
	telemetry := &recordingTelemetry{}
	result, err := newFakeAdapterWithTelemetry(t, fake, telemetry).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
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
	{"project:linear-backlog", "A", "backlog"},
	{"project:linear-planned", "B", "planned"},
	{"project:linear-started", "C", "started"},
	{"project:linear-paused", "D", "paused"},
	{"project:linear-completed", "E", "completed"},
	{"project:linear-canceled", "F", "canceled"},
	{"project:jira-free-text", "G", "In Progress"},
	{"project:github-free-text", "H", "OPEN"},
	{"project:gitlab-free-text", "I", "opened"},
	{"project:no-state", "J", ""},
}

// Incident statuses: the canonical set ops writes for PagerDuty and Jira
// Service Management incidents, and a raw status no normalization mapped.
var incidentStatusFixtures = []memberStatusFixture{
	{"incident:pagerduty-active", "A", "active"},
	{"incident:pagerduty-acknowledged", "B", "acknowledged"},
	{"incident:pagerduty-resolved", "C", "resolved"},
	{"incident:jsm-open", "D", "open"},
	{"incident:jsm-closed", "E", "closed"},
	{"incident:suppressed", "F", "suppressed"},
	{"incident:raw-unmapped", "G", "Investigating"},
	{"incident:no-status", "H", ""},
}

func TestProjectAndIncidentMembersAreFilteredByTheStatedStatus(t *testing.T) {
	cases := []struct {
		kind     contextfabric.SubjectKind
		fixtures []memberStatusFixture
		status   string
		want     []string
		outside  int
	}{
		{contextfabric.SubjectProject, projectStatusFixtures, "started", []string{"project:linear-started"}, 4},
		{contextfabric.SubjectProject, projectStatusFixtures, "completed", []string{"project:linear-completed"}, 4},
		{contextfabric.SubjectProject, projectStatusFixtures, "paused", []string{"project:linear-paused"}, 4},
		{contextfabric.SubjectIncident, incidentStatusFixtures, "resolved", []string{"incident:pagerduty-resolved"}, 2},
		{contextfabric.SubjectIncident, incidentStatusFixtures, "open", []string{"incident:jsm-open"}, 2},
		{contextfabric.SubjectIncident, incidentStatusFixtures, "suppressed", []string{"incident:suppressed"}, 2},
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
			// Projects are the team's own reach: the member read carries the
			// status. Incidents are off the tree: the pool is filtered.
			want := []PullRequestStatusFilterDecision{{MemberKind: tc.kind, Status: tc.status, Site: PullRequestStatusFilterSiteWalk, Candidates: len(tc.want), Kept: len(tc.want), OutsideSet: tc.outside, Pool: -1}}
			if tc.kind == contextfabric.SubjectIncident {
				want[0].Site, want[0].Candidates = PullRequestStatusFilterSitePool, len(tc.fixtures)
			}
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
		if kind == contextfabric.SubjectProject {
			if len(telemetry.teamAnchorWalks) != 1 || telemetry.teamAnchorWalks[0].Outcome != TeamAnchorWalkReadFailed || len(telemetry.pullRequestStatusFilters) != 0 {
				t.Fatalf("%s: walk lines = %+v, status lines = %+v, want one read_failed walk line", kind, telemetry.teamAnchorWalks, telemetry.pullRequestStatusFilters)
			}
			continue
		}
		if len(telemetry.pullRequestStatusFilters) != 1 || telemetry.pullRequestStatusFilters[0].Err == nil {
			t.Fatalf("%s: status filter lines = %+v, want one read_failed line", kind, telemetry.pullRequestStatusFilters)
		}
	}
}

func TestNoMemberHoldingTheStatusIsAnEmptyCompleteSet(t *testing.T) {
	fixtures := []memberStatusFixture{{"project:a", "A", "started"}, {"project:b", "B", "In Progress"}}
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
	if got := servedMemberIDs(result); len(got) != len(projectStatusFixtures) {
		t.Fatalf("served %d projects, want all %d", len(got), len(projectStatusFixtures))
	}
	if len(telemetry.pullRequestStatusFilters) != 0 || result.MemberStatusOutsideSet.Count != 0 {
		t.Fatalf("an unqualified question recorded a filter: %+v %+v", telemetry.pullRequestStatusFilters, result.MemberStatusOutsideSet)
	}
}
