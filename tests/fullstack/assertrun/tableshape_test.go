package main

import (
	"strings"
	"testing"
)

// TestRowCountSQL_FinalOnlyWhenSupported covers the concrete failure the team lead described:
// file_hotspot_daily and file_complexity_snapshots are plain MergeTree and reject FINAL
// outright, so the query built for them must omit it while a ReplacingMergeTree table's query
// must include it.
func TestRowCountSQL_FinalOnlyWhenSupported(t *testing.T) {
	replacing := tableShape{HasOrgID: true, HasRepoID: true, SupportsAll: true}
	plain := tableShape{HasOrgID: true, HasRepoID: true, SupportsAll: false}

	got := rowCountSQL("git_commits", "example-org/widget-service", "repo-1", "org-1", replacing)
	if !strings.Contains(got, "FROM git_commits FINAL") {
		t.Fatalf("expected FINAL for a ReplacingMergeTree table, got %q", got)
	}

	got = rowCountSQL("file_hotspot_daily", "example-org/widget-service", "repo-1", "org-1", plain)
	if strings.Contains(got, "FINAL") {
		t.Fatalf("expected no FINAL for a plain MergeTree table, got %q", got)
	}
}

func TestRowCountSQL_ReposIsAlwaysSlugScoped(t *testing.T) {
	got := rowCountSQL("repos", "example-org/widget-service", "repo-1", "org-1", tableShape{SupportsAll: true})
	if !strings.Contains(got, "repo = 'example-org/widget-service'") {
		t.Fatalf("repos query must scope by slug, got %q", got)
	}
	if strings.Contains(got, "repo_id") {
		t.Fatalf("repos query must not reference repo_id (it has no such column to filter itself by), got %q", got)
	}
}

func TestRowCountSQL_ScopingFollowsTableShape(t *testing.T) {
	cases := []struct {
		name  string
		shape tableShape
		want  string
		avoid string
	}{
		{
			name:  "org and repo scoped",
			shape: tableShape{HasOrgID: true, HasRepoID: true},
			want:  "org_id = 'org-1' AND repo_id = 'repo-1'",
		},
		{
			name:  "repo scoped only (no org_id column)",
			shape: tableShape{HasOrgID: false, HasRepoID: true},
			want:  "repo_id = 'repo-1'",
			avoid: "org_id",
		},
		{
			name:  "org scoped only (no repo_id column, e.g. work_item_dependencies)",
			shape: tableShape{HasOrgID: true, HasRepoID: false},
			want:  "org_id = 'org-1'",
			avoid: "repo_id",
		},
		{
			name:  "neither column present",
			shape: tableShape{},
			avoid: "WHERE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rowCountSQL("some_table", "example-org/widget-service", "repo-1", "org-1", tc.shape)
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("%s: expected query to contain %q, got %q", tc.name, tc.want, got)
			}
			if tc.avoid != "" && strings.Contains(got, tc.avoid) {
				t.Fatalf("%s: expected query NOT to contain %q, got %q", tc.name, tc.avoid, got)
			}
		})
	}
}
