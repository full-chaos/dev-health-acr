package directread_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func hotspotsReposAnswer(rowIDs, repoIDs []any) string {
	rows := make([]any, 0, len(rowIDs))
	for _, id := range rowIDs {
		rows = append(rows, map[string]any{"repoId": id, "filePath": "a.go"})
	}
	repos := make([]any, 0, len(repoIDs))
	for _, id := range repoIDs {
		row := map[string]any{"repoName": "r", "topFilePath": "a.go", "topRiskScore": 1}
		if id != opMissing {
			row["repoId"] = id
		}
		repos = append(repos, row)
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"hotspots": map[string]any{"rows": rows, "repos": repos}}})
	return string(raw)
}

func TestHotspotsReposRowsAreRowChecked(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("hotspots")
	scope := op.Scope(directread.CallerRestricted)
	if !slices.Contains(scope.RowIDPaths, "hotspots.repos[*].repoId") || !slices.Contains(scope.RowIDPaths, "hotspots.rows[*].repoId") {
		t.Errorf("row id paths = %v", scope.RowIDPaths)
	}
	vars := opMerge(opMinimalVariables(t, op), scope.ForcedVariablePath, []any{opRepo(opRepoA)})
	for _, tc := range []struct {
		name  string
		rows  []any
		repos []any
		want  directread.CallStatus
	}{
		{"both_granted", []any{opRepoA}, []any{opRepoA}, directread.CallServed},
		{"foreign_repo_in_repos", []any{opRepoA}, []any{opRepoA, opRepoB}, directread.CallRefused},
		{"foreign_only_in_repos", []any{}, []any{opRepoB}, directread.CallRefused},
		{"repos_missing_id", []any{opRepoA}, []any{opMissing}, directread.CallRefused},
		{"foreign_repo_in_rows", []any{opRepoB}, []any{opRepoA}, directread.CallRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := hotspotsReposAnswer(tc.rows, tc.repos)
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, body }, opHarnessOptions{})
			resp := h.run(t, opRestrictedA(), op.Name, vars)
			if resp.Call != tc.want {
				raw, _ := json.Marshal(resp)
				t.Fatalf("call = %s, want %s: %s", resp.Call, tc.want, raw)
			}
			if tc.want == directread.CallRefused {
				if resp.Refusal.Code != directread.RefusalRowOutsideGrant || resp.Data != nil {
					t.Fatalf("refusal = %+v, data = %s", resp.Refusal, resp.Data)
				}
				if h.runner.Counters().RowsForeign == 0 {
					t.Fatal("rows_foreign counter not raised")
				}
			} else if !strings.Contains(string(resp.Data), "topFilePath") {
				t.Fatalf("repos[] not served: %s", resp.Data)
			}
		})
	}
}
