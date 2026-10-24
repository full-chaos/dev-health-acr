package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	chaos11026OldReason = "devhealthfacts: the team owns no repository in synced ownership, so no rollup applies; this is not a measured zero"
	chaos11026Summaries = "uniqExact(t.provider, t.id)"
)

func chaos11026Read(t *testing.T, kind contextfabric.FactKind, subjects []contextfabric.SubjectRef, tables ...fakeTable) (contextfabric.FactProviderResult, *fakeClient) {
	t.Helper()
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts(%s): %v", kind, err)
	}
	return result, client
}

func chaos11026ProjectOwner(teamID string, count int64, names ...string) fakeTable {
	return fakeTable{match: chaos11026Summaries, rows: [][]any{{teamID, count, names}}}
}

// Every kind that stays not_applicable for a project-only team carries the new
// text: a repository-derived kind (or, for investment, the repository-owned
// mix, held by a separate ruling) has nothing to roll up, and says why.
var chaos11026NotApplicableKinds = []contextfabric.FactKind{
	contextfabric.FactBlockers, contextfabric.FactPullRequests, contextfabric.FactDeployments,
	contextfabric.FactIncidents, contextfabric.FactMetrics, contextfabric.FactHealth,
	contextfabric.FactInvestment, contextfabric.FactOperationalDeficiencies,
}

func TestChaos11026_TeamOwningOnlyAProjectIsToldSoNotThatItOwnsNoRepository(t *testing.T) {
	for _, kind := range chaos11026NotApplicableKinds {
		t.Run(string(kind), func(t *testing.T) {
			team := teamSubject("jira-team")
			result, _ := chaos11026Read(t, kind, []contextfabric.SubjectRef{team}, chaos11026ProjectOwner("jira-team", 1, "DEV"))
			if result.State != contextfabric.SourceNotApplicable {
				t.Fatalf("state = %q, want not_applicable (nothing repository-derived exists)", result.State)
			}
			for name, text := range map[string]string{"kind reason": result.Reason, "subject reason": result.SubjectReasons[contextfabric.FactSubjectKey(team)]} {
				if strings.Contains(text, "owns no repository") {
					t.Fatalf("%s still says the team owns no repository: %q", name, text)
				}
				if !strings.Contains(text, "project") || !strings.Contains(text, "no linked pull request reaches a repository") {
					t.Fatalf("%s = %q, want it to name the project and the missing PR link", name, text)
				}
			}
			if !strings.Contains(result.SubjectReasons[contextfabric.FactSubjectKey(team)], "DEV") {
				t.Fatalf("subject reason does not name project DEV: %q", result.SubjectReasons[contextfabric.FactSubjectKey(team)])
			}
		})
	}
}

// A kind that already computes a team fact from project data keeps it for a
// project-only team, with its own provenance: the team health row is served.
func TestChaos11026_HealthKeepsTheTeamRowOfAProjectOnlyTeam(t *testing.T) {
	team := teamSubject("jira-team")
	result, _ := chaos11026Read(t, contextfabric.FactHealth, []contextfabric.SubjectRef{team},
		fakeTable{match: healthScalarMatch, rows: [][]any{healthRow("jira-team")}},
		chaos11026ProjectOwner("jira-team", 1, "DEV"))
	if len(result.Facts) != 1 || result.State == contextfabric.SourceNotApplicable {
		t.Fatalf("facts=%d state=%q, want the team health row served", len(result.Facts), result.State)
	}
	if _, na := result.NotApplicableSubjects[contextfabric.FactSubjectKey(team)]; na {
		t.Fatal("the team with a served health fact was marked not applicable")
	}
}

func TestChaos11026_TeamOwningNothingKeepsTheOldTextByteForByte(t *testing.T) {
	for _, kind := range chaos11026NotApplicableKinds {
		t.Run(string(kind), func(t *testing.T) {
			team := teamSubject("empty-team")
			result, _ := chaos11026Read(t, kind, []contextfabric.SubjectRef{team})
			if result.State != contextfabric.SourceNotApplicable || result.Reason != chaos11026OldReason {
				t.Fatalf("state %q reason %q, want the unchanged not_applicable text", result.State, result.Reason)
			}
			if got := result.SubjectReasons[contextfabric.FactSubjectKey(team)]; !strings.Contains(got, chaos11026OldReason) {
				t.Fatalf("subject reason %q, want it to carry the unchanged text", got)
			}
		})
	}
}

func TestChaos11026_MixedTeamsEachGetTheirOwnCause(t *testing.T) {
	withProject, none := teamSubject("jira-team"), teamSubject("empty-team")
	result, _ := chaos11026Read(t, contextfabric.FactPullRequests, []contextfabric.SubjectRef{withProject, none},
		chaos11026ProjectOwner("jira-team", 4, "A", "B", "C"))
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(none)]; got != chaos11026OldReason {
		t.Fatalf("empty team reason %q, want the old text", got)
	}
	got := result.SubjectReasons[contextfabric.FactSubjectKey(withProject)]
	if !strings.Contains(got, "owns projects A, B, C (+1 more)") {
		t.Fatalf("project team reason %q, want the projects named with the overflow count", got)
	}
	if result.Reason != chaos11026OldReason {
		t.Fatalf("kind reason %q, want the old text while one team owns nothing", result.Reason)
	}
}

func TestChaos11026_NoNotApplicableTeamRunsNoProjectRead(t *testing.T) {
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r1", Label: "r1"}
	_, client := chaos11026Read(t, contextfabric.FactPullRequests, []contextfabric.SubjectRef{repo})
	for _, q := range client.queries {
		if strings.Contains(q.statement, chaos11026Summaries) {
			t.Fatal("a read with no not-applicable team asked who owns projects")
		}
	}
}

// A project-owning team keeps the facts the code already derives from its
// projects: the team_metrics_daily row is served, never replaced by the
// not-applicable text (a pin; it was never suppressed).
func TestChaos11026_MetricsKeepsTheTeamRowOfAProjectOnlyTeam(t *testing.T) {
	team := teamSubject("jira-team")
	result, _ := chaos11026Read(t, contextfabric.FactMetrics, []contextfabric.SubjectRef{team},
		fakeTable{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("jira-team")}},
		chaos11026ProjectOwner("jira-team", 1, "DEV"))
	if len(result.Facts) != 1 || result.State == contextfabric.SourceNotApplicable {
		t.Fatalf("facts=%d state=%q, want the team row served", len(result.Facts), result.State)
	}
	if _, na := result.NotApplicableSubjects[contextfabric.FactSubjectKey(team)]; na {
		t.Fatal("the team with a served fact was marked not applicable")
	}
}

// The project read only explains a state already decided: when it fails (the
// oracle store has no projects table, ClickHouse code 60) the kind is NOT
// failed and no fact or state moves, but the cause text says the check could not
// run instead of claiming the team owns no repository, which would present a
// missing check as a measured absence.
const (
	chaos11070SubjectText = "devhealthfacts: the project-link check could not run for this team, so ownership through projects is unknown; no repository rollup applies; this is not a measured zero"
	chaos11070KindText    = "devhealthfacts: the project-link check could not run for the requested teams, so ownership through projects is unknown; no repository rollup applies; this is not a measured zero"
)

func TestChaos11070_AFailedProjectReadSaysTheCheckCouldNotRun(t *testing.T) {
	for _, kind := range chaos11026NotApplicableKinds {
		t.Run(string(kind), func(t *testing.T) {
			team := teamSubject("jira-team")
			result, _ := chaos11026Read(t, kind, []contextfabric.SubjectRef{team},
				fakeTable{match: chaos11026Summaries, err: errors.New("code: 60, UNKNOWN_TABLE")})
			if result.State != contextfabric.SourceNotApplicable {
				t.Fatalf("state %q, want not_applicable unchanged", result.State)
			}
			if result.Reason != chaos11070KindText {
				t.Fatalf("kind reason %q, want the check-could-not-run limitation", result.Reason)
			}
			if got := result.SubjectReasons[contextfabric.FactSubjectKey(team)]; !strings.Contains(got, chaos11070SubjectText) {
				t.Fatalf("subject reason %q, want it to carry the check-could-not-run limitation", got)
			}
			for name, text := range map[string]string{"reason": result.Reason, "subject": result.SubjectReasons[contextfabric.FactSubjectKey(team)]} {
				if strings.Contains(text, "owns no repository") {
					t.Fatalf("%s still claims the team owns no repository: %q", name, text)
				}
			}
			if len(result.Facts) != 0 {
				t.Fatalf("facts moved: %d", len(result.Facts))
			}
		})
	}
}

func TestChaos11070_AFailedProjectReadWithTwoTeamsKeepsBothUnknown(t *testing.T) {
	a, b := teamSubject("team-a"), teamSubject("team-b")
	result, _ := chaos11026Read(t, contextfabric.FactPullRequests, []contextfabric.SubjectRef{a, b},
		fakeTable{match: chaos11026Summaries, err: errors.New("code: 60, UNKNOWN_TABLE")})
	if result.State != contextfabric.SourceNotApplicable || result.Reason != chaos11070KindText {
		t.Fatalf("state %q reason %q, want not_applicable with the limitation", result.State, result.Reason)
	}
	for _, team := range []contextfabric.SubjectRef{a, b} {
		if got := result.SubjectReasons[contextfabric.FactSubjectKey(team)]; got != chaos11070SubjectText {
			t.Fatalf("%s reason %q, want the limitation", team.CanonicalID, got)
		}
	}
}

// A team that owns repositories beside one whose check failed keeps its state;
// the request note counts the unchecked team with the limitation wording.
func TestChaos11070_ARequestNoteCountsTheTeamsThatWereNotChecked(t *testing.T) {
	for _, kind := range []contextfabric.FactKind{contextfabric.FactHealth, contextfabric.FactOperationalDeficiencies} {
		t.Run(string(kind), func(t *testing.T) {
			client := teamOwningClient(kind, "OWNER")
			client.tables = append(client.tables, fakeTable{match: chaos11026Summaries, err: errors.New("code: 60, UNKNOWN_TABLE")})
			result := readTeamsKind(t, client, kind, "OWNER", "NOTHING")
			if !strings.Contains(result.RequestNote, "the project-link check could not run for 1 of 2 requested teams") || strings.Contains(result.RequestNote, "own no repository") {
				t.Fatalf("request note %q, want the unchecked team counted with the limitation wording and no ownership claim", result.RequestNote)
			}
			if got := result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("NOTHING"))]; !strings.Contains(got, chaos11070SubjectText) {
				t.Fatalf("unchecked team reason %q, want the limitation", got)
			}
			if got, has := result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("OWNER"))]; has && strings.Contains(got, "project-link check") {
				t.Fatalf("the owner carries the limitation %q", got)
			}
		})
	}
}

// failSecondProjectRead answers the first project-summary chunk and fails the
// second: a later chunk failing after an earlier one succeeded.
type failSecondProjectRead struct {
	inner *fakeClient
	calls int
}

func (c *failSecondProjectRead) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, chaos11026Summaries) {
		c.calls++
		if c.calls == 2 {
			return nil, errors.New("code: 60, UNKNOWN_TABLE")
		}
	}
	return c.inner.Query(ctx, statement, bindings)
}

// Teams of a chunk that completed keep what that chunk answered; only the teams
// of the chunk that failed are told the check could not run.
func TestChaos11070_ALaterChunkFailureLeavesEarlierCheckedTeamsAlone(t *testing.T) {
	const teams = 201
	subjects := make([]contextfabric.SubjectRef, 0, teams)
	for i := 0; i < teams; i++ {
		subjects = append(subjects, teamSubject(fmt.Sprintf("team-%03d", i)))
	}
	client := &failSecondProjectRead{inner: &fakeClient{tables: []fakeTable{chaos11026ProjectOwner("team-000", 1, "DEV")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactPullRequests, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("project reads = %d, want 2 (the test needs a second chunk to fail)", client.calls)
	}
	first := result.SubjectReasons[contextfabric.FactSubjectKey(subjects[0])]
	if !strings.Contains(first, "owns project DEV") || strings.Contains(first, "could not run") {
		t.Fatalf("first-chunk team reason %q, want the project it owns and no limitation", first)
	}
	checkedEmpty := result.SubjectReasons[contextfabric.FactSubjectKey(subjects[1])]
	if !strings.Contains(checkedEmpty, chaos11026OldReason) {
		t.Fatalf("a checked team that owns nothing %q, want the unchanged text", checkedEmpty)
	}
	last := result.SubjectReasons[contextfabric.FactSubjectKey(subjects[teams-1])]
	if !strings.Contains(last, chaos11070SubjectText) {
		t.Fatalf("failed-chunk team reason %q, want the limitation", last)
	}
	if result.Reason == chaos11070KindText || result.Reason == "" {
		t.Fatalf("kind reason %q must not claim the check failed for every team", result.Reason)
	}
}

// Investment appends its own mix-unavailable note after the cause text moves; it
// must not call a team repository-less when the team owns projects or the check
// that would tell did not run.
func TestChaos11070_InvestmentMixNoteDoesNotCallAProjectOwnerRepositoryLess(t *testing.T) {
	cases := []struct {
		name   string
		extra  []fakeTable
		teams  []string
		want   []string
		forbid []string
	}{
		{"team owns a project", []fakeTable{chaos11026ProjectOwner("NOTHING", 1, "DEV")}, []string{"OWNER", "NOTHING"},
			[]string{"investment_team_no_owned_repository for 1: the team owns projects whose linked pull requests reach no repository"}, []string{"the team owns no repository"}},
		{"project check failed", []fakeTable{{match: chaos11026Summaries, err: errors.New("code: 60, UNKNOWN_TABLE")}}, []string{"OWNER", "NOTHING"},
			[]string{"investment_team_no_owned_repository for 1: the project-link check could not run, so ownership through projects is unknown"}, []string{"the team owns no repository"}},
		{"one project owner beside a team that owns nothing", []fakeTable{chaos11026ProjectOwner("NOTHING", 1, "DEV")}, []string{"OWNER", "NOTHING", "EMPTY"},
			[]string{"investment_team_no_owned_repository for 1: the team owns no repository", "investment_team_no_owned_repository for 1: the team owns projects whose linked pull requests reach no repository"}, nil},
		{"a team that owns nothing keeps the old words", nil, []string{"OWNER", "NOTHING"},
			[]string{"investment_team_no_owned_repository for 1: the team owns no repository"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := teamOwningClient(contextfabric.FactInvestment, "OWNER")
			client.tables = append(client.tables, c.extra...)
			result := readTeamsKind(t, client, contextfabric.FactInvestment, c.teams...)
			for _, want := range c.want {
				if !strings.Contains(result.RequestNote, want) {
					t.Fatalf("request note %q lacks %q", result.RequestNote, want)
				}
			}
			for _, bad := range c.forbid {
				if strings.Contains(result.RequestNote, bad) {
					t.Fatalf("request note %q still says %q", result.RequestNote, bad)
				}
			}
		})
	}
}
