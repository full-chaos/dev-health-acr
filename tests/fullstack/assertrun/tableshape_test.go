package main

import (
	"strings"
	"testing"
)

// TestRowCountSQL_FinalOnlyWhenSupported covers the concrete failure the team lead described:
// a plain MergeTree table rejects FINAL outright, so the query built for it must omit it while
// a ReplacingMergeTree table's query must include it. file_hotspot_daily is the plain one this
// fixture still seeds; file_complexity_snapshots stopped being plain when ops migration 087
// converted it to ReplacingMergeTree(computed_at) -- which is exactly why the engine is read
// live per table rather than listed anywhere in this package.
func TestRowCountSQL_FinalOnlyWhenSupported(t *testing.T) {
	replacing := tableShape{HasOrgID: true, HasRepoID: true, SupportsAll: true, Engine: "ReplacingMergeTree"}
	plain := tableShape{HasOrgID: true, HasRepoID: true, SupportsAll: false, Engine: "MergeTree"}

	got := rowCountSQL("git_commits", "example-org/widget-service", "repo-1", "org-1", replacing)
	if !strings.Contains(got, "FROM git_commits FINAL") {
		t.Fatalf("expected FINAL for a ReplacingMergeTree table, got %q", got)
	}

	got = rowCountSQL("file_complexity_snapshots", "example-org/widget-service", "repo-1", "org-1", replacing)
	if !strings.Contains(got, "FROM file_complexity_snapshots FINAL") {
		t.Fatalf("expected FINAL once file_complexity_snapshots reports a Replacing engine, got %q", got)
	}

	got = rowCountSQL("file_hotspot_daily", "example-org/widget-service", "repo-1", "org-1", plain)
	if strings.Contains(got, "FINAL") {
		t.Fatalf("expected no FINAL for a plain MergeTree table, got %q", got)
	}
}

// TestCountingContext_NamesTheEngineAndTheUnit pins the diagnostic that was missing when ops
// migration 087 landed: the probe failed with nothing but "did not match the manifest's
// expected value", which reads like a broken seed even though the ENGINE -- and with it the
// unit the count is taken in -- is what had moved.
func TestCountingContext_NamesTheEngineAndTheUnit(t *testing.T) {
	final := countingContext(tableShape{SupportsAll: true, Engine: "ReplacingMergeTree"})
	if !strings.Contains(final, "ReplacingMergeTree") || !strings.Contains(final, "WITH FINAL") {
		t.Fatalf("expected the engine and the FINAL choice in the message, got %q", final)
	}
	if !strings.Contains(final, "distinct sorting-key tuples") {
		t.Fatalf("expected the FINAL message to say which unit it counted in, got %q", final)
	}

	plain := countingContext(tableShape{SupportsAll: false, Engine: "MergeTree"})
	if !strings.Contains(plain, "WITHOUT FINAL") || !strings.Contains(plain, "raw inserted rows") {
		t.Fatalf("expected the no-FINAL message to name the unit it counted in, got %q", plain)
	}

	// An engine this tool never observed must not produce a message asserting an engine of "".
	if got := countingContext(tableShape{}); got != "" {
		t.Fatalf("expected no counting context without an observed engine, got %q", got)
	}
}

// TestWithCountingContext_OnlyAnnotatesFailures keeps the passing path's report byte-identical:
// a green probe already discloses the FINAL choice through its own recorded SQL.
func TestWithCountingContext_OnlyAnnotatesFailures(t *testing.T) {
	shape := tableShape{SupportsAll: true, Engine: "ReplacingMergeTree"}

	ok := withCountingContext(probeCheck{Name: "p", OK: true}, shape)
	if ok.Message != "" {
		t.Fatalf("a passing probe must not gain a message, got %q", ok.Message)
	}

	failed := withCountingContext(probeCheck{Name: "p", Message: "probe result did not match the manifest's expected value"}, shape)
	if !strings.Contains(failed.Message, "ReplacingMergeTree") {
		t.Fatalf("a failing probe must name the engine it counted against, got %q", failed.Message)
	}

	// A probe that failed with no message of its own must not be handed a bare engine note
	// with nothing to attach it to.
	empty := withCountingContext(probeCheck{Name: "p"}, shape)
	if empty.Message != "" {
		t.Fatalf("expected no message to be invented, got %q", empty.Message)
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
