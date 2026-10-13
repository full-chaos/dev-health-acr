package v1

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// setJSONPath sets every value the dotted path names; "*" walks every element.
func setJSONPath(t *testing.T, node any, path []string, value any) any {
	t.Helper()
	if len(path) == 0 {
		return value
	}
	switch n := node.(type) {
	case map[string]any:
		child, ok := n[path[0]]
		if !ok {
			t.Fatalf("path segment %q is absent", path[0])
		}
		n[path[0]] = setJSONPath(t, child, path[1:], value)
		return n
	case []any:
		if path[0] == "*" {
			for i := range n {
				n[i] = setJSONPath(t, n[i], path[1:], value)
			}
			return n
		}
		i, err := strconv.Atoi(path[0])
		if err != nil || i >= len(n) {
			t.Fatalf("path segment %q does not index a list of %d", path[0], len(n))
		}
		n[i] = setJSONPath(t, n[i], path[1:], value)
		return n
	default:
		t.Fatalf("path segment %q reaches a %T", path[0], node)
		return nil
	}
}

func resultWithValueAt(t *testing.T, fixture string, value string, paths ...string) ContextFabricInvestigationResult {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		doc = setJSONPath(t, doc, strings.Split(p, "."), value)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var r ContextFabricInvestigationResult
	if err := json.Unmarshal(encoded, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

const (
	resultV1             = "context_fabric_investigation_result.v1.json"
	resultV2             = "context_fabric_investigation_result.v2.json"
	resultOriginState    = "context_fabric_investigation_result_origin_state.v1.json"
	resultStatusCorrect  = "context_fabric_investigation_result_status_corrected.v1.json"
	resultHistorical     = "context_fabric_investigation_result_historical.v1.json"
	resultUnsupported    = "context_fabric_investigation_result_unsupported.v1.json"
	addedOutcomeRequired = "evidence/subject/project"
)

type storedVocabularyRow struct {
	name    string
	fixture string
	paths   []string
	mutate  func(r *ContextFabricInvestigationResult, value string)
	// neverChecked marks a field the stored read did not check before open
	// vocabularies (finding kinds, coverage details), so a malformed value
	// is not refused there either.
	neverChecked bool
}

func (row storedVocabularyRow) build(t *testing.T, value string) ContextFabricInvestigationResult {
	t.Helper()
	r := resultWithValueAt(t, row.fixture, value, row.paths...)
	if row.mutate != nil {
		row.mutate(&r, value)
	}
	return r
}

func storedAdditiveRows() []storedVocabularyRow {
	return []storedVocabularyRow{
		{name: "interpretation.shape", fixture: resultV1, paths: []string{"interpretation.shape"}},
		{name: "interpretation.fact_requirements.kind", fixture: resultV1, paths: []string{"interpretation.fact_requirements.0.kind"}},
		{name: "interpretation.fact_requirements.subjects.kind", fixture: resultV1, paths: []string{"interpretation.fact_requirements.*.subjects.*.kind"}},
		{name: "interpretation.window_class", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.Interpretation.WindowClass = ContextFabricWindowClass(v)
		}},
		{name: "interpretation.window_confidence", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.Interpretation.WindowConfidence = ContextFabricWindowConfidence(v)
		}},
		{name: "interpretation.requested_judgment_kind", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.Interpretation.RequestedJudgmentKind = ContextFabricRequestedJudgmentKind(v)
		}},
		{name: "subject_resolution.candidates.state", fixture: resultV1, paths: []string{"subject_resolution.candidates.0.state"}},
		{name: "subject_resolution.candidates.match_mechanisms", fixture: resultV1, paths: []string{"subject_resolution.candidates.0.match_mechanisms.0"}},
		{name: "subject_resolution.candidates.subject.kind", fixture: resultV1, paths: []string{"subject_resolution.candidates.0.subject.kind"}},
		{name: "subject_resolution.committed.kind", fixture: resultV1, paths: []string{"subject_resolution.committed.0.kind", "subject_resolution.commit_decision_digests.0.subject.kind"}},
		{name: "commit_decision_digests.commit_gate", fixture: resultV1, paths: []string{"subject_resolution.commit_decision_digests.0.commit_gate"}},
		{name: "commit_decision_digests.identity_lookup", fixture: resultV1, paths: []string{"subject_resolution.commit_decision_digests.0.identity_lookup"}},
		{name: "subject_resolution.prior_subject_receipt_dispositions", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.SubjectResolution.PriorSubjectReceiptDispositions = []ContextFabricPriorSubjectReceiptEntry{{PriorResultID: "result_prior_0001", ReceiptID: "receipt_prior_0001", Disposition: ContextFabricPriorSubjectReceiptDisposition(v)}}
		}},
		{name: "claimed_facts.kind", fixture: resultV1, paths: []string{"claimed_facts.0.kind", "drivers.0.category"}},
		{name: "claimed_facts.subject.kind", fixture: resultV1, paths: []string{"claimed_facts.0.subject.kind"}},
		{name: "cohort.kind", fixture: resultV1, paths: []string{"cohort.kind", "cohort.members.*.subject.kind", "claimed_facts.1.subject.kind", "claimed_facts.2.subject.kind", "claimed_facts.3.subject.kind"}},
		{name: "drivers.category", fixture: resultV1, paths: []string{"drivers.0.category"}},
		{name: "drivers.standing", fixture: resultV1, paths: []string{"drivers.0.standing"}},
		{name: "drivers.derivation", fixture: resultV1, paths: []string{"drivers.0.derivation"}},
		{name: "drivers.epistemic_status", fixture: resultV1, paths: []string{"drivers.0.epistemic_status"}},
		{name: "drivers.affected_subjects.kind", fixture: resultV1, paths: []string{"drivers.0.affected_subjects.0.kind"}},
		{name: "remaining_work.kind", fixture: resultV1, paths: []string{"remaining_work.0.kind"}, neverChecked: true},
		{name: "paths.edges.type", fixture: resultV1, paths: []string{"paths.0.edges.0.type"}},
		{name: "paths.edges.derivation", fixture: resultV1, paths: []string{"paths.0.edges.0.derivation"}},
		{name: "paths.edges.epistemic_status", fixture: resultV1, paths: []string{"paths.0.edges.0.epistemic_status"}},
		{name: "paths.nodes.kind", fixture: resultV1, paths: []string{"paths.0.nodes.0.kind", "paths.0.edges.0.from.kind"}},
		{name: "coverage.sources.state", fixture: resultV1, paths: []string{"coverage.sources.0.state"}},
		{name: "coverage.details.code", fixture: resultOriginState, paths: []string{"coverage.details.0.code"}, neverChecked: true},
		{name: "coverage.details.fact_kind", fixture: resultOriginState, paths: []string{"coverage.details.0.fact_kind"}, neverChecked: true},
		{name: "coverage.details.source_state", fixture: resultOriginState, paths: []string{"coverage.details.0.source_state"}, neverChecked: true},
		{name: "coverage.details.origin_kind", fixture: resultOriginState, paths: []string{"coverage.details.0.origin_kind"}, neverChecked: true},
		{name: "confirmed_structure.member", fixture: resultV1, paths: []string{"confirmed_structure.0.member"}},
		{name: "confirmed_structure.source", fixture: resultV1, paths: []string{"confirmed_structure.0.source"}},
		{name: "confirmed_structure.provenance", fixture: resultV1, paths: []string{"confirmed_structure.0.provenance"}},
		{name: "confirmed_structure.disposition", fixture: resultV1, paths: []string{"confirmed_structure.0.disposition"}},
		{name: "structure_offer_snapshot.member", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.StructureOfferSnapshot = []ContextFabricStructureOfferSnapshotEntry{{Member: ContextFabricStructureNeedKind(v), OfferID: "offer_1", OfferSource: ContextFabricStructureOfferEngine}}
		}},
		{name: "structure_offer_snapshot.offer_source", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.StructureOfferSnapshot = []ContextFabricStructureOfferSnapshotEntry{{Member: ContextFabricStructureNeedWindow, OfferID: "offer_1", OfferSource: ContextFabricStructureOfferSource(v)}}
		}},
		{name: "structure_needs.missing", fixture: resultV2, paths: []string{"structure_needs.missing.0"}},
		{name: "structure_needs.anchor_options.kind", fixture: resultV2, paths: []string{"structure_needs.anchor_options.0.kind"}},
		{name: "structure_needs.anchor_options.offer_source", fixture: resultV2, paths: []string{"structure_needs.anchor_options.0.offer_source"}},
		{name: "structure_needs.window_options.relative_id", fixture: resultV2, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.StructureNeeds.WindowOptions = []ContextFabricWindowOption{{ReceiptID: "winr_added_0001", OptionID: "opt_added_window", Label: "added", RelativeID: ContextFabricRelativeWindowID(v)}}
		}},
		{name: "structure_needs.window_expand_options.window_class", fixture: resultV2, mutate: func(r *ContextFabricInvestigationResult, v string) {
			withWindowExpand(r, func(o *ContextFabricWindowExpandOption) { o.WindowClass = ContextFabricWindowClass(v) })
		}},
		{name: "structure_needs.window_expand_options.candidate_kind", fixture: resultV2, mutate: func(r *ContextFabricInvestigationResult, v string) {
			withWindowExpand(r, func(o *ContextFabricWindowExpandOption) { o.CandidateKind = ContextFabricSubjectKind(v) })
		}},
		{name: "window_clarification.options.relative_id", fixture: resultV2, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.WindowClarification = &ContextFabricWindowClarification{Options: []ContextFabricWindowOption{{ReceiptID: "winr_added_0002", OptionID: "opt_added_window2", Label: "added", RelativeID: ContextFabricRelativeWindowID(v)}}}
		}},
		{name: "semantic_reading.status", fixture: resultV2, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.SemanticReading = &ContextFabricSemanticReading{Status: ContextFabricSemanticReadingStatus(v), Reason: ContextFabricSemanticReadingStateAbsent}
		}},
		{name: "semantic_reading.reason", fixture: resultV2, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.SemanticReading = &ContextFabricSemanticReading{Status: ContextFabricSemanticReadingUnavailable, Reason: ContextFabricSemanticReadingReason(v)}
		}},
		{name: "effective_evidence_window.relative_id", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.EffectiveEvidenceWindow = &ContextFabricEffectiveEvidenceWindow{RelativeID: ContextFabricRelativeWindowID(v), Provenance: ContextFabricWindowInferredDefault}
		}},
		{name: "effective_evidence_window.provenance", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.EffectiveEvidenceWindow = &ContextFabricEffectiveEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime, Provenance: ContextFabricWindowProvenance(v)}
		}},
		{name: "effective_evidence_window.window_class", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.EffectiveEvidenceWindow = &ContextFabricEffectiveEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime, Provenance: ContextFabricWindowInferredDefault, WindowClass: ContextFabricWindowClass(v)}
		}},
		{name: "effective_evidence_window.confidence", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.EffectiveEvidenceWindow = &ContextFabricEffectiveEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime, Provenance: ContextFabricWindowInferredDefault, Confidence: ContextFabricWindowConfidence(v)}
		}},
		{name: "completeness.terminal_reason", fixture: resultHistorical, paths: []string{"completeness.terminal_reason"}},
		{name: "refusal_basis", fixture: resultUnsupported, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.RefusalBasis = ContextFabricRefusalBasis(v)
			r.Completeness.RefusalBasis = ContextFabricRefusalBasis(v)
		}},
		{name: "completeness.outcomes.stage", fixture: resultStatusCorrect, paths: []string{"completeness.outcomes.0.stage"}},
		{name: "completeness.outcomes.impact", fixture: resultStatusCorrect, paths: []string{"completeness.outcomes.0.impact"}},
		{name: "completeness.outcomes.cause_coverage", fixture: resultStatusCorrect, paths: []string{"completeness.outcomes.0.cause_coverage"}},
		{name: "completeness.outcomes.outcome", fixture: resultStatusCorrect, paths: []string{
			"completeness.outcomes.0.outcome", "completeness.status_correction.deciding_outcome", "status_correction.deciding_outcome",
		}},
		{name: "completeness.outcomes.obligation", fixture: resultStatusCorrect, mutate: func(r *ContextFabricInvestigationResult, v string) {
			requirement := v + strings.TrimPrefix(addedOutcomeRequired, "evidence")
			r.Completeness.Outcomes[0].Obligation = v
			r.Completeness.Outcomes[0].Requirement = requirement
			r.StatusCorrection.DecidingRequirement = requirement
			r.Completeness.StatusCorrection.DecidingRequirement = requirement
		}},
		{name: "status_correction.direction", fixture: resultStatusCorrect, paths: []string{"completeness.status_correction.direction", "status_correction.direction"}},
		{name: "status_correction.replaced_status", fixture: resultStatusCorrect, paths: []string{"completeness.status_correction.replaced_status", "status_correction.replaced_status"}},
		{name: "fact_scope_census.requirement_kind", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.FactScopeCensus = []ContextFabricFactScopeCensusRecord{{RequirementKind: v}}
		}},
		{name: "fact_scope_census.origin_kind", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.FactScopeCensus = []ContextFabricFactScopeCensusRecord{{OriginKind: v}}
		}},
		{name: "answer_plan.family", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.Family = ContextFabricQuestionFamily(v)
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.family_source", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.FamilySource = ContextFabricQuestionFamilySource(v)
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.member_kind", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.MemberKind = ContextFabricSubjectKind(v)
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.fact_kinds", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.FactKinds = []ContextFabricFactKind{ContextFabricFactKind(v)}
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.axes", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.Axes = []ContextFabricStructureNeedKind{ContextFabricStructureNeedKind(v)}
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.narrowing.stage", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.Narrowing[0].Stage = ContextFabricPlanNarrowingStage(v)
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.narrowing.basis", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.Narrowing[0].Basis = ContextFabricNarrowingBasis(v)
			r.AnswerPlan = &plan
		}},
		{name: "answer_plan.narrowing.overrun", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			plan := addedVocabularyPlan()
			plan.Narrowing[0].Overrun = ContextFabricBudgetOverrun(v)
			r.AnswerPlan = &plan
		}},
	}
}

func addedVocabularyPlan() ContextFabricAnswerPlan {
	return ContextFabricAnswerPlan{
		Family:        ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "v1",
		Budget:        ContextFabricAnswerPlanBudget{MaxItems: 30, MaxSerializedBytes: 1 << 20},
		Narrowing: []ContextFabricPlanNarrowing{{
			Stage: ContextFabricPlanNarrowingAssembledResult, Basis: ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 50, After: 10, Overrun: ContextFabricBudgetOverrunItems,
		}},
	}
}

func withWindowExpand(r *ContextFabricInvestigationResult, set func(*ContextFabricWindowExpandOption)) {
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	option := ContextFabricWindowOption{ReceiptID: "winr_added_0003", OptionID: "opt_added_window3", Label: "last 90 days", RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start, End: &end}
	expand := ContextFabricWindowExpandOption{ReceiptID: option.ReceiptID, OptionID: option.OptionID, Label: option.Label, RelativeID: option.RelativeID}
	set(&expand)
	r.StructureNeeds.WindowOptions = []ContextFabricWindowOption{option}
	r.StructureNeeds.WindowExpandOptions = []ContextFabricWindowExpandOption{expand}
}

func TestStoredResultAcceptsAddedVocabularyValues(t *testing.T) {
	for _, row := range storedAdditiveRows() {
		t.Run(row.name, func(t *testing.T) {
			if err := ValidateStoredResult(row.build(t, addedVocabularyValue)); err != nil {
				t.Fatalf("a stored result read by a client rejects an added value: %v", err)
			}
			if row.neverChecked {
				return
			}
			if err := ValidateStoredResult(row.build(t, "has space")); err == nil {
				t.Fatal("a stored result accepts a value that is not token-shaped")
			}
		})
	}
}

func TestStoredResultKeepsFrozenVocabulariesClosed(t *testing.T) {
	for _, row := range []storedVocabularyRow{
		{name: "status", fixture: resultV1, paths: []string{"status", "completeness.terminal_status"}},
		{name: "completeness.state", fixture: resultStatusCorrect, paths: []string{"completeness.state"}},
		{name: "cohort.members.outcome", fixture: resultV1, paths: []string{"cohort.members.0.outcome"}},
		{name: "claimed_facts.table.shape", fixture: "context_fabric_investigation_result_render_shapes.v1.json", paths: []string{"claimed_facts.0.table.shape"}},
		{name: "render_shapes.kind", fixture: "context_fabric_investigation_result_render_shapes.v1.json", paths: []string{"render_shapes.0.kind"}},
		{name: "versions.synthesis_source", fixture: resultV1, mutate: func(r *ContextFabricInvestigationResult, v string) {
			r.Versions.SynthesisSource = ContextFabricSynthesisSource(v)
		}},
		{name: "interpretation.time_context.axis", fixture: resultV1, paths: []string{"interpretation.time_context.axis"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := ValidateStoredResult(row.build(t, addedVocabularyValue)); err == nil {
				t.Fatal("a frozen vocabulary accepts an unknown value on the stored read")
			}
		})
	}
}

func TestResultWritePathKeepsAdditiveVocabulariesClosed(t *testing.T) {
	byName := map[string]storedVocabularyRow{}
	for _, row := range storedAdditiveRows() {
		byName[row.name] = row
	}
	for _, name := range []string{
		"claimed_facts.kind", "interpretation.fact_requirements.kind", "drivers.category",
		"paths.edges.type", "coverage.sources.state",
		"subject_resolution.committed.kind", "completeness.outcomes.outcome", "answer_plan.family",
		"refusal_basis", "structure_needs.missing",
	} {
		row, ok := byName[name]
		if !ok {
			t.Fatalf("no row %q", name)
		}
		t.Run(name, func(t *testing.T) {
			r := row.build(t, addedVocabularyValue)
			validate := r.Validate
			if r.SchemaVersion == ContextFabricInvestigationResultSchemaV2 {
				validate = r.ValidateV2
			}
			if err := validate(); err == nil {
				t.Fatal("the write path accepts an unknown value in an additive vocabulary")
			}
		})
	}
}

func TestCoverageDetailWritePathKeepsAddedCodesClosed(t *testing.T) {
	r := loadFixture[ContextFabricInvestigationResult](t, resultOriginState)
	detail := r.Coverage.Details[0]
	if err := detail.Validate(); err != nil {
		t.Fatalf("the fixture detail must be valid on the write path: %v", err)
	}
	detail.Code = ContextFabricCoverageDetailCode(addedVocabularyValue)
	if err := detail.Validate(); err == nil {
		t.Fatal("the write path accepts an unknown coverage detail code")
	}
}

func TestStoredResultFixturesStillValidateOnTheWritePath(t *testing.T) {
	for _, fixture := range []string{resultV1, resultStatusCorrect, resultHistorical, resultUnsupported} {
		r := loadFixture[ContextFabricInvestigationResult](t, fixture)
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: %v", fixture, err)
		}
	}
	r := loadFixture[ContextFabricInvestigationResult](t, resultV2)
	if err := r.ValidateV2(); err != nil {
		t.Fatalf("%s: %v", resultV2, err)
	}
}

func TestPlanRequirementReadAcceptsAddedVocabularyValues(t *testing.T) {
	base := func() ContextFabricPlanRequirement {
		kinds := ContextFabricFactKindVocabulary()
		return maximalPlanRequirementAt("evidence", "subject", ContextFabricSubjectProject, []ContextFabricFactKind{kinds[0]})
	}
	coordinate := func(r *ContextFabricPlanRequirement) {
		r.Requirement = r.Obligation + "/" + r.Role + "/" + string(r.Subject)
	}
	read := func(r *ContextFabricPlanRequirement) {
		r.Step, r.StepExecution, r.InputClass, r.InputFactKinds = "", "", "", nil
		r.Kind = contextFabricObligationKindRead
		r.FactKinds = []ContextFabricFactKind{ContextFabricFactKindVocabulary()[0]}
	}
	unavailable := func(r *ContextFabricPlanRequirement) {
		r.Step, r.StepExecution, r.InputClass, r.InputFactKinds = "", "", "", nil
		r.Quantifier = contextFabricCompletionQuantifierNone
		r.Unavailable = "no_declaring_producer"
	}
	for _, tc := range []struct {
		name string
		set  func(r *ContextFabricPlanRequirement, v string)
	}{
		{"obligation", func(r *ContextFabricPlanRequirement, v string) { r.Obligation = v; coordinate(r) }},
		{"role", func(r *ContextFabricPlanRequirement, v string) { r.Role = v; coordinate(r) }},
		{"subject", func(r *ContextFabricPlanRequirement, v string) {
			r.Subject = ContextFabricSubjectKind(v)
			coordinate(r)
		}},
		{"kind", func(r *ContextFabricPlanRequirement, v string) { r.Kind = v }},
		{"step", func(r *ContextFabricPlanRequirement, v string) { r.Step = v }},
		{"step_execution", func(r *ContextFabricPlanRequirement, v string) { r.StepExecution = v }},
		{"input_class", func(r *ContextFabricPlanRequirement, v string) { r.InputClass = v }},
		{"input_fact_kinds", func(r *ContextFabricPlanRequirement, v string) {
			r.InputFactKinds = []ContextFabricFactKind{ContextFabricFactKind(v)}
		}},
		{"scope", func(r *ContextFabricPlanRequirement, v string) { r.Scope = v }},
		{"quantifier", func(r *ContextFabricPlanRequirement, v string) { r.Quantifier = v }},
		{"fact_kinds", func(r *ContextFabricPlanRequirement, v string) {
			read(r)
			r.FactKinds = []ContextFabricFactKind{ContextFabricFactKind(v)}
		}},
		{"unavailable", func(r *ContextFabricPlanRequirement, v string) { unavailable(r); r.Unavailable = v }},
		{"unavailable.quantifier", func(r *ContextFabricPlanRequirement, v string) { unavailable(r); r.Quantifier = v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			known := base()
			if err := validatePlanRequirements([]ContextFabricPlanRequirement{known}, contextFabricWriteBounds); err != nil {
				t.Fatalf("the base row must be valid on the write path: %v", err)
			}
			row := base()
			tc.set(&row, addedVocabularyValue)
			if err := validatePlanRequirements([]ContextFabricPlanRequirement{row}, contextFabricLegacyBounds); err != nil {
				t.Fatalf("a read rejects an added value: %v", err)
			}
			if err := ValidateContextFabricPlanRequirements([]ContextFabricPlanRequirement{row}); err == nil {
				t.Fatal("the write path accepts an added value")
			}
		})
	}
}
