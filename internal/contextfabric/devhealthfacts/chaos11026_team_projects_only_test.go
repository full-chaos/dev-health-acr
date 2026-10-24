package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
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
// failed, no fact or state moves and the old text stays.
func TestChaos11026_AFailedProjectReadKeepsTheOldTextAndFailsNothing(t *testing.T) {
	for _, kind := range chaos11026NotApplicableKinds {
		t.Run(string(kind), func(t *testing.T) {
			team := teamSubject("jira-team")
			result, _ := chaos11026Read(t, kind, []contextfabric.SubjectRef{team},
				fakeTable{match: chaos11026Summaries, err: errors.New("code: 60, UNKNOWN_TABLE")})
			if result.State != contextfabric.SourceNotApplicable || result.Reason != chaos11026OldReason {
				t.Fatalf("state %q reason %q, want the unchanged not_applicable text", result.State, result.Reason)
			}
			if got := result.SubjectReasons[contextfabric.FactSubjectKey(team)]; !strings.Contains(got, chaos11026OldReason) {
				t.Fatalf("subject reason %q, want the old text", got)
			}
			if len(result.Facts) != 0 {
				t.Fatalf("facts moved: %d", len(result.Facts))
			}
		})
	}
}
