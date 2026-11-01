package devhealthsource_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12308: a pull request's stated project membership is the union of the
// sets of the issues linked to it by actual link rows; the tier property names
// the weakest contributing link; nothing contributing states nothing.

const chaos12308PullRequest = "pull_request:repo-1:1042"

// linkRow is one row of the membership link read: (pr repo, pr number, issue
// repo, work item, provenance, issue type, outranked).
func linkRow(workItemID, issueRepo, provenance string) []any {
	return []any{"repo-1", uint32(1042), issueRepo, workItemID, provenance, "bug", uint8(0)}
}

func chaos12308Batch(t *testing.T, tables []fakeTable, membership bool) (contextfabric.ProjectionBatch, error) {
	t.Helper()
	return chaos12226Batch(t, &fakeClient{tables: tables}, membership)
}

func chaos12308PullRequestEntity(t *testing.T, batch contextfabric.ProjectionBatch) contextfabric.EntityProjection {
	t.Helper()
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID == chaos12308PullRequest {
			return entity
		}
	}
	t.Fatalf("pull request %s not projected", chaos12308PullRequest)
	return contextfabric.EntityProjection{}
}

func chaos12308Tier(entity contextfabric.EntityProjection) string {
	if value := entity.Properties[contractsv1.ContextFabricProjectMembershipTierProperty].String; value != nil {
		return *value
	}
	return ""
}

func TestAPullRequestStatesTheUnionOfItsLinkedIssues(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	memberships := [][]any{
		membershipRow("repo-1", "WIDGET-101", "github", "P-1", 1),
		membershipRow("repo-1", "WIDGET-101", "jira", "ENG", 1),
		membershipRow(chaos12226Linear, "WIDGET-LINEAR", "linear", "L-1", 1),
		// A project no other issue names: a row that wrongly contributes
		// shows in the set, not only in the tier.
		membershipRow(chaos12226Orphan, "WIDGET-ORPHAN", "github", "P-9", 1),
	}
	outranked := linkRow("WIDGET-ORPHAN", chaos12226Orphan, "heuristic")
	outranked[6] = uint8(1)
	pullRequestTyped := linkRow("WIDGET-ORPHAN", chaos12226Orphan, "native")
	pullRequestTyped[5] = "pr"
	for name, tc := range map[string]struct {
		links    [][]any
		wantSet  []string
		wantTier string
	}{
		"native links": {[][]any{linkRow("WIDGET-101", "repo-1", "native"), linkRow("WIDGET-LINEAR", chaos12226Linear, "native")},
			[]string{"project.v2:github:P-1", "project.v2:jira:ENG", "project.v2:linear:L-1"}, "native"},
		"the weakest contributing tier": {[][]any{linkRow("WIDGET-101", "repo-1", "explicit_text"), linkRow("WIDGET-LINEAR", chaos12226Linear, "heuristic")},
			[]string{"project.v2:github:P-1", "project.v2:jira:ENG", "project.v2:linear:L-1"}, "heuristic"},
		// An issue in no project contributes nothing, so its weaker link does
		// not lower the tier.
		"a weaker link that contributes nothing": {[][]any{linkRow("WIDGET-101", "repo-1", "explicit_text"), linkRow("WIDGET-NOPROJECT", "repo-1", "heuristic")},
			[]string{"project.v2:github:P-1", "project.v2:jira:ENG"}, "explicit_text"},
		"an outranked row contributes nothing":         {[][]any{linkRow("WIDGET-101", "repo-1", "native"), outranked}, []string{"project.v2:github:P-1", "project.v2:jira:ENG"}, "native"},
		"a pull-request work item contributes nothing": {[][]any{linkRow("WIDGET-101", "repo-1", "native"), pullRequestTyped}, []string{"project.v2:github:P-1", "project.v2:jira:ENG"}, "native"},
		"an unknown tier contributes nothing":          {[][]any{linkRow("WIDGET-101", "repo-1", "native"), linkRow("WIDGET-ORPHAN", chaos12226Orphan, "inferred")}, []string{"project.v2:github:P-1", "project.v2:jira:ENG"}, "native"},
		// Unstated, not empty: it stays disclosed, never reads as "no project".
		"no link":                     {nil, nil, ""},
		"linked issues in no project": {[][]any{linkRow("WIDGET-NOPROJECT", "repo-1", "native")}, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tables := append(chaos12226Tables(at, memberships, nil), fakeTable{match: pullRequestMembershipLinkMarker, rows: tc.links})
			batch, err := chaos12308Batch(t, tables, true)
			if err != nil {
				t.Fatalf("next projection batch: %v", err)
			}
			entity := chaos12308PullRequestEntity(t, batch)
			if tc.wantSet == nil {
				if entity.ProjectIdentities != nil || chaos12308Tier(entity) != "" {
					t.Fatalf("stated %v tier %q, want unstated", *entity.ProjectIdentities, chaos12308Tier(entity))
				}
				return
			}
			if entity.ProjectIdentities == nil || !reflect.DeepEqual(*entity.ProjectIdentities, tc.wantSet) || chaos12308Tier(entity) != tc.wantTier {
				t.Fatalf("stated %v tier %q, want %v %q", entity.ProjectIdentities, chaos12308Tier(entity), tc.wantSet, tc.wantTier)
			}
		})
	}
}

func TestAPullRequestStatesNothingWithMembershipOff(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := append(chaos12226Tables(at, nil, nil),
		fakeTable{match: pullRequestMembershipLinkMarker, err: errors.New("the link read must not run with membership off")},
		fakeTable{match: pullRequestMembershipRefreshMarker, err: errors.New("the refresh table must not be read with membership off")})
	batch, err := chaos12308Batch(t, tables, false)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if entity := chaos12308PullRequestEntity(t, batch); entity.ProjectIdentities != nil {
		t.Fatalf("stated %v with membership off", *entity.ProjectIdentities)
	}
}

// A failed read fails the page: a partial set is never stated as current.
func TestAFailedPullRequestMembershipReadFailsThePage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	broken := errors.New("link read failed")
	tables := append(chaos12226Tables(at, nil, nil), fakeTable{match: pullRequestMembershipLinkMarker, err: broken})
	if _, err := chaos12308Batch(t, tables, true); err == nil || !strings.Contains(err.Error(), "git_pull_requests") {
		t.Fatalf("error = %v, want the pull request read to fail", err)
	}
}

// chaos12308FailingRead fails the statement that names fail, but only after a
// statement that names after, so the failure lands in one exact step.
type chaos12308FailingRead struct {
	*fakeClient
	after, fail string
	armed       bool
}

func (c *chaos12308FailingRead) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.armed && strings.Contains(statement, c.fail) {
		return nil, errors.New("read failed")
	}
	if strings.Contains(statement, c.after) {
		c.armed = true
	}
	return c.fakeClient.Query(ctx, statement, bindings)
}

// The sets of the linked issues are read after the links; a failure there
// fails the page too.
func TestAFailedLinkedIssueMembershipReadFailsThePage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := append(chaos12226Tables(at, nil, nil), fakeTable{match: pullRequestMembershipLinkMarker, rows: [][]any{linkRow("WIDGET-101", "repo-1", "native")}})
	client := &chaos12308FailingRead{fakeClient: &fakeClient{tables: tables}, after: pullRequestMembershipLinkMarker, fail: workItemMembershipMarker}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(true)
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err == nil || !strings.Contains(err.Error(), "git_pull_requests") {
		t.Fatalf("error = %v, want the pull request read to fail", err)
	}
}

func TestAFailedMembershipReadFailsTheRefreshPage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := append(chaos12226Tables(at, nil, nil), fakeTable{match: pullRequestMembershipRefreshMarker, rows: [][]any{{"repo-1", "example-org/widget-service", uint32(1042), at, at.Add(time.Hour)}}})
	client := &chaos12308FailingRead{fakeClient: &fakeClient{tables: tables}, after: pullRequestMembershipRefreshMarker, fail: pullRequestMembershipLinkMarker}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(true)
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err == nil || !strings.Contains(err.Error(), "pull_requests_project_membership") {
		t.Fatalf("error = %v, want the refresh table to fail the page", err)
	}
}

// A set over the contract's bound is left unstated, so the batch stays valid
// and no partial set is stated.
func TestAPullRequestOverTheBoundStaysUnstated(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	var memberships, links [][]any
	for i := 0; i <= contractsv1.ContextFabricEntityProjectIdentitiesMax; i++ {
		item := "WIDGET-" + strings.Repeat("X", i%7) + string(rune('A'+i%26)) + string(rune('a'+i/26))
		memberships = append(memberships, membershipRow("repo-1", item, "github", "P-"+item, 1))
		links = append(links, linkRow(item, "repo-1", "native"))
	}
	tables := append(chaos12226Tables(at, memberships, nil), fakeTable{match: pullRequestMembershipLinkMarker, rows: links})
	batch, err := chaos12308Batch(t, tables, true)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if entity := chaos12308PullRequestEntity(t, batch); entity.ProjectIdentities != nil {
		t.Fatalf("stated %d identities, want unstated over the bound", len(*entity.ProjectIdentities))
	}
}

// The refresh table re-emits a pull request whose membership inputs moved
// without the pull request syncing, with the change stamp as its cursor.
func TestTheRefreshTableReemitsAPullRequestWhoseMembershipChanged(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	changed := at.Add(time.Hour)
	tables := append(chaos12226Tables(at, [][]any{membershipRow("repo-1", "WIDGET-101", "github", "P-1", 1)}, nil),
		fakeTable{match: pullRequestMembershipLinkMarker, rows: [][]any{linkRow("WIDGET-101", "repo-1", "native")}},
		fakeTable{match: pullRequestMembershipRefreshMarker, rows: [][]any{{"repo-1", "example-org/widget-service", uint32(1042), at, changed}}})
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(true)
	cursor, stated := "", 0
	for page := 0; page < 20; page++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		for _, entity := range batch.Entities {
			if entity.Subject.CanonicalID == chaos12308PullRequest && entity.ProjectIdentities != nil {
				stated++
				if !reflect.DeepEqual(*entity.ProjectIdentities, []string{"project.v2:github:P-1"}) {
					t.Fatalf("stated %v", *entity.ProjectIdentities)
				}
			}
		}
		if batch.NextCursor == cursor {
			break
		}
		cursor = batch.NextCursor
	}
	if stated < 2 {
		t.Fatalf("pull request stated %d times, want the table's write and the refresh", stated)
	}
}
