package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// Modes of a root pair.
const (
	ModeValue = "value"
	ModeShape = "shape"
)

// rootPair says how one root field is compared.
type rootPair struct {
	Mode string
	// Reason is why a shape-only root has no value compare.
	Reason string
	// compare runs the value pairs of the root.
	compare func(ctx context.Context, o *Oracle, report *RootReport) error
}

// rootPairs covers every allowed root field. Run fails when the policy
// serves a root that is not in this table.
var rootPairs = map[string]rootPair{
	"analytics":            {Mode: ModeValue, compare: compareInvestment},
	"capacityForecast":     {Mode: ModeShape, Reason: "no acr fact, by design (design r5 A1.1): the on-demand forecast draws a fresh seed per request and acr has no on-demand forecast; class forecast_by_design, shape, echo and limits only"},
	"capacityForecasts":    {Mode: ModeValue, compare: compareWorkload},
	"catalog":              {Mode: ModeValue, compare: compareCatalog},
	"cognitiveLoad":        {Mode: ModeShape, Reason: "no acr fact: no fact provider reads team_cognitive_load_daily or user_metrics_daily"},
	"complexityTimeseries": {Mode: ModeShape, Reason: "no acr fact: no fact provider reads repo_complexity_daily or file_complexity_snapshots"},
	"compoundingRisk":      {Mode: ModeValue, compare: compareHealth},
	"hotspots":             {Mode: ModeShape, Reason: "no acr fact: no file subject kind and no provider reads file_hotspot_daily"},
	"securityOverview":     {Mode: ModeShape, Reason: "no acr fact: no fact kind reads security_alerts"},
	"throughputForecast":   {Mode: ModeValue, compare: compareReadiness},
	"workGraphArtifacts":   {Mode: ModeShape, Reason: "no acr fact: acr serves graph edges through read_relationships, from other tables than work_graph_edges (lead ruling: shape only)"},
	"workGraphEdges":       {Mode: ModeShape, Reason: "no acr fact: acr serves graph edges through read_relationships, from other tables than work_graph_edges (lead ruling: shape only)"},
	"workGraphFlow":        {Mode: ModeShape, Reason: "no acr fact: acr serves graph edges through read_relationships, from other tables than work_graph_edges (lead ruling: shape only)"},
}

// ShapeCase is one ops call of the shape pass.
type ShapeCase struct {
	ShapeID   string         `json:"shape"`
	Variables map[string]any `json:"variables"`
}

// Oracle is one configured run.
type Oracle struct {
	// denied is, per fact kind and bare id, the team subjects the token has
	// no grant for; deniedNotes states each once for the report.
	denied      map[string]bool
	deniedNotes []string
	deniedList  []deniedTeam
	// DeniedTeams are, per fact kind, the teams the capture was denied on the
	// venue (ids as the extract holds them): a recorded run leaves them out
	// the way the capture did, since the seeded store has no authorization.
	DeniedTeams map[string][]string
	Policy      *directread.GraphQLPolicy
	Planes      Planes
	// Store is the reference reading of the rows both planes read.
	Store  *Store
	Window Window
	// ShapeCases, when set, replaces the cases the bindings generate from
	// the store (the recorded mode replays the cases of the capture).
	ShapeCases []ShapeCase
	// FactVersions is, after Run, the fact query versions the acr plane
	// answered with.
	FactVersions map[string]bool
	// OnlyRoots, when set, limits the run to these root fields (the
	// acceptance gate runs one root).
	OnlyRoots []string
	// RepositoryEffort is, after Run, the acr repository mix per repository
	// id and theme.
	RepositoryEffort map[string]map[string]float64
	// Residual is, after Run, the investment residual per theme (ops
	// organization value minus the sum of the acr repository mixes).
	Residual map[string]float64
	// ListenerDark and OperationDark name the root fields the venue is known
	// not to serve through graphql_query and through run_operation. A root
	// that is unavailable on a path and is not named for it, or that is named
	// and served, makes the run invalid: a path that was not measured is
	// never a silent pass.
	ListenerDark  []string
	OperationDark []string
	// Allowances, when set, replaces temporaryAllowances (tests).
	Allowances map[string]temporaryAllowance

	shapes    []Shape
	answers   map[string]json.RawMessage
	opAnswers map[string]json.RawMessage
	// Calls lists every graphql_query call of the run, in order;
	// OperationCalls every run_operation call.
	Calls          []ShapeCase
	OperationCalls []ShapeCase
}

func (o *Oracle) graphQL(ctx context.Context, shape Shape, variables map[string]any) (GraphQLAnswer, error) {
	key := CaseKey(shape, variables)
	raw, seen := o.answers[key]
	if !seen {
		var err error
		raw, err = o.Planes.GraphQL(ctx, shape, variables)
		if err != nil {
			return GraphQLAnswer{}, fmt.Errorf("%s: %w", shape.ID(), err)
		}
		o.answers[key] = raw
		o.Calls = append(o.Calls, ShapeCase{ShapeID: shape.ID(), Variables: variables})
	}
	var answer GraphQLAnswer
	if err := decodeNumbered(raw, &answer); err != nil {
		return GraphQLAnswer{}, fmt.Errorf("%s: answer is not JSON: %w", shape.ID(), err)
	}
	return answer, nil
}

// Answer returns the raw answer of an ops call the run made.
func (o *Oracle) Answer(c ShapeCase) (json.RawMessage, bool) {
	shape, ok := ShapeByID(o.shapes, c.ShapeID)
	if !ok {
		return nil, false
	}
	raw, ok := o.answers[CaseKey(shape, c.Variables)]
	return raw, ok
}

// How a fact read treats its window and a cut source.
const (
	// readWindowed: over the oracle window, tables included. A source the
	// provider marks truncated is refused.
	readWindowed = iota
	// readCurrent: no window, tables omitted. A truncated source is refused.
	readCurrent
	// readCurrentHeldToStore: no window, tables omitted, a truncated source
	// accepted. With no window the provider's daily series runs over all
	// time and its row cap marks the whole source truncated, although the
	// scalar facts come from another query. The caller must hold every fact
	// it compares against the store rows, so a scalar fact that was cut
	// shows as a difference with the store.
	readCurrentHeldToStore
)

// facts reads one fact kind over the oracle window.
func (o *Oracle) facts(ctx context.Context, kind, subjectKind string, ids []string) ([]ServedFact, error) {
	return o.readFacts(ctx, kind, subjectKind, ids, readWindowed)
}

// factRowCap is the fact providers' row cap per query.
const factRowCap = 200

// deniedFactsError is a facts read the caller is not allowed to make: the
// subjects asked for have no grant. It is a state of the venue, not a defect
// of the read, so a caller that can go on without those subjects may.
type deniedFactsError struct {
	err      error
	outcomes string
}

func (e *deniedFactsError) Error() string { return e.err.Error() }
func (e *deniedFactsError) Unwrap() error { return e.err }

func (o *Oracle) readFacts(ctx context.Context, kind, subjectKind string, ids []string, mode int) ([]ServedFact, error) {
	var out []ServedFact
	// A provider's row cap is shared by the subjects of one read: with many
	// teams in one request the daily series of some are cut or absent. A
	// team is read alone; a repository fact carries small tables only.
	chunk := 10
	if subjectKind == "team" {
		chunk = 1
	}
	deniedCount := 0
	var deniedRows []string
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		request := FactsRequest{Kinds: []string{kind}, MaxBytes: directread.MaxMaxBytes, Tables: directread.TablesOmit}
		if mode == readWindowed {
			request.Window = &FactsWindow{Mode: directread.WindowRange, Start: o.Window.Start, End: o.Window.End}
			request.Tables = directread.TablesInclude
		}
		if chunk == 1 && subjectKind == "team" && o.recordedDenied(kind, ids[start]) {
			o.noteDenied(kind, ids[start])
			deniedCount++
			deniedRows = append(deniedRows, "denied on the venue at capture")
			continue
		}
		for _, id := range ids[start:end] {
			request.Subjects = append(request.Subjects, FactsSubject{Kind: subjectKind, CanonicalID: subjectKind + ":" + id})
		}
		raw, err := o.Planes.Facts(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("read_facts %s: %w", kind, err)
		}
		var answer FactsAnswer
		if err := decodeNumbered(raw, &answer); err != nil {
			return nil, fmt.Errorf("read_facts %s: answer is not JSON: %w", kind, err)
		}
		// A partial read is a read with a subject that has no fact. A read
		// that could not measure, or that was cut, is not compared.
		if answer.Status != directread.StatusComplete && answer.Status != directread.StatusPartial {
			var rows []string
			for _, row := range answer.Coverage {
				rows = append(rows, row.Subject.CanonicalID+"="+row.Outcome)
			}
			err := fmt.Errorf("read_facts %s: status %s (asked %d subjects; coverage rows: %s)", kind, answer.Status, end-start, strings.Join(rows, ", "))
			if answer.Status == directread.StatusDenied {
				// A team read alone that the token has no grant for is not
				// joined: stated, not compared. A call whose every subject is
				// denied is a lost authorization and fails.
				if chunk == 1 {
					o.noteDenied(kind, ids[start])
					deniedCount++
					deniedRows = append(deniedRows, strings.Join(rows, ", "))
					continue
				}
				return nil, &deniedFactsError{err: err, outcomes: strings.Join(rows, ", ")}
			}
			return nil, err
		}
		for _, row := range answer.Coverage {
			cut := row.Outcome == directread.OutcomeTruncated && mode != readCurrentHeldToStore
			if row.Outcome == directread.OutcomeUnavailable || row.Outcome == directread.OutcomeWithheldBudget || cut {
				return nil, fmt.Errorf("read_facts %s: a subject was not measured whole (%s)", kind, row.Outcome)
			}
		}
		for _, version := range answer.Versions.Kinds {
			if o.FactVersions == nil {
				o.FactVersions = map[string]bool{}
			}
			o.FactVersions[version] = true
		}
		if len(answer.Facts) >= factRowCap {
			return nil, fmt.Errorf("read_facts %s: %d facts in one read, at the provider row cap", kind, len(answer.Facts))
		}
		if answer.Truncation != nil {
			return nil, fmt.Errorf("read_facts %s: truncated by %s", kind, answer.Truncation.TruncatedBy)
		}
		if len(answer.Coverage) != end-start {
			return nil, fmt.Errorf("read_facts %s: %d coverage rows for %d subjects", kind, len(answer.Coverage), end-start)
		}
		// The answer is about the subjects that were asked: each is covered
		// once, and no fact or coverage row names another subject.
		asked := map[string]int{}
		for _, subject := range request.Subjects {
			asked[strings.ToLower(subject.CanonicalID)] = 0
		}
		for _, row := range answer.Coverage {
			id := strings.ToLower(row.Subject.CanonicalID)
			if _, ok := asked[id]; !ok {
				return nil, fmt.Errorf("read_facts %s: a coverage row names %s, which was not asked", kind, row.Subject.CanonicalID)
			}
			if row.Kind != kind || row.Subject.Kind != subjectKind {
				return nil, fmt.Errorf("read_facts %s: the coverage row of %s is of kind %s, subject kind %s, want %s and %s", kind, row.Subject.CanonicalID, row.Kind, row.Subject.Kind, kind, subjectKind)
			}
			asked[id]++
		}
		// As many rows as subjects, each row a subject asked: a subject with
		// no row means another one has two.
		for id, n := range asked {
			if n > 1 {
				return nil, fmt.Errorf("read_facts %s: subject %s has %d coverage rows, want 1", kind, id, n)
			}
		}
		for _, fact := range answer.Facts {
			if _, ok := asked[strings.ToLower(fact.Subject.CanonicalID)]; !ok {
				return nil, fmt.Errorf("read_facts %s: a fact names %s, which was not asked", kind, fact.Subject.CanonicalID)
			}
			if fact.Subject.Kind != subjectKind {
				return nil, fmt.Errorf("read_facts %s: a fact of %s has subject kind %s, want %s", kind, fact.Subject.CanonicalID, fact.Subject.Kind, subjectKind)
			}
			if fact.Kind != kind {
				return nil, fmt.Errorf("read_facts %s: the answer holds a %s fact", kind, fact.Kind)
			}
			if err := checkFactPlan(fact); err != nil {
				return nil, err
			}
		}
		out = append(out, answer.Facts...)
	}
	if chunk == 1 && len(ids) > 0 && deniedCount == len(ids) {
		return nil, &deniedFactsError{err: fmt.Errorf("read_facts %s: every one of %d teams was denied (coverage rows: %s)", kind, len(ids), strings.Join(deniedRows, "; ")), outcomes: strings.Join(deniedRows, "; ")}
	}
	return out, nil
}

type deniedTeam struct{ kind, id string }

// recordedDenied reports whether the capture was denied this team.
func (o *Oracle) recordedDenied(kind, id string) bool {
	for _, d := range o.DeniedTeams[kind] {
		if strings.EqualFold(d, id) {
			return true
		}
	}
	return false
}

// noteDenied records a team the token cannot read, once per kind.
func (o *Oracle) noteDenied(kind, id string) {
	if o.denied == nil {
		o.denied = map[string]bool{}
	}
	key := kind + "|" + strings.ToLower(id)
	if o.denied[key] {
		return
	}
	o.denied[key] = true
	o.deniedList = append(o.deniedList, deniedTeam{kind: kind, id: id})
	o.deniedNotes = append(o.deniedNotes, kind+": team "+id+" is denied to the venue token")
}

// withoutDenied drops the teams a read of this kind was denied.
func (o *Oracle) withoutDenied(kind string, ids []string) []string {
	var out []string
	for _, id := range ids {
		if !o.denied[kind+"|"+strings.ToLower(id)] {
			out = append(out, id)
		}
	}
	return out
}

// Run executes the shape pass and the value pairs of every allowed root.
func (o *Oracle) Run(ctx context.Context) (*Report, error) {
	if o.Policy == nil || o.Planes == nil || o.Store == nil {
		return nil, fmt.Errorf("oracle needs a policy, planes and a store")
	}
	if err := o.Window.Validate(); err != nil {
		return nil, err
	}
	if err := checkValuePaths(o.Policy); err != nil {
		return nil, err
	}
	// The providers are built for their field declaration only; nothing is
	// read through them here.
	if err := checkFactPlanDeclared(devhealthfacts.NewProviders(nil)); err != nil {
		return nil, err
	}
	shapes, err := Shapes(o.Policy)
	if err != nil {
		return nil, err
	}
	o.shapes, o.answers, o.opAnswers, o.Calls, o.OperationCalls = shapes, map[string]json.RawMessage{}, map[string]json.RawMessage{}, nil, nil
	cases := o.ShapeCases
	if cases == nil {
		if cases, err = o.generatedCases(); err != nil {
			return nil, err
		}
	}
	byShape := map[string][]ShapeCase{}
	for _, c := range cases {
		byShape[c.ShapeID] = append(byShape[c.ShapeID], c)
	}
	only := map[string]bool{}
	for _, name := range o.OnlyRoots {
		if _, known := o.Policy.Root(name); !known {
			return nil, fmt.Errorf("root %s is not an allowed root field", name)
		}
		only[name] = true
	}
	report := &Report{}
	for _, root := range o.Policy.Roots() {
		pair, ok := rootPairs[root.Field]
		if !ok {
			return nil, fmt.Errorf("root %s is served by the policy and has no pair", root.Field)
		}
		if len(only) > 0 && !only[root.Field] {
			continue
		}
		rr := &RootReport{Root: root.Field, Mode: pair.Mode, Reason: pair.Reason, Listener: "served", ByClass: map[Class]int{}}
		report.Roots = append(report.Roots, rr)
		candidates := map[string]bool{}
		for _, name := range root.Operations() {
			candidates[name] = true
		}
		for _, shape := range shapes {
			if shape.Root != root.Field {
				continue
			}
			if len(byShape[shape.ID()]) == 0 {
				return nil, fmt.Errorf("shape %s has no case: a generated shape that is not run is not a pass", shape.ID())
			}
			for _, c := range byShape[shape.ID()] {
				if err := o.runShape(ctx, rr, shape, c, candidates); err != nil {
					return nil, err
				}
			}
		}
		// run_operation, for the full selection of every operation.
		for _, shape := range shapes {
			if shape.Root != root.Field || shape.Name != "all" {
				continue
			}
			for _, c := range byShape[shape.ID()] {
				if err := o.runOperation(ctx, rr, shape, c); err != nil {
					return nil, err
				}
			}
		}
		if pair.Mode == ModeValue && rr.listenerServed > 0 && rr.listenerDark == 0 {
			rr.Excluded = excludedPaths(root.Field)
			if err := pair.compare(ctx, o, rr); err != nil {
				return nil, fmt.Errorf("root %s: %w", root.Field, err)
			}
			rr.NotJoined = append(rr.NotJoined, o.deniedNotes...)
			o.deniedNotes = nil
		}
		if err := o.temporaryAllowance(ctx, rr, root, byShape); err != nil {
			return nil, fmt.Errorf("root %s: %w", root.Field, err)
		}
		o.validate(rr, root, pair)
		sort.Strings(rr.NotJoined)
	}
	return report, nil
}

func named(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// validate decides whether the run of a root is a measurement. Every reason
// it is not goes to RootReport.Invalid, and Report.Err fails on it: a path
// that did not serve the root, an answer with no leaf, two served paths with
// nothing compared between them, a value root that compared nothing.
func (o *Oracle) validate(rr *RootReport, root *directread.GraphQLRootPolicy, pair rootPair) {
	path := func(name string, served, dark int, declaredDark bool, leaves int) {
		switch {
		case served > 0 && dark > 0:
			rr.invalid("%s served %d cases of the root and was unavailable for %d", name, served, dark)
		case dark > 0 && !declaredDark:
			rr.invalid("%s is unavailable for the root and the venue does not declare the root dark there: the path was not measured", name)
		case served > 0 && declaredDark:
			rr.invalid("the venue declares the root dark on %s and it is served: remove the declaration", name)
		case served == 0 && dark == 0:
			rr.invalid("%s served no case of the root", name)
		case served > 0 && leaves == 0:
			rr.invalid("%s served the root and gave no leaf: nothing was measured", name)
		}
	}
	path("graphql_query", rr.listenerServed, rr.listenerDark, named(o.ListenerDark, rr.Root), rr.Leaves)
	path("run_operation", rr.operationServed, rr.operationDark, named(o.OperationDark, rr.Root), rr.OperationLeaves)
	if rr.listenerServed == 0 && rr.operationServed == 0 {
		rr.invalid("no path served the root")
	}
	if _, volatile := volatileRoots[rr.Root]; !volatile && rr.listenerServed > 0 && rr.operationServed > 0 && rr.CrossPaths == 0 {
		rr.invalid("both paths served the root and no leaf was compared between them")
	}
	if pair.Mode == ModeValue && rr.listenerServed > 0 && rr.listenerDark == 0 {
		if rr.Compared == 0 {
			rr.invalid("a value root compared nothing with the acr facts")
		}
		checkTouched(rr)
	}
	if rr.listenerServed > 0 || rr.operationServed > 0 {
		for _, output := range rootOutputs(o.Policy, root) {
			if !rr.measured[output] {
				rr.Unmeasured = append(rr.Unmeasured, output)
			}
		}
	}
}

// runShape runs one shape case and checks shape, echo and limits.
func (o *Oracle) runShape(ctx context.Context, rr *RootReport, shape Shape, c ShapeCase, candidates map[string]bool) error {
	answer, err := o.graphQL(ctx, shape, c.Variables)
	if err != nil {
		return err
	}
	rr.ShapesRun++
	key := CaseKey(shape, c.Variables)
	if answer.Call != string(directread.CallServed) {
		if answer.Call == string(directread.CallOperationUnavailable) {
			// The root is not enabled on the listener: stated here; validate
			// fails the run unless the venue declares the root dark there.
			rr.Listener = answer.Call
			rr.listenerDark++
			return nil
		}
		detail := "call " + answer.Call
		if answer.Refusal != nil {
			detail += ", refusal " + answer.Refusal.Code
		}
		for _, e := range answer.Errors {
			detail += ", error class " + e.Class
		}
		rr.find(Finding{Pair: "shape", Key: key, Detail: detail})
		return nil
	}
	rr.listenerServed++
	if len(answer.RootFields) != 1 || answer.RootFields[0].Field != shape.Root || answer.RootFields[0].Key != shape.Root || !candidates[answer.RootFields[0].Operation] {
		rr.find(Finding{Pair: "shape", Key: key, Detail: fmt.Sprintf("echo: root_fields %+v is not one field %s of a candidate operation", answer.RootFields, shape.Root)})
	}
	if answer.Page.MaxBytes <= 0 || answer.Page.ReturnedBytes <= 0 || answer.Page.ReturnedBytes > answer.Page.MaxBytes {
		rr.find(Finding{Pair: "shape", Key: key, Detail: fmt.Sprintf("limits: returned_bytes %d, max_bytes %d", answer.Page.ReturnedBytes, answer.Page.MaxBytes)})
	}
	data, derr := decodeJSON(answer.Data)
	if derr != nil {
		rr.find(Finding{Pair: "shape", Key: key, Detail: "data is not JSON"})
		return nil
	}
	leaves, problems := shape.shapeLeaves(data)
	for _, problem := range problems {
		rr.find(Finding{Pair: "shape", Key: key, Detail: problem})
	}
	rr.Leaves += rr.measure(leaves)
	return nil
}

func dateTime(day string) string { return day + "T00:00:00Z" }

// generatedCases builds the shape pass from the store and the window: one
// variable set per shape (five for catalogValues, one per dimension). Each
// variable path is checked against the policy before it is used.
func (o *Oracle) generatedCases() ([]ShapeCase, error) {
	start, last := o.Window.startDate(), o.Window.lastDay()
	teams := o.Store.TeamIDs()
	team := ""
	if len(teams) > 0 {
		team = "team:" + teams[0]
	}
	var out []ShapeCase
	for _, shape := range o.shapes {
		var sets []map[string]any
		switch shape.Operation {
		case "investmentBreakdown", "investmentFull":
			sets = append(sets, investmentVariables(o.Window, "THEME"), investmentVariables(o.Window, "SUBCATEGORY"), investmentVariables(o.Window, "WORK_TYPE"))
		case "capacityForecast", "capacityCompletionDistribution":
			sets = append(sets, map[string]any{"input": map[string]any{"teamId": team, "historyDays": 90, "simulations": 1000}})
		case "capacityForecasts":
			sets = append(sets, map[string]any{"filters": map[string]any{"fromDate": start, "toDate": last, "limit": 50}})
		case "acrRepositoryScopes":
			sets = append(sets, map[string]any{})
		case "catalogValues":
			dimensions, derr := o.variableValues(shape.Operation, "dimension")
			if derr != nil {
				return nil, derr
			}
			for _, dimension := range dimensions {
				sets = append(sets, map[string]any{"dimension": dimension})
			}
		case "cognitiveLoad":
			sets = append(sets, map[string]any{"input": map[string]any{"sinceDate": start, "untilDate": last, "teamId": team}})
		case "complexityTimeseries":
			sets = append(sets, map[string]any{"input": map[string]any{"sinceUtc": dateTime(start), "untilUtc": dateTime(last), "granularity": "DAY", "scope": "REPO", "limit": 1}})
		case "compoundingRisk":
			sets = append(sets, map[string]any{"filter": map[string]any{"breakout": "REPO", "trendDays": 30}}, map[string]any{"filter": map[string]any{"breakout": "TEAM", "trendDays": 30}})
		case "hotspots":
			sets = append(sets, map[string]any{"input": map[string]any{"sinceUtc": dateTime(start), "untilUtc": dateTime(last), "limit": 50}})
		case "securityOverview":
			sets = append(sets, map[string]any{"filters": map[string]any{"since": start, "until": last}})
		case "throughputForecast":
			sets = append(sets, map[string]any{"input": map[string]any{"teamIds": []any{team}, "historyWeeks": 12}})
		case "workGraphArtifacts", "workGraphEdges":
			sets = append(sets, map[string]any{"filters": map[string]any{"limit": 50}})
		case "workGraphFlow":
			sets = append(sets, map[string]any{})
		default:
			return nil, fmt.Errorf("operation %s has no shape binding", shape.Operation)
		}
		for _, variables := range sets {
			if err := checkVariablePaths(shape, "", variables); err != nil {
				return nil, err
			}
			out = append(out, ShapeCase{ShapeID: shape.ID(), Variables: variables})
		}
	}
	return out, nil
}

// sendableValues are the values of an enum variable a client may send: the
// allowed values, or the enum without the refused ones.
func sendableValues(v directread.VariableRule) []string {
	if len(v.AllowedValues) > 0 {
		return v.AllowedValues
	}
	refused := map[string]bool{}
	for _, r := range v.RefusedValues {
		refused[r.Value] = true
	}
	var values []string
	for _, e := range v.Enum {
		if !refused[e] {
			values = append(values, e)
		}
	}
	return values
}

// variableValues are the values a client may send for an enum variable of an
// operation, read from the policy: the allowed values, or the enum without
// the refused ones. A dimension the policy gains is run without an edit here.
func (o *Oracle) variableValues(operation, path string) ([]string, error) {
	op, refusal := o.Policy.Catalogue().Lookup(operation)
	if refusal != nil || op == nil {
		return nil, fmt.Errorf("operation %s is not served", operation)
	}
	for _, v := range op.Variables {
		if v.Path != path || !v.Allowed {
			continue
		}
		values := sendableValues(v)
		if len(values) == 0 {
			return nil, fmt.Errorf("operation %s: variable %s has no value a client may send", operation, path)
		}
		return values, nil
	}
	return nil, fmt.Errorf("operation %s has no allowed variable %s", operation, path)
}

func investmentVariables(w Window, dimension string) map[string]any {
	return map[string]any{"batch": map[string]any{"breakdowns": []any{map[string]any{
		"dimension": dimension, "measure": "CHURN_LOC",
		"dateRange": map[string]any{"startDate": w.startDate(), "endDate": w.endDate()},
		"topN":      100,
	}}}}
}

// checkVariablePaths refuses a binding that sets a path the policy does not
// let a client set, so a binding cannot drift from the production policy.
func checkVariablePaths(shape Shape, prefix string, value any) error {
	switch v := value.(type) {
	case map[string]any:
		for name, item := range v {
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if !shape.Allowed(path) {
				return fmt.Errorf("%s: variable path %s is not client-settable in the policy", shape.ID(), path)
			}
			if err := checkVariablePaths(shape, path, item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if _, isObject := item.(map[string]any); isObject {
				if err := checkVariablePaths(shape, prefix+"[*]", item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (o *Oracle) shape(id string) (Shape, error) {
	shape, ok := ShapeByID(o.shapes, id)
	if !ok {
		return Shape{}, fmt.Errorf("shape %s is not generated by the policy", id)
	}
	return shape, nil
}

// served runs a value-pair ops call that must be served.
func (o *Oracle) served(ctx context.Context, rr *RootReport, pair, shapeID string, variables map[string]any) (map[string]any, bool, error) {
	shape, err := o.shape(shapeID)
	if err != nil {
		return nil, false, err
	}
	if err := checkVariablePaths(shape, "", variables); err != nil {
		return nil, false, err
	}
	answer, err := o.graphQL(ctx, shape, variables)
	if err != nil {
		return nil, false, err
	}
	key := CaseKey(shape, variables)
	if answer.Call != string(directread.CallServed) {
		detail := "call " + answer.Call
		if answer.Refusal != nil {
			detail += ", refusal " + answer.Refusal.Code
		}
		rr.find(Finding{Pair: pair, Key: key, Detail: detail})
		return nil, false, nil
	}
	data, derr := decodeJSON(answer.Data)
	if derr != nil {
		rr.find(Finding{Pair: pair, Key: key, Detail: "data is not JSON"})
		return nil, false, nil
	}
	object, ok := data.(map[string]any)
	if !ok {
		rr.find(Finding{Pair: pair, Key: key, Detail: "data is not an object"})
		return nil, false, nil
	}
	root, ok := object[shape.Root].(map[string]any)
	if !ok {
		rr.find(Finding{Pair: pair, Key: key, Detail: "data has no " + shape.Root + " object"})
		return nil, false, nil
	}
	return root, true, nil
}

func items(value any) []map[string]any {
	list, _ := value.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

// leafPair compares one ops leaf with one acr leaf. path is the ops output
// path, or "acr:..." for a check with no ops side. rel is the declared
// relative tolerance of a float pair (0 = exact).
func (rr *RootReport) leafPair(pair, key, path string, ops, acr Leaf, opsErr, acrErr error, rel float64) {
	rr.Compared++
	rr.touch(path)
	switch {
	case opsErr != nil:
		rr.find(Finding{Pair: pair, Key: key, Path: path, Detail: "ops value: " + opsErr.Error()})
	case acrErr != nil:
		rr.find(Finding{Pair: pair, Key: key, Path: path, Detail: "acr value: " + acrErr.Error()})
	case ops == acr:
		rr.Matches++
	default:
		if a, ok := leafFloat(ops); ok && ops.T == LeafFloat && acr.T == LeafFloat {
			if b, ok := leafFloat(acr); ok && closeEnough(a, b, rel) {
				rr.Matches++
				return
			}
		}
		rr.find(Finding{Pair: pair, Key: key, Path: path, Detail: fmt.Sprintf("ops %s, acr %s", ops, acr)})
	}
}

// factInteger types an acr integer field. read_facts serves integers as
// decimal strings (the acr-data.v1 wire form).
func factInteger(value any) (Leaf, error) {
	switch v := value.(type) {
	case nil:
		return Leaf{T: LeafNull}, nil
	case string:
		return typedLeaf(LeafInt, json.Number(v))
	default:
		return typedLeaf(LeafInt, value)
	}
}

func tableColumn(table ServedTable, name string) int {
	for i, column := range table.Columns {
		if column == name {
			return i
		}
	}
	return -1
}

// cell returns the value of a named column of a table row, nil when the
// table has no such column.
func cell(table ServedTable, row []any, name string) any {
	i := tableColumn(table, name)
	if i < 0 || i >= len(row) {
		return nil
	}
	return row[i]
}

func bareID(subjectID string) string {
	_, rest, ok := strings.Cut(subjectID, ":")
	if !ok {
		return subjectID
	}
	return rest
}
