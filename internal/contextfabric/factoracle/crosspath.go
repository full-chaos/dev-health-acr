package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// volatilePaths are output paths whose value is made for each request, so
// two calls never agree on them.
var volatilePaths = map[string]string{
	"compoundingRisk.generatedAt":   "the time of the request",
	"throughputForecast.forecastId": "an id made for each request",
	"throughputForecast.computedAt": "the time of the request",
}

// aggregateFloatPaths are float aggregates over unordered rows: two identical
// requests were seen to differ in the last digits of the standard deviation.
// They are compared with the relative tolerance of a reordered sum.
var aggregateFloatPaths = map[string]float64{
	"analytics.evidenceQualityStats.stddev": relSum,
	"analytics.evidenceQualityStats.mean":   relSum,
}

// volatileRoots are roots whose whole answer is made for each request.
var volatileRoots = map[string]string{
	"capacityForecast": "forecast_by_design: a Monte Carlo draw with a fresh seed for each request",
}

func (o *Oracle) operation(ctx context.Context, shape Shape, variables map[string]any) (OperationAnswer, error) {
	key := OperationKey(shape, variables)
	raw, seen := o.opAnswers[key]
	if !seen {
		var err error
		raw, err = o.Planes.Operation(ctx, shape, variables)
		if err != nil {
			return OperationAnswer{}, fmt.Errorf("run_operation %s: %w", shape.Operation, err)
		}
		o.opAnswers[key] = raw
		o.OperationCalls = append(o.OperationCalls, ShapeCase{ShapeID: shape.ID(), Variables: variables})
	}
	var answer OperationAnswer
	if err := decodeNumbered(raw, &answer); err != nil {
		return OperationAnswer{}, fmt.Errorf("run_operation %s: answer is not JSON: %w", shape.Operation, err)
	}
	return answer, nil
}

// OperationAnswer returns the raw answer of a run_operation call of the run.
func (o *Oracle) OperationAnswer(c ShapeCase) (json.RawMessage, bool) {
	shape, ok := ShapeByID(o.shapes, c.ShapeID)
	if !ok {
		return nil, false
	}
	raw, ok := o.opAnswers[OperationKey(shape, c.Variables)]
	return raw, ok
}

// runOperation runs run_operation for the full selection of one operation
// and holds its answer against graphql_query's for the same variables: the
// registered document and the generated query select the same fields, so
// every leaf must be equal on both paths.
func (o *Oracle) runOperation(ctx context.Context, rr *RootReport, shape Shape, c ShapeCase) error {
	answer, err := o.operation(ctx, shape, c.Variables)
	if err != nil {
		return err
	}
	rr.OperationsRun++
	key := OperationKey(shape, c.Variables)
	if rr.RunOperation == "" {
		rr.RunOperation = string(directread.CallServed)
	}
	if answer.Call != string(directread.CallServed) {
		if answer.Call == string(directread.CallOperationUnavailable) {
			rr.RunOperation = answer.Call
			return nil
		}
		detail := "call " + answer.Call
		if answer.Refusal != nil {
			detail += ", refusal " + answer.Refusal.Code
		}
		for _, e := range answer.Errors {
			detail += ", error class " + e.Class
		}
		rr.find(Finding{Pair: "run_operation", Key: key, Detail: detail})
		return nil
	}
	if answer.Operation != shape.Operation {
		rr.find(Finding{Pair: "run_operation", Key: key, Detail: "echo: the answer names operation " + answer.Operation})
	}
	if answer.Page.MaxBytes <= 0 || answer.Page.ReturnedBytes <= 0 || answer.Page.ReturnedBytes > answer.Page.MaxBytes {
		rr.find(Finding{Pair: "run_operation", Key: key, Detail: fmt.Sprintf("limits: returned_bytes %d, max_bytes %d", answer.Page.ReturnedBytes, answer.Page.MaxBytes)})
	}
	data, derr := decodeJSON(answer.Data)
	if derr != nil {
		rr.find(Finding{Pair: "run_operation", Key: key, Detail: "data is not JSON"})
		return nil
	}
	leaves, problems := shape.shapeLeaves(data)
	for _, problem := range problems {
		rr.find(Finding{Pair: "run_operation", Key: key, Detail: problem})
	}

	// The other path, when it served the same case.
	raw, seen := o.answers[CaseKey(shape, c.Variables)]
	if !seen {
		return nil
	}
	var other GraphQLAnswer
	if decodeNumbered(raw, &other) != nil || other.Call != string(directread.CallServed) {
		return nil
	}
	if _, volatile := volatileRoots[shape.Root]; volatile {
		return nil
	}
	otherData, oerr := decodeJSON(other.Data)
	if oerr != nil {
		return nil
	}
	otherLeaves, _ := shape.shapeLeaves(otherData)
	paths := map[string]bool{}
	for path := range leaves {
		paths[path] = true
	}
	for path := range otherLeaves {
		paths[path] = true
	}
	for _, path := range sortedKeys(paths) {
		if _, volatile := volatilePaths[path]; volatile {
			continue
		}
		a, b := leafStrings(leaves[path]), leafStrings(otherLeaves[path])
		rr.CrossPaths += max(len(a), len(b))
		if strings.Join(a, "\x00") == strings.Join(b, "\x00") || floatsClose(leaves[path], otherLeaves[path], aggregateFloatPaths[path]) {
			rr.CrossMatches += len(a)
			continue
		}
		rr.find(Finding{Pair: "cross_path", Key: key, Path: path, Detail: fmt.Sprintf("run_operation and graphql_query answer differently: %d and %d leaves, not the same values", len(a), len(b))})
	}
	return nil
}

// floatsClose reports whether two leaf lists are the same floats within rel,
// in sorted order. rel 0 never matches: an exact compare was already made.
func floatsClose(a, b []Leaf, rel float64) bool {
	if rel <= 0 || len(a) != len(b) {
		return false
	}
	values := func(leaves []Leaf) ([]float64, bool) {
		out := make([]float64, 0, len(leaves))
		for _, leaf := range leaves {
			v, ok := leafFloat(leaf)
			if !ok || leaf.T != LeafFloat {
				return nil, false
			}
			out = append(out, v)
		}
		sort.Float64s(out)
		return out, true
	}
	x, okX := values(a)
	y, okY := values(b)
	if !okX || !okY {
		return false
	}
	for i := range x {
		if !closeEnough(x[i], y[i], rel) {
			return false
		}
	}
	return true
}

func leafStrings(leaves []Leaf) []string {
	out := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		out = append(out, leaf.String())
	}
	sort.Strings(out)
	return out
}

// temporaryOpsPaths are the ops output paths of the temporary class
// latest_day_vs_window: values of the latest day, or of all time, served
// beside a window with no label. Each entry is a path or the prefix of a
// block. This is read from the code of the resolvers; the oracle does not
// reproduce it. The allowance holds while the paths are in the policy. (The
// acr half of the class is gone: the flow fact names its window and its
// latest-day counts, and compareFlowWindow checks both.)
var temporaryOpsPaths = map[string][]string{
	"throughputForecast": {"throughputForecast.backlogSize", "throughputForecast.wipCongestion", "throughputForecast.staleWip", "throughputForecast.estimateCoverage"},
	"capacityForecast":   {"capacityForecast.backlogSize"},
	"workGraphFlow":      {"workGraphFlow.rows[*].inflow", "workGraphFlow.rows[*].outflow"},
}

// temporaryPathsMissing lists the paths of spec that outputs no longer hold.
func temporaryPathsMissing(spec []string, outputs []string) []string {
	var missing []string
	for _, want := range spec {
		found := false
		for _, path := range outputs {
			if path == want || strings.HasPrefix(path, want+".") {
				found = true
			}
		}
		if !found {
			missing = append(missing, want)
		}
	}
	return missing
}

// temporaryAllowance reports the temporary class for a root that still
// serves the paths it covers, and an expired allowance when the policy no
// longer holds them.
func (o *Oracle) temporaryAllowance(rr *RootReport, root *directread.GraphQLRootPolicy) {
	spec, ok := temporaryOpsPaths[root.Field]
	if !ok {
		return
	}
	var outputs []string
	for _, name := range root.Operations() {
		if op, refusal := o.Policy.Catalogue().Lookup(name); refusal == nil && op != nil {
			for _, out := range op.Outputs {
				outputs = append(outputs, out.Path)
			}
		}
	}
	if missing := temporaryPathsMissing(spec, outputs); len(missing) > 0 {
		rr.Expired = append(rr.Expired, fmt.Sprintf("class %s: %s is no longer an output path of the policy; remove the allowance", ClassLatestDayVsWindow, strings.Join(missing, ", ")))
		return
	}
	if rr.Listener != string(directread.CallServed) && rr.RunOperation != string(directread.CallServed) {
		return
	}
	rr.differ(Difference{Pair: "unlabelled_latest_value", Key: strings.Join(spec, ", "), Class: ClassLatestDayVsWindow, Exact: false,
		Detail: "read from the resolver code, not reproduced by this oracle: a latest-day or all-time value served beside a window with no label"})
}
