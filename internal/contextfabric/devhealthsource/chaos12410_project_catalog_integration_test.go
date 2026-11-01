package devhealthsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestLiveTheMembershipRefreshFollowsTheCatalogNotItsStamp is CHAOS-12410
// against a real ClickHouse with the ops presence view, on every provider. A
// project sync that re-stamps every row re-emits nothing; a real change to
// (provider, id, project_key) re-states every subject once; a crash before the
// cursor write re-emits; and a cursor that lost the digest re-emits.
func TestLiveTheMembershipRefreshFollowsTheCatalogNotItsStamp(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	const repo = "33333333-3333-3333-3333-333333333333"
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	for _, provider := range []string{"linear", "jira", "github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			org := "org-12410-" + provider
			step := 0
			at := func() time.Time { step++; return base.Add(time.Duration(step) * time.Minute) }
			// A project id each provider's presence arm reads.
			id := func(name string) string {
				if provider == "github" {
					return "ghprojv2:" + name
				}
				return name
			}
			projects := map[string]string{id("P1"): "K1", id("P2"): "K2"}
			writeProject := func(project, key string, stamp time.Time) {
				mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, 'started', '', 1, ?, ?)`,
					project, org, "Project "+project, key, provider, stamp, stamp)
			}
			sync := func() {
				stamp := at()
				for project, key := range projects {
					writeProject(project, key, stamp)
				}
			}
			// I-2 names a project that does not exist yet.
			issue := func(item, project string) {
				stamp := at()
				column := project
				if provider == "gitlab" {
					column = ""
				}
				mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES (?, ?, ?, ?, 'open', '', '', ?, ?, ?, ?, ?)`,
					item, repo, org, "Item "+item, provider, column, stamp, stamp, stamp)
				if provider == "gitlab" {
					mustExec(t, ctx, direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, ?, '', ?, '', '', '', ?, ?, ?, ?)`,
						org, repo, item, provider, project, stamp, stamp, "ev-"+item, stamp)
				}
			}
			mustExec(t, ctx, direct, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/one', ?, ?)`, repo, org, provider, base)
			sync()
			issue("I-1", id("P1"))
			issue("I-2", id("LATER"))
			mustExec(t, ctx, direct, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, 1, 'PR', 'open', ?)`, repo, org, at())
			mustExec(t, ctx, direct, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), 'I-2', 1, 0.9, 'native', '', ?, ?)`, repo, at(), org)

			source, err := devhealthsource.NewClickHouseProjectionSource(query)
			if err != nil {
				t.Fatalf("new source: %v", err)
			}
			source.WithWorkItemProjectMembership(true)
			if _, err := source.WithOverlap(time.Millisecond); err != nil {
				t.Fatal(err)
			}
			type emitted struct {
				workItems, pullRequests []string
				sets                    map[string][]string
			}
			page := func(cursor string) (contextfabric.ProjectionBatch, bool) {
				t.Helper()
				batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
				if err != nil {
					t.Fatalf("next batch: %v", err)
				}
				if available {
					if err := batch.Validate(); err != nil {
						t.Fatalf("batch failed contract validation: %v", err)
					}
				}
				return batch, available
			}
			drain := func(cursor string) (emitted, string) {
				t.Helper()
				out := emitted{sets: map[string][]string{}}
				for i := 0; i < 80; i++ {
					batch, available := page(cursor)
					if !available {
						break
					}
					for _, entity := range batch.Entities {
						switch entity.Subject.Kind {
						case contractsv1.ContextFabricSubjectWorkItem:
							out.workItems = append(out.workItems, entity.Subject.Label)
						case contractsv1.ContextFabricSubjectPullRequest:
							out.pullRequests = append(out.pullRequests, entity.Subject.Label)
						default:
							continue
						}
						if entity.ProjectIdentities != nil {
							out.sets[entity.Subject.CanonicalID] = append([]string{}, *entity.ProjectIdentities...)
						}
					}
					if batch.NextCursor == cursor {
						break
					}
					cursor = batch.NextCursor
				}
				sort.Strings(out.workItems)
				sort.Strings(out.pullRequests)
				return out, cursor
			}
			pullRequest := fmt.Sprintf("pull_request:%s:1", repo)

			_, cursor := drain("")
			if field := chaos12410CursorField(t, cursor); field == "" {
				t.Fatal("a drained cursor carries no digest")
			}

			// A sync that re-stamps every project row, with nothing changed.
			for i := 0; i < 3; i++ {
				sync()
				var quiet emitted
				quiet, cursor = drain(cursor)
				if len(quiet.workItems) != 0 || len(quiet.pullRequests) != 0 {
					t.Fatalf("quiet sync %d re-emitted %+v", i, quiet)
				}
			}

			// A real change: the project the issue already names arrives, so
			// its id resolves. No subject row moves.
			beforeChange := cursor
			projects[id("LATER")] = "K3"
			sync()

			// A crash after the page is built and before the cursor is
			// written: the old cursor runs again and misses nothing.
			if batch, available := page(beforeChange); !available || batch.NextCursor == beforeChange {
				t.Fatal("the change produced no page")
			}
			changed, cursor := drain(beforeChange)
			item := "work_item.v2:" + repo + ":I-2"
			if len(changed.sets[item]) != 1 {
				t.Fatalf("after the change I-2 states %v; emitted %+v", changed.sets[item], changed)
			}
			if got := changed.sets[pullRequest]; len(got) != 1 || got[0] != changed.sets[item][0] {
				t.Fatalf("after the change the pull request states %v, want %v", got, changed.sets[item])
			}
			if len(changed.workItems) < 2 {
				t.Fatalf("a real change re-stated %v, want every subject", changed.workItems)
			}

			// The refresh runs once.
			sync()
			if quiet, next := drain(cursor); len(quiet.workItems) != 0 || len(quiet.pullRequests) != 0 {
				t.Fatalf("after the refresh a quiet sync re-emitted %+v", quiet)
			} else {
				cursor = next
			}

			// A late row: I-2 leaves its project by a transition that lands
			// behind the cursor, past the overlap. No digest moves, so a quiet
			// sync leaves it, and the daily pass re-states it and its pull
			// request. A late retraction must not wait for an unrelated write.
			late := base.Add(-time.Hour)
			mustExec(t, ctx, direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', 'I-2', ?, ?, ?, '', '', '', ?, ?, 'ev-late', ?)`,
				org, repo, provider, id("LATER"), id("P1"), at(), late, late)
			sync()
			beforeDaily, next := drain(cursor)
			if len(beforeDaily.workItems) != 0 || len(beforeDaily.pullRequests) != 0 {
				t.Fatalf("a quiet sync before the daily pass re-emitted %+v", beforeDaily)
			}
			cursor = next
			daily, next := drain(chaos12410WithField(t, cursor, devhealthsource.AgeProjectCatalogField(chaos12410CursorField(t, cursor), devhealthsource.ProjectCatalogFullRefreshEvery)))
			wantProject := chaos12410ProjectIdentity(provider, id("P1"))
			if got := daily.sets[item]; len(got) != 1 || got[0] != wantProject {
				t.Fatalf("after the daily pass I-2 states %v, want %s; emitted %+v", got, wantProject, daily)
			}
			if got := daily.sets[pullRequest]; len(got) != 1 || got[0] != wantProject {
				t.Fatalf("after the daily pass the pull request states %v, want %s", got, wantProject)
			}
			cursor = next
			sync()
			if quiet, next := drain(cursor); len(quiet.workItems) != 0 || len(quiet.pullRequests) != 0 {
				t.Fatalf("the daily pass ran again: %+v", quiet)
			} else {
				cursor = next
			}

			// A cursor that lost the digest is a full refresh, never a skip.
			lost, _ := drain(chaos12410WithField(t, cursor, ""))
			if len(lost.workItems) < 2 || len(lost.pullRequests) < 1 {
				t.Fatalf("a lost digest re-emitted %+v, want every subject", lost)
			}
		})
	}
}

// The digest holds every part of the tuple on its own, in any order of rows, on
// any organization: a change to one part alone is a change.
func TestLiveTheDigestHoldsEachPartOfTheTuple(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC()
	type row struct{ provider, id, key string }
	digest := func(org string, rows ...row) uint64 {
		t.Helper()
		for _, r := range rows {
			mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at, last_synced) VALUES (?, ?, 'p', ?, ?, 'started', '', 1, ?, ?)`,
				r.id, org, r.key, r.provider, at, at)
		}
		got, _, err := devhealthsource.ReadProjectCatalogDigest(ctx, query, org)
		if err != nil {
			t.Fatalf("read digest: %v", err)
		}
		return got
	}
	a, b := row{"jira", "A", "KA"}, row{"jira", "B", "KB"}
	base := digest("org-base", a, b)
	if got := digest("org-order", b, a); got != base {
		t.Fatal("the digest depends on the order of the rows")
	}
	for name, rows := range map[string][]row{
		"a key":         {{"jira", "A", "KZ"}, b},
		"an id":         {{"jira", "Z", "KA"}, b},
		"a provider":    {{"linear", "A", "KA"}, b},
		"swapped keys":  {{"jira", "A", "KB"}, {"jira", "B", "KA"}},
		"one row fewer": {a},
		"no key":        {{"jira", "A", ""}, b},
	} {
		if got := digest("org-"+name, rows...); got == base {
			t.Errorf("%s: the digest did not change", name)
		}
	}
	// Not the stamps: a re-stamped row of the same tuple is the same catalog.
	at = at.Add(time.Hour)
	if got := digest("org-base", a, b); got != base {
		t.Fatal("re-stamping a row moved the digest")
	}
}

func chaos12410Decode(t *testing.T, cursor string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func chaos12410CursorField(t *testing.T, cursor string) string {
	t.Helper()
	field, _ := chaos12410Decode(t, cursor)["pc"].(string)
	return field
}

func chaos12410ProjectIdentity(provider, id string) string {
	derived, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("derive %s %s: %v %v", provider, id, omitted, err))
	}
	return derived
}

// chaos12410WithField rewrites the cursor's digest field; "" removes it.
func chaos12410WithField(t *testing.T, cursor, field string) string {
	t.Helper()
	state := chaos12410Decode(t, cursor)
	delete(state, "pc")
	if field != "" {
		state["pc"] = field
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
