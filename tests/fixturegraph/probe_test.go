//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func edgeTable(rel doc) string {
	type agg struct {
		n        int
		from, to string
	}
	m := map[string]*agg{}
	for _, e := range list(rel, "edges") {
		ty := str(e, "type")
		a := m[ty]
		if a == nil {
			a = &agg{from: str(e, "provenance", "valid_from"), to: fmt.Sprint(get(e, "provenance", "valid_to"))}
			m[ty] = a
		}
		a.n++
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s x%d (example valid_from=%s valid_to=%s)", k, m[k].n, m[k].from, m[k].to))
	}
	if len(out) == 0 {
		return "(no edge) status=" + str(rel, "status")
	}
	return strings.Join(out, "; ")
}

func TestProbeEdgeTypesOnTheCurrentAxis(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	one, _ := slugs(t)
	repoCanonical := subjectID(t, c, one)
	rows := ch(t, fmt.Sprintf(`SELECT toString(p.number), w.work_item_id,
  formatDateTime(if(least(w.completed_at, p.merged_at) > greatest(w.created_at, p.created_at) + INTERVAL 2 MINUTE, greatest(w.created_at, p.created_at) + INTERVAL 1 MINUTE, least(w.completed_at, p.merged_at) - INTERVAL 1 MINUTE), '%%Y-%%m-%%dT%%H:%%i:%%SZ'),
  formatDateTime(p.merged_at - INTERVAL 1 MINUTE, '%%Y-%%m-%%dT%%H:%%i:%%SZ'), toString(p.merged_at), toString(w.completed_at)
FROM work_graph_issue_pr AS l FINAL
INNER JOIN git_pull_requests AS p FINAL ON p.org_id = l.org_id AND p.repo_id = l.repo_id AND p.number = l.pr_number
INNER JOIN repos AS r FINAL ON r.id = l.repo_id AND r.org_id = l.org_id
INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
WHERE l.org_id = %s AND r.repo = %s AND p.merged_at IS NOT NULL AND w.completed_at IS NOT NULL AND p.merged_at < now() AND w.completed_at < now()
ORDER BY (least(w.completed_at, p.merged_at) > greatest(w.created_at, p.created_at) + INTERVAL 2 MINUTE) DESC, p.number, w.work_item_id LIMIT 1`, sqlStr(orgID(t)), sqlStr(one)))
	if len(rows) != 1 {
		t.Fatalf("no merged pull request linked to a completed issue: %v", rows)
	}
	pr, key, asOfLink, asOfPR := rows[0][0], rows[0][1], rows[0][2], rows[0][3]
	t.Logf("PROBE subjects: PR %s (merged_at=%s), issue %s (completed_at=%s); as_of A (inside the link window) %s; as_of B (just before the merge) %s", pr, rows[0][4], key, rows[0][5], asOfLink, asOfPR)
	d, _ := c.call("find_subjects", doc{"handle": "PR " + pr, "anchor": doc{"kind": "repository", "id": repoCanonical}})
	prID := str(list(d, "subjects")[0], "canonical_id")
	read := func(kind, id string, extra doc) doc {
		args := doc{"subject": doc{"kind": kind, "canonical_id": id}, "direction": "both", "depth": 1, "limit": 100}
		for k, v := range extra {
			args[k] = v
		}
		rel, _ := c.call("read_relationships", args)
		return rel
	}
	prA := read("pull_request", prID, doc{"as_of": asOfLink})
	t.Logf("PROBE PR %s current : %s", pr, edgeTable(read("pull_request", prID, nil)))
	t.Logf("PROBE PR %s as_of A : %s", pr, edgeTable(prA))
	t.Logf("PROBE PR %s as_of B : %s", pr, edgeTable(read("pull_request", prID, doc{"as_of": asOfPR})))
	issueID, issueKey := "", ""
	for _, e := range list(read("pull_request", prID, doc{"as_of": asOfPR}), "edges") {
		if str(e, "type") == "LINKS_PULL_REQUEST" {
			issueID, issueKey = str(e, "from", "canonical_id"), str(e, "from", "label")
			if str(e, "from", "kind") != "work_item" {
				issueID, issueKey = str(e, "to", "canonical_id"), str(e, "to", "label")
			}
			break
		}
	}
	if issueID == "" {
		t.Fatal("PROBE no link edge at as_of B to take an issue from")
	}
	meta := ch(t, fmt.Sprintf("SELECT status, toString(created_at), toString(completed_at) FROM work_items FINAL WHERE org_id = %s AND work_item_id = %s", sqlStr(orgID(t)), sqlStr(issueKey)))
	t.Logf("PROBE issue %s (status, created_at, completed_at) = %v", issueKey, meta)
	t.Logf("PROBE issue current : %s", edgeTable(read("work_item", issueID, nil)))
	t.Logf("PROBE issue as_of B : %s", edgeTable(read("work_item", issueID, doc{"as_of": asOfPR})))
	t.Logf("PROBE issue as_of 2026-10-02T12:00:00Z : %s", edgeTable(read("work_item", issueID, doc{"as_of": "2026-10-02T12:00:00Z"})))
}
