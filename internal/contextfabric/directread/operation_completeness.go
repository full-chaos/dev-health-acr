package directread

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// CompletenessReason says why a served answer is not declared_complete. It is
// a closed set; an answer that is declared_complete carries none.
type CompletenessReason string

const (
	// ReasonPageCut: the answer was cut to whole rows to fit max_bytes.
	ReasonPageCut CompletenessReason = "page_cut"
	// ReasonBounded: a list reached the row cap the request (or its default) set.
	ReasonBounded CompletenessReason = "bounded"
	// ReasonCoverageBelowOne: the payload states a coverage ratio below one.
	ReasonCoverageBelowOne CompletenessReason = "coverage_below_one"
	// ReasonInsufficientHistory: the payload states the history was too short.
	ReasonInsufficientHistory CompletenessReason = "insufficient_history"
	// ReasonDegraded: the payload states a degraded reason.
	ReasonDegraded CompletenessReason = "degraded"
	// ReasonDisclosureAbsent: a field the verdict needs is not in the answer.
	ReasonDisclosureAbsent CompletenessReason = "disclosure_absent"
	// ReasonLimitNotSent: the row cap is unknown: no limit was sent and the
	// operation has no default.
	ReasonLimitNotSent CompletenessReason = "limit_not_sent"
)

// CompletenessReasonVocabulary is the closed set of reasons.
func CompletenessReasonVocabulary() [7]CompletenessReason {
	return [7]CompletenessReason{ReasonPageCut, ReasonBounded, ReasonCoverageBelowOne, ReasonInsufficientHistory, ReasonDegraded, ReasonDisclosureAbsent, ReasonLimitNotSent}
}

// CompletenessVerdict is the one answer to "did this read cover what it was
// asked for". Reason is empty exactly when State is declared_complete.
type CompletenessVerdict struct {
	State  Completeness
	Reason CompletenessReason
}

// capSpec ties one request row cap to the list it bounds. A "[*]" in both
// paths pairs the elements of the two arrays by index.
type capSpec struct {
	VarPath  string
	ListPath string
}

// operationCaps enumerates every served operation: the row caps it can hit.
// An operation with no cap has the empty list and is read in full or not at
// all. TestEveryOperationHasACompletenessBasis fails on an operation missing
// here, and on a cap that names no variable or no output list.
var operationCaps = map[string][]capSpec{
	"acrRepositoryScopes":            {},
	"capacityCompletionDistribution": {},
	"capacityForecast":               {},
	"capacityForecasts":              {{"filters.limit", "capacityForecasts.edges"}},
	"catalogValues":                  {},
	"cognitiveLoad":                  {},
	"complexityTimeseries":           {{"input.limit", "complexityTimeseries.points"}},
	"compoundingRisk":                {},
	"home":                           {},
	"hotspots":                       {{"input.limit", "hotspots.rows"}},
	"investmentBreakdown":            {{"batch.breakdowns[*].topN", "analytics.breakdowns[*].items"}},
	"investmentFull":                 {{"batch.breakdowns[*].topN", "analytics.breakdowns[*].items"}},
	"recommendations":                {},
	"securityOverview":               {},
	"throughputForecast":             {},
	"workGraphArtifacts":             {{"filters.limit", "workGraphArtifacts.rows"}},
	"workGraphEdges":                 {{"filters.limit", "workGraphEdges.edges"}},
	"workGraphFlow":                  {},
	"workItemTeamAttributions":       {},
}

// Verdict is the one producer of completeness for every served operation.
// Order: a disclosure field that fires or a list at its cap = declared_partial;
// else a field the verdict needs but the answer lacks = unknown; else
// declared_complete. data is the filtered answer; vars is the validated
// variable tree of the same read.
func (op *OperationPolicy) Verdict(data []byte, vars map[string]any) CompletenessVerdict {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if dec.Decode(&root) != nil {
		return CompletenessVerdict{State: CompletenessUnknown, Reason: ReasonDisclosureAbsent}
	}
	var missing *CompletenessVerdict
	note := func(r CompletenessReason) {
		if missing == nil {
			missing = &CompletenessVerdict{State: CompletenessUnknown, Reason: r}
		}
	}
	for _, d := range op.Disclosure {
		value, present := lookupScalarPath(root, d.Path)
		if !present {
			note(ReasonDisclosureAbsent)
			continue
		}
		switch d.Rule {
		case DisclosurePartialWhenNonNull:
			if value != nil {
				return CompletenessVerdict{State: CompletenessDeclaredPartial, Reason: ReasonDegraded}
			}
		case DisclosurePartialWhenTrue:
			if b, ok := value.(bool); ok && b {
				return CompletenessVerdict{State: CompletenessDeclaredPartial, Reason: ReasonInsufficientHistory}
			}
		case DisclosurePartialWhenBelowOne:
			if n, ok := value.(json.Number); ok {
				if x, err := n.Float64(); err == nil && x < 1 {
					return CompletenessVerdict{State: CompletenessDeclaredPartial, Reason: ReasonCoverageBelowOne}
				}
			}
		}
	}
	for _, c := range operationCaps[op.Name] {
		hit, reason := op.capReached(root, vars, c)
		if hit {
			return CompletenessVerdict{State: CompletenessDeclaredPartial, Reason: ReasonBounded}
		}
		if reason != "" {
			note(reason)
		}
	}
	if missing != nil {
		return *missing
	}
	return CompletenessVerdict{State: CompletenessDeclaredComplete}
}

// capReached reports whether a list holds as many rows as its cap. A list or
// a cap that cannot be read gives a reason and no verdict of its own.
func (op *OperationPolicy) capReached(root any, vars map[string]any, c capSpec) (bool, CompletenessReason) {
	varPre, varPost, indexed := strings.Cut(c.VarPath, "[*].")
	listPre, listPost, _ := strings.Cut(c.ListPath, "[*].")
	rule, _ := op.Variable(c.VarPath)
	capOf := func(holder any, path string) (int, bool) {
		if v, ok := lookupScalarPath(holder, path); ok && v != nil {
			return numberToInt(v)
		}
		return numberToInt(json.Number(rule.Default))
	}
	if !indexed {
		list, ok := lookupListPath(root, c.ListPath)
		if !ok {
			return false, ReasonDisclosureAbsent
		}
		limit, known := capOf(vars, c.VarPath)
		if !known {
			return false, ReasonLimitNotSent
		}
		return len(list) >= limit, ""
	}
	elems, ok := lookupListPath(root, listPre)
	if !ok {
		return false, ReasonDisclosureAbsent
	}
	varElems, _ := lookupListPath(vars, varPre)
	reason := CompletenessReason("")
	for i, el := range elems {
		list, ok := lookupListPath(el, listPost)
		if !ok {
			reason = ReasonDisclosureAbsent
			continue
		}
		var holder any = map[string]any{}
		if i < len(varElems) {
			holder = varElems[i]
		}
		limit, known := capOf(holder, varPost)
		if !known {
			reason = ReasonLimitNotSent
			continue
		}
		if len(list) >= limit {
			return true, ""
		}
	}
	return false, reason
}

func lookupListPath(root any, path string) ([]any, bool) {
	cur := root
	if path != "" {
		v, ok := lookupScalarPath(root, path)
		if !ok {
			return nil, false
		}
		cur = v
	}
	list, ok := cur.([]any)
	return list, ok
}

func numberToInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.Atoi(n.String())
		return i, err == nil && i > 0
	case float64:
		return int(n), n >= 1
	case int:
		return n, n >= 1
	case int64:
		return int(n), n >= 1
	}
	return 0, false
}

// joinVerdicts folds the per-root verdicts of one graphql_query: any partial
// root makes the answer partial, else any unknown root makes it unknown, else
// every root was read in full.
func joinVerdicts(acc *CompletenessVerdict, next CompletenessVerdict) *CompletenessVerdict {
	rank := func(c Completeness) int {
		switch c {
		case CompletenessDeclaredPartial:
			return 2
		case CompletenessUnknown:
			return 1
		}
		return 0
	}
	if acc == nil || rank(next.State) > rank(acc.State) {
		n := next
		return &n
	}
	return acc
}
