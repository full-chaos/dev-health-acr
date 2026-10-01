package factoracle

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

// Classes lists the named classes.
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
	// Residual is, for root analytics, the ops organization value minus the
	// sum of the acr repository mixes, per theme.
	Residual map[string]float64 `json:"residual,omitempty"`
	Excluded map[string]string  `json:"excluded,omitempty"`
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

// Findings lists every finding of the run.
func (r *Report) Findings() []Finding {
	var out []Finding
	for _, root := range r.Roots {
		out = append(out, root.Findings...)
	}
	return out
}
