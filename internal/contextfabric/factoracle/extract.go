package factoracle

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// Window is the one extract window: [Start, End), both UTC midnights.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// MaxWindowDays bounds the extract window.
const MaxWindowDays = 31

// Validate refuses a window that is not whole UTC days or is too long.
func (w Window) Validate() error {
	if !w.Start.Before(w.End) {
		return fmt.Errorf("window start must be before its end")
	}
	for _, t := range []time.Time{w.Start, w.End} {
		if t.Location() != time.UTC || !t.Equal(t.Truncate(24*time.Hour)) {
			return fmt.Errorf("window bounds must be UTC midnights")
		}
	}
	if w.End.Sub(w.Start) > MaxWindowDays*24*time.Hour {
		return fmt.Errorf("window is longer than %d days", MaxWindowDays)
	}
	return nil
}

func (w Window) startDate() string { return w.Start.Format("2006-01-02") }
func (w Window) endDate() string   { return w.End.Format("2006-01-02") }

// lastDay is the last whole day inside the window.
func (w Window) lastDay() string { return w.End.Add(-24 * time.Hour).Format("2006-01-02") }

// columnRule says what the scrubber does with one column.
type columnRule int

const (
	ruleKeep columnRule = iota + 1
	ruleOrg
	ruleUUID
	ruleTeam
	ruleWorkScope
	ruleWorkUnit
	ruleRun
	ruleNode
	ruleSlug
	ruleHash
	ruleDrop
	ruleDropArray
	ruleEvidence
	ruleScopeID
)

// tableSpec is one extracted table: its row predicate (the org predicate is
// always added) and one rule per column the fact readers select. The column
// list itself is devhealthschema.ProductionColumns, the declaration the fact
// reader fixtures are built from; a declared column with no rule here stops
// the capture.
type tableSpec struct {
	Table string
	// Where uses {start:String} and {end:String} (YYYY-MM-DD) and
	// {org:String}.
	Where string
	// OrderBy and LimitBy keep the first row of each LimitBy group.
	OrderBy string
	LimitBy string
	Rules   map[string]columnRule
}

const unitsInWindow = `work_unit_id IN (
	SELECT work_unit_id FROM work_unit_investments
	WHERE org_id = {org:String}
	  AND from_ts < toDateTime64({end:String}, 3, 'UTC')
	  AND to_ts >= toDateTime64({start:String}, 3, 'UTC'))`

// extractTables are the tables the value-compared pairs read.
var extractTables = []tableSpec{
	{Table: "work_unit_investments", Where: unitsInWindow, Rules: map[string]columnRule{
		"work_unit_id": ruleWorkUnit, "from_ts": ruleKeep, "to_ts": ruleKeep, "repo_id": ruleUUID, "effort_value": ruleKeep,
		"theme_distribution_json": ruleKeep, "subcategory_distribution_json": ruleKeep, "structural_evidence_json": ruleEvidence,
		"computed_at": ruleKeep, "org_id": ruleOrg,
	}},
	{Table: "work_unit_supersessions", Where: strings.Replace(unitsInWindow, "work_unit_id IN", "superseded_work_unit_id IN", 1), Rules: map[string]columnRule{
		"org_id": ruleOrg, "superseded_work_unit_id": ruleWorkUnit, "superseded_at": ruleKeep,
	}},
	{Table: "work_unit_membership_runs", Where: "1", Rules: map[string]columnRule{
		"org_id": ruleOrg, "run_id": ruleRun, "completed_at": ruleKeep,
	}},
	// One membership row per work unit of the latest complete run is enough
	// for the scope filter, which reads DISTINCT work_unit_id of that run.
	{Table: "work_unit_membership", LimitBy: "work_unit_id",
		Where: unitsInWindow + ` AND run_id = (SELECT argMax(run_id, completed_at) FROM work_unit_membership_runs WHERE org_id = {org:String})`,
		Rules: map[string]columnRule{
			"org_id": ruleOrg, "node_type": ruleKeep, "node_id": ruleNode, "work_unit_id": ruleWorkUnit, "category_kind": ruleKeep,
			"category": ruleHash, "computed_at": ruleKeep, "run_id": ruleRun,
		}},
	{Table: "repos", Where: "1", Rules: map[string]columnRule{
		"id": ruleUUID, "repo": ruleSlug, "ref": ruleDrop, "created_at": ruleKeep, "tags": ruleDrop, "last_synced": ruleKeep,
		"org_id": ruleOrg, "provider": ruleKeep,
	}},
	{Table: "teams", Where: "1", Rules: map[string]columnRule{
		"id": ruleTeam, "name": ruleHash, "description": ruleDrop, "updated_at": ruleKeep, "org_id": ruleOrg, "provider": ruleKeep,
		"native_team_key": ruleDrop, "project_keys": ruleDropArray, "is_active": ruleKeep, "last_synced": ruleKeep,
	}},
	// Ownership rows that ended before the window can match no read of it.
	// The source writes one more row, equal but for valid_from and
	// updated_at, at every sync; of such rows the earliest is valid whenever
	// a later one is, so it alone decides every read.
	{Table: "team_repo_ownership", Where: "valid_to IS NULL OR valid_to >= toDateTime64({start:String}, 3, 'UTC')",
		OrderBy: "valid_from ASC, updated_at ASC",
		LimitBy: "provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_to",
		Rules: map[string]columnRule{
			"org_id": ruleOrg, "provider": ruleKeep, "team_id": ruleTeam, "repo_id": ruleUUID, "repo_full_name": ruleSlug, "match_type": ruleKeep,
			"source": ruleKeep, "is_primary": ruleKeep, "specificity": ruleKeep, "priority": ruleKeep, "valid_from": ruleKeep, "valid_to": ruleKeep,
			"updated_at": ruleKeep,
		}},
	{Table: "capacity_forecasts", Where: "toDate(computed_at) >= toDate({start:String}) AND toDate(computed_at) < toDate({end:String})", Rules: map[string]columnRule{
		"forecast_id": ruleUUID, "computed_at": ruleKeep, "team_id": ruleTeam, "work_scope_id": ruleWorkScope, "backlog_size": ruleKeep,
		"p50_days": ruleKeep, "throughput_mean": ruleKeep, "throughput_stddev": ruleKeep, "insufficient_history": ruleKeep,
		"high_variance": ruleKeep, "org_id": ruleOrg,
	}},
	{Table: "compounding_risk_daily", Where: "day >= toDate({start:String}) AND day < toDate({end:String})", Rules: map[string]columnRule{
		"org_id": ruleOrg, "day": ruleKeep, "scope": ruleKeep, "scope_id": ruleScopeID, "compounding_risk": ruleKeep, "severity": ruleKeep,
		"churn_norm": ruleKeep, "complexity_norm": ruleKeep, "ownership_norm": ruleKeep, "review_norm": ruleKeep, "w_churn": ruleKeep,
		"w_complexity": ruleKeep, "w_ownership": ruleKeep, "w_review": ruleKeep, "computed_at": ruleKeep,
	}},
	{Table: "work_item_metrics_daily", Where: "day >= toDate({start:String}) AND day < toDate({end:String})", Rules: map[string]columnRule{
		"day": ruleKeep, "provider": ruleKeep, "work_scope_id": ruleWorkScope, "team_id": ruleTeam, "items_started": ruleKeep,
		"items_completed": ruleKeep, "wip_count_end_of_day": ruleKeep, "cycle_time_p50_hours": ruleKeep, "cycle_time_p90_hours": ruleKeep,
		"lead_time_p50_hours": ruleKeep, "lead_time_p90_hours": ruleKeep, "wip_age_p50_hours": ruleKeep, "wip_age_p90_hours": ruleKeep,
		"bug_completed_ratio": ruleKeep, "story_points_completed": ruleKeep, "computed_at": ruleKeep, "org_id": ruleOrg,
	}},
	{Table: "estimate_coverage_metrics_daily", Where: "day >= toDate({start:String}) AND day < toDate({end:String})", Rules: map[string]columnRule{
		"day": ruleKeep, "provider": ruleKeep, "work_scope_id": ruleWorkScope, "team_id": ruleTeam, "estimated_count": ruleKeep,
		"unestimated_count": ruleKeep, "backlog_size": ruleKeep, "ratio": ruleKeep, "computed_at": ruleKeep, "org_id": ruleOrg,
	}},
}

// ExtractTableNames lists the extracted tables, in seeding order.
func ExtractTableNames() []string {
	out := make([]string, 0, len(extractTables))
	for _, spec := range extractTables {
		out = append(out, spec.Table)
	}
	return out
}

// Row is one table row as ClickHouse prints it in JSONEachRow.
type Row map[string]any

// Extract is the rows of the extracted tables.
type Extract struct {
	Tables map[string][]Row
}

// RowQuerier runs a statement with named string parameters and yields the
// first column of every row.
type RowQuerier interface {
	QueryStrings(ctx context.Context, statement string, params map[string]string, each func(string) error) error
}

func declaredColumns(table string) ([]string, error) {
	columns, ok := devhealthschema.ProductionColumns[table]
	if !ok {
		return nil, fmt.Errorf("table %s has no declared columns", table)
	}
	out := make([]string, 0, len(columns))
	for _, c := range columns {
		out = append(out, c.Name)
	}
	return out, nil
}

// checkSpecs proves every declared column of every extract table has a rule
// and no rule names a column that is not declared.
func checkSpecs() error { return checkSpecList(extractTables) }

func checkSpecList(specs []tableSpec) error {
	for _, spec := range specs {
		columns, err := declaredColumns(spec.Table)
		if err != nil {
			return err
		}
		declared := map[string]bool{}
		for _, c := range columns {
			declared[c] = true
			if _, ok := spec.Rules[c]; !ok {
				return fmt.Errorf("table %s: column %s has no scrub rule", spec.Table, c)
			}
		}
		for c := range spec.Rules {
			if !declared[c] {
				return fmt.Errorf("table %s: rule for %s, which is not a declared column", spec.Table, c)
			}
		}
	}
	return nil
}

// CaptureExtract reads the extract tables for one organization and window.
// It runs SELECT statements only. The rows are NOT scrubbed.
func CaptureExtract(ctx context.Context, db RowQuerier, orgID string, window Window) (*Extract, error) {
	if err := window.Validate(); err != nil {
		return nil, err
	}
	if err := checkSpecs(); err != nil {
		return nil, err
	}
	params := map[string]string{"org": orgID, "start": window.startDate(), "end": window.endDate()}
	out := &Extract{Tables: map[string][]Row{}}
	for _, spec := range extractTables {
		columns, _ := declaredColumns(spec.Table)
		statement := "SELECT formatRow('JSONEachRow', " + strings.Join(columns, ", ") + ") FROM " + spec.Table +
			" WHERE org_id = {org:String} AND (" + spec.Where + ")"
		if spec.OrderBy != "" {
			statement += " ORDER BY " + spec.OrderBy
		}
		if spec.LimitBy != "" {
			statement += " LIMIT 1 BY " + spec.LimitBy
		}
		rows := []Row{}
		err := db.QueryStrings(ctx, statement, params, func(line string) error {
			decoded, derr := decodeJSON([]byte(line))
			if derr != nil {
				return fmt.Errorf("table %s: a row is not JSON: %v", spec.Table, derr)
			}
			row, ok := decoded.(map[string]any)
			if !ok {
				return fmt.Errorf("table %s: a row is not an object", spec.Table)
			}
			rows = append(rows, row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", spec.Table, err)
		}
		out.Tables[spec.Table] = rows
	}
	return out, nil
}

// Scrubbed returns a copy of the extract with every column rule applied.
func (e *Extract) Scrubbed(s *Scrubber) (*Extract, error) {
	if err := checkSpecs(); err != nil {
		return nil, err
	}
	out := &Extract{Tables: map[string][]Row{}}
	for _, spec := range extractTables {
		rows := make([]Row, 0, len(e.Tables[spec.Table]))
		for _, row := range e.Tables[spec.Table] {
			clean := Row{}
			for column, value := range row {
				rule, ok := spec.Rules[column]
				if !ok {
					return nil, fmt.Errorf("table %s: column %s has no scrub rule", spec.Table, column)
				}
				scrubbed, err := scrubValue(s, rule, value, row)
				if err != nil {
					return nil, fmt.Errorf("table %s column %s: %w", spec.Table, column, err)
				}
				clean[column] = scrubbed
			}
			rows = append(rows, clean)
		}
		out.Tables[spec.Table] = rows
	}
	return out, nil
}

func scrubValue(s *Scrubber, rule columnRule, value any, row Row) (any, error) {
	if rule == ruleKeep {
		return value, nil
	}
	if rule == ruleDropArray {
		return []any{}, nil
	}
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("want a string value, got %T", value)
	}
	switch rule {
	case ruleOrg:
		return s.Org(text)
	case ruleUUID:
		return s.UUID(text), nil
	case ruleTeam:
		return s.Token("team", text), nil
	case ruleWorkScope:
		return s.Token("scope", text), nil
	case ruleWorkUnit:
		return s.Token("wu", text), nil
	case ruleRun:
		if text == "" || text == legacyRunID {
			return text, nil
		}
		return s.Token("run", text), nil
	case ruleNode:
		return s.Token("node", text), nil
	case ruleSlug:
		return s.Slug(text), nil
	case ruleHash:
		return s.Token("h", text), nil
	case ruleDrop:
		return "", nil
	case ruleEvidence:
		return s.Evidence(text), nil
	case ruleScopeID:
		if scope, _ := row["scope"].(string); scope == "repo" {
			return s.UUID(text), nil
		}
		return s.Token("team", text), nil
	default:
		return nil, fmt.Errorf("scrub rule %d is not known", rule)
	}
}

// legacyRunID is the reserved run id of the ops legacy membership marker.
const legacyRunID = "__legacy__"

func extractFile(dir, table string) string { return filepath.Join(dir, table+".jsonl.gz") }

// Write stores the extract, one gzip JSON-lines file per table, rows in a
// stable order.
func (e *Extract) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, spec := range extractTables {
		lines := make([]string, 0, len(e.Tables[spec.Table]))
		for _, row := range e.Tables[spec.Table] {
			encoded, err := json.Marshal(row)
			if err != nil {
				return err
			}
			lines = append(lines, string(encoded))
		}
		sort.Strings(lines)
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		for _, line := range lines {
			_, _ = zw.Write([]byte(line))
			_, _ = zw.Write([]byte{'\n'})
		}
		if err := zw.Close(); err != nil {
			return err
		}
		if err := os.WriteFile(extractFile(dir, spec.Table), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ReadExtract loads an extract written by Write. A missing table file is an
// error: an absent table is never read as an empty one.
func ReadExtract(dir string) (*Extract, error) {
	out := &Extract{Tables: map[string][]Row{}}
	for _, spec := range extractTables {
		file, err := os.Open(extractFile(dir, spec.Table))
		if err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("table %s: %w", spec.Table, err)
		}
		rows := []Row{}
		scanner := bufio.NewScanner(zr)
		scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for scanner.Scan() {
			decoded, derr := decodeJSON(scanner.Bytes())
			if derr != nil {
				_ = file.Close()
				return nil, fmt.Errorf("table %s: %w", spec.Table, derr)
			}
			row, ok := decoded.(map[string]any)
			if !ok {
				_ = file.Close()
				return nil, fmt.Errorf("table %s: a row is not an object", spec.Table)
			}
			rows = append(rows, row)
		}
		err = scanner.Err()
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", spec.Table, err)
		}
		out.Tables[spec.Table] = rows
	}
	return out, nil
}

// Clone copies the extract so a case can change rows without touching the
// shared one.
func (e *Extract) Clone() *Extract {
	out := &Extract{Tables: map[string][]Row{}}
	for table, rows := range e.Tables {
		copied := make([]Row, 0, len(rows))
		for _, row := range rows {
			next := Row{}
			for k, v := range row {
				next[k] = v
			}
			copied = append(copied, next)
		}
		out.Tables[table] = copied
	}
	return out
}

// SeedTarget is a ClickHouse HTTP endpoint the extract is loaded into.
type SeedTarget struct {
	// BaseURL is the HTTP interface, for example http://127.0.0.1:8123.
	BaseURL  string
	User     string
	Password string
	Database string
	Client   *http.Client
}

func (t SeedTarget) exec(ctx context.Context, database, statement string, body io.Reader) error {
	query := url.Values{}
	query.Set("query", statement)
	if database != "" {
		query.Set("database", database)
	}
	if body == nil {
		body = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/?"+query.Encode(), body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(t.User, t.Password)
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("clickhouse answered %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Seed creates the database and the extract tables, with the declared
// production types and engines, and loads the rows. ClickHouse parses its own
// JSONEachRow, so no value passes through a Go type on the way in.
func (e *Extract) Seed(ctx context.Context, target SeedTarget) error {
	if err := target.exec(ctx, "", "CREATE DATABASE "+target.Database, nil); err != nil {
		return err
	}
	names := ExtractTableNames()
	for _, statement := range devhealthschema.DDL(names...) {
		if err := target.exec(ctx, target.Database, statement, nil); err != nil {
			return err
		}
	}
	for _, table := range names {
		rows := e.Tables[table]
		if len(rows) == 0 {
			continue
		}
		var body bytes.Buffer
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return err
			}
			body.Write(encoded)
			body.WriteByte('\n')
		}
		if err := target.exec(ctx, target.Database, "INSERT INTO "+table+" FORMAT JSONEachRow", &body); err != nil {
			return fmt.Errorf("table %s: %w", table, err)
		}
	}
	return nil
}
