package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// t4TeamRollupCases drive the team rollup of deployments, incidents, pull
// requests and blockers through the catalogue-truth test. Each case owns more
// repositories than a table holds, so the omitted-count fields are emitted too,
// and alternates repositories with and without a name or a percentile so the
// nullable columns are exercised on both sides.
func t4TeamRollupCases() []t4Case {
	team := []contextfabric.SubjectRef{teamSubject("CHAOS")}
	const repositories = 70
	repoKey := func(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }
	ownership := func() fakeTable {
		rows := make([][]any, 0, repositories)
		for i := 0; i < repositories; i++ {
			name := fmt.Sprintf("acme/repo-%02d", i)
			if i%2 == 1 {
				name = ""
			}
			rows = append(rows, []any{"CHAOS", repoKey(i), name})
		}
		return fakeTable{match: "GROUP BY team_id, repo_key", rows: rows}
	}
	perRepo := func(build func(i int, key string) []any) [][]any {
		rows := make([][]any, 0, repositories)
		for i := 0; i < repositories; i++ {
			rows = append(rows, build(i, repoKey(i)))
		}
		return rows
	}
	return []t4Case{
		{name: "deployments/team", kind: contextfabric.FactDeployments, subjects: team, tables: []fakeTable{
			ownership(),
			{match: "FROM deploy_metrics_daily", rows: perRepo(func(i int, key string) []any {
				if i%2 == 1 {
					return []any{key, int64(4), int64(1), int64(2), "2026-09-20", uint8(0), float64(0), uint8(0), float64(0)}
				}
				return []any{key, int64(8), int64(2), int64(3), "2026-09-21", uint8(1), 1.5, uint8(1), 3.0}
			})},
		}},
		{name: "pull_requests/team", kind: contextfabric.FactPullRequests, subjects: team, tables: []fakeTable{
			ownership(),
			{match: "FROM git_pull_requests FINAL", rows: perRepo(func(_ int, key string) []any {
				return []any{key, int64(3), int64(2), int64(1)}
			})},
		}},
		{name: "incidents/team", kind: contextfabric.FactIncidents, subjects: team, tables: []fakeTable{
			ownership(),
			{match: "GROUP BY e.repo_id", rows: perRepo(func(_ int, key string) []any {
				return []any{key, int64(2), int64(1)}
			})},
			{match: "id NOT IN", rows: [][]any{{int64(4)}}},
			{match: "SELECT DISTINCT incident_id", rows: [][]any{{int64(5), int64(2)}}},
		}},
		{name: "blockers/team", kind: contextfabric.FactBlockers, subjects: team, tables: []fakeTable{
			ownership(),
			{match: "FROM work_item_dependencies", rows: perRepo(func(_ int, key string) []any {
				return []any{key, int64(2), int64(3)}
			})},
		}},
	}
}

// TestTeamRollupDeclarationMarks pins the safety marks of the team rollup
// declarations: the pointer's repository ids are subject references the gate
// checks, every count over the owned set is withheld from a repository-
// restricted caller (aggregate) or labelled as the caller's own population
// (blockers), and no rollup field is a judged score.
func TestTeamRollupDeclarationMarks(t *testing.T) {
	for _, kind := range []contextfabric.FactKind{contextfabric.FactDeployments, contextfabric.FactIncidents, contextfabric.FactPullRequests, contextfabric.FactBlockers} {
		capability := t4Capability(kind)
		pointer, ok := capability.FieldDeclaration("owned_repositories", contextfabric.SubjectTeam)
		if !ok {
			t.Fatalf("%s: owned_repositories is not declared for a team", kind)
		}
		column, ok := pointer.Column("repository_id")
		if !ok || column.SubjectRef == nil || column.SubjectRef.Kind != contextfabric.SubjectRepository || column.SubjectRef.IDForm != contextfabric.FactSubjectIDRepositoryUUID {
			t.Fatalf("%s: owned_repositories.repository_id must be a repository uuid reference, got %+v", kind, column)
		}
		for _, field := range capability.Fields {
			if len(field.SubjectKinds) != 1 || field.SubjectKinds[0] != contextfabric.SubjectTeam {
				continue
			}
			if field.Score {
				t.Errorf("%s %s: a team rollup field is a count, never a score", kind, field.Name)
			}
			counts := field.Type == contextfabric.FactFieldInteger && (strings.HasSuffix(field.Name, "_window") || strings.HasSuffix(field.Name, "_current") || strings.HasSuffix(field.Name, "_count"))
			if counts && field.Name != "owned_repositories_omitted_count" && field.Name != "repository_breakdown_omitted_count" && !field.Aggregate && !field.CallerScoped {
				t.Errorf("%s %s: a count over the owned set must be Aggregate or CallerScoped", kind, field.Name)
			}
		}
	}
}

// A team that owns more repositories than the pointer table holds is served a
// capped pointer; the result must say so (truncated) even when no repository
// has a metric row, or the omitted repositories cannot be followed and the
// read still claims to be complete.
func TestTeamRollupCappedOwnedRepositoryPointerDegradesTheResult(t *testing.T) {
	owned := make([][]any, 0, 70)
	for i := 0; i < 70; i++ {
		owned = append(owned, []any{"CHAOS", fmt.Sprintf("00000000-0000-4000-8000-%012d", i), fmt.Sprintf("acme/repo-%02d", i)})
	}
	for _, kind := range []contextfabric.FactKind{contextfabric.FactDeployments, contextfabric.FactPullRequests, contextfabric.FactIncidents, contextfabric.FactBlockers} {
		client := &fakeClient{tables: []fakeTable{{match: "GROUP BY team_id, repo_key", rows: owned}}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("%s: err=%v facts=%d, want one team fact", kind, err, len(result.Facts))
		}
		omitted := result.Facts[0].Fields["owned_repositories_omitted_count"]
		if omitted.Integer == nil || *omitted.Integer != 6 {
			t.Fatalf("%s: owned_repositories_omitted_count = %#v, want 6", kind, omitted)
		}
		if !result.Truncated {
			t.Fatalf("%s: a capped owned_repositories pointer was served as untruncated (state=%s)", kind, result.State)
		}
	}
}
