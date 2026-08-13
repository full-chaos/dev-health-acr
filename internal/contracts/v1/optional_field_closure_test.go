package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This file closes a defect CLASS rather than another instance of it.
//
// Three separate fields have now been found where a Go field carries
// `omitempty`, the JSON Schema does not mark it required, and the Go
// validator nevertheless demanded a non-nil value:
//
//   - Coverage.DegradedReasons        (CHAOS-3755 finding M2)
//   - SubjectCandidate.EvidenceRefIDs (CHAOS-3746)
//   - CohortMember.EvidenceRefIDs     (CHAOS-3746)
//
// The shape is always the same. An empty-but-non-nil slice serializes to an
// OMITTED field, decodes back as nil, and the validator then rejects the
// service's own valid output. It stays hidden until something re-reads a
// document -- which InvestigationResultStore.Get does on every read -- and
// until a stored document happens to carry an empty optional list.
//
// Point-fixing a fourth instance would leave a fifth. These tests instead
// walk the real contract documents and prove the property for EVERY
// optional collection field, at every depth, in both the nil form and the
// empty-value round-trip form.

// pathStep is one hop through a document: into a struct field, or into a
// slice element.
type pathStep struct {
	field   int
	index   int
	isIndex bool
}

type fieldPath struct {
	steps []pathStep
	label string
}

func (p fieldPath) String() string { return p.label }

func (p fieldPath) with(step pathStep, label string) fieldPath {
	steps := make([]pathStep, len(p.steps), len(p.steps)+1)
	copy(steps, p.steps)
	return fieldPath{steps: append(steps, step), label: label}
}

// resolve navigates a path to the settable value it names.
func (p fieldPath) resolve(root reflect.Value) (reflect.Value, bool) {
	value := root
	for _, step := range p.steps {
		for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
			if value.IsNil() {
				return reflect.Value{}, false
			}
			value = value.Elem()
		}
		if step.isIndex {
			if value.Kind() != reflect.Slice || step.index >= value.Len() {
				return reflect.Value{}, false
			}
			value = value.Index(step.index)
			continue
		}
		if value.Kind() != reflect.Struct || step.field >= value.NumField() {
			return reflect.Value{}, false
		}
		value = value.Field(step.field)
	}
	return value, value.CanSet()
}

// omitemptyCollectionPaths finds every slice or map field reachable from
// root that carries `omitempty`.
//
// Only slices and maps are considered, because those are where the defect
// lives: Go distinguishes nil from empty, JSON does not. A scalar with
// `omitempty` decodes back to the same zero value it encoded from, so it
// cannot exhibit this failure.
func omitemptyCollectionPaths(root any) []fieldPath {
	var found []fieldPath
	var walk func(value reflect.Value, path fieldPath)
	walk = func(value reflect.Value, path fieldPath) {
		switch value.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !value.IsNil() {
				walk(value.Elem(), path)
			}
		case reflect.Struct:
			// time.Time is a struct with unexported state; walking into
			// it finds nothing useful and its fields are not contract
			// fields.
			if value.Type() == reflect.TypeOf(time.Time{}) {
				return
			}
			for i := 0; i < value.NumField(); i++ {
				field := value.Type().Field(i)
				if !field.IsExported() {
					continue
				}
				tag := field.Tag.Get("json")
				name := strings.Split(tag, ",")[0]
				if name == "-" {
					continue
				}
				child := path.with(pathStep{field: i}, path.label+"."+name)
				kind := field.Type.Kind()
				if strings.Contains(tag, ",omitempty") && (kind == reflect.Slice || kind == reflect.Map) {
					found = append(found, child)
				}
				walk(value.Field(i), child)
			}
		case reflect.Slice:
			for i := 0; i < value.Len(); i++ {
				walk(value.Index(i), path.with(pathStep{index: i, isIndex: true}, fmt.Sprintf("%s[%d]", path.label, i)))
			}
		}
	}
	walk(reflect.ValueOf(root), fieldPath{})
	return found
}

type validatable interface{ Validate() error }

// closureDocuments are the contract documents this property is proven over:
// the ones that get persisted, re-read, and revalidated, which is exactly
// where this defect class does its damage.
func closureDocuments() map[string]func() validatable {
	return map[string]func() validatable{
		"context_fabric_investigation_result.v1": func() validatable {
			value := closureResult()
			return &value
		},
		"context_fabric_answer_projection.v1": func() validatable {
			value := closureProjection()
			return &value
		},
		"context_fabric_investigation_request.v1": func() validatable {
			value := closureRequest()
			return &value
		},
	}
}

// TestOptionalCollectionsAcceptTheNilForm proves every `omitempty` slice or
// map in these documents may legitimately be nil.
//
// A field omitted on the wire arrives as nil. A validator that rejects nil
// there is asserting a requirement the wire format cannot express, so the
// document could never survive a round trip.
func TestOptionalCollectionsAcceptTheNilForm(t *testing.T) {
	for name, build := range closureDocuments() {
		t.Run(name, func(t *testing.T) {
			base := build()
			if err := base.Validate(); err != nil {
				t.Fatalf("closure fixture is not valid to begin with: %v", err)
			}
			paths := omitemptyCollectionPaths(base)
			if len(paths) == 0 {
				t.Fatal("found no omitempty collection fields; the walker is not working")
			}
			t.Logf("checked %d optional collection fields", len(paths))
			for _, path := range paths {
				if reason := exemptOptionalField(path.String()); reason != "" {
					t.Logf("skipped %s: %s", path, reason)
					continue
				}
				document := build()
				target, ok := path.resolve(reflect.ValueOf(document).Elem())
				if !ok {
					continue
				}
				target.Set(reflect.Zero(target.Type()))
				if err := document.Validate(); err != nil {
					t.Errorf("%s rejects the nil (omitted) form: %v", path, err)
				}
			}
		})
	}
}

// TestOptionalCollectionsSurviveAnEmptyValueRoundTrip is the production
// shape of the same property: a service builds a document with an empty
// optional list, it is stored as JSON, and something reads it back and
// revalidates it.
func TestOptionalCollectionsSurviveAnEmptyValueRoundTrip(t *testing.T) {
	for name, build := range closureDocuments() {
		t.Run(name, func(t *testing.T) {
			for _, path := range omitemptyCollectionPaths(build()) {
				if exemptOptionalField(path.String()) != "" {
					continue
				}
				document := build()
				target, ok := path.resolve(reflect.ValueOf(document).Elem())
				if !ok {
					continue
				}
				switch target.Kind() {
				case reflect.Slice:
					target.Set(reflect.MakeSlice(target.Type(), 0, 0))
				case reflect.Map:
					target.Set(reflect.MakeMap(target.Type()))
				default:
					continue
				}
				encoded, err := json.Marshal(document)
				if err != nil {
					t.Fatalf("%s: marshal: %v", path, err)
				}
				decoded := build()
				if err := json.Unmarshal(encoded, decoded); err != nil {
					t.Fatalf("%s: unmarshal: %v", path, err)
				}
				if err := decoded.Validate(); err != nil {
					t.Errorf("%s fails revalidation after an empty-value round trip: %v", path, err)
				}
			}
		})
	}
}

// exemptOptionalField names optional collections whose emptiness is
// constrained by another contract rule, so rejecting the empty form is
// correct rather than an instance of this defect class.
//
// Every entry states its reason. An unexplained exemption would let a real
// instance hide behind this list, defeating the point of closing the class.
// An empty return value means "not exempt".
func exemptOptionalField(path string) string {
	switch {
	case strings.HasSuffix(path, ".claimed_fact_ids"):
		// A driver or finding in a canonical-fact-shaped category MUST
		// cite a claimed fact (ContextFabricDriverCategoryRequiresClaimedFact).
		// Emptiness is conditionally illegal by design, and the
		// closure rules already have dedicated tests.
		return "conditionally required by the driver category closure rule"
	default:
		return ""
	}
}

func closureResult() ContextFabricInvestigationResult {
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	team := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	amber := "amber"
	return ContextFabricInvestigationResult{
		SchemaVersion: ContextFabricInvestigationResultSchema,
		ResultID:      "result_closure_001", RequestID: "request_closure_01",
		GeneratedAt: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		Status:      ContextFabricInvestigationComplete,
		Question:    "What is the status of Ask Dev?",
		Interpretation: ContextFabricInterpretedQuestion{
			Shape: ContextFabricShapeSingleSubject, RequestedJudgment: "status",
			SubjectTerms: []string{"Ask Dev"}, ComparisonTerms: []string{"last quarter"},
			TimeContext: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
			FactRequirements: []ContextFabricFactRequirement{{
				Kind: ContextFabricFactStatus, Subjects: []ContextFabricSubjectRef{project},
				Parameters: map[string]string{"window": "30d"},
			}},
		},
		SubjectResolution: ContextFabricSubjectResolution{
			Candidates: []ContextFabricSubjectCandidate{{
				ReceiptID: "receipt_closure_01", Subject: project, State: ContextFabricResolutionCommitted,
				MatchedTerms: []string{"Ask Dev"}, MatchReasons: []string{"exact label"},
				Confidence: 1, EvidenceRefIDs: []string{"evidence_identity_01"},
			}},
			Committed: []ContextFabricSubjectRef{project},
		},
		Cohort: &ContextFabricCohort{
			Kind: ContextFabricSubjectTeam,
			Members: []ContextFabricCohortMember{{
				Subject: team, Rank: 1, InclusionReasons: []string{"highest load"},
				EvidenceRefIDs: []string{"evidence_cohort_001"},
			}},
			Exclusions: []ContextFabricCohortExclusion{{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_b", Label: "Team B"}, Reason: "below the load threshold"}},
			Rationale:  "Teams carrying the most open work.", Complete: true,
		},
		DirectJudgment: "Ask Dev is amber.", CurrentState: "Work remains open.",
		StrongestPressures: []string{"open blockers"},
		Drivers: []ContextFabricDriverJudgment{{
			DriverID: "driver_closure_001", Standing: ContextFabricDriverPrincipal, Category: "status",
			Title: "Status is amber", Summary: "Status remains amber.",
			AffectedSubjects: []ContextFabricSubjectRef{project},
			PathIDs:          []string{"path_closure_0001"},
			EvidenceRefIDs:   []string{"evidence_status_0001"},
			ClaimedFactIDs:   []string{"claim_status_00001"},
			Derivation:       ContextFabricDerivationCanonicalStructured,
			EpistemicStatus:  ContextFabricEpistemicObserved,
			Confidence:       0.9, Qualification: "Limited to current evidence.", Current: true,
		}},
		RemainingWork: []ContextFabricFinding{{
			FindingID: "finding_closure_01", Kind: "narrative", Summary: "Work remains.",
			Subjects: []ContextFabricSubjectRef{project}, EvidenceRefIDs: []string{"evidence_work_000001"},
		}},
		ReadinessGaps: []ContextFabricFinding{},
		Paths: []ContextFabricRelationshipPath{{
			PathID: "path_closure_0001",
			Nodes:  []ContextFabricSubjectRef{project, team},
			Edges: []ContextFabricRelationshipEdge{{
				Type: ContextFabricRelationshipRelatedTo, From: project, To: team,
				Derivation: ContextFabricDerivationCanonicalStructured, EpistemicStatus: ContextFabricEpistemicObserved,
				EvidenceRefIDs: []string{"evidence_edge_000001"},
			}},
			WhyRelevant: "Team owns the project.", EvidenceRefIDs: []string{"evidence_edge_000001"},
		}},
		Conflicts:      []ContextFabricFinding{},
		Limitations:    []string{"deployments unavailable"},
		EvidenceRefIDs: []string{"evidence_status_0001"},
		ClaimedFacts: []ContextFabricClaimedFact{{
			ClaimID: "claim_status_00001", Kind: ContextFabricFactStatus, Subject: project,
			Field: "status", Value: ContextFabricScalarValue{String: &amber},
		}},
		Coverage: ContextFabricCoverage{
			Sources: []ContextFabricSourceObservation{{
				Source: "work_items", State: ContextFabricSourceAvailable,
				Watermark: "2026-08-13", Reason: "",
			}},
			Partial: true, DegradedReasons: []string{"deployments unavailable"},
		},
		Versions: ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: ContextFabricInvestigationResultSchema, Backend: "graph",
			BackendVersion: "v1", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		DeterministicAnswer: "Ask Dev is amber because work remains open.",
		Warnings:            []string{"partial coverage"},
	}
}

func closureProjection() ContextFabricAnswerProjection {
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	team := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	amber := "amber"
	return ContextFabricAnswerProjection{
		SchemaVersion: ContextFabricAnswerProjectionSchema,
		ResultID:      "result_closure_001", RequestID: "request_closure_01",
		GeneratedAt:    time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		Status:         ContextFabricInvestigationClarificationRequired,
		Question:       "What is the status of Ask Dev?",
		DirectJudgment: "Ask Dev is amber.", CurrentState: "Work remains open.",
		StrongestPressures: []string{"open blockers"},
		CommittedSubjects:  []ContextFabricSubjectRef{project},
		Clarification: &ContextFabricProjectedClarification{
			Prompt: "Which Ask Dev did you mean?",
			Candidates: []ContextFabricProjectedCandidate{{
				ReceiptID: "receipt_closure_01", Subject: project, State: ContextFabricResolutionAmbiguous,
				Confidence: 0.5, MatchReasons: []string{"similar label"},
			}},
		},
		Cohort: &ContextFabricProjectedCohort{
			Kind: ContextFabricSubjectTeam, Total: 1, Rationale: "Teams carrying the most open work.",
			Complete: true,
			Members: []ContextFabricProjectedCohortMember{{
				Subject: team, Rank: 1, InclusionReasons: []string{"highest load"},
				EvidenceRefIDs: []string{"evidence_cohort_001"},
			}},
		},
		PrincipalDrivers: []ContextFabricProjectedDriver{{
			DriverID: "driver_closure_001", Standing: ContextFabricDriverPrincipal, Category: "status",
			Title: "Status is amber", Summary: "Status remains amber.", Qualification: "Limited.",
			Confidence: 0.9, EvidenceRefIDs: []string{"evidence_status_0001"},
			ClaimedFactIDs: []string{"claim_status_00001"},
		}},
		KeyFacts: []ContextFabricProjectedFact{{
			ClaimID: "claim_status_00001", Kind: ContextFabricFactStatus, Subject: project,
			Field: "status", Value: ContextFabricScalarValue{String: &amber},
		}},
		CoverageSummary: []ContextFabricProjectedCoverage{{
			Source: "deployments", State: ContextFabricSourceUnavailable, Reason: "not configured",
		}},
		CoveragePartial: true,
		Limitations:     []string{"deployments unavailable"},
		Warnings:        []string{"partial coverage"},
		EvidenceRefIDs:  []string{"evidence_status_0001"},
		SubjectReceipts: []ContextFabricBoundSubjectReceipt{{ResultID: "result_closure_001", ReceiptID: "receipt_closure_01"}},
		Versions: ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: ContextFabricAnswerProjectionSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		ProjectionBudget: ContextFabricProjectionBudget{},
	}
}

func closureRequest() ContextFabricInvestigationRequest {
	return ContextFabricInvestigationRequest{
		SchemaVersion: ContextFabricInvestigationRequestSchema,
		RequestID:     "request_closure_01",
		Question:      "What is the status of Ask Dev?",
		Conversation: []ContextFabricConversationTurn{{
			TurnID: "turn_closure_0001", Role: ContextFabricConversationUser,
			Content: "Tell me about Ask Dev.", CreatedAt: time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC),
		}},
		PriorSubjectReceipts: []ContextFabricBoundSubjectReceipt{{ResultID: "result_closure_001", ReceiptID: "receipt_closure_01"}},
		RequestedScope: ContextFabricRequestedScope{
			RepositorySlugs: []string{"owner/repository"},
			ProjectIDs:      []string{"project_ask_dev"},
			TeamIDs:         []string{"team_a"},
			SubjectHints:    []ContextFabricSubjectHint{{Kind: ContextFabricSubjectProject, ID: "project_ask_dev", Label: "Ask Dev", Source: "workbench"}},
		},
		TimeContext: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
		Options: ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: ContextFabricConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
}
