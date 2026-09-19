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
// canonical facts, paths and drivers. The label is the one
// canonicalSubjectLabels already binds for the key, which is the one
// requireBoundLabel accepts, so what the model is shown and what the
// validator accepts are the same string by construction. The validator is not
// widened.
//
// The bound label is the first source in the published walk, which puts a
// cohort member's label ahead of a fact's, and the cohort member row is the
// label the returned answer carries for that subject (the result cohort is
// the graph cohort).
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
	fix := func(subject SubjectRef) SubjectRef {
		if want, ok := bound[subjectKeyForModel(subject)]; ok {
			subject.Label = want
		}
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

	out.Graph.Resolution.Committed = fixAll(input.Graph.Resolution.Committed)
	if input.Graph.Resolution.Candidates != nil {
		out.Graph.Resolution.Candidates = make([]SubjectCandidate, len(input.Graph.Resolution.Candidates))
		for i, candidate := range input.Graph.Resolution.Candidates {
			candidate.Subject = fix(candidate.Subject)
			out.Graph.Resolution.Candidates[i] = candidate
		}
	}
	if input.Graph.Cohort != nil && cohortLabelsChange(*input.Graph.Cohort, fix) {
		cohort := *input.Graph.Cohort
		if cohort.Members != nil {
			cohort.Members = make([]CohortMember, len(input.Graph.Cohort.Members))
			for i, member := range input.Graph.Cohort.Members {
				member.Subject = fix(member.Subject)
				cohort.Members[i] = member
			}
		}
		if cohort.Exclusions != nil {
			cohort.Exclusions = make([]CohortExclusion, len(input.Graph.Cohort.Exclusions))
			for i, exclusion := range input.Graph.Cohort.Exclusions {
				exclusion.Subject = fix(exclusion.Subject)
				cohort.Exclusions[i] = exclusion
			}
		}
		if cohort.Groups != nil {
			cohort.Groups = append(cohort.Groups[:0:0], input.Graph.Cohort.Groups...)
			for i := range cohort.Groups {
				cohort.Groups[i].Subject = fix(cohort.Groups[i].Subject)
			}
		}
		out.Graph.Cohort = &cohort
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

// cohortLabelsChange reports whether rewriting would alter any subject label
// in the cohort. A cohort that needs no rewrite keeps its identity: the served
// answer carries this very value, so the synthesizer and the answer read one
// cohort.
func cohortLabelsChange(cohort Cohort, fix func(SubjectRef) SubjectRef) bool {
	for _, member := range cohort.Members {
		if fix(member.Subject) != member.Subject {
			return true
		}
	}
	for _, exclusion := range cohort.Exclusions {
		if fix(exclusion.Subject) != exclusion.Subject {
			return true
		}
	}
	for _, group := range cohort.Groups {
		if fix(group.Subject) != group.Subject {
			return true
		}
	}
	return false
}
