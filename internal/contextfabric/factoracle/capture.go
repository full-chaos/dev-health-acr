package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// VenueConfig names the venue. Nothing in it is written to the repository.
type VenueConfig struct {
	// MCPURL is the hosted MCP endpoint; TokenFile holds its bearer.
	MCPURL    string
	TokenFile string
	// ClickHouseDSNFile holds the read-only DSN acr itself uses;
	// ClickHouseAddr replaces the DSN's host when the DSN names a host that
	// only resolves inside the venue network.
	ClickHouseDSNFile string
	ClickHouseAddr    string
	// OrgID is the venue organization. It is used as a bind value only.
	OrgID  string
	Window Window
	// OpsBuild names the ops build the listener runs (commit and image).
	OpsBuild string
	// AcrBuild names the acr build the venue runs.
	AcrBuild string
	// ListenerDark and OperationDark name the root fields the venue does not
	// serve through graphql_query and through run_operation (Oracle).
	ListenerDark  []string
	OperationDark []string
}

type clickHouseQuerier struct{ conn clickhouse.Conn }

// venueQueryTimeout bounds one SELECT. The driver sends the context deadline
// as max_execution_time, and the venue's read-only profile caps that setting
// at 30 seconds.
const venueQueryTimeout = 25 * time.Second

func (q clickHouseQuerier) QueryStrings(ctx context.Context, statement string, params map[string]string, each func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, venueQueryTimeout)
	defer cancel()
	rows, err := q.conn.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(params))), statement)
	if err != nil {
		return fmt.Errorf("select failed: %s", safeClickHouseError(err))
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("row scan failed: %s", safeClickHouseError(err))
		}
		if err := each(strings.TrimRight(line, "\n")); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("select failed: %s", safeClickHouseError(err))
	}
	return nil
}

var clickHouseCode = regexp.MustCompile(`code: [0-9]+`)

// safeClickHouseError keeps the error code only: a ClickHouse error text can
// quote the statement and its bound values.
func safeClickHouseError(err error) string {
	if code := clickHouseCode.FindString(err.Error()); code != "" {
		return code
	}
	return "no code"
}

func openVenueClickHouse(cfg VenueConfig) (clickhouse.Conn, error) {
	dsn, err := os.ReadFile(cfg.ClickHouseDSNFile)
	if err != nil {
		return nil, fmt.Errorf("venue ClickHouse DSN file is not readable")
	}
	opts, err := clickhouse.ParseDSN(strings.TrimSpace(string(dsn)))
	if err != nil {
		return nil, fmt.Errorf("venue ClickHouse DSN does not parse")
	}
	if cfg.ClickHouseAddr != "" {
		opts.Addr = []string{cfg.ClickHouseAddr}
	}
	opts.DialTimeout = 10 * time.Second
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("venue ClickHouse connection could not be opened")
	}
	return conn, nil
}

// LiveRun is one live run on the venue.
type LiveRun struct {
	Report  *Report
	Oracle  *Oracle
	Extract *Extract
}

// RunLive runs the oracle on the venue: both planes through the hosted MCP
// server, the reference store read with SELECT statements from the venue
// ClickHouse. The store must not change while the run reads it: the extract
// is read before and after, and a run over a store that moved is refused
// (the error names the tables). Rows a job writes and rows a background merge
// removes both read as a store that moved.
func RunLive(ctx context.Context, cfg VenueConfig) (*LiveRun, error) {
	if cfg.MCPURL == "" || cfg.TokenFile == "" || cfg.ClickHouseDSNFile == "" || cfg.OrgID == "" {
		return nil, fmt.Errorf("venue configuration is incomplete")
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		return nil, err
	}
	conn, err := openVenueClickHouse(cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	db := clickHouseQuerier{conn: conn}
	const attempts = 2
	var moved []string
	for attempt := 1; attempt <= attempts; attempt++ {
		before, err := CaptureExtract(ctx, db, cfg.OrgID, cfg.Window)
		if err != nil {
			return nil, err
		}
		store, err := NewStore(before)
		if err != nil {
			return nil, err
		}
		oracle := &Oracle{Policy: policy, Planes: &VenuePlanes{URL: cfg.MCPURL, TokenFile: cfg.TokenFile}, Store: store, Window: cfg.Window,
			ListenerDark: cfg.ListenerDark, OperationDark: cfg.OperationDark}
		report, err := oracle.Run(ctx)
		if err != nil {
			return nil, err
		}
		after, err := CaptureExtract(ctx, db, cfg.OrgID, cfg.Window)
		if err != nil {
			return nil, err
		}
		if moved = movedTables(before, after); len(moved) == 0 {
			return &LiveRun{Report: report, Oracle: oracle, Extract: before}, nil
		}
	}
	return nil, fmt.Errorf("the venue store changed during each of %d runs (last: %s); no run is reported: run again when no job writes these tables", attempts, strings.Join(moved, ", "))
}

// movedTables names the extract tables whose rows are not the same in the
// two extracts.
func movedTables(before, after *Extract) []string {
	tables := map[string]bool{}
	for table := range before.Tables {
		tables[table] = true
	}
	for table := range after.Tables {
		tables[table] = true
	}
	var moved []string
	for _, table := range sortedKeys(tables) {
		if tableDigest(before.Tables[table]) != tableDigest(after.Tables[table]) {
			moved = append(moved, table)
		}
	}
	return moved
}

func tableDigest(rows []Row) string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		encoded, _ := json.Marshal(row)
		lines = append(lines, string(encoded))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func extractDigest(e *Extract) string {
	tables := make([]string, 0, len(e.Tables))
	for table := range e.Tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	var all []string
	for _, table := range tables {
		all = append(all, table+"\n"+tableDigest(e.Tables[table]))
	}
	return strings.Join(all, "\n\n")
}

// RootExpectation is the pinned outcome of one root.
type RootExpectation struct {
	Mode      string        `json:"mode"`
	Listener  string        `json:"listener"`
	ShapesRun int           `json:"shapes_run"`
	Leaves    int           `json:"leaves"`
	Compared  int           `json:"compared"`
	Matches   int           `json:"matches"`
	ByClass   map[Class]int `json:"by_class"`
	Findings  []Finding     `json:"findings"`
	NotJoined []string      `json:"not_joined"`
	// RunOperation, OperationsRun, OperationLeaves, CrossPaths and
	// CrossMatches are the run_operation pass and its compare with
	// graphql_query.
	RunOperation    string `json:"run_operation"`
	OperationsRun   int    `json:"operations_run"`
	OperationLeaves int    `json:"operation_leaves"`
	CrossPaths      int    `json:"cross_paths"`
	CrossMatches    int    `json:"cross_matches"`
	// Differences pins every named difference with its key and its values,
	// so a class that is named with other values is not the pinned outcome.
	Differences []PinnedDifference `json:"differences"`
	// Unmeasured, CodeRead and Invalid are the root's lists of the same name.
	Unmeasured []string `json:"unmeasured"`
	CodeRead   []string `json:"code_read"`
	Invalid    []string `json:"invalid"`
}

// PinnedDifference is the pinned form of a Difference.
type PinnedDifference struct {
	Pair   string             `json:"pair"`
	Key    string             `json:"key"`
	Class  Class              `json:"class"`
	Exact  bool               `json:"exact"`
	Values map[string]float64 `json:"values,omitempty"`
}

// Expectation summarizes a root report for pinning.
func Expectation(rr *RootReport) RootExpectation {
	out := RootExpectation{Mode: rr.Mode, Listener: rr.Listener, ShapesRun: rr.ShapesRun, Leaves: rr.Leaves, Compared: rr.Compared,
		Matches: rr.Matches, ByClass: map[Class]int{}, Findings: []Finding{}, NotJoined: []string{},
		RunOperation: rr.RunOperation, OperationsRun: rr.OperationsRun, OperationLeaves: rr.OperationLeaves, CrossPaths: rr.CrossPaths, CrossMatches: rr.CrossMatches,
		Differences: []PinnedDifference{}, Unmeasured: []string{}, CodeRead: []string{}, Invalid: []string{}}
	for class, n := range rr.ByClass {
		out.ByClass[class] = n
	}
	for _, d := range rr.Differences {
		out.Differences = append(out.Differences, PinnedDifference{Pair: d.Pair, Key: d.Key, Class: d.Class, Exact: d.Exact, Values: d.Values})
	}
	sort.SliceStable(out.Differences, func(i, j int) bool {
		a, b := out.Differences[i], out.Differences[j]
		if a.Pair != b.Pair {
			return a.Pair < b.Pair
		}
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		return a.Key < b.Key
	})
	out.Unmeasured = append(out.Unmeasured, rr.Unmeasured...)
	out.CodeRead = append(out.CodeRead, rr.CodeRead...)
	out.Invalid = append(out.Invalid, rr.Invalid...)
	for _, f := range rr.Findings {
		// The key of an ops call holds a digest of the venue variables.
		if strings.Contains(f.Key, "#") {
			f.Key = ""
		}
		out.Findings = append(out.Findings, f)
	}
	out.NotJoined = append(out.NotJoined, rr.NotJoined...)
	return out
}

// Manifest describes one capture.
type Manifest struct {
	CapturedAt string `json:"captured_at"`
	Window     Window `json:"window"`
	FixtureOrg string `json:"fixture_org"`
	// OpsBuild is the ops build the recorded replies came from.
	OpsBuild string `json:"ops_build"`
	AcrBuild string `json:"acr_build"`
	// SchemaDigest is the SDL the shapes and the replies belong to. A build
	// that pins another SDL must be captured again.
	SchemaDigest string `json:"schema_digest"`
	// VenueFactQueryVersions are the fact query versions the venue's acr
	// answered with. FactQueryVersion is the version of the build whose
	// providers gave Expect (see Repin); the providers run in every test, so
	// a version that differs is information, not a failure.
	VenueFactQueryVersions []string `json:"venue_fact_query_versions"`
	FactQueryVersion       string   `json:"fact_query_version"`
	// Rows counts the extract rows per table.
	Rows map[string]int `json:"rows"`
	// ShapeCases is the shape pass of the capture, variables scrubbed.
	ShapeCases []ShapeCase `json:"shape_cases"`
	// Residual is the investment residual per theme the venue gave.
	Residual map[string]float64 `json:"residual"`
	// Expect is the venue outcome per root.
	Expect map[string]RootExpectation `json:"expect"`
	// ListenerDark and OperationDark are the root fields the venue did not
	// serve through graphql_query and through run_operation, as the capture
	// was told (VenueConfig).
	ListenerDark  []string `json:"listener_dark"`
	OperationDark []string `json:"operation_dark"`
	// DeniedTeams are, per fact kind, the teams (ids scrubbed like the
	// extract) the venue token had no grant for: the capture left them out,
	// and so does a recorded run.
	DeniedTeams map[string][]string `json:"denied_teams,omitempty"`
	// SubjectTeam is the team (id scrubbed like the extract) the team-scoped
	// shapes ran for: the first measurable team. SubjectTeamsSkipped counts
	// the teams before it that gave no backlog size.
	SubjectTeam         string `json:"subject_team,omitempty"`
	SubjectTeamsSkipped int    `json:"subject_teams_skipped,omitempty"`
}

// Capture file names under the capture directory.
const (
	ManifestFile = "manifest.json"
	RepliesFile  = "replies.json"
	ExtractDir   = "extract"
)

// Capture runs the oracle on the venue and writes the scrubbed extract, the
// scrubbed recorded replies and the manifest to dir.
func Capture(ctx context.Context, cfg VenueConfig, dir string) (*LiveRun, error) {
	if strings.TrimSpace(cfg.OpsBuild) == "" {
		return nil, fmt.Errorf("a capture must name the ops build its replies come from")
	}
	run, err := RunLive(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// A run that is not a measurement is not recorded.
	if invalid := run.Report.Invalid(); len(invalid) > 0 {
		return run, fmt.Errorf("the venue run is not a measurement; nothing was written: %s", strings.Join(invalid, "; "))
	}
	scrubber, err := NewScrubber(cfg.OrgID)
	if err != nil {
		return nil, err
	}
	scrubbed, err := run.Extract.Scrubbed(scrubber)
	if err != nil {
		return nil, err
	}
	schema := run.Oracle.Policy.Schema()
	recording := Recording{Replies: map[string]RecordedReply{}}
	for _, call := range run.Oracle.Calls {
		shape, err := run.Oracle.shape(call.ShapeID)
		if err != nil {
			return nil, err
		}
		raw, ok := run.Oracle.Answer(call)
		if !ok {
			return nil, fmt.Errorf("call %s has no answer", call.ShapeID)
		}
		var answer GraphQLAnswer
		if err := decodeNumbered(raw, &answer); err != nil {
			return nil, err
		}
		variables := scrubVariables(scrubber, call.Variables)
		reply := RecordedReply{Call: answer.Call, Result: answer.Result}
		switch answer.Call {
		case string(directread.CallServed):
			data, derr := decodeJSON(answer.Data)
			if derr != nil {
				return nil, fmt.Errorf("call %s: data is not JSON", call.ShapeID)
			}
			clean, serr := scrubReply(scrubber, schema, shape, call.Variables, data)
			if serr != nil {
				return nil, serr
			}
			encoded, merr := json.Marshal(clean)
			if merr != nil {
				return nil, merr
			}
			reply.Status, reply.Data = 200, encoded
			if len(answer.RootFields) == 1 {
				reply.Operation = answer.RootFields[0].Operation
			}
		case string(directread.CallOperationUnavailable):
			reply.Status, reply.Reason = 404, directread.ListenerNotFoundRootNotEnabled
		default:
			// A refusal by acr itself reaches no listener: nothing to replay.
			continue
		}
		recording.Replies[CaseKey(shape, variables)] = reply
	}
	for _, call := range run.Oracle.OperationCalls {
		shape, err := run.Oracle.shape(call.ShapeID)
		if err != nil {
			return nil, err
		}
		raw, ok := run.Oracle.OperationAnswer(call)
		if !ok {
			return nil, fmt.Errorf("run_operation call %s has no answer", call.ShapeID)
		}
		var answer OperationAnswer
		if err := decodeNumbered(raw, &answer); err != nil {
			return nil, err
		}
		reply := RecordedReply{Call: answer.Call, Result: answer.Result, Operation: answer.Operation}
		switch answer.Call {
		case string(directread.CallServed):
			data, derr := decodeJSON(answer.Data)
			if derr != nil {
				return nil, fmt.Errorf("run_operation %s: data is not JSON", shape.Operation)
			}
			clean, serr := scrubReply(scrubber, schema, shape, call.Variables, data)
			if serr != nil {
				return nil, serr
			}
			encoded, merr := json.Marshal(clean)
			if merr != nil {
				return nil, merr
			}
			reply.Status, reply.Data = 200, encoded
		case string(directread.CallOperationUnavailable):
			reply.Status, reply.Reason = 404, "not_registered_or_routing_off"
		default:
			continue
		}
		recording.Replies[OperationKey(shape, scrubVariables(scrubber, call.Variables))] = reply
	}
	generated, err := run.Oracle.generatedCases(ctx)
	if err != nil {
		return nil, err
	}
	manifest := Manifest{
		CapturedAt: time.Now().UTC().Format(time.RFC3339), Window: cfg.Window, FixtureOrg: FixtureOrgID,
		OpsBuild: cfg.OpsBuild, AcrBuild: cfg.AcrBuild,
		SchemaDigest: run.Oracle.Policy.Catalogue().SchemaDigest(), FactQueryVersion: devhealthfacts.QueryVersion,
		VenueFactQueryVersions: sortedKeys(run.Oracle.FactVersions),
		Rows:                   map[string]int{}, Residual: run.Oracle.Residual, Expect: map[string]RootExpectation{},
		ListenerDark: append([]string{}, cfg.ListenerDark...), OperationDark: append([]string{}, cfg.OperationDark...),
	}
	for _, d := range run.Oracle.deniedList {
		id := d.id
		if mapped, ok := scrubber.SubjectID("team:" + id); ok {
			id = strings.TrimPrefix(mapped, "team:")
		}
		manifest.DeniedTeams = appendDenied(manifest.DeniedTeams, d.kind, id)
		// The notes of the report name the team as the extract does.
		for _, rr := range run.Report.Roots {
			for i, note := range rr.NotJoined {
				rr.NotJoined[i] = strings.Replace(note, d.kind+": team "+d.id+" is denied", d.kind+": team "+id+" is denied", 1)
			}
		}
	}
	if run.Oracle.SubjectTeam != "" {
		id := run.Oracle.SubjectTeam
		if mapped, ok := scrubber.SubjectID("team:" + id); ok {
			id = strings.TrimPrefix(mapped, "team:")
		}
		manifest.SubjectTeam, manifest.SubjectTeamsSkipped = id, run.Oracle.SubjectTeamsSkipped
	}
	for _, c := range generated {
		manifest.ShapeCases = append(manifest.ShapeCases, ShapeCase{ShapeID: c.ShapeID, Variables: scrubVariables(scrubber, c.Variables)})
	}
	for table, rows := range scrubbed.Tables {
		manifest.Rows[table] = len(rows)
	}
	for _, rr := range run.Report.Roots {
		manifest.Expect[rr.Root] = Expectation(rr)
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	repliesBytes, err := json.MarshalIndent(recording, "", " ")
	if err != nil {
		return nil, err
	}
	for name, content := range map[string][]byte{ManifestFile: manifestBytes, RepliesFile: repliesBytes, "extract rows": []byte(extractDigest(scrubbed))} {
		if scrubber.Leaks(content) {
			return nil, fmt.Errorf("%s still holds the venue organization id; nothing was written", name)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := scrubbed.Write(filepath.Join(dir, ExtractDir)); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), append(manifestBytes, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, RepliesFile), append(repliesBytes, '\n'), 0o644); err != nil {
		return nil, err
	}
	return run, nil
}

// Repin replaces the pinned outcome of a capture with the outcome of a
// recorded-mode run of this build: the recorded replies against this build's
// fact providers on the extract. It is for a build whose fact providers are
// newer than the venue's acr; the replies and the extract are not touched.
func Repin(dir string, report *Report, residual map[string]float64) error {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := decodeNumbered(raw, &manifest); err != nil {
		return err
	}
	manifest.FactQueryVersion, manifest.Residual, manifest.Expect = devhealthfacts.QueryVersion, residual, map[string]RootExpectation{}
	for _, rr := range report.Roots {
		manifest.Expect[rr.Root] = Expectation(rr)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestFile), append(encoded, '\n'), 0o644)
}

// LoadCapture reads a capture directory.
func LoadCapture(dir string) (Manifest, Recording, *Extract, error) {
	var manifest Manifest
	var recording Recording
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return manifest, recording, nil, err
	}
	if err := decodeNumbered(raw, &manifest); err != nil {
		return manifest, recording, nil, fmt.Errorf("manifest: %w", err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, RepliesFile))
	if err != nil {
		return manifest, recording, nil, err
	}
	if err := decodeNumbered(raw, &recording); err != nil {
		return manifest, recording, nil, fmt.Errorf("replies: %w", err)
	}
	extract, err := ReadExtract(filepath.Join(dir, ExtractDir))
	if err != nil {
		return manifest, recording, nil, err
	}
	for table, want := range manifest.Rows {
		if got := len(extract.Tables[table]); got != want {
			return manifest, recording, nil, fmt.Errorf("extract table %s has %d rows, the manifest says %d", table, got, want)
		}
	}
	return manifest, recording, extract, nil
}

// scrubVariables replaces the subject ids of a variable set.
func scrubVariables(s *Scrubber, variables map[string]any) map[string]any {
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, item := range t {
				out[k] = walk(item)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, item := range t {
				out[i] = walk(item)
			}
			return out
		case string:
			if mapped, ok := s.SubjectID(t); ok {
				return mapped
			}
			return t
		default:
			return v
		}
	}
	out, _ := walk(variables).(map[string]any)
	return out
}

// Reply scrub rules by generalized output path. A String or ID leaf with no
// rule is replaced by a pseudonym; enums, numbers, booleans, dates and
// __typename are kept.
const (
	replyKeep = iota + 1
	replyOrg
	replyUUID
	replyTeam
	replyWorkScope
	replyScopeID
	replyCatalogValue
	// replyTaxonomy is a term of the fixed investment taxonomy (theme,
	// subcategory, work type): kept when it has the shape of a taxonomy
	// identifier, replaced by a token when it has any other shape.
	replyTaxonomy
)

var replyRules = map[string]int{
	"analytics.breakdowns[*].dimension":              replyKeep,
	"analytics.breakdowns[*].measure":                replyKeep,
	"analytics.breakdowns[*].items[*].key":           replyTaxonomy,
	"analytics.sankey.unit":                          replyKeep,
	"capacityForecast.forecastId":                    replyUUID,
	"capacityForecast.teamId":                        replyTeam,
	"capacityForecast.workScopeId":                   replyWorkScope,
	"capacityForecasts.edges[*].node.forecastId":     replyUUID,
	"capacityForecasts.edges[*].node.teamId":         replyTeam,
	"capacityForecasts.edges[*].node.workScopeId":    replyWorkScope,
	"capacityForecasts.edges[*].cursor":              replyUUID,
	"capacityForecasts.pageInfo.startCursor":         replyUUID,
	"capacityForecasts.pageInfo.endCursor":           replyUUID,
	"catalog.values[*].value":                        replyCatalogValue,
	"cognitiveLoad.orgId":                            replyOrg,
	"cognitiveLoad.teamId":                           replyTeam,
	"compoundingRisk.orgId":                          replyOrg,
	"compoundingRisk.breakout":                       replyKeep,
	"compoundingRisk.rows[*].scope":                  replyKeep,
	"compoundingRisk.rows[*].scopeId":                replyScopeID,
	"compoundingRisk.rows[*].severity":               replyKeep,
	"compoundingRisk.trend[*].severity":              replyKeep,
	"securityOverview.severityBreakdown[*].severity": replyKeep,
	"throughputForecast.forecastId":                  replyUUID,
	"throughputForecast.teamId":                      replyTeam,
	"throughputForecast.workScopeId":                 replyWorkScope,
	"throughputForecast.primaryRisk.kind":            replyKeep,
	"throughputForecast.wipCongestion.kind":          replyKeep,
	"throughputForecast.reviewBottleneck.kind":       replyKeep,
	"throughputForecast.incidentLoad.kind":           replyKeep,
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// scrubReply scrubs the data of one served answer by the SDL type of every
// leaf. A leaf on a path the shape did not select stops the capture.
func scrubReply(s *Scrubber, schema *ast.Schema, shape Shape, variables map[string]any, data any) (any, error) {
	dimension, _ := variables["dimension"].(string)
	if shape.Operation == "acrRepositoryScopes" {
		dimension = "REPO"
	}
	var walk func(path string, parent map[string]any, v any) (any, error)
	walk = func(path string, parent map[string]any, v any) (any, error) {
		sdl, isLeaf := shape.OutputType(path)
		switch t := v.(type) {
		case map[string]any:
			if isLeaf {
				// A JSON scalar: keys are kept, string values are replaced.
				return scrubJSONScalar(s, t), nil
			}
			out := make(map[string]any, len(t))
			for k, item := range t {
				cleaned, err := walk(path+"."+k, t, item)
				if err != nil {
					return nil, err
				}
				out[k] = cleaned
			}
			return out, nil
		case []any:
			out := make([]any, len(t))
			for i, item := range t {
				next := path + "[*]"
				if isLeaf {
					next = path
				}
				cleaned, err := walk(next, parent, item)
				if err != nil {
					return nil, err
				}
				out[i] = cleaned
			}
			return out, nil
		case string:
			if !isLeaf {
				return nil, fmt.Errorf("%s: string at %s, which is not a selected output path", shape.ID(), path)
			}
			if strings.HasSuffix(path, ".__typename") {
				return t, nil
			}
			named := sdlNamedType(sdl)
			if def := schema.Types[named]; def != nil && def.Kind == ast.Enum {
				return t, nil
			}
			if named != "String" && named != "ID" {
				return t, nil
			}
			switch replyRules[path] {
			case replyKeep:
				return t, nil
			case replyTaxonomy:
				return taxonomyTerm(s, t), nil
			case replyOrg:
				return s.Org(t)
			case replyUUID:
				return s.UUID(t), nil
			case replyTeam:
				return s.Token("team", t), nil
			case replyWorkScope:
				return s.Token("scope", t), nil
			case replyScopeID:
				if scope, _ := parent["scope"].(string); strings.EqualFold(scope, "repo") {
					return s.UUID(t), nil
				}
				return s.Token("team", t), nil
			case replyCatalogValue:
				switch dimension {
				case "REPO":
					return s.Slug(t), nil
				case "TEAM":
					return s.Token("team", t), nil
				case "THEME", "SUBCATEGORY", "WORK_TYPE":
					return taxonomyTerm(s, t), nil
				default:
					return s.Token("h", t), nil
				}
			}
			if uuidShape.MatchString(t) {
				return s.UUID(t), nil
			}
			// A date or a time printed in a String field names nothing.
			if _, err := parseInstant(t); err == nil {
				return t, nil
			}
			return s.Token("h", t), nil
		default:
			return v, nil
		}
	}
	root, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: data is not an object", shape.ID())
	}
	out := map[string]any{}
	for k, v := range root {
		cleaned, err := walk(k, root, v)
		if err != nil {
			return nil, err
		}
		out[k] = cleaned
	}
	return out, nil
}

// taxonomyShape is the shape of a term of the investment taxonomy: lower
// snake case words, optionally a theme and a subcategory joined by a dot.
// A value of another shape (spaces, capitals, digits first, a URL, an address)
// is not a term of the taxonomy and is replaced.
var taxonomyShape = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*(\.[a-z][a-z0-9]*(_[a-z0-9]+)*)?$`)

func taxonomyTerm(s *Scrubber, t string) string {
	if len(t) <= 64 && taxonomyShape.MatchString(t) {
		return t
	}
	return s.Token("h", t)
}

func scrubJSONScalar(s *Scrubber, value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for k, v := range value {
		switch t := v.(type) {
		case string:
			out[k] = s.Token("h", t)
		case map[string]any:
			out[k] = scrubJSONScalar(s, t)
		default:
			if reflect.TypeOf(v) != nil && reflect.TypeOf(v).Kind() == reflect.Slice {
				out[k] = []any{}
				continue
			}
			out[k] = v
		}
	}
	return out
}

func appendDenied(m map[string][]string, kind, id string) map[string][]string {
	if m == nil {
		m = map[string][]string{}
	}
	m[kind] = append(m[kind], id)
	sort.Strings(m[kind])
	return m
}
