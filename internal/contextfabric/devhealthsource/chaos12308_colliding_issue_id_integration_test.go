package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A link row names no issue repository. When its work_item_id has rows in more
// than one repository, nothing says which issue the link means: the link may
// be to the foreign issue while the local one holds the project a project-only
// caller has. This row decides what that caller may read, so such an id
// contributes nothing and the pull request stays on the disclosed path.
func TestLiveAWorkItemIdInTwoRepositoriesContributesNothing(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	const local, foreign, single = "44444444-4444-4444-4444-444444444444", "55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666"
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	for _, provider := range []string{"linear", "jira", "github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			org := "org-collision-" + provider
			id := func(name string) string {
				if provider == "github" {
					return "ghprojv2:" + name
				}
				return name
			}
			member := func(item, repo, project string) {
				column := project
				if provider == "gitlab" {
					column = ""
				}
				mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES (?, ?, ?, ?, 'open', '', '', ?, ?, ?, ?, ?)`,
					item, repo, org, "Item "+item, provider, column, at, at, at)
				if provider == "gitlab" {
					mustExec(t, ctx, direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, ?, '', ?, '', '', '', ?, ?, ?, ?)`,
						org, repo, item, provider, project, at, at, "ev-"+item+repo, at)
				}
			}
			for _, project := range []string{id("LOCAL"), id("FOREIGN"), id("ONLY")} {
				mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', ?, 'started', '', 1, ?)`,
					project, org, "Project "+project, provider, at)
			}
			mustExec(t, ctx, direct, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/local', ?, ?)`, local, org, provider, at)
			// One id, two issues. The link below is to the foreign one; the
			// row cannot say so.
			member("ISSUE-7", local, id("LOCAL"))
			member("ISSUE-7", foreign, id("FOREIGN"))
			// Control: an id with one row, in another repository, is a real
			// cross-repository link and stays stated.
			member("ISSUE-8", single, id("ONLY"))
			link := func(item string, number uint32) {
				mustExec(t, ctx, direct, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, 'PR', 'open', ?)`, local, org, number, at)
				mustExec(t, ctx, direct, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), ?, ?, 0.9, 'native', '', ?, ?)`, local, item, number, at, org)
			}
			link("ISSUE-7", 1)
			link("ISSUE-8", 2)

			source, err := devhealthsource.NewClickHouseProjectionSource(query)
			if err != nil {
				t.Fatalf("new source: %v", err)
			}
			source.WithWorkItemProjectMembership(true)
			stated := map[string]*[]string{}
			tiers := map[string]string{}
			cursor := ""
			for page := 0; page < 40; page++ {
				batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				if !available {
					break
				}
				if err := batch.Validate(); err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				for _, entity := range batch.Entities {
					if entity.Subject.Kind != contractsv1.ContextFabricSubjectPullRequest {
						continue
					}
					stated[entity.Subject.CanonicalID] = entity.ProjectIdentities
					if tier := entity.Properties[contractsv1.ContextFabricProjectMembershipTierProperty].String; tier != nil {
						tiers[entity.Subject.CanonicalID] = *tier
					}
				}
				if batch.NextCursor == cursor {
					break
				}
				cursor = batch.NextCursor
			}
			colliding, control := fmt.Sprintf("pull_request:%s:1", local), fmt.Sprintf("pull_request:%s:2", local)
			if _, found := stated[colliding]; !found {
				t.Fatalf("pull request %s not projected", colliding)
			}
			if got := stated[colliding]; got != nil {
				t.Fatalf("the colliding id stated %v at tier %q; a foreign issue's link took a local project", *got, tiers[colliding])
			}
			if got := stated[control]; got == nil || !reflect.DeepEqual(*got, []string{chaos12308Identity(provider, id("ONLY"))}) || tiers[control] != "native" {
				t.Fatalf("the control: %v tier %q, want its one project at native", got, tiers[control])
			}
		})
	}
}

func chaos12308Identity(provider, id string) string {
	derived, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("derive %s %s: %v %v", provider, id, omitted, err))
	}
	return derived
}
