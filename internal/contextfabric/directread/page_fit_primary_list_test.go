package directread_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// twoListCatalogue is the shipped catalogue with a second top-level list,
// hotspots.repos, added to hotspots, and hotspots.rows declared as its primary
// list (declared "") or left to inference.
func twoListCatalogue(t *testing.T, declared string) (*directread.Catalogue, error) {
	t.Helper()
	return twoListCatalogueChecked(t, declared, true)
}

// twoListCatalogueChecked can leave the second list out of the restricted
// caller's row check.
func twoListCatalogueChecked(t *testing.T, declared string, rowChecked bool) (*directread.Catalogue, error) {
	t.Helper()
	raw, err := os.ReadFile("operations.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var file directread.CatalogueFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for i := range file.Operations {
		if file.Operations[i].Name != "hotspots" {
			continue
		}
		op := &file.Operations[i]
		op.PrimaryListPath = declared
		for j := range op.Scopes {
			if rowChecked && op.Scopes[j].Caller == directread.CallerRestricted && !slices.Contains(op.Scopes[j].RowIDPaths, "hotspots.repos[*].repoId") {
				op.Scopes[j].RowIDPaths = append(op.Scopes[j].RowIDPaths, "hotspots.repos[*].repoId")
			}
		}
		for _, f := range []string{"repoId", "repoName", "topFilePath"} {
			path := "hotspots.repos[*]." + f
			if !slices.ContainsFunc(op.Outputs, func(o directread.OutputPath) bool { return o.Path == path }) {
				op.Outputs = append(op.Outputs, directread.OutputPath{Path: path, Type: "String!", Leaf: directread.LeafScalar})
			}
		}
	}
	out, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	return directread.LoadCatalogue(out)
}

func hotspotsTwoLists(rows, repos int, pad string) string {
	r := make([]map[string]any, rows)
	for i := range r {
		r[i] = map[string]any{"filePath": pad, "repoId": opRepoA}
	}
	g := make([]map[string]any, repos)
	for i := range g {
		g[i] = map[string]any{"repoId": opRepoA, "repoName": pad, "topFilePath": "a.go"}
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"hotspots": map[string]any{"rows": r, "repos": g}}})
	return string(raw)
}

func runTwoLists(t *testing.T, declared string, body string) directread.OperationResponse {
	t.Helper()
	cat, err := twoListCatalogue(t, declared)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := cat.Lookup("hotspots")
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, body }, opHarnessOptions{catalogue: cat})
	return h.run(t, opUnrestricted(opOrgA), "hotspots", opMinimalVariables(t, op))
}

func TestTwoListOperationWithoutADeclaredPrimaryListIsStillRefused(t *testing.T) {
	resp := runTwoLists(t, "", hotspotsTwoLists(400, 3, strings.Repeat("y", 120)))
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalResponseBudget {
		out, _ := json.Marshal(resp)
		t.Fatalf("%.300s", out)
	}
}

func TestDeclaredPrimaryListIsCutAndTheOtherListRidesAlongWhole(t *testing.T) {
	resp := runTwoLists(t, "hotspots.rows", hotspotsTwoLists(400, 3, strings.Repeat("y", 120)))
	if resp.Call != directread.CallServed || resp.Completeness != directread.CompletenessDeclaredPartial || resp.CompletenessReason != directread.ReasonPageCut {
		out, _ := json.Marshal(resp)
		t.Fatalf("%.400s", out)
	}
	var data struct {
		Hotspots struct {
			Rows  []map[string]any `json:"rows"`
			Repos []map[string]any `json:"repos"`
		} `json:"hotspots"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) > 32768 || len(data.Hotspots.Rows) < 1 || len(data.Hotspots.Rows) >= 400 || resp.Page.RowsRead != 400 || resp.Page.RowsReturned != len(data.Hotspots.Rows) {
		t.Fatalf("rows %d of %d, bytes %d", len(data.Hotspots.Rows), resp.Page.RowsRead, len(resp.Data))
	}
	if len(data.Hotspots.Repos) != 3 || strings.Contains(resp.Page.Cut, "left out: hotspots.repos") || strings.Contains(resp.Page.Cut, "did not fit beside") {
		t.Fatalf("repos %d, cut %q", len(data.Hotspots.Repos), resp.Page.Cut)
	}
}

func TestDeclaredPrimaryListDropsAndNamesTheOtherListWhenNoRowFitsBesideIt(t *testing.T) {
	resp := runTwoLists(t, "hotspots.rows", hotspotsTwoLists(400, 400, strings.Repeat("y", 120)))
	if resp.Call != directread.CallServed || resp.Completeness != directread.CompletenessDeclaredPartial || resp.CompletenessReason != directread.ReasonPageCut {
		out, _ := json.Marshal(resp)
		t.Fatalf("%.400s", out)
	}
	var data struct {
		Hotspots struct {
			Rows  []map[string]any `json:"rows"`
			Repos []map[string]any `json:"repos"`
		} `json:"hotspots"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Hotspots.Rows) < 1 || len(data.Hotspots.Repos) != 0 || !strings.Contains(resp.Page.Cut, "did not fit beside even one row and were left out") || !strings.Contains(resp.Page.Cut, "hotspots.repos") {
		t.Fatalf("rows %d repos %d cut %q", len(data.Hotspots.Rows), len(data.Hotspots.Repos), resp.Page.Cut)
	}
}

func TestADeclaredPrimaryListMustBeATopLevelListOutput(t *testing.T) {
	for _, bad := range []string{"hotspots.nope", "hotspots", "hotspots.rows[*]", "otherRoot.rows"} {
		if _, err := twoListCatalogue(t, bad); err == nil || !strings.Contains(err.Error(), "primary_list") {
			t.Fatalf("primary_list %q loaded: %v", bad, err)
		}
	}
}

func TestShippedHotspotsDeclaresRowsAsItsPrimaryList(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("hotspots")
	if got, ok := op.PrimaryList(); !ok || got != "hotspots.rows" || len(op.SiblingLists()) != 0 {
		t.Fatalf("primary %q %v siblings %v", got, ok, op.SiblingLists())
	}
}

func TestARestrictedScopeMustRowCheckEveryListOfADeclaredPrimaryListOperation(t *testing.T) {
	if _, err := twoListCatalogueChecked(t, "hotspots.rows", false); err == nil || !strings.Contains(err.Error(), "has no row id path") {
		t.Fatalf("a second list outside the row check loaded: %v", err)
	}
}

func TestARestrictedCallerNeverReceivesAForeignRepositoryFromTheSecondList(t *testing.T) {
	cat, err := twoListCatalogue(t, "hotspots.rows")
	if err != nil {
		t.Fatal(err)
	}
	op, _ := cat.Lookup("hotspots")
	scope := op.Scope(directread.CallerRestricted)
	vars := opMerge(opMinimalVariables(t, op), scope.ForcedVariablePath, []any{opRepo(opRepoA)})
	for _, tc := range []struct {
		name  string
		repos []any
		want  directread.CallStatus
	}{
		{"granted", []any{opRepoA}, directread.CallServed},
		{"foreign_only_in_the_second_list", []any{opRepoB}, directread.CallRefused},
		{"foreign_beside_granted", []any{opRepoA, opRepoB}, directread.CallRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []map[string]any{{"filePath": "a.go", "repoId": opRepoA}}
			repos := make([]map[string]any, 0, len(tc.repos))
			for _, id := range tc.repos {
				repos = append(repos, map[string]any{"repoId": id, "repoName": "r", "topFilePath": "a.go"})
			}
			raw, _ := json.Marshal(map[string]any{"data": map[string]any{"hotspots": map[string]any{"rows": rows, "repos": repos}}})
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{catalogue: cat})
			resp := h.run(t, opRestrictedA(), "hotspots", vars)
			if resp.Call != tc.want || (tc.want == directread.CallRefused && (resp.Refusal == nil || resp.Refusal.Code != directread.RefusalRowOutsideGrant || resp.Data != nil)) {
				out, _ := json.Marshal(resp)
				t.Fatalf("%.300s", out)
			}
		})
	}
}
