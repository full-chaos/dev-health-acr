package directread

import (
	"encoding/json"
	"strings"
	"testing"
)

// A dropped sibling connection describes the page that was returned: no rows,
// more exist, the end cursor unknown; totalCount still counts the full read.
func TestADroppedSiblingConnectionDescribesTheReturnedPage(t *testing.T) {
	pad := strings.Repeat("y", 200)
	rows := make([]map[string]any, 50)
	for i := range rows {
		rows[i] = map[string]any{"filePath": pad}
	}
	repos := []map[string]any{{"repoId": "r1", "cursor": "c9", "note": strings.Repeat("z", 400)}}
	data, _ := json.Marshal(map[string]any{"hotspots": map[string]any{
		"rows":  rows,
		"repos": map[string]any{"edges": repos, "pageInfo": map[string]any{"hasNextPage": false, "startCursor": "c1", "endCursor": "c9"}, "totalCount": 400},
	}})
	cut, ok := fitListPage(data, "hotspots.rows", 450, []string{"hotspots.repos.edges"})
	if !ok {
		t.Fatal("no cut")
	}
	var out struct {
		Hotspots struct {
			Repos struct {
				Edges    []any          `json:"edges"`
				PageInfo map[string]any `json:"pageInfo"`
				Total    int            `json:"totalCount"`
			} `json:"repos"`
		} `json:"hotspots"`
	}
	if err := json.Unmarshal(cut.data, &out); err != nil {
		t.Fatal(err)
	}
	info := out.Hotspots.Repos.PageInfo
	if len(out.Hotspots.Repos.Edges) != 0 || info["hasNextPage"] != true || info["endCursor"] != nil || info["startCursor"] != nil || out.Hotspots.Repos.Total != 400 || len(cut.dropped) != 1 {
		t.Fatalf("dropped connection: %s (dropped %v)", cut.data, cut.dropped)
	}
}

// One primary row that fits by itself is still served when only the emptied
// sibling brings the answer under the budget; without a sibling a single row
// has nothing to cut.
func TestASinglePrimaryRowIsServedBesideAnEmptiedSibling(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"hotspots": map[string]any{
		"rows":  []map[string]any{{"filePath": "a.go"}},
		"repos": []map[string]any{{"repoId": "r1", "note": strings.Repeat("z", 800)}},
	}})
	cut, ok := fitListPage(data, "hotspots.rows", 200, []string{"hotspots.repos"})
	if !ok || cut.rowsReturned != 1 || cut.rowsRead != 1 || len(cut.dropped) != 1 || cut.dropped[0] != "hotspots.repos" {
		t.Fatalf("cut %+v ok %v", cut, ok)
	}
	if strings.Contains(string(cut.data), "zzzz") {
		t.Fatalf("the sibling was not emptied: %s", cut.data)
	}
	if _, ok := fitListPage(data, "hotspots.rows", 200, nil); ok {
		t.Fatal("one row with no sibling was cut")
	}
}
