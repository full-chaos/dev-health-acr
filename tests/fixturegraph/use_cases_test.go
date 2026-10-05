//go:build fixturegraph

package fixturegraph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func slugs(t *testing.T) (one, two string) {
	return mustEnv(t, "FG_SCOPED_SLUG"), mustEnv(t, "FG_OTHER_SLUG")
}

// subjectID finds a repository's canonical id through find_subjects, as a client does.
func subjectID(t *testing.T, c *client, slug string) string {
	t.Helper()
	d, _ := c.call("find_subjects", doc{"query": slug, "kinds": []string{"repository"}})
	var ids []string
	for _, s := range list(d, "subjects") {
		if str(s, "label") == slug && str(s, "kind") == "repository" {
			ids = append(ids, str(s, "canonical_id"))
		}
	}
	if len(ids) != 1 {
		t.Fatalf("find_subjects %q returned %d repositories named so, want 1: %v", slug, len(ids), d)
	}
	return ids[0]
}

// walkInterpretation is the interpretation a client model would produce for
// "Which issues belong to repository <slug>?"
func walkInterpretation(slug string) doc {
	return doc{
		"shape": "explicit_cohort", "requested_judgment": "list the work items of the repository",
		"subject_terms": []string{slug}, "scope_anchor_term": slug, "scope_anchor_kind": "repository",
		"requested_subject_kind": "work_item", "time_context": doc{"axis": "current"},
		"fact_requirements": []doc{{"kind": "status"}, {"kind": "work"}}, "clarification_needed": false,
		"question_frame": doc{
			"goals": []string{"assess_state"}, "temporal": "current",
			"subject_expression": doc{"kind": "children_of_scope", "anchor_terms": []string{slug}, "member_kind": "work_item"},
		},
	}
}

func walk(t *testing.T, c *client, slug string) (doc, string) {
	t.Helper()
	c.requireTools("investigate_with_interpretation")
	args := doc{
		"question":       fmt.Sprintf("Which issues belong to repository %s?", slug),
		"interpretation": walkInterpretation(slug),
		"contract":       c.interpretContract(),
		"synthesis":      "client",
		"budget":         doc{"max_cohort_members": 250, "max_evidence_refs": 500, "max_serialized_bytes": 1048576},
	}
	d, raw := c.call("investigate_with_interpretation", args)
	if str(structured(d), "status") != "clarification_required" {
		return d, raw
	}
	// The fixture names a project and a repository alike, so the service asks which one was
	// meant. A client answers by confirming the repository receipt of that answer.
	var receipts []doc
	var collect func(v any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if rid, ok := x["receipt_id"].(string); ok && str(x, "subject", "kind") == "repository" {
				receipts = append(receipts, doc{"result_id": str(structured(d), "result_id"), "receipt_id": rid})
			}
			for _, child := range x {
				collect(child)
			}
		case []any:
			for _, child := range x {
				collect(child)
			}
		}
	}
	collect(structured(d))
	if len(receipts) != 1 {
		t.Fatalf("clarification offered %d repository receipts, want 1: %.2000s", len(receipts), raw)
	}
	args["parent_result_id"] = str(structured(d), "result_id")
	args["prior_subject_receipts"] = receipts
	return c.call("investigate_with_interpretation", args)
}

func structured(d doc) any {
	if s, ok := d["structured"]; ok {
		return s
	}
	return d
}

func memberLabels(d doc) map[string]bool {
	m := map[string]bool{}
	for _, mem := range list(structured(d), "cohort", "members") {
		m[str(mem, "subject", "label")] = true
	}
	return m
}

func TestFixtureWorldsHoldTwoRepositoriesWithLinks(t *testing.T) {
	one, two := slugs(t)
	if n := chScalar(t, fmt.Sprintf("SELECT count() FROM repos FINAL WHERE org_id = %s", sqlStr(orgID(t)))); n != "2" {
		t.Fatalf("the organization holds %s repositories, want the 2 the two frozen worlds define", n)
	}
	for _, slug := range []string{one, two} {
		sql := fmt.Sprintf(`SELECT uniqExact(l.work_item_id) - uniqExact(w.title) FROM work_graph_issue_pr AS l FINAL
INNER JOIN repos AS r FINAL ON r.id = l.repo_id AND r.org_id = l.org_id
INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
WHERE l.org_id = %s AND r.repo = %s`, sqlStr(orgID(t)), sqlStr(slug))
		if d := chScalar(t, sql); d != "0" {
			t.Fatalf("%s: linked issues share titles (%s collisions): the served label cannot identify an issue", slug, d)
		}
	}
	a, b := issueSet(linkRows(t, one)), issueSet(linkRows(t, two))
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("a world holds no issue linked to a pull request: %s=%d %s=%d", one, len(a), two, len(b))
	}
	for k := range a {
		if b[k] {
			t.Fatalf("issue %s is linked from both repositories: the restricted-caller case needs disjoint sets", k)
		}
	}
}

func TestFindSubjectsServesEverySeededRepository(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("find_subjects", "data_catalog")
	want := map[string]bool{}
	for _, r := range ch(t, fmt.Sprintf("SELECT repo FROM repos FINAL WHERE org_id = %s", sqlStr(orgID(t)))) {
		want[r[0]] = true
	}
	d, _ := c.call("find_subjects", doc{"kind": "repository", "limit": 200})
	got := map[string]bool{}
	for _, s := range list(d, "subjects") {
		got[str(s, "label")] = true
	}
	if len(got) != len(want) || diffSets(want, got) != diffSets(want, want) {
		t.Fatalf("served repositories differ from seeded: %s", diffSets(want, got))
	}
	if cat, _ := c.call("data_catalog", doc{"sections": []string{"facts", "subjects", "relationships", "limits"}}); str(cat, "status") == "" && len(cat) == 0 {
		t.Fatal("data_catalog returned nothing")
	}
}

// Use-case: the issues of a repository come through its pull requests' link rows, and the
// census of the walk equals the members returned.
func TestRepositoryIssuesComeThroughItsPullRequestLinks(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	one, two := slugs(t)
	for _, slug := range []string{one, two} {
		want := issueSet(linkRows(t, slug))
		d, _ := walk(t, c, slug)
		got := memberLabels(d)
		if len(want) == 0 {
			t.Fatalf("no seeded link for %s", slug)
		}
		if diffSets(want, got) != diffSets(want, want) {
			t.Fatalf("%s: members differ from the seeded link rows: %s\nseeded rows of the missing issues (title, type, status, provider, own repo, pr, tier):\n%s\nanswer digest: %.3000s", slug, diffSets(want, got), describeMissing(t, slug, want, got), answerDigest(d))
		}
		cohort := get(structured(d), "cohort")
		total, _ := get(cohort, "total").(float64)
		if int(total) != len(got) || int(total) != len(want) {
			t.Fatalf("%s: census total=%v members=%d seeded=%d, want all equal", slug, total, len(got), len(want))
		}
		if complete, _ := get(cohort, "complete").(bool); !complete {
			t.Fatalf("%s: the walk did not read every member: %v", slug, cohort)
		}
		if truncated, _ := get(cohort, "truncated").(bool); truncated {
			t.Fatalf("%s: the cohort is truncated", slug)
		}
	}
}

// Use-case: read_relationships serves LINKS_PULL_REQUEST with its tier, equal to the tier of
// the seeded link row.
func TestReadRelationshipsServesTheLinkTier(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("read_relationships", "find_subjects")
	one, _ := slugs(t)
	repoCanonical := subjectID(t, c, one)
	rows := linkRows(t, one)
	checked := 0
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.pr] || checked >= 6 {
			continue
		}
		seen[r.pr] = true
		d, _ := c.call("find_subjects", doc{"handle": "PR " + r.pr, "anchor": doc{"kind": "repository", "id": repoCanonical}})
		subs := list(d, "subjects")
		if len(subs) != 1 {
			t.Fatalf("find_subjects handle PR %s returned %d subjects: %v", r.pr, len(subs), d)
		}
		prID := str(subs[0], "canonical_id")
		rel, raw := c.call("read_relationships", doc{"subject": doc{"kind": "pull_request", "canonical_id": prID}, "types": []string{"LINKS_PULL_REQUEST"}, "direction": "both", "depth": 1, "limit": 100})
		wantTier := map[string]string{}
		for _, x := range rows {
			if x.pr == r.pr {
				wantTier[x.issue] = x.tier
			}
		}
		gotTier := map[string]string{}
		for _, e := range list(rel, "edges") {
			if str(e, "type") != "LINKS_PULL_REQUEST" {
				t.Fatalf("PR %s: unexpected edge type in %s", r.pr, raw)
			}
			label := str(e, "from", "label")
			if str(e, "from", "kind") != "work_item" {
				label = str(e, "to", "label")
			}
			gotTier[label] = str(e, "provenance", "link_tier")
		}
		if fmt.Sprint(wantTier) != fmt.Sprint(gotTier) {
			t.Fatalf("PR %s: served link tiers %v differ from seeded %v\n%.2000s", r.pr, gotTier, wantTier, raw)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no pull request was checked")
	}
}

// Use-case: a credential granted one repository sees no member or field of another.
func TestRestrictedCallerSeesNothingOfAnotherRepository(t *testing.T) {
	org := connect(t, "FG_ORG_TOKEN_FILE")
	scoped := connect(t, "FG_SCOPED_TOKEN_FILE")
	one, two := slugs(t)
	foreignRepoID := subjectID(t, org, two)
	foreignIssues := issueSet(linkRows(t, two))

	// own repository: the walk still serves every member
	d, _ := walk(t, scoped, one)
	own := issueSet(linkRows(t, one))
	if got := memberLabels(d); diffSets(own, got) != diffSets(own, own) {
		t.Fatalf("restricted caller lost its own members: %s", diffSets(own, got))
	}

	// the other repository: the walk serves no member and leaks no seeded identifier
	d, raw := walk(t, scoped, two)
	if got := memberLabels(d); len(got) != 0 {
		t.Fatalf("restricted caller saw members of another repository: %v", sortedKeys(got))
	}
	assertNoForeignText(t, raw, two, foreignIssues)

	// subjects and edges of the other repository answer as not found, for every tool
	subs, raw := scoped.call("find_subjects", doc{"kind": "repository", "limit": 200})
	for _, s := range list(subs, "subjects") {
		if str(s, "label") == two {
			t.Fatalf("find_subjects listed the other repository to a restricted caller: %s", raw)
		}
	}
	facts, raw := scoped.call("read_facts", doc{"kinds": []string{"metrics"}, "subjects": []doc{{"kind": "repository", "canonical_id": foreignRepoID}}})
	if n := len(list(facts, "facts")); n != 0 {
		t.Fatalf("read_facts served %d facts of another repository: %.1500s", n, raw)
	}
	if answer := str(list(facts, "request", "subjects_refused")[0], "answer"); answer != "denied_or_not_found" {
		t.Fatalf("read_facts refusal answer = %q, want denied_or_not_found", answer)
	}
	assertNoForeignText(t, raw, two, foreignIssues)
	rel, raw := scoped.call("read_relationships", doc{"subject": doc{"kind": "repository", "canonical_id": foreignRepoID}, "direction": "both", "depth": 1})
	if n := len(list(rel, "edges")); n != 0 {
		t.Fatalf("read_relationships served %d edges of another repository: %.1500s", n, raw)
	}
	assertNoForeignText(t, raw, two, foreignIssues)
}

func assertNoForeignText(t *testing.T, raw, slug string, issues map[string]bool) {
	t.Helper()
	if strings.Contains(raw, slug) {
		t.Fatalf("a response to a restricted caller names the other repository %q: %.1500s", slug, raw)
	}
	for k := range issues {
		if strings.Contains(raw, k) {
			t.Fatalf("a response to a restricted caller names an issue %q of the other repository: %.1500s", k, raw)
		}
	}
}

// Use-case: a period total states its coverage. The total the service serves must equal the
// sum of the seeded daily rows, and a repository with no seeded daily rows must be answered as
// not measured, never as zero.
func TestPeriodTotalStatesItsCoverage(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("read_facts")
	one, _ := slugs(t)
	id := subjectID(t, c, one)
	rows := ch(t, fmt.Sprintf("SELECT toString(min(day)), toString(max(day)), toString(count()), toString(sum(commits_count)) FROM repo_metrics_daily FINAL WHERE org_id = %s AND repo_id = toUUID(%s)", sqlStr(orgID(t)), sqlStr(repoID(t, one))))
	seededDays := rows[0][2]
	d, raw := c.call("read_facts", doc{"kinds": []string{"metrics"}, "subjects": []doc{{"kind": "repository", "canonical_id": id}}, "tables": "include", "window": doc{"mode": "trailing", "days": 60}})
	cov := list(d, "coverage")
	if len(cov) != 1 {
		t.Fatalf("want one coverage row for (metrics, repository), got %d: %.1500s", len(cov), raw)
	}
	outcome := str(cov[0], "outcome")
	if seededDays == "0" {
		if outcome == "measured_zero" || outcome == "fact_served" {
			t.Fatalf("no daily rows are seeded, yet coverage says %q: a missing period was served as measured: %.1500s", outcome, raw)
		}
		return
	}
	if outcome != "fact_served" {
		t.Fatalf("%s daily rows are seeded, coverage outcome = %q: %.1500s", seededDays, outcome, raw)
	}
	var sum float64
	for _, f := range list(d, "facts") {
		tbl := get(f, "tables", "daily_metrics")
		cols := list(tbl, "columns")
		idx := -1
		for i, col := range cols {
			if col == "commits_count" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("no commits_count column in %.1500s", raw)
		}
		for _, row := range list(tbl, "rows") {
			v, _ := row.([]any)[idx].(float64)
			sum += v
		}
	}
	if fmt.Sprint(int64(sum)) != rows[0][3] {
		t.Fatalf("served commits total %v != seeded sum %s", sum, rows[0][3])
	}
}

func describeMissing(t *testing.T, slug string, want, got map[string]bool) string {
	var out []string
	for k := range want {
		if got[k] {
			continue
		}
		rows := ch(t, fmt.Sprintf(`SELECT w.title, w.type, w.status, w.provider, toString(w.repo_id), toString(l.pr_number), l.provenance
FROM work_graph_issue_pr AS l FINAL INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
WHERE l.org_id = %s AND w.title = %s`, sqlStr(orgID(t)), sqlStr(k)))
		for _, r := range rows {
			out = append(out, strings.Join(r, " | "))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// answerDigest is the part of an answer that explains a short member list.
func answerDigest(d doc) string {
	st, _ := structured(d).(map[string]any)
	cohort := map[string]any{}
	if c, ok := st["cohort"].(map[string]any); ok {
		for k, v := range c {
			if k != "members" {
				cohort[k] = v
			}
		}
	}
	b, _ := json.Marshal(doc{"status": st["status"], "cohort": cohort, "limitations": st["limitations"], "coverage_summary": st["coverage_summary"], "coverage_details": st["coverage_details"], "completeness": st["completeness"], "warnings": st["warnings"]})
	return string(b)
}
