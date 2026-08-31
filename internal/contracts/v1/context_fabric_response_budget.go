package v1

import "encoding/json"

// This file is the ONE definition of "how big is a Context Fabric answer",
// called by BOTH planes (CHAOS-4636 / intent-engine design §6.3 stage 3).
//
// Why it lives here rather than where the enforcement lives. The budget was
// specified three times before this one, and revision 3 failed on an import
// cycle: the measurement was `MarshalContextFabricResponse` in
// internal/api, but internal/api imports internal/contextfabric and NOT the
// reverse, so the engine could never call it. `go list -deps ./internal/api`
// contains .../internal/contextfabric; `go list -deps ./internal/contextfabric`
// does not contain .../internal/api. internal/contracts/v1 is imported by
// both, so it is the only place a single definition can sit.
//
// Revision 3 also tried to TRIM a composed answer to fit. That is abandoned
// on purpose and must not be reintroduced here: by the time the route holds a
// result, render selection, Validate/ValidateAgainst and persistence have all
// run, so dropping a driver can orphan a ContextFabricRenderShape point that
// cites it, break grounding closure, and make the STORED and the SERVED
// answer diverge. This file therefore only ever MEASURES. What to do about an
// over-budget answer is the engine's decision (re-synthesize once with a
// smaller input; then a planned, explained refusal), never a mutation applied
// down here.
//
// The route's own gate becomes an assertion over these same numbers rather
// than a second, drifting measurement -- the "one derivation, used
// everywhere" discipline investigationScopeSubjectSet already documents in
// internal/contextfabric/fact_planner.go.

// ContextFabricResultItemCounts is the per-collection breakdown of what the
// item budget charges against a Context Fabric investigation result.
//
// The set of charged collections is deliberately explicit rather than
// reflective: a new result collection must be a DECISION to charge or not to
// charge it, made in this struct, not something a reflective walk silently
// starts billing on the day the field is added.
type ContextFabricResultItemCounts struct {
	Candidates    int `json:"candidates"`
	Drivers       int `json:"drivers"`
	Paths         int `json:"paths"`
	RemainingWork int `json:"remaining_work"`
	ReadinessGaps int `json:"readiness_gaps"`
	Conflicts     int `json:"conflicts"`
	ClaimedFacts  int `json:"claimed_facts"`
	CohortMembers int `json:"cohort_members"`
}

// Total is every charged item, Paths included. It is what a caller's usage
// accounting reports.
func (c ContextFabricResultItemCounts) Total() int {
	return c.Candidates + c.Drivers + c.Paths + c.RemainingWork + c.ReadinessGaps +
		c.Conflicts + c.ClaimedFacts + c.CohortMembers
}

// Budgeted is Total minus Paths: graph-evidence relationship paths are
// EXCLUDED from the item budget (CHAOS-4523). Keeping the exclusion here,
// beside the count it excludes from, is why the engine and the route cannot
// disagree about it.
func (c ContextFabricResultItemCounts) Budgeted() int { return c.Total() - c.Paths }

// CountContextFabricResultItems charges every collection the item budget
// covers. It is total, not partial: a result whose Cohort is nil charges zero
// cohort members rather than being un-countable.
func CountContextFabricResultItems(result ContextFabricInvestigationResult) ContextFabricResultItemCounts {
	counts := ContextFabricResultItemCounts{
		Candidates:    len(result.SubjectResolution.Candidates),
		Drivers:       len(result.Drivers),
		Paths:         len(result.Paths),
		RemainingWork: len(result.RemainingWork),
		ReadinessGaps: len(result.ReadinessGaps),
		Conflicts:     len(result.Conflicts),
		ClaimedFacts:  len(result.ClaimedFacts),
	}
	if result.Cohort != nil {
		counts.CohortMembers = len(result.Cohort.Members)
	}
	return counts
}

// ContextFabricResponseBudget is the effective ceiling a result is measured
// against. MaxItems bounds Budgeted(); MaxSerializedBytes bounds the marshaled
// response body.
//
// A zero field means "unbounded on that axis" so a caller that knows only one
// of the two (the engine before it has the route's config, a test) is not
// forced to invent the other.
type ContextFabricResponseBudget struct {
	MaxItems           int   `json:"max_items"`
	MaxSerializedBytes int64 `json:"max_serialized_bytes"`
}

// ContextFabricBudgetOverrun is the CLOSED vocabulary for which axis a result
// exceeded. It is closed because it reaches telemetry and a caller-facing
// refusal, and both of those are dashboards and contracts rather than prose.
type ContextFabricBudgetOverrun string

const (
	// ContextFabricBudgetFits is the zero value: the result is inside both
	// axes. It is spelled rather than left as "" so a switch over this
	// vocabulary has no unnamed arm.
	ContextFabricBudgetFits ContextFabricBudgetOverrun = "fits"
	// ContextFabricBudgetOverrunItems means Budgeted() exceeded MaxItems.
	ContextFabricBudgetOverrunItems ContextFabricBudgetOverrun = "items"
	// ContextFabricBudgetOverrunBytes means the marshaled body exceeded
	// MaxSerializedBytes.
	ContextFabricBudgetOverrunBytes ContextFabricBudgetOverrun = "bytes"
)

var contextFabricBudgetOverruns = [3]ContextFabricBudgetOverrun{
	ContextFabricBudgetFits,
	ContextFabricBudgetOverrunItems,
	ContextFabricBudgetOverrunBytes,
}

// ContextFabricBudgetOverrunCount is the size of the closed overrun
// vocabulary, as a compile-time constant.
const ContextFabricBudgetOverrunCount = len(contextFabricBudgetOverruns)

// ContextFabricBudgetOverrunVocabulary returns the closed overrun vocabulary
// in published order. The return type is an ARRAY, so the value is copied to
// the caller -- see ContextFabricFactKindVocabulary for why that matters.
func ContextFabricBudgetOverrunVocabulary() [ContextFabricBudgetOverrunCount]ContextFabricBudgetOverrun {
	return contextFabricBudgetOverruns
}

// ValidContextFabricBudgetOverrun reports whether value is a member of the
// closed overrun vocabulary.
func ValidContextFabricBudgetOverrun(value ContextFabricBudgetOverrun) bool {
	for _, member := range contextFabricBudgetOverruns {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricResponseMeasurement is one measurement of one assembled
// result: the item breakdown, and the size of the body that would be served
// for it.
type ContextFabricResponseMeasurement struct {
	Items ContextFabricResultItemCounts `json:"items"`
	Bytes int64                         `json:"bytes"`
}

// MeasureContextFabricResponse counts and sizes result with the SAME encoder
// the investigation route serves with, so a fit decided here is a fit there.
//
// The error is the marshal error and nothing else. A result that cannot be
// marshaled is not "too large" -- it is a server defect, and conflating the
// two would let a serialization bug present as a budget refusal.
func MeasureContextFabricResponse(result ContextFabricInvestigationResult) (ContextFabricResponseMeasurement, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return ContextFabricResponseMeasurement{}, err
	}
	return ContextFabricResponseMeasurement{
		Items: CountContextFabricResultItems(result),
		Bytes: int64(len(encoded)),
	}, nil
}

// Overrun reports which axis, if any, this measurement exceeds under budget.
//
// Items are checked before bytes, deliberately and stably: the two can be
// exceeded together, a single closed value must be reported for telemetry and
// for the refusal, and the item budget is the one a caller can act on by
// asking a narrower question. A zero on either budget axis disables that axis.
func (m ContextFabricResponseMeasurement) Overrun(budget ContextFabricResponseBudget) ContextFabricBudgetOverrun {
	if budget.MaxItems > 0 && m.Items.Budgeted() > budget.MaxItems {
		return ContextFabricBudgetOverrunItems
	}
	if budget.MaxSerializedBytes > 0 && m.Bytes > budget.MaxSerializedBytes {
		return ContextFabricBudgetOverrunBytes
	}
	return ContextFabricBudgetFits
}

// Fits is Overrun's boolean form, for the common call site that does not need
// to know which axis failed.
func (m ContextFabricResponseMeasurement) Fits(budget ContextFabricResponseBudget) bool {
	return m.Overrun(budget) == ContextFabricBudgetFits
}
