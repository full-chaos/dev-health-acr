package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func ownReason(result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) string {
	return result.SubjectReasons[contextfabric.FactSubjectKey(subject)]
}

func requireOnly(t *testing.T, what, got, want string, notWant ...string) {
	t.Helper()
	if want != "" && !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to carry %q", what, got, want)
	}
	for _, bad := range notWant {
		if strings.Contains(got, bad) {
			t.Fatalf("%s = %q carries %q, a sentence about other subjects", what, got, bad)
		}
	}
}

const (
	projectReachSentence = "reached through owned projects"
	revertSentence       = "revert_rate not applicable"
)

func historicalTime() contextfabric.TimeContext {
	asOf := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}
}

func TestMetricsCaveatsRideOnlyTheSubjectsTheyDescribe(t *testing.T) {
	t.Parallel()
	client := projectReachFake()
	row := metricsRow("repo-1")
	row[5], row[6] = uint8(0), float64(0)
	row[11], row[12] = int64(2), int64(1)
	client.tables = append(client.tables, fakeTable{match: "FROM repo_metrics_daily", rows: [][]any{row}})
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	repo, team := repoSubject("repo-1"), teamSubject("CHAOS")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repo, team},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	requireOnly(t, "kind reason", result.Reason, "", projectReachSentence, revertSentence)
	requireOnly(t, "repository row reason", ownReason(result, repo), revertSentence, projectReachSentence)
	requireOnly(t, "team row reason", ownReason(result, team), projectReachSentence, revertSentence)
}

func TestDeploymentsProjectReachSentenceRidesOnlyTheReachingTeam(t *testing.T) {
	t.Parallel()
	provider := findProvider(t, devhealthfacts.NewProviders(projectReachFake()), contextfabric.FactDeployments)
	reaching, other := teamSubject("CHAOS"), teamSubject("OTHER")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{reaching, other},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	requireOnly(t, "kind reason", result.Reason, "", projectReachSentence)
	requireOnly(t, "reaching team reason", ownReason(result, reaching), projectReachSentence)
	requireOnly(t, "other team reason", ownReason(result, other), "", projectReachSentence)
}

func TestPullRequestUnwindowedLinkSentenceSkipsDirectPullRequestRows(t *testing.T) {
	t.Parallel()
	client := projectReachFake()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	pr, team := pullRequestSubject("00000000-0000-4000-8000-000000000042", "7"), teamSubject("CHAOS")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: historicalTime(), Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{pr, team},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	const sentence = "issue-to-pull-request links as they stand now"
	requireOnly(t, "kind reason", result.Reason, "", sentence)
	requireOnly(t, "team row reason", ownReason(result, team), sentence)
	requireOnly(t, "pull request row reason", ownReason(result, pr), "", sentence)
}

func TestIncidentSeverityOmittedSentenceSkipsTeamRowsAndKeepsOtherReasons(t *testing.T) {
	t.Parallel()
	client := projectReachFake()
	client.tables = append(client.tables, fakeTable{match: "FROM operational_incidents", rows: [][]any{{"incident-1", "open", "high"}}})
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIncidents)
	incident, team := incidentSubject("incident-1"), teamSubject("CHAOS")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: historicalTime(), Kind: contextfabric.FactIncidents, Subjects: []contextfabric.SubjectRef{incident, team},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	const sentence = "incident severity is omitted"
	requireOnly(t, "kind reason", result.Reason, "", sentence, projectReachSentence)
	requireOnly(t, "incident row reason", ownReason(result, incident), sentence, projectReachSentence)
	requireOnly(t, "team row reason", ownReason(result, team), projectReachSentence, sentence)
}

func TestInvestmentUnitsCursorInstructionRidesOnlyTheCutSubject(t *testing.T) {
	t.Parallel()
	theme := map[string]float64{"feature_delivery": 1}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	unit := func(id string) []any {
		return []any{id, repoUUID("units-a"), 1.0, 1.0, 0.0, theme, from, from, []string{"7"}, uint64(0), []string{}, 3.0, uint64(3), 0.0, uint64(0), uint64(3), uint64(0)}
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "WHERE win >= 0", rows: [][]any{unit("wu-a"), unit("wu-b"), unit("wu-c")}},
		{match: "IN {ids:Array(String)}\n)\nORDER BY share DESC", rows: [][]any{unit("wu-d")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	org := organizationSubject("org-1")
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID("units-a")}
	ctx := contextfabric.WithInvestmentUnits(context.Background(), contextfabric.InvestmentUnitsRequest{Max: 2})
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{org, repo},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	cut := contextfabric.InvestmentUnitsCutReason
	if !result.Truncated {
		t.Fatal("the organization page was cut but the result is not truncated")
	}
	requireOnly(t, "kind reason", result.Reason, "", cut)
	requireOnly(t, "organization row reason", ownReason(result, org), cut)
	requireOnly(t, "repository row reason", ownReason(result, repo), "", cut)
}

func TestIdentityOwningTeamsSentenceRidesOnlyRepositoryRows(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "team_repo_ownership", err: errors.New("ownership store down")},
		{match: "FROM repos", rows: [][]any{{"repo-1", "acme/one", "github"}}},
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "Investigate checkout flake", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactIdentity)
	repo, item := repoSubject("repo-1"), workItemSubject("repo-1", "WIDGET-101")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactIdentity,
		Subjects: []contextfabric.SubjectRef{repo, item},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	requireOnly(t, "kind reason", result.Reason, "", "owning_teams_unread")
	requireOnly(t, "repository row reason", ownReason(result, repo), "owning_teams_unread")
	requireOnly(t, "work item row reason", ownReason(result, item), "", "owning_teams_unread")
}

func TestPullRequestUnwindowedLinkSentenceRidesOnlyTheTeamWhoseLinksAreUnwindowed(t *testing.T) {
	t.Parallel()
	client := projectReachFake()
	client.tables = append([]fakeTable{
		{match: "SELECT team_id, repo_key, any(repo_full_name)", rows: [][]any{{"OTHER", "repo-own", "acme/own"}}},
		{match: "AND toString(repo_id) IN {ids:Array(String)}\nGROUP BY repo_id", rows: [][]any{{"repo-own", int64(4), int64(2), int64(1)}}},
	}, client.tables...)
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	flagged, clean := teamSubject("CHAOS"), teamSubject("OTHER")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: historicalTime(), Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{flagged, clean},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %d, want a fact for each team", len(result.Facts))
	}
	for _, fact := range result.Facts {
		if fact.Subject.CanonicalID == clean.CanonicalID {
			wantInt(t, fact.Fields, "pull_requests_opened_window", 4)
		}
	}
	const sentence = "issue-to-pull-request links as they stand now"
	requireOnly(t, "kind reason", result.Reason, "", sentence)
	requireOnly(t, "flagged team row reason", ownReason(result, flagged), sentence)
	requireOnly(t, "clean team row reason", ownReason(result, clean), "", sentence)
}
