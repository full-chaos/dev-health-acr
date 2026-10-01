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
			// Not served through run_operation: stated here; validate fails
			// the run unless the venue declares the root dark on this path.
			rr.RunOperation = answer.Call
			rr.operationDark++
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
	rr.operationServed++
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
	rr.OperationLeaves += rr.measure(leaves)

	// The other path, when it served the same case. Its own shape problems
	// were reported by the shape pass, which ran this same answer.
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

// setVariable returns a deep copy of variables with the value at a dotted
// path replaced, and the value that was there.
func setVariable(variables map[string]any, path string, value any) (map[string]any, any, bool) {
	generic, err := decodeJSON([]byte(canonicalJSON(variables)))
	if err != nil {
		return nil, nil, false
	}
	out, ok := generic.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	segs := strings.Split(path, ".")
	cur := out
	for _, seg := range segs[:len(segs)-1] {
		next, isObject := cur[seg].(map[string]any)
		if !isObject {
			return nil, nil, false
		}
		cur = next
	}
	last := segs[len(segs)-1]
	prior, had := cur[last]
	if !had {
		return nil, nil, false
	}
	cur[last] = value
	return out, prior, true
}

// probeLeaves runs one case of the window probe on the path that serves the
// root and returns its typed leaves. served is false, with the reason, when
// the case gave nothing to read.
func (o *Oracle) probeLeaves(ctx context.Context, rr *RootReport, shape Shape, variables map[string]any) (leaves map[string][]Leaf, reason string, err error) {
	var data []byte
	switch {
	case rr.listenerServed > 0:
		answer, gerr := o.graphQL(ctx, shape, variables)
		if gerr != nil {
			return nil, "", gerr
		}
		if answer.Call != string(directread.CallServed) {
			return nil, "graphql_query answered " + answer.Call, nil
		}
		data = answer.Data
	case rr.operationServed > 0:
		answer, oerr := o.operation(ctx, shape, variables)
		if oerr != nil {
			return nil, "", oerr
		}
		if answer.Call != string(directread.CallServed) {
			return nil, "run_operation answered " + answer.Call, nil
		}
		data = answer.Data
	default:
		return nil, "no path served the root", nil
	}
	decoded, derr := decodeJSON(data)
	if derr != nil {
		return nil, "the answer is not JSON", nil
	}
	object, _ := decoded.(map[string]any)
	if object == nil || object[shape.Root] == nil {
		return nil, "the answer is null", nil
	}
	leaves, _ = shape.shapeLeaves(decoded)
	return leaves, "", nil
}

// leavesUnder returns the leaves of a path or of the block under it, as
// sorted strings per path, type names left out. measured is false when no
// leaf holds a value.
func leavesUnder(leaves map[string][]Leaf, prefix string) (out string, measured bool) {
	var paths []string
	for path := range leaves {
		if strings.HasSuffix(path, ".__typename") {
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+".") || strings.HasPrefix(path, prefix+"[*]") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, path := range paths {
		for _, leaf := range leaves[path] {
			if leaf.T != LeafNull {
				measured = true
			}
		}
		b.WriteString(path + "=" + strings.Join(leafStrings(leaves[path]), ",") + ";")
	}
	return b.String(), measured
}

// temporaryAllowance measures the temporary class for a root. The allowance
// expires when a covered path leaves the policy, when the contract of its
// operation is not the one it was read against, or when a covered value is
// another value for another history: it then follows the requested window,
// which is the fix the allowance waits for. A covered value that is the same
// for both histories, while the answer states both histories, is one counted
// difference of the class. What the run could not measure is stated in
// CodeRead and is not counted.
func (o *Oracle) temporaryAllowance(ctx context.Context, rr *RootReport, root *directread.GraphQLRootPolicy, cases map[string][]ShapeCase) error {
	spec, ok := o.allowances()[root.Field]
	if !ok {
		return nil
	}
	if missing := temporaryPathsMissing(spec.Paths, rootOutputs(o.Policy, root)); len(missing) > 0 {
		rr.Expired = append(rr.Expired, fmt.Sprintf("class %s: %s is no longer an output path of the policy; remove the allowance", ClassLatestDayVsWindow, strings.Join(missing, ", ")))
		return nil
	}
	op, refusal := o.Policy.Catalogue().Lookup(spec.Operation)
	if refusal != nil || op == nil {
		rr.Expired = append(rr.Expired, fmt.Sprintf("class %s: operation %s is no longer served; remove the allowance", ClassLatestDayVsWindow, spec.Operation))
		return nil
	}
	if got := contractDigest(op); got != spec.Contract {
		rr.Expired = append(rr.Expired, fmt.Sprintf("class %s: the contract of operation %s is %s, not the one the allowance was read against; read the resolver again, then renew the digest or remove the allowance", ClassLatestDayVsWindow, spec.Operation, got))
		return nil
	}
	covered := strings.Join(spec.Paths, ", ")
	unmeasured := func(reason string) {
		rr.CodeRead = append(rr.CodeRead, fmt.Sprintf("class %s, %s: read from the resolver code, not measured by this run (%s)", ClassLatestDayVsWindow, covered, reason))
	}
	if spec.Window == "" {
		unmeasured("the operation takes no window argument")
		return nil
	}
	shape, err := o.shape(root.Field + "/" + spec.Operation + "/all")
	if err != nil {
		return err
	}
	if len(cases[shape.ID()]) == 0 {
		return fmt.Errorf("shape %s has no case for the window probe", shape.ID())
	}
	wide := cases[shape.ID()][0].Variables
	narrow, prior, ok := setVariable(wide, spec.Window, spec.Narrow)
	if !ok {
		return fmt.Errorf("the case of shape %s does not set %s: the window probe has no history to change", shape.ID(), spec.Window)
	}
	if fmt.Sprint(prior) == fmt.Sprint(spec.Narrow) {
		return fmt.Errorf("the case of shape %s already asks for a history of %d: the window probe needs two histories", shape.ID(), spec.Narrow)
	}
	if err := checkVariablePaths(shape, "", narrow); err != nil {
		return err
	}
	wideLeaves, reason, err := o.probeLeaves(ctx, rr, shape, wide)
	if err != nil {
		return err
	}
	if reason != "" {
		unmeasured(reason)
		return nil
	}
	narrowLeaves, reason, err := o.probeLeaves(ctx, rr, shape, narrow)
	if err != nil {
		return err
	}
	if reason != "" {
		unmeasured("for the second history " + reason)
		return nil
	}
	wideEcho, _ := leavesUnder(wideLeaves, spec.Echo)
	narrowEcho, echoMeasured := leavesUnder(narrowLeaves, spec.Echo)
	if !echoMeasured || wideEcho == narrowEcho {
		unmeasured(fmt.Sprintf("the answer states the same history at %s for both requests", spec.Echo))
		return nil
	}
	for _, path := range spec.Paths {
		a, measured := leavesUnder(wideLeaves, path)
		b, _ := leavesUnder(narrowLeaves, path)
		switch {
		case a != b:
			rr.Expired = append(rr.Expired, fmt.Sprintf("class %s: %s is another value for a history of %v and of %d: it now follows the requested window; remove it from the allowance", ClassLatestDayVsWindow, path, prior, spec.Narrow))
		case !measured:
			rr.CodeRead = append(rr.CodeRead, fmt.Sprintf("class %s, %s: read from the resolver code, not measured by this run (the answer holds no value there)", ClassLatestDayVsWindow, path))
		default:
			rr.differ(Difference{Pair: "window_probe", Key: path, Class: ClassLatestDayVsWindow, Exact: true,
				Detail: fmt.Sprintf("the value is the same for a history of %v and of %d, and the answer states both histories: it does not follow the requested window", prior, spec.Narrow)})
		}
	}
	return nil
}

func (o *Oracle) allowances() map[string]temporaryAllowance {
	if o.Allowances != nil {
		return o.Allowances
	}
	return temporaryAllowances
}
