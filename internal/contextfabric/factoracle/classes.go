package factoracle

import (
	"fmt"
	"strings"
)

// Class is a named difference between the two planes (design r5 J.4, O4).
// A difference that is not one of these, or whose witness does not hold, is a
// Finding.
type Class string

const (
	// ClassAttributionBasis: acr attributes effort to repositories by pull
	// request reference share; effort that reaches no repository is in the
	// ops organization value and in no acr repository mix (ruling K10).
	ClassAttributionBasis Class = "attribution_basis"
	// ClassNullRepoID: a team ownership row with no repo_id (ruling K11).
	ClassNullRepoID Class = "null_repo_id"
	// ClassSupersession: a superseded work unit read as live (ruling K16).
	ClassSupersession Class = "supersession"
	// ClassMembershipScope: a work unit outside the latest complete
	// membership run read as in scope (ruling K16).
	ClassMembershipScope Class = "membership_scope"
	// ClassNullableArgmax: a latest NULL repo_id replaced by the repo_id of
	// an older generation (ruling K16).
	ClassNullableArgmax Class = "nullable_argmax"
	// ClassForecastByDesign: forecast values that have no acr counterpart or
	// a different row shape by design (chris ruling; design A1.1).
	ClassForecastByDesign Class = "forecast_by_design"
)

// ClassLatestDayVsWindow is a TEMPORARY class: a value that is the latest
// day (or all time) of a series, served beside a window with no label that
// says so. It covers exactly the ops output paths of temporaryAllowances, and
// it is not one of the design's classes. A run counts the class only where it
// measured it (temporaryAllowance), and reports an expired allowance
// (Report.Expired) when a covered path leaves the policy, when the contract of
// its operation changes, or when the value starts to follow the requested
// window, so the allowance cannot outlive the fix it waits for.
const ClassLatestDayVsWindow Class = "latest_day_vs_window"

// TemporaryClasses lists the classes that are allowed only until a fix lands.
func TemporaryClasses() []Class { return []Class{ClassLatestDayVsWindow} }

// Classes lists the named classes of the design. They are permanent.
func Classes() []Class {
	return []Class{ClassAttributionBasis, ClassNullRepoID, ClassSupersession, ClassMembershipScope, ClassNullableArgmax, ClassForecastByDesign}
}

// Difference is an accepted, named difference.
type Difference struct {
	Root   string             `json:"root"`
	Pair   string             `json:"pair"`
	Key    string             `json:"key"`
	Class  Class              `json:"class"`
	Detail string             `json:"detail"`
	Values map[string]float64 `json:"values,omitempty"`
	// Exact is true when the witness equals the difference, false when the
	// witness only bounds it.
	Exact bool `json:"exact"`
}

// Finding is a difference outside the named classes.
type Finding struct {
	Root   string `json:"root"`
	Pair   string `json:"pair"`
	Key    string `json:"key"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
}

// RootReport is the outcome of one root field.
type RootReport struct {
	Root string `json:"root"`
	// Mode is "value" (compared with an acr fact) or "shape" (shape, echo
	// and limits only).
	Mode string `json:"mode"`
	// Reason says why a root has no value compare.
	Reason string `json:"reason,omitempty"`
	// Listener is "served", or the call status the root gave when it is not
	// enabled on the listener.
	Listener    string        `json:"listener"`
	ShapesRun   int           `json:"shapes_run"`
	Leaves      int           `json:"leaves"`
	Compared    int           `json:"compared"`
	Matches     int           `json:"matches"`
	Differences []Difference  `json:"differences"`
	Findings    []Finding     `json:"findings"`
	ByClass     map[Class]int `json:"by_class"`
	NotJoined   []string      `json:"not_joined,omitempty"`
	// RunOperation is "served", or the call status run_operation gave for
	// the root's operations; empty when no operation was run.
	RunOperation string `json:"run_operation,omitempty"`
	// OperationsRun counts run_operation calls; CrossPaths and CrossMatches
	// count the leaves compared between run_operation and graphql_query for
	// the same variables, and the equal ones.
	OperationsRun int `json:"operations_run"`
	CrossPaths    int `json:"cross_paths"`
	CrossMatches  int `json:"cross_matches"`
	// OperationLeaves counts the typed leaves of the served run_operation
	// answers.
	OperationLeaves int `json:"operation_leaves"`
	// Unmeasured lists the selected output paths for which no served answer
	// of the root gave a value: every answer had a null parent or an empty
	// list above them.
	Unmeasured []string `json:"unmeasured,omitempty"`
	// Expired lists temporary allowances that no longer appear.
	Expired []string `json:"expired,omitempty"`
	// Invalid lists the reasons the run of this root is not a measurement: a
	// path that was not served and is not declared dark, a served answer
	// with no leaf, a value root that compared nothing. A run with one fails.
	Invalid []string `json:"invalid,omitempty"`
	// CodeRead lists statements about the root that are read from the code
	// of a resolver and that this run did not measure. They are not counted
	// as differences.
	CodeRead []string `json:"code_read,omitempty"`
	// Residual is, for root analytics, the ops organization value minus the
	// sum of the acr repository mixes, per theme.
	Residual map[string]float64 `json:"residual,omitempty"`
	Excluded map[string]string  `json:"excluded,omitempty"`

	// The cases of each path by outcome, the output paths a served answer
	// gave a value for, and the ops output paths the value pairs compared.
	listenerServed, listenerDark   int
	operationServed, operationDark int
	measured                       map[string]bool
	touched                        map[string]int
}

func (r *RootReport) invalid(format string, args ...any) {
	r.Invalid = append(r.Invalid, fmt.Sprintf(format, args...))
}

// touch records that a value pair compared the ops output path.
func (r *RootReport) touch(paths ...string) {
	if r.touched == nil {
		r.touched = map[string]int{}
	}
	for _, path := range paths {
		r.touched[path]++
	}
}

func (r *RootReport) measure(leaves map[string][]Leaf) int {
	if r.measured == nil {
		r.measured = map[string]bool{}
	}
	n := 0
	for path, values := range leaves {
		if len(values) > 0 {
			r.measured[path] = true
		}
		n += len(values)
	}
	return n
}

func (r *RootReport) differ(d Difference) {
	d.Root = r.Root
	r.Differences = append(r.Differences, d)
	if r.ByClass == nil {
		r.ByClass = map[Class]int{}
	}
	r.ByClass[d.Class]++
}

func (r *RootReport) find(f Finding) {
	f.Root = r.Root
	r.Findings = append(r.Findings, f)
}

// Report is one oracle run.
type Report struct {
	Roots []*RootReport `json:"roots"`
}

// Root returns the report of one root field.
func (r *Report) Root(name string) *RootReport {
	for _, root := range r.Roots {
		if root.Root == name {
			return root
		}
	}
	return nil
}

// Expired lists every temporary allowance that stopped appearing. A run
// with one must fail: the class is to be removed.
func (r *Report) Expired() []string {
	var out []string
	for _, root := range r.Roots {
		out = append(out, root.Expired...)
	}
	return out
}

// Invalid lists, per root, every reason the run is not a measurement.
func (r *Report) Invalid() []string {
	var out []string
	for _, root := range r.Roots {
		for _, reason := range root.Invalid {
			out = append(out, root.Root+": "+reason)
		}
	}
	return out
}

// Err is the failure of a run that did not measure what it reports, or that
// measured an expired allowance. Findings are not an error: they are measured
// differences to report.
func (r *Report) Err() error {
	if invalid := r.Invalid(); len(invalid) > 0 {
		return fmt.Errorf("the run is not a measurement: %s", strings.Join(invalid, "; "))
	}
	if expired := r.Expired(); len(expired) > 0 {
		return fmt.Errorf("a temporary allowance expired; remove the class: %s", strings.Join(expired, "; "))
	}
	return nil
}

// Findings lists every finding of the run.
func (r *Report) Findings() []Finding {
	var out []Finding
	for _, root := range r.Roots {
		out = append(out, root.Findings...)
	}
	return out
}
