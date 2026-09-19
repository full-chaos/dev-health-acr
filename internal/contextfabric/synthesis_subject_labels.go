package contextfabric

import "encoding/json"

// SubjectLabelCanonicalization reports what canonicalizeSynthesisSubjectLabels
// did to one synthesis input. It is engine bookkeeping (`json:"-"` on its
// carrier), never model-facing.
type SubjectLabelCanonicalization struct {
	// KeysCollapsed is the number of subject keys (kind + canonical id) the
	// payload showed under more than one label before canonicalization.
	KeysCollapsed int
	// KeysResidual is the number of keys still shown under more than one
	// label afterwards. It is zero unless the payload carries a subject
	// source the typed rewrite does not cover; the serialized-form census
	// finds those by shape, so a nonzero value is a defect made visible.
	KeysResidual int
	// Measured is false when the payload could not be serialized for the
	// census; both counts are then unknown, never zero.
	Measured bool
}

// synthesisPayloadDecoded is the serialized payload synthesis shows the model,
// decoded to generic JSON. One definition feeds the subject census
// (synthesisPayloadSubjects) and the label census below, so the two cannot
// describe different payloads.
func synthesisPayloadDecoded(input SynthesisInput) (any, bool) {
	payload := struct {
		Interpretation   InterpretedQuestion `json:"interpretation"`
		Resolution       SubjectResolution   `json:"subject_resolution"`
		Cohort           *Cohort             `json:"cohort,omitempty"`
		Paths            []RelationshipPath  `json:"paths"`
		DriverCandidates []DriverJudgment    `json:"driver_candidates"`
		Facts            []CanonicalFact     `json:"canonical_facts"`
		Coverage         Coverage            `json:"coverage"`
		AnswerBudget     ItemAllocation      `json:"answer_budget"`
	}{
		AnswerBudget:     input.Allocation,
		Interpretation:   input.Interpretation,
		Resolution:       input.Graph.Resolution,
		Cohort:           input.Graph.Cohort,
		Paths:            input.Graph.Paths,
		DriverCandidates: input.Graph.DriverCandidates,
		Facts:            input.Facts.Facts,
		Coverage:         input.Graph.Coverage,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

// synthesisPayloadSubjectLabels maps every subject key the payload serializes
// to the set of labels it is shown under, found by shape (an object carrying a
// string "kind" and "canonical_id") so no nesting can be missed.
func synthesisPayloadSubjectLabels(input SynthesisInput) (map[string]map[string]struct{}, bool) {
	decoded, ok := synthesisPayloadDecoded(input)
	if !ok {
		return nil, false
	}
	result := make(map[string]map[string]struct{})
	var walk func(node any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			kind, kindOK := value["kind"].(string)
			canonicalID, idOK := value["canonical_id"].(string)
			if kindOK && idOK && canonicalID != "" {
				label, _ := value["label"].(string)
				key := string(SubjectKind(kind)) + "\x00" + canonicalID
				if result[key] == nil {
					result[key] = make(map[string]struct{})
				}
				result[key][label] = struct{}{}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(decoded)
	return result, true
}

func multiLabelKeys(labels map[string]map[string]struct{}) int {
	count := 0
	for _, set := range labels {
		if len(set) > 1 {
			count++
		}
	}
	return count
}

// canonicalizeSynthesisSubjectLabels makes every subject key carry ONE label
// across everything the synthesis payload shows the model: cohort members,
// canonical facts, paths and drivers.
//
// The label is the one the served answer already carries for a key the
// cohort or the committed subjects name (cohort first), and the label
// canonicalSubjectLabels binds (first source in the published walk) otherwise.
// Every occurrence then carries that label, so the binding requireBoundLabel
// enforces is that same label whatever the walk order, and the validator is
// not widened. The cohort and the committed subjects are served as they stand,
// so they are the sources for their own keys and are never rewritten.
//
// The input is copied on write; the caller's value is never mutated.
func canonicalizeSynthesisSubjectLabels(input SynthesisInput) (SynthesisInput, SubjectLabelCanonicalization) {
	before, beforeMeasured := synthesisPayloadSubjectLabels(input)
	if multiLabelKeys(before) == 0 {
		// One label per key already: nothing to rewrite, and the input is
		// returned as it came so the served cohort stays the very value the
		// synthesizer was shown.
		report := SubjectLabelCanonicalization{Measured: beforeMeasured}
		input.LabelCanonicalization = report
		return input, report
	}
	bound := canonicalSubjectLabels(input)
	// The cohort and the committed subjects are the parts of the payload the
	// returned answer carries as they stand and the model cites, so their
	// labels are the labels for every key they name and neither is rewritten.
	// Two labels for one key inside those structures cannot be resolved here
	// and are reported as residual. Resolution candidates are alternatives the
	// model may not cite, so they follow the label the rest of the payload has.
	named := make(map[string]struct{})
	claim := func(subject SubjectRef) {
		key := subjectKeyForModel(subject)
		if _, done := named[key]; done {
			return
		}
		named[key] = struct{}{}
		bound[key] = subject.Label
	}
	if cohort := input.Graph.Cohort; cohort != nil {
		for _, member := range cohort.Members {
			claim(member.Subject)
		}
		for _, group := range cohort.Groups {
			claim(group.Subject)
		}
		for _, exclusion := range cohort.Exclusions {
			claim(exclusion.Subject)
		}
	}
	for _, subject := range input.Graph.Resolution.Committed {
		claim(subject)
	}
	// Every subject rewritten below is visited by the walk canonicalSubjectLabels
	// binds from, so each has a binding; the enumeration test pins that.
	fix := func(subject SubjectRef) SubjectRef {
		subject.Label = bound[subjectKeyForModel(subject)]
		return subject
	}
	fixAll := func(subjects []SubjectRef) []SubjectRef {
		if subjects == nil {
			return nil
		}
		out := make([]SubjectRef, len(subjects))
		for i, subject := range subjects {
			out[i] = fix(subject)
		}
		return out
	}
	out := input

	if input.Graph.Resolution.Candidates != nil {
		out.Graph.Resolution.Candidates = make([]SubjectCandidate, len(input.Graph.Resolution.Candidates))
		for i, candidate := range input.Graph.Resolution.Candidates {
			candidate.Subject = fix(candidate.Subject)
			out.Graph.Resolution.Candidates[i] = candidate
		}
	}
	if input.Graph.Paths != nil {
		out.Graph.Paths = make([]RelationshipPath, len(input.Graph.Paths))
		for i, path := range input.Graph.Paths {
			path.Nodes = fixAll(path.Nodes)
			if path.Edges != nil {
				edges := make([]RelationshipEdge, len(path.Edges))
				for j, edge := range path.Edges {
					edge.From = fix(edge.From)
					edge.To = fix(edge.To)
					edges[j] = edge
				}
				path.Edges = edges
			}
			out.Graph.Paths[i] = path
		}
	}
	if input.Graph.DriverCandidates != nil {
		out.Graph.DriverCandidates = make([]DriverJudgment, len(input.Graph.DriverCandidates))
		for i, candidate := range input.Graph.DriverCandidates {
			candidate.AffectedSubjects = fixAll(candidate.AffectedSubjects)
			out.Graph.DriverCandidates[i] = candidate
		}
	}
	if input.Facts.Facts != nil {
		out.Facts.Facts = make([]CanonicalFact, len(input.Facts.Facts))
		for i, fact := range input.Facts.Facts {
			fact.Subject = fix(fact.Subject)
			out.Facts.Facts[i] = fact
		}
	}

	// Only a payload that serialized before reaches here with keys to collapse,
	// and the payload after differs from it in label strings alone, so it
	// serializes too.
	after, _ := synthesisPayloadSubjectLabels(out)
	report := SubjectLabelCanonicalization{
		Measured:      true,
		KeysCollapsed: multiLabelKeys(before),
		KeysResidual:  multiLabelKeys(after),
	}
	out.LabelCanonicalization = report
	return out, report
}

// Label canonicalization outcomes, the closed vocabulary of the
// synthesis_input line's label_canonicalization field.
const (
	// LabelCanonicalizationUnchanged: every key already carried one label.
	LabelCanonicalizationUnchanged = "unchanged"
	// LabelCanonicalizationCollapsed: at least one key carried several labels
	// and each key in the output carries one.
	LabelCanonicalizationCollapsed = "collapsed"
	// LabelCanonicalizationResidual: some key still carries several labels.
	LabelCanonicalizationResidual = "residual"
	// LabelCanonicalizationUnmeasured: the payload could not be serialized.
	LabelCanonicalizationUnmeasured = "unmeasured"
)

// Outcome names the report's state for the trace line.
func (r SubjectLabelCanonicalization) Outcome() string {
	switch {
	case !r.Measured:
		return LabelCanonicalizationUnmeasured
	case r.KeysResidual > 0:
		return LabelCanonicalizationResidual
	case r.KeysCollapsed > 0:
		return LabelCanonicalizationCollapsed
	default:
		return LabelCanonicalizationUnchanged
	}
}
