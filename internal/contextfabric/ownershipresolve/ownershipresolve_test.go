package ownershipresolve_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/ownershipresolve"
)

// The default rendering filters to resolved, existing repositories; the graph
// edge's mode keeps every row and marks the unresolved ones with an empty repo_key. Both
// share the same resolution clauses, which is the point of the package.
func TestKeepUnresolvedChangesOnlyTheFilter(t *testing.T) {
	facts := ownershipresolve.OwnedRepositoriesSource("", ownershipresolve.Options{})
	edge := ownershipresolve.OwnedRepositoriesSource("", ownershipresolve.Options{KeepUnresolved: true})
	for _, clause := range []string{
		"r.provider = o.provider",
		"lower(r.repo) = lower(o.repo_full_name)",
		"1 AS matched",
		"FROM team_repo_ownership FINAL",
	} {
		if !strings.Contains(facts, clause) || !strings.Contains(edge, clause) {
			t.Errorf("clause %q missing from one rendering", clause)
		}
	}
	if !strings.Contains(facts, "WHERE "+ownershipresolve.ResolvedPredicate) || !strings.Contains(facts, ownershipresolve.RepoKeyExpr+" IN (") {
		t.Errorf("fact rendering lost its resolved/existence filter:\n%s", facts)
	}
	if strings.Contains(edge, " IN (") || strings.Contains(edge, "WHERE "+ownershipresolve.ResolvedPredicate) {
		t.Errorf("edge rendering must keep every row (unresolved as ''):\n%s", edge)
	}
	wantKey := "if(" + ownershipresolve.ResolvedPredicate + ", " + ownershipresolve.RepoKeyExpr + ", '') AS repo_key"
	if !strings.Contains(edge, wantKey) {
		t.Errorf("edge rendering repo_key is not %q:\n%s", wantKey, edge)
	}
}

func TestExtraColumnsAreAppended(t *testing.T) {
	got := ownershipresolve.OwnedRepositoriesSource(" AND x = 1", ownershipresolve.Options{
		OwnershipColumns: []string{"source", "updated_at"},
		Columns:          []string{"o.source AS source"},
	})
	for _, want := range []string{
		"SELECT org_id, provider, team_id, repo_id, repo_full_name, source, updated_at\n",
		"o.repo_full_name AS repo_full_name,\n\t\to.source AS source\n",
		"WHERE org_id = {org_id:String} AND x = 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendering lacks %q:\n%s", want, got)
		}
	}
}

// TestNoSecondInlineCopyOfTheRule is the drift guard: the name-resolution
// clause must live here only. A second inline copy in the fact reads or the
// graph producer is exactly the divergence CHAOS-7119 removed.
func TestNoSecondInlineCopyOfTheRule(t *testing.T) {
	for _, dir := range []string{"../devhealthfacts", "../devhealthsource"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		scanned := 0
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			scanned++
			for _, clause := range []string{"lower(r.repo) = lower(", "1 AS matched"} {
				if strings.Contains(string(body), clause) {
					t.Errorf("%s/%s carries an inline copy of %q; use ownershipresolve", dir, name, clause)
				}
			}
		}
		if scanned == 0 {
			t.Fatalf("%s: scanned no Go files -- the guard measured nothing", dir)
		}
	}
}
