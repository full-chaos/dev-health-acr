//go:build fixturegraph

// Package fixturegraph is the standing use-case venue: real fixture-generator rows in
// ClickHouse, the real projector's graph in FalkorDB, the real acr-api, and the real acr-mcp
// driven as a client. Every expected value is derived from the seeded rows by a ClickHouse
// query; none is typed. scripts/e2e/fixture-graph.sh provisions the stack and runs this package.
package fixturegraph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func mustEnv(t testing.TB, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Fatalf("%s is not set: the venue did not provision this run", name)
	}
	return v
}

func readTokenFile(t testing.TB, envName string) string {
	t.Helper()
	b, err := os.ReadFile(mustEnv(t, envName))
	if err != nil {
		t.Fatalf("read %s: %v", envName, err)
	}
	return strings.TrimSpace(string(b))
}

// ch runs one SQL statement inside the isolated ClickHouse and returns its tab-separated rows.
func ch(t testing.TB, sql string) [][]string {
	t.Helper()
	cmd := exec.Command(mustEnv(t, "FG_CH_QUERY"))
	cmd.Stdin = strings.NewReader(sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("clickhouse query failed: %v\n%s\nSQL: %s", err, stderr.String(), sql)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

func chScalar(t testing.TB, sql string) string {
	t.Helper()
	rows := ch(t, sql)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("want one scalar from %q, got %v", sql, rows)
	}
	return rows[0][0]
}

func orgID(t testing.TB) string { return mustEnv(t, "FG_ORG_ID") }

func sqlStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

type client struct {
	t       testing.TB
	session *mcpsdk.ClientSession
	tools   map[string]bool
}

// connect launches the real acr-mcp binary exactly as an agent client would.
func connect(t *testing.T, tokenEnv string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, mustEnv(t, "FG_MCP_BIN"), "serve")
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+mustEnv(t, "FG_API_URL"),
		"ACR_API_CA_BUNDLE="+mustEnv(t, "FG_CA_FILE"),
		"ACR_API_TOKEN="+readTokenFile(t, tokenEnv),
		"ACR_SIDECAR_VERSION=1.0.0",
		"ACR_SIDECAR_CLIENT_VERSION=1.0.0",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cl := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "fixture-graph", Version: "0.0.1"}, nil)
	session, err := cl.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to acr-mcp: %v\nstderr: %s", err, stderr.String())
	}
	t.Cleanup(func() { _ = session.Close() })
	c := &client{t: t, session: session, tools: map[string]bool{}}
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, tool := range list.Tools {
		c.tools[tool.Name] = true
	}
	return c
}

func (c *client) requireTools(names ...string) {
	c.t.Helper()
	for _, n := range names {
		if !c.tools[n] {
			c.t.Fatalf("the server did not advertise tool %s (advertised: %v)", n, c.toolNames())
		}
	}
}

func (c *client) toolNames() []string {
	var n []string
	for k := range c.tools {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

type doc = map[string]any

// call invokes a tool and returns its structured document plus the raw JSON text for scans.
func (c *client) call(name string, args doc) (doc, string) {
	c.t.Helper()
	res, err := c.session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
	raw, _ := json.Marshal(res)
	if res.IsError {
		c.t.Fatalf("%s returned a tool error: %s", name, raw)
	}
	body, _ := json.Marshal(res.StructuredContent)
	var d doc
	if err := json.Unmarshal(body, &d); err != nil || d == nil {
		c.t.Fatalf("%s returned no structured document: %s", name, raw)
	}
	return d, string(raw)
}

func (c *client) interpretContract() doc {
	c.t.Helper()
	res, err := c.session.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{Name: "interpret_question", Arguments: map[string]string{"question": "which issues belong to a repository"}})
	if err != nil {
		c.t.Fatalf("prompts/get interpret_question: %v", err)
	}
	meta := res.Meta
	out := doc{}
	for _, k := range []string{"model_output_version", "prompt_version", "system_sha256"} {
		v, _ := meta[k].(string)
		if v == "" {
			c.t.Fatalf("prompt _meta lacks %s: %v", k, meta)
		}
		out[k] = v
	}
	return out
}

func get(d any, path ...string) any {
	cur := d
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func list(d any, path ...string) []any {
	l, _ := get(d, path...).([]any)
	return l
}

func str(d any, path ...string) string {
	s, _ := get(d, path...).(string)
	return s
}

func sortedKeys(m map[string]bool) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func diffSets(want, got map[string]bool) string {
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Sprintf("missing from served=%v extra in served=%v", missing, extra)
}

// repoID returns the seeded repository id for a slug.
func repoID(t testing.TB, slug string) string {
	return chScalar(t, fmt.Sprintf("SELECT toString(id) FROM repos FINAL WHERE org_id = %s AND repo = %s", sqlStr(orgID(t)), sqlStr(slug)))
}

// linkRow (issue = the issue's title, which is the label the service serves) is one issue-to-pull-request link of the entity tree for one repository.
type linkRow struct {
	issue, tier string
	pr          string
}

// linkRows reads the links of a repository the way the tree defines them: the issues linked
// to the repository's pull requests through work_graph_issue_pr, never an issue's own repo.
func linkRows(t testing.TB, slug string) []linkRow {
	sql := fmt.Sprintf(`SELECT w.title, l.provenance, toString(l.pr_number)
FROM work_graph_issue_pr AS l FINAL
INNER JOIN git_pull_requests AS p FINAL ON p.org_id = l.org_id AND p.repo_id = l.repo_id AND p.number = l.pr_number
INNER JOIN repos AS r FINAL ON r.id = l.repo_id AND r.org_id = l.org_id
INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
WHERE l.org_id = %s AND r.repo = %s AND l.provenance IN ('native','explicit_text','heuristic') AND lower(w.type) NOT IN ('pr','merge_request')
ORDER BY w.title, l.pr_number`, sqlStr(orgID(t)), sqlStr(slug))
	var out []linkRow
	for _, r := range ch(t, sql) {
		out = append(out, linkRow{issue: r[0], tier: r[1], pr: r[2]})
	}
	return out
}

func issueSet(rows []linkRow) map[string]bool {
	m := map[string]bool{}
	for _, r := range rows {
		m[r.issue] = true
	}
	return m
}
