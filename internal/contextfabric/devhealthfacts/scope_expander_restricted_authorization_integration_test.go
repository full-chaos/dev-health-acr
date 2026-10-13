package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The two work-item selections under a repository-restricted principal: a
// repo-less work item is admitted only through its project's owning team or
// a native pull-request link to a granted repository, which is exactly what
// the candidate-narrowed authorization relation decides. Each selection must
// serve the same rows as its organization-wide form, and the served targets
// and counts must name every admission path.
func TestScopeExpanderWorkItemsAuthorizeRepoLessItemsForARestrictedPrincipalOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sharedClickHouseTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "5b0d2c71-8e4f-4a39-9c62-1f7e3d8a4b05"
	const noRepo = "00000000-0000-0000-0000-000000000000"
	at := time.Now().UTC().Truncate(time.Second)
	since := at.Add(-30 * 24 * time.Hour)
	granted, denied := repoUUID(orgID+"-granted"), repoUUID(orgID+"-denied")
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, repo := range []struct{ id, slug string }{{granted, "acme/granted"}, {denied, "other/denied"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo.id, orgID, repo.slug, "github", at)
	}
	for _, own := range []struct{ team, repo, slug, project string }{{"TG", granted, "acme/granted", "PG"}, {"TD", denied, "other/denied", "PD"}} {
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", own.team, own.repo, own.slug, "exact", "native", uint8(1), uint16(100), int32(0), since, nil, at)
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			own.project, orgID, "linear", nil, "Project "+own.project, uint8(1), "active", "", at)
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", own.team, own.project, nil, "native", since, nil, at)
	}
	items := []struct {
		id, repo, project, link string
	}{
		{"wi-project", noRepo, "PG", ""},
		{"wi-link", noRepo, "PD", "native"},
		{"wi-heuristic", noRepo, "PD", "heuristic"},
		{"wi-denied", noRepo, "PD", ""},
		{"wi-direct", granted, "PD", ""},
	}
	for number, item := range items {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, item.repo, orgID, "linear", "issue "+item.id, "open", "", "", item.project, at)
		exec(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, item.repo, item.id, "TG", "native_team", uint8(1), "high", at)
		if item.link != "" {
			exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				granted, item.id, uint32(number+1), float32(1), item.link, "", at, orgID)
		}
	}
	// Repo-less items of a team and projects nobody requested, linked to the
	// granted repository: rows the narrowed relation leaves out.
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at)
SELECT concat('other-', toString(number)), ?, ?, 'linear', 'other', 'open', '', '', concat('OTHER-', toString(number % 50)), ? FROM numbers(40000)`, noRepo, orgID, at)
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('other-', toString(number)), toUInt32(number + 100), 1, 'native', '', ?, ? FROM numbers(40000)`, granted, at, orgID)

	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)
	principal := storage.Principal{OrgID: orgID, Subject: "restricted", CredentialID: "restricted", RepositoryScopes: []string{"acme/granted"}}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	for _, tc := range []struct {
		name, fragment string
		policy         contextfabric.FactScopePolicy
		origins        []contextfabric.SubjectRef
		narrowed       bool
	}{
		{"teamWorkItems", "min(a.team_id) AS origin_id", contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus, []contextfabric.SubjectRef{teamSubject("TG")}, true},
		// projectWorkItems filters w.work_item_id by its candidate set in its
		// own WHERE, which the planner applies to the link aggregate; it
		// passes no candidates, so only its rows and counts are checked.
		{"projectWorkItems", "p.id)) AS origin_id", contextfabric.FactScopePolicyProjectWorkItemStatus, []contextfabric.SubjectRef{projectSubject("linear", "PG"), projectSubject("linear", "PD")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder.reset()
			result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
				Principal: principal, RequirementKind: contextfabric.FactStatus, Origins: tc.origins,
				Policy: tc.policy, TargetKind: contextfabric.SubjectWorkItem, TimeContext: current, Limit: 200,
			})
			if err != nil {
				t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
			}
			counts := result.Counts
			if len(result.Targets) != 3 || counts.AuthorizationDroppedCount != 2 || counts.ProjectOwnershipAuthorizedCount != 1 ||
				counts.PullRequestLinkAuthorizedCount != 1 || counts.DirectRepositoryAuthorizedCount != 1 || counts.ExcludedHeuristicLinkCount != 1 {
				t.Errorf("served %d targets, counts %+v; want 3 targets (project, link, direct), 2 dropped, one per path, one excluded heuristic link", len(result.Targets), counts)
			}

			captured, ok := recorder.find(tc.fragment)
			if !ok {
				t.Fatalf("no statement carrying %q was sent", tc.fragment)
			}
			orgWide, filtered, clean := devhealthfacts.OrgWideScopeStatementForTest(captured.statement)
			if !filtered || !clean {
				t.Fatalf("the served statement does not carry a removable candidate filter (filtered %v, clean %v)", filtered, clean)
			}
			scoped, whole := scopeRows(t, ctx, query, captured), scopeRows(t, ctx, query, capturedStatement{statement: orgWide, bindings: captured.bindings})
			if len(scoped) == 0 {
				t.Fatal("the statement returned no rows, the comparison did not happen")
			}
			if !slices.Equal(scoped, whole) {
				t.Errorf("rows differ from the organization-wide form:\nscoped   %q\norg-wide %q", scoped, whole)
			}

			peak := func(statement string, strict bool) uint64 {
				fragment, _, _ := strings.Cut(tc.fragment, "\n")
				var least uint64
				for run := 0; run < 3; run++ {
					mark := serverNowMicros(t, ctx, direct)
					rows, err := query.Query(ctx, statement, captured.bindings)
					if err == nil {
						for rows.Next() {
						}
						_ = rows.Close()
					} else if strict {
						t.Fatalf("run statement: %v", err)
					}
					measured := peakOf(t, ctx, direct, "work_items", fragment, mark)
					if measured == 0 {
						t.Fatal("no query_log peak for the statement: the measurement did not happen")
					}
					if run == 0 || measured < least {
						least = measured
					}
				}
				return least
			}
			mib := func(bytes uint64) float64 { return float64(bytes) / (1 << 20) }
			if !tc.narrowed {
				// projectWorkItems passes no candidates: its WHERE filter on
				// w.work_item_id reaches the link aggregate only through the
				// planner's filter push-down (ClickHouse 26.7). Its served peak
				// must stay under the ceiling and must not grow with unrelated
				// organization rows; the same statement with that push-down
				// turned off is the control and must break one of the two.
				off := strings.Replace(captured.statement, "\nSETTINGS ", "\nSETTINGS query_plan_filter_push_down = 0, ", 1)
				if off == captured.statement {
					t.Fatal("the served statement carries no SETTINGS clause to extend")
				}
				small, smallOff := peak(captured.statement, true), peak(off, false)
				exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at)
SELECT concat('more-', toString(number)), ?, ?, 'linear', 'other', 'open', '', '', concat('OTHER-', toString(number % 50)), ? FROM numbers(160000)`, noRepo, orgID, at)
				exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('more-', toString(number)), toUInt32(number + 100000), 1, 'native', '', ?, ? FROM numbers(160000)`, granted, at, orgID)
				large, largeOff := peak(captured.statement, true), peak(off, false)
				grew := func(small, large uint64) bool {
					return float64(large) > 1.15*float64(small) && large > small+(2<<20)
				}
				breaks := func(small, large uint64) bool {
					return small >= scopeStatementCeiling || large >= scopeStatementCeiling || grew(small, large)
				}
				t.Run(fmt.Sprintf("served_%.1f_to_%.1fMiB_under_%dMiB_pushdown_off_%.1f_to_%.1fMiB", mib(small), mib(large), scopeStatementCeiling>>20, mib(smallOff), mib(largeOff)), func(t *testing.T) {
					if breaks(small, large) {
						t.Errorf("served statement peaked %.1f -> %.1f MiB with unrelated rows, want under %d MiB and not grown by 15%% and 2 MiB", mib(small), mib(large), scopeStatementCeiling>>20)
					}
					if !breaks(smallOff, largeOff) {
						t.Errorf("with filter push-down off the statement peaked %.1f -> %.1f MiB, under the bound and not grown: the guard cannot fail on this data", mib(smallOff), mib(largeOff))
					}
				})
				return
			}
			// The served statement's peak is bounded by the scope statement
			// ceiling; the same statement with the organization-wide
			// authorization relation must exceed it on this data, or the
			// bound could not fail. Each peak is the least of three warm runs.
			unnarrowed, found := devhealthfacts.UnnarrowedAuthorizationStatementForTest(captured.statement)
			if !found {
				t.Fatal("the served statement carries no candidate-narrowed authorization relation")
			}
			narrowed, orgWidePeak := peak(captured.statement, true), peak(unnarrowed, true)
			t.Run(fmt.Sprintf("narrowed_%.1fMiB_under_%dMiB_org_wide_relation_%.1fMiB", mib(narrowed), scopeStatementCeiling>>20, mib(orgWidePeak)), func(t *testing.T) {
				if narrowed >= scopeStatementCeiling {
					t.Errorf("served statement peaked at %.1f MiB, want under %d MiB", mib(narrowed), scopeStatementCeiling>>20)
				}
				if orgWidePeak <= scopeStatementCeiling {
					t.Errorf("the organization-wide authorization relation peaked at %.1f MiB, not over the %d MiB bound: the bound cannot fail on this data", mib(orgWidePeak), scopeStatementCeiling>>20)
				}
			})
		})
	}
}
