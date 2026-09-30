package contextpacket_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The org-scope sweep reads every catalog statement with the scope-aware
// reader in catalog_org_scope_sql_test.go (CHAOS-7244). It replaced a regex
// sweep that accepted an org predicate defeated by OR (or hidden anywhere in
// the text) and that tracked aliases for the whole statement, so an inner
// `FROM t AS p` inherited the binding of an outer `p`.

// orgScopeTableOracle is an independent, deliberately dumb table finder. The
// reader must find exactly the tables it finds in every catalog statement, so
// a statement shape the reader silently skips cannot pass as "no violation".
var (
	orgScopeTableOracle = regexp.MustCompile(`(?:FROM|JOIN)\s+([a-z_]+)`)
	// A comma-joined table (`FROM a AS x FINAL, b AS y FINAL`) follows the first
	// table of a FROM; the first-table pattern above never sees it.
	orgScopeCommaOracle = regexp.MustCompile(`(?:FROM|JOIN)\s+[a-z_]+(?:\s+(?:AS\s+[a-z_]+|FINAL))*((?:\s*,\s*[a-z_]+(?:\s+(?:AS\s+[a-z_]+|FINAL))*)+)`)
	orgScopeCommaTable  = regexp.MustCompile(`,\s*([a-z_]+)`)
)

// CHAOS-7237: every table of every packet catalog statement is scoped to the
// caller's organization. ops mints repos.id deterministically from the
// repository name, not per organization (providersync repositoryIdentity,
// externalRepoUUID, internal ingest NewSHA1(repo URL)), so two organizations
// that sync one repository share its UUID, and a join on repo_id alone
// returns the other organization's rows into this one's context packet.
func TestEveryCatalogStatementScopesEveryTableToTheOrganization(t *testing.T) {
	if len(contextpacket.SourceQueryCatalogV1) == 0 {
		t.Fatal("the catalog is empty: the sweep matched nothing")
	}
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		report, err := analyzeOrgScope(query.Statement)
		if err != nil {
			t.Errorf("%s: %v", query.ID, err)
			continue
		}
		var want []string
		for _, m := range orgScopeTableOracle.FindAllStringSubmatch(query.Statement, -1) {
			want = append(want, m[1])
		}
		for _, m := range orgScopeCommaOracle.FindAllStringSubmatch(query.Statement, -1) {
			for _, c := range orgScopeCommaTable.FindAllStringSubmatch(m[1], -1) {
				want = append(want, c[1])
			}
		}
		got := slices.Clone(report.Tables)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: the reader found tables %v, the text search found %v: the sweep skipped part of the statement", query.ID, got, want)
		}
		for _, violation := range report.Violations {
			t.Errorf("%s: %s: another organization's rows can be read\n%s", query.ID, violation, query.Statement)
		}
	}
}

// The sweep sees the defect it exists for: a repo_id-only join to repos.
func TestOrgScopeSweepFlagsARepositoryOnlyJoin(t *testing.T) {
	report, err := analyzeOrgScope(`SELECT 1 FROM git_pull_requests AS p FINAL INNER JOIN repos AS repo FINAL ON repo.id = p.repo_id WHERE repo.org_id = {org_id:String} AND repo.id = {repo_id:UUID}`)
	if err != nil || len(report.Violations) != 1 || report.Violations[0] != `table git_pull_requests (alias "p")` {
		t.Fatalf("violations = %v, err %v", report.Violations, err)
	}
	if report, _ := analyzeOrgScope(`SELECT 1 FROM git_pull_requests AS p FINAL INNER JOIN repos AS repo FINAL ON repo.id = p.repo_id AND repo.org_id = p.org_id WHERE repo.org_id = {org_id:String}`); len(report.Violations) != 0 {
		t.Fatalf("a join-scoped table flagged: %v", report.Violations)
	}
	if report, _ := analyzeOrgScope(`SELECT 1 FROM file_hotspot_daily WHERE repo_id = {repo_id:UUID}`); len(report.Violations) != 1 {
		t.Fatalf("an unaliased unscoped table passed: %v", report.Violations)
	}
}

const orgScopeBound = `{org_id:String}`

// Every shape below is a statement the guard must judge correctly. want lists
// the exact violations; a shape with no want must be clean (the guard must not
// cry wolf on the forms the catalog uses); a shape with wantErr must be
// refused as unreadable, never passed.
func TestOrgScopeSweepIsStructural(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		want    []string
		wantErr string
	}{
		// ---- scoped: the sweep must accept these
		{name: "bound in WHERE", sql: `SELECT 1 FROM repos FINAL WHERE org_id = ` + orgScopeBound},
		{name: "bound, parameter first", sql: `SELECT 1 FROM repos AS r FINAL WHERE ` + orgScopeBound + ` = r.org_id`},
		{name: "bound through toString", sql: `SELECT 1 FROM repos AS r WHERE toString(r.org_id) = ` + orgScopeBound},
		{name: "bound in a parenthesised AND group", sql: `SELECT 1 FROM repos AS r WHERE (r.org_id = ` + orgScopeBound + ` AND r.id = {repo_id:UUID}) AND r.repo = 'x'`},
		{name: "bound after BETWEEN ... AND", sql: `SELECT 1 FROM repos AS r WHERE r.last_synced BETWEEN 1 AND 2 AND r.org_id = ` + orgScopeBound},
		{name: "OR group next to the binding", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND ({branch:String} = '' OR r.ref = {branch:String})`},
		{name: "join scoped by ON equality", sql: `SELECT 1 FROM git_commits AS c FINAL INNER JOIN repos AS repo FINAL ON repo.id = c.repo_id AND repo.org_id = c.org_id WHERE repo.org_id = ` + orgScopeBound},
		{name: "join scoped by ON binding of the joined table", sql: `SELECT 1 FROM repos AS r INNER JOIN git_commits AS c ON c.repo_id = r.id AND c.org_id = ` + orgScopeBound + ` WHERE r.org_id = ` + orgScopeBound},
		{name: "left join scoped by ON binding of the joined table", sql: `SELECT 1 FROM repos AS r LEFT JOIN git_commits AS c ON c.repo_id = r.id AND c.org_id = ` + orgScopeBound + ` WHERE r.org_id = ` + orgScopeBound},
		{name: "left join scoped by ON equality from a bound left table", sql: `SELECT 1 FROM repos AS r LEFT JOIN git_commits AS c ON c.repo_id = r.id AND c.org_id = r.org_id WHERE r.org_id = ` + orgScopeBound},
		{name: "comma join, both bound in WHERE", sql: `SELECT 1 FROM repos AS r, git_commits AS c WHERE r.org_id = ` + orgScopeBound + ` AND c.org_id = r.org_id`},
		{name: "scoped subquery in FROM", sql: `SELECT 1 FROM (SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound + `)`},
		{name: "scoped derived table with an alias", sql: `SELECT 1 FROM (SELECT id FROM repos WHERE org_id = ` + orgScopeBound + `) AS x`},
		{name: "scoped scalar subquery in WHERE", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND (r.last_synced) = (SELECT max(last_synced) FROM repos WHERE org_id = ` + orgScopeBound + `)`},
		{name: "scoped EXISTS with its own alias", sql: `SELECT 1 FROM git_commits AS c FINAL WHERE c.org_id = ` + orgScopeBound + ` AND EXISTS (SELECT 1 FROM git_commit_stats AS s FINAL WHERE s.org_id = ` + orgScopeBound + `)`},
		{name: "correlated EXISTS scoped by equality to a bound outer table", sql: `SELECT 1 FROM git_commits AS c FINAL WHERE c.org_id = ` + orgScopeBound + ` AND EXISTS (SELECT 1 FROM git_commit_stats AS s FINAL WHERE s.org_id = c.org_id)`},
		{name: "scoped UNION ALL sides", sql: `SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound + ` UNION ALL SELECT 1 FROM git_commits WHERE org_id = ` + orgScopeBound},
		{name: "scoped CTE", sql: `WITH x AS (SELECT id FROM repos WHERE org_id = ` + orgScopeBound + `) SELECT 1 FROM x`},
		{name: "SETTINGS and LIMIT after the WHERE", sql: `SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound + ` ORDER BY id LIMIT 1 SETTINGS force_optimize_projection_name='p'`},
		{name: "org_id in a string and a comment is ignored", sql: "SELECT 'p.org_id = {org_id:String}' AS s FROM repos WHERE org_id = " + orgScopeBound + " -- p.org_id = {org_id:String}\n"},

		// ---- CHAOS-7244: an OR defeats the org predicate
		{name: "OR at the top of the WHERE", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` OR r.id = {repo_id:UUID}`, want: []string{`table repos (alias "r")`}},
		{name: "OR before the binding", sql: `SELECT 1 FROM repos AS r WHERE r.id = {repo_id:UUID} OR r.org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "AND binds tighter than OR", sql: `SELECT 1 FROM repos AS r WHERE r.id = {repo_id:UUID} OR r.repo = 'x' AND r.org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "the binding inside an OR group", sql: `SELECT 1 FROM repos AS r WHERE (r.org_id = ` + orgScopeBound + ` OR 1 = 1) AND r.id = {repo_id:UUID}`, want: []string{`table repos (alias "r")`}},
		{name: "OR in the ON that carries the join equality", sql: `SELECT 1 FROM repos AS repo INNER JOIN git_commits AS c ON repo.id = c.repo_id AND repo.org_id = c.org_id OR c.repo_id = repo.id WHERE repo.org_id = ` + orgScopeBound, want: []string{`table git_commits (alias "c")`}},
		{name: "OR in the WHERE that carries the join equality", sql: `SELECT 1 FROM repos AS repo INNER JOIN git_commits AS c ON repo.id = c.repo_id WHERE repo.org_id = ` + orgScopeBound + ` AND (c.org_id = repo.org_id OR 1 = 1)`, want: []string{`table git_commits (alias "c")`}},
		{name: "OR inside a scoped subquery WHERE", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND r.id IN (SELECT repo_id FROM git_commits WHERE org_id = ` + orgScopeBound + ` OR repo_id = {repo_id:UUID})`, want: []string{`table git_commits (alias "")`}},

		// ---- the same class: text that is not a top-level conjunct
		{name: "NOT", sql: `SELECT 1 FROM repos AS r WHERE NOT (r.org_id = ` + orgScopeBound + `)`, want: []string{`table repos (alias "r")`}},
		{name: "not equal", sql: `SELECT 1 FROM repos AS r WHERE r.org_id != ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "inside a function", sql: `SELECT 1 FROM repos AS r WHERE if(r.org_id = ` + orgScopeBound + `, 1, 0) = 1`, want: []string{`table repos (alias "r")`}},
		{name: "inside CASE", sql: `SELECT 1 FROM repos AS r WHERE CASE WHEN 1 = 1 AND r.org_id = ` + orgScopeBound + ` THEN 1 ELSE 0 END = 1`, want: []string{`table repos (alias "r")`}},
		{name: "in the select list", sql: `SELECT r.org_id = ` + orgScopeBound + ` AS ok FROM repos AS r WHERE r.id = {repo_id:UUID}`, want: []string{`table repos (alias "r")`}},
		{name: "in HAVING", sql: `SELECT 1 FROM repos AS r WHERE r.id = {repo_id:UUID} GROUP BY r.id HAVING r.org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "in a string", sql: `SELECT 1 FROM repos AS r WHERE r.id = {repo_id:UUID} AND 'r.org_id = {org_id:String}' != ''`, want: []string{`table repos (alias "r")`}},
		{name: "in a comment", sql: "SELECT 1 FROM repos AS r WHERE r.id = {repo_id:UUID} -- AND r.org_id = {org_id:String}\n", want: []string{`table repos (alias "r")`}},
		{name: "another parameter", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = {other:String}`, want: []string{`table repos (alias "r")`}},
		{name: "another column", sql: `SELECT 1 FROM repos AS r WHERE r.repo = ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "the binding names the other table", sql: `SELECT 1 FROM repos AS r INNER JOIN git_commits AS c ON r.id = c.repo_id WHERE c.org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "an unqualified binding in a scope with joins is ambiguous", sql: `SELECT 1 FROM repos AS r INNER JOIN git_commits AS c ON r.id = c.repo_id WHERE org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`, `table git_commits (alias "c")`}},

		// ---- CHAOS-7244 / #731 r2 P2: aliases are per scope
		{name: "an inner alias does not inherit the outer binding", sql: `SELECT 1 FROM git_commits AS p FINAL WHERE p.org_id = ` + orgScopeBound + ` AND EXISTS (SELECT 1 FROM git_commit_stats AS p FINAL)`, want: []string{`table git_commit_stats (alias "p")`}},
		{name: "an inner binding does not scope the outer table", sql: `SELECT 1 FROM git_commits AS p FINAL WHERE EXISTS (SELECT 1 FROM git_commit_stats AS q FINAL WHERE q.org_id = ` + orgScopeBound + `)`, want: []string{`table git_commits (alias "p")`}},
		{name: "an inner binding of the same alias does not scope the outer table", sql: `SELECT 1 FROM git_commits AS p FINAL WHERE EXISTS (SELECT 1 FROM git_commit_stats AS p FINAL WHERE p.org_id = ` + orgScopeBound + `)`, want: []string{`table git_commits (alias "p")`}},
		{name: "an inner binding that names an outer alias scopes nothing", sql: `SELECT 1 FROM git_commits AS p FINAL WHERE EXISTS (SELECT 1 FROM git_commit_stats AS q FINAL WHERE p.org_id = ` + orgScopeBound + `)`, want: []string{`table git_commits (alias "p")`, `table git_commit_stats (alias "q")`}},
		{name: "a correlated equality with an unbound outer table", sql: `SELECT 1 FROM git_commits AS c FINAL WHERE EXISTS (SELECT 1 FROM git_commit_stats AS s FINAL WHERE s.org_id = c.org_id)`, want: []string{`table git_commits (alias "c")`, `table git_commit_stats (alias "s")`}},
		{name: "a subquery in FROM without a binding", sql: `SELECT 1 FROM (SELECT id FROM repos) AS x`, want: []string{`table repos (alias "")`}},
		{name: "an unscoped scalar subquery", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND r.last_synced = (SELECT max(last_synced) FROM repos)`, want: []string{`table repos (alias "")`}},
		{name: "an unscoped IN subquery", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND r.id IN (SELECT repo_id FROM git_commits)`, want: []string{`table git_commits (alias "")`}},
		{name: "a subquery in the select list", sql: `SELECT (SELECT count() FROM git_commits) AS n FROM repos AS r WHERE r.org_id = ` + orgScopeBound, want: []string{`table git_commits (alias "")`}},
		{name: "the second side of a UNION ALL", sql: `SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound + ` UNION ALL SELECT 1 FROM git_commits`, want: []string{`table git_commits (alias "")`}},
		{name: "a parenthesised UNION side", sql: `SELECT 1 FROM ((SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound + `) UNION ALL (SELECT 1 FROM git_commits))`, want: []string{`table git_commits (alias "")`}},
		{name: "an unscoped CTE body", sql: `WITH x AS (SELECT id FROM repos) SELECT 1 FROM x`, want: []string{`table repos (alias "")`}},
		{name: "a table that shadows a CTE name is a table", sql: `WITH repos AS (SELECT 1 AS id FROM git_commits WHERE org_id = ` + orgScopeBound + `) SELECT 1 FROM git_commits AS c, repos`, want: []string{`table git_commits (alias "c")`}},

		// ---- comma-separated FROM tables (#731 r3): every table of the list is read
		{name: "comma FROM: the second table has no binding", sql: `SELECT 1 FROM git_commits AS c FINAL, git_commit_stats AS s FINAL WHERE c.org_id = ` + orgScopeBound, want: []string{`table git_commit_stats (alias "s")`}},
		{name: "comma FROM: the first table has no binding", sql: `SELECT 1 FROM git_commits AS c FINAL, git_commit_stats AS s FINAL WHERE s.org_id = ` + orgScopeBound, want: []string{`table git_commits (alias "c")`}},
		{name: "comma FROM: unaliased tables, one bound by its name", sql: `SELECT 1 FROM git_commits, git_commit_stats WHERE git_commits.org_id = ` + orgScopeBound, want: []string{`table git_commit_stats (alias "")`}},
		{name: "comma FROM: an unqualified binding is ambiguous", sql: `SELECT 1 FROM git_commits AS c, git_commit_stats AS s WHERE org_id = ` + orgScopeBound, want: []string{`table git_commits (alias "c")`, `table git_commit_stats (alias "s")`}},
		{name: "comma FROM: the third table has no binding", sql: `SELECT 1 FROM repos AS r, git_commits AS c, git_commit_stats AS s WHERE r.org_id = ` + orgScopeBound + ` AND c.org_id = r.org_id`, want: []string{`table git_commit_stats (alias "s")`}},
		{name: "comma FROM: linked only by a non-org equality", sql: `SELECT 1 FROM git_commits AS c, git_commit_stats AS s WHERE c.org_id = ` + orgScopeBound + ` AND s.repo_id = c.repo_id`, want: []string{`table git_commit_stats (alias "s")`}},
		{name: "comma FROM: an OR defeats both bindings", sql: `SELECT 1 FROM git_commits AS c, git_commit_stats AS s WHERE c.org_id = ` + orgScopeBound + ` OR s.org_id = ` + orgScopeBound, want: []string{`table git_commits (alias "c")`, `table git_commit_stats (alias "s")`}},
		{name: "comma FROM after a scoped derived table", sql: `SELECT 1 FROM (SELECT id FROM repos WHERE org_id = ` + orgScopeBound + `) AS x, git_commits AS c WHERE c.repo_id = x.id`, want: []string{`table git_commits (alias "c")`}},
		{name: "comma FROM inside a subquery", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND EXISTS (SELECT 1 FROM git_commits AS c, git_commit_stats AS s WHERE c.org_id = ` + orgScopeBound + `)`, want: []string{`table git_commit_stats (alias "s")`}},
		{name: "comma FROM before a JOIN", sql: `SELECT 1 FROM repos AS r, git_commits AS c INNER JOIN git_commit_stats AS s ON s.commit_hash = c.hash WHERE r.org_id = ` + orgScopeBound + ` AND c.org_id = r.org_id`, want: []string{`table git_commit_stats (alias "s")`}},
		{name: "comma FROM with every table bound", sql: `SELECT 1 FROM repos AS r, git_commits AS c, git_commit_stats AS s WHERE r.org_id = ` + orgScopeBound + ` AND c.org_id = r.org_id AND s.org_id = c.org_id`},
		{name: "commas in the select list and in a function call are not tables", sql: `SELECT r.id, concat(r.repo, 'x'), if(r.id = 1, 2, 3) FROM repos AS r WHERE r.org_id = ` + orgScopeBound},

		// ---- an alias can rebind a column name (r1 P1): deny by default
		{name: "a SELECT alias rebinds org_id (the r1 probe)", sql: `SELECT repo, {org_id:String} AS org_id FROM repos FINAL WHERE org_id = ` + orgScopeBound, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "an implicit alias rebinds org_id", sql: `SELECT {org_id:String} org_id FROM repos WHERE org_id = ` + orgScopeBound, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "a WITH alias rebinds org_id", sql: `WITH {org_id:String} AS org_id SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "an alias in a function argument rebinds org_id", sql: `SELECT 1 FROM repos WHERE org_id = ` + orgScopeBound + ` AND toString({org_id:String} AS org_id) != ''`, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "an alias of the qualified column is still org_id", sql: `SELECT r.org_id AS org_id FROM repos AS r WHERE r.org_id = ` + orgScopeBound, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "an alias in a subquery scope rebinds org_id there", sql: `SELECT 1 FROM repos AS r WHERE r.org_id = ` + orgScopeBound + ` AND EXISTS (SELECT 1 AS org_id FROM git_commits AS c WHERE c.org_id = ` + orgScopeBound + `)`, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "an alias named org_id on a derived-table select", sql: `SELECT org_id FROM (SELECT {org_id:String} AS org_id FROM repos WHERE org_id = ` + orgScopeBound + `)`, want: []string{`alias "org_id" rebinds the organization column`}},
		{name: "a declared column shadowed and used unqualified", sql: `SELECT toString(id) AS repo FROM repos WHERE org_id = ` + orgScopeBound + ` AND repo = 'x'`, want: []string{`alias "repo" shadows a column of repos that the scope also reads`}},
		{name: "a declared column shadowed and used in another SELECT item", sql: `SELECT toString(id) AS repo, upper(repo) FROM repos WHERE org_id = ` + orgScopeBound, want: []string{`alias "repo" shadows a column of repos that the scope also reads`}},
		{name: "a declared column shadowed but only used qualified", sql: `SELECT r.repo AS repo FROM repos AS r WHERE r.org_id = ` + orgScopeBound},
		{name: "a declared column aliased and never read again", sql: `SELECT toString(id) id FROM repos WHERE org_id = ` + orgScopeBound},
		{name: "an undeclared table: an alias read again unqualified", sql: `SELECT c.hash AS sha FROM git_commits AS c WHERE c.org_id = ` + orgScopeBound + ` AND sha = 'x'`, want: []string{`alias "sha" rebinds a name the scope also reads, and a table it reads has no declared columns`}},
		{name: "an undeclared table: an alias never read again", sql: `SELECT c.hash AS sha FROM git_commits AS c WHERE c.org_id = ` + orgScopeBound},
		{name: "an alias that only renames a name the derived table exposes", sql: `SELECT toFloat64(confidence) confidence FROM (SELECT 1.0 confidence FROM repos WHERE org_id = ` + orgScopeBound + `)`},
		{name: "a table alias equal to a column name is not an expression alias", sql: `SELECT 1 FROM git_commits AS c INNER JOIN repos AS repo ON repo.id = c.repo_id AND repo.org_id = c.org_id WHERE c.org_id = ` + orgScopeBound},
		{name: "CAST ... AS Type is not an alias of a column", sql: `SELECT CAST(r.id AS String) FROM repos AS r WHERE r.org_id = ` + orgScopeBound},
		{name: "operators and IS NULL at the end of an item are not aliases", sql: `SELECT r.id IS NULL, r.id + 1, r.repo = 'x' AND r.id != 2 FROM repos AS r WHERE r.org_id = ` + orgScopeBound},
		{name: "an AS that names nothing", sql: `SELECT 1 AS FROM repos WHERE org_id = ` + orgScopeBound, wantErr: "an AS that names nothing"},
		{name: "an AS followed by a group", sql: `SELECT 1 AS (2) FROM repos WHERE org_id = ` + orgScopeBound, wantErr: "an AS that names nothing"},
		{name: "an empty SELECT item", sql: `SELECT , 1 FROM repos WHERE org_id = ` + orgScopeBound, wantErr: "an empty SELECT item"},

		// ---- outer joins: an ON keeps the rows of the preserved side
		{name: "left join: a binding of the preserved left table in ON", sql: `SELECT 1 FROM repos AS r LEFT JOIN git_commits AS c ON c.repo_id = r.id AND r.org_id = ` + orgScopeBound + ` AND c.org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`}},
		{name: "left join: an equality that only scopes the left table", sql: `SELECT 1 FROM repos AS r LEFT JOIN git_commits AS c ON c.repo_id = r.id AND c.org_id = ` + orgScopeBound + ` AND r.org_id = c.org_id`, want: []string{`table repos (alias "r")`}},
		{name: "right join: a binding in ON scopes nothing", sql: `SELECT 1 FROM repos AS r RIGHT JOIN git_commits AS c ON c.repo_id = r.id AND c.org_id = ` + orgScopeBound + ` AND r.org_id = ` + orgScopeBound, want: []string{`table repos (alias "r")`, `table git_commits (alias "c")`}},

		// ---- shapes the reader must refuse, never pass
		{name: "USING", sql: `SELECT 1 FROM repos AS r INNER JOIN git_commits AS c USING (org_id) WHERE r.org_id = ` + orgScopeBound, wantErr: "USING"},
		{name: "ARRAY JOIN", sql: `SELECT 1 FROM repos AS r ARRAY JOIN [1] AS x WHERE r.org_id = ` + orgScopeBound, wantErr: "ARRAY JOIN"},
		{name: "a table function", sql: `SELECT 1 FROM remote('h', db, repos) WHERE org_id = ` + orgScopeBound, wantErr: "table function"},
		{name: "the same name twice in one scope", sql: `SELECT 1 FROM repos AS r INNER JOIN git_commits AS r ON r.id = r.repo_id WHERE r.org_id = ` + orgScopeBound, wantErr: "used twice"},
		{name: "an unterminated string", sql: `SELECT 1 FROM repos WHERE repo = 'x AND org_id = ` + orgScopeBound, wantErr: "unterminated string"},
		{name: "an unbalanced parenthesis", sql: `SELECT 1 FROM repos WHERE (org_id = ` + orgScopeBound, wantErr: "unclosed"},
		{name: "an unterminated parameter", sql: `SELECT 1 FROM repos WHERE org_id = {org_id:String`, wantErr: "unterminated {parameter}"},
		{name: "no table", sql: `SELECT 1`, wantErr: "no table found"},
		{name: "not a SELECT", sql: `INSERT INTO repos VALUES (1)`, wantErr: "does not start with SELECT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := analyzeOrgScope(tc.sql)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q (violations %v)", err, tc.wantErr, report.Violations)
				}
				return
			}
			if err != nil {
				t.Fatalf("unreadable: %v", err)
			}
			got := slices.Clone(report.Violations)
			want := slices.Clone(tc.want)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("violations = %v, want %v\n%s", got, want, tc.sql)
			}
		})
	}
}
