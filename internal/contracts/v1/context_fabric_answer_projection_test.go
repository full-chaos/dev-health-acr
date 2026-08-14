package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func loadSchemaDocument(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", name))
	if err != nil {
		t.Fatalf("read schema %s: %v", name, err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode schema %s: %v", name, err)
	}
	return document
}

// schemaEnumAt walks a schema document by key path and returns the enum
// array it finds there.
func schemaEnumAt(t *testing.T, document map[string]any, path ...string) []string {
	t.Helper()
	var node any = document
	for _, key := range path {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", path, key)
		}
		node, ok = object[key]
		if !ok {
			t.Fatalf("path %v: %q is missing", path, key)
		}
	}
	object, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("path %v does not resolve to an object", path)
	}
	raw, ok := object["enum"].([]any)
	if !ok {
		t.Fatalf("path %v has no enum array", path)
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			t.Fatalf("path %v: enum member %v is not a string", path, item)
		}
		values = append(values, value)
	}
	return values
}

// TestAnswerProjectionVocabulariesMatchTheCanonicalOnes is the anti-drift
// mechanism for the answer projection contract.
//
// context_fabric_answer_projection.v1 is deliberately a self-contained
// schema rather than a composition of context_fabric_common.v1's $defs (see
// ContextFabricAnswerProjectionSchema's doc comment for why). That buys a
// small, agent-appropriate document and costs one risk: the projection's
// copy of a closed vocabulary could drift from the canonical one, and a
// consumer would then be told a value is legal on one surface and illegal
// on another.
//
// This test closes that risk by comparing the projected enums directly
// against the canonical documents -- same members, same order -- and by
// proving every member is still one the Go validators accept. A new
// vocabulary member added to the canonical contract without being added
// here fails, rather than silently narrowing what a bounded consumer may
// receive.
func TestAnswerProjectionVocabulariesMatchTheCanonicalOnes(t *testing.T) {
	projection := loadSchemaDocument(t, "context_fabric_answer_projection.v1.schema.json")
	common := loadSchemaDocument(t, "context_fabric_common.v1.schema.json")
	result := loadSchemaDocument(t, "context_fabric_investigation_result.v1.schema.json")

	cases := []struct {
		name       string
		projected  []string
		canonical  []string
		acceptedBy func(string) bool
	}{
		{
			name:       "subject_kind",
			projected:  schemaEnumAt(t, projection, "$defs", "SubjectRef", "properties", "kind"),
			canonical:  schemaEnumAt(t, common, "$defs", "SubjectRef", "properties", "kind"),
			acceptedBy: func(v string) bool { return validContextFabricSubjectKind(ContextFabricSubjectKind(v)) },
		},
		{
			name:       "investigation_status",
			projected:  schemaEnumAt(t, projection, "properties", "status"),
			canonical:  schemaEnumAt(t, result, "properties", "status"),
			acceptedBy: func(v string) bool { return validInvestigationStatus(ContextFabricInvestigationStatus(v)) },
		},
		{
			name:       "resolution_state",
			projected:  schemaEnumAt(t, projection, "$defs", "ProjectedCandidate", "properties", "state"),
			canonical:  schemaEnumAt(t, common, "$defs", "SubjectCandidate", "properties", "state"),
			acceptedBy: func(v string) bool { return validResolutionState(ContextFabricResolutionState(v)) },
		},
		{
			name:       "driver_standing",
			projected:  schemaEnumAt(t, projection, "$defs", "ProjectedDriver", "properties", "standing"),
			canonical:  schemaEnumAt(t, common, "$defs", "DriverJudgment", "properties", "standing"),
			acceptedBy: func(v string) bool { return validDriverStanding(ContextFabricDriverStanding(v)) },
		},
		{
			name:       "driver_category",
			projected:  schemaEnumAt(t, projection, "$defs", "ProjectedDriver", "properties", "category"),
			canonical:  schemaEnumAt(t, common, "$defs", "DriverJudgment", "properties", "category"),
			acceptedBy: func(v string) bool { return validDriverCategory(ContextFabricDriverCategory(v)) },
		},
		{
			name:       "fact_kind",
			projected:  schemaEnumAt(t, projection, "$defs", "ProjectedFact", "properties", "kind"),
			canonical:  schemaEnumAt(t, common, "$defs", "ClaimedFact", "properties", "kind"),
			acceptedBy: func(v string) bool { return validFactKind(ContextFabricFactKind(v)) },
		},
		{
			name:       "source_state",
			projected:  schemaEnumAt(t, projection, "$defs", "ProjectedCoverage", "properties", "state"),
			canonical:  schemaEnumAt(t, common, "$defs", "SourceObservation", "properties", "state"),
			acceptedBy: func(v string) bool { return validSourceState(ContextFabricSourceState(v)) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.projected, tc.canonical) {
				t.Fatalf("projected vocabulary drifted from canonical:\n projected = %v\n canonical = %v", tc.projected, tc.canonical)
			}
			for _, value := range tc.projected {
				if !tc.acceptedBy(value) {
					t.Errorf("Go validation rejects %q, which both schemas admit", value)
				}
			}
		})
	}
}

func validAnswerProjection() ContextFabricAnswerProjection {
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	value := "amber"
	return ContextFabricAnswerProjection{
		SchemaVersion:      ContextFabricAnswerProjectionSchema,
		ResultID:           "result_12345678",
		RequestID:          "request_12345678",
		GeneratedAt:        time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		Status:             ContextFabricInvestigationComplete,
		Question:           "What is the actual status of Ask Dev?",
		DirectJudgment:     "Ask Dev is not release-ready.",
		CurrentState:       "Required work remains open.",
		StrongestPressures: []string{"open blockers"},
		CommittedSubjects:  []ContextFabricSubjectRef{project},
		PrincipalDrivers: []ContextFabricProjectedDriver{{
			DriverID: "driver_12345678", Standing: ContextFabricDriverPrincipal, Category: "status",
			Title: "Status is amber", Summary: "Status remains amber.", Confidence: 0.9,
			EvidenceRefIDs: []string{"evidence_status_01"}, ClaimedFactIDs: []string{"claim_status_001"},
		}},
		KeyFacts: []ContextFabricProjectedFact{{
			ClaimID: "claim_status_001", Kind: ContextFabricFactStatus, Subject: project,
			Field: "status", Value: ContextFabricScalarValue{String: &value},
		}},
		CoverageSummary: []ContextFabricProjectedCoverage{{Source: "work_items", State: ContextFabricSourceAvailable}},
		CoveragePartial: false,
		Limitations:     []string{},
		Warnings:        []string{},
		EvidenceRefIDs:  []string{"evidence_status_01"},
		SubjectReceipts: []ContextFabricBoundSubjectReceipt{{ResultID: "result_12345678", ReceiptID: "receipt_12345678"}},
		Versions: ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: ContextFabricAnswerProjectionSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		ProjectionBudget: ContextFabricProjectionBudget{},
	}
}

// TestAnswerProjectionValidateMatchesSchemaBounds pins the Go validator to
// the published schema in both directions: a Go-valid projection must pass
// the schema, and each mutation the Go validator rejects must be one the
// schema would reject too.
func TestAnswerProjectionValidateMatchesSchemaBounds(t *testing.T) {
	base := validAnswerProjection()
	if err := base.Validate(); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
	assertSchemaParity(t, "context_fabric_answer_projection.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*ContextFabricAnswerProjection)
	}{
		{"schema_version", func(p *ContextFabricAnswerProjection) { p.SchemaVersion = "context_fabric_answer_projection.v2" }},
		{"result_id", func(p *ContextFabricAnswerProjection) { p.ResultID = "short" }},
		{"status", func(p *ContextFabricAnswerProjection) { p.Status = "invented" }},
		{"driver_standing", func(p *ContextFabricAnswerProjection) { p.PrincipalDrivers[0].Standing = "invented" }},
		{"driver_category", func(p *ContextFabricAnswerProjection) { p.PrincipalDrivers[0].Category = "invented" }},
		{"coverage_state", func(p *ContextFabricAnswerProjection) { p.CoverageSummary[0].State = "invented" }},
		{"fact_kind", func(p *ContextFabricAnswerProjection) { p.KeyFacts[0].Kind = "invented" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := validAnswerProjection()
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted a schema-invalid projection")
			}
		})
	}
}

// TestAnswerProjectionRejectsUnresolvableClaimReference proves the
// document-level invariant that makes value-level evidence usable: a driver
// may not cite a claim the projection does not carry, because the consumer
// that received it could not then check the claim.
func TestAnswerProjectionRejectsUnresolvableClaimReference(t *testing.T) {
	value := validAnswerProjection()
	value.PrincipalDrivers[0].ClaimedFactIDs = []string{"claim_not_carried"}
	if err := value.Validate(); err == nil {
		t.Fatal("validator accepted a driver citing a claim the projection dropped")
	}
}

// TestAnswerProjectionRejectsForeignSubjectReceipt proves a continuation
// handle can never point at another result. A caller that echoed one back
// would bind its next turn to a subject this answer never resolved.
func TestAnswerProjectionRejectsForeignSubjectReceipt(t *testing.T) {
	value := validAnswerProjection()
	value.SubjectReceipts[0].ResultID = "result_87654321"
	if err := value.Validate(); err == nil {
		t.Fatal("validator accepted a receipt bound to a different result")
	}
}

// TestProjectionBudgetTruncationIsIfAndOnlyIf covers both failure
// directions of the honesty rule.
func TestProjectionBudgetTruncationIsIfAndOnlyIf(t *testing.T) {
	silent := validAnswerProjection()
	silent.ProjectionBudget = ContextFabricProjectionBudget{DriversOmitted: 2}
	if err := silent.Validate(); err == nil {
		t.Error("validator accepted a silent truncation")
	}

	overclaimed := validAnswerProjection()
	overclaimed.ProjectionBudget = ContextFabricProjectionBudget{Truncated: true}
	if err := overclaimed.Validate(); err == nil {
		t.Error("validator accepted truncation that never happened")
	}

	honest := validAnswerProjection()
	honest.ProjectionBudget = ContextFabricProjectionBudget{Truncated: true, DriversOmitted: 2}
	if err := honest.Validate(); err != nil {
		t.Errorf("validator rejected an honestly declared truncation: %v", err)
	}
}

// TestOptionalEvidenceRefsSurviveJSONRoundTripRevalidation is the probe for
// a validator/schema mismatch found while wiring the CHAOS-3746 answer
// surface.
//
// SubjectCandidate.EvidenceRefIDs and CohortMember.EvidenceRefIDs are
// OPTIONAL in the JSON Schema (absent from each shape's required list) and
// carry `omitempty` in Go. A legitimately empty, non-nil slice therefore
// serializes to an omitted field and decodes back as nil. The Go validator
// used to demand non-nil for both, so the service's own valid output failed
// revalidation the moment anything re-read it -- and
// InvestigationResultStore.Get revalidates on EVERY read, which means a
// stored result carrying a candidate or cohort member with no evidence
// references could not be loaded back at all.
//
// This is the same class of defect already recorded for
// Coverage.DegradedReasons (CHAOS-3755 finding M2), reached through two
// different fields.
func TestOptionalEvidenceRefsSurviveJSONRoundTripRevalidation(t *testing.T) {
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	team := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_a", Label: "Team A"}

	candidate := ContextFabricSubjectCandidate{
		ReceiptID: "receipt_12345678", Subject: project, State: ContextFabricResolutionCommitted,
		MatchReasons: []string{"exact label"}, Confidence: 1, EvidenceRefIDs: []string{},
	}
	member := ContextFabricCohortMember{
		Subject: team, Rank: 1, InclusionReasons: []string{"highest load"}, EvidenceRefIDs: []string{},
	}
	for name, value := range map[string]any{"subject_candidate": candidate, "cohort_member": member} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			switch value.(type) {
			case ContextFabricSubjectCandidate:
				var back ContextFabricSubjectCandidate
				if err := json.Unmarshal(encoded, &back); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if back.EvidenceRefIDs != nil {
					t.Fatalf("expected the omitted field to decode as nil, got %v", back.EvidenceRefIDs)
				}
				if err := back.Validate(); err != nil {
					t.Errorf("valid candidate rejected after a JSON round trip: %v", err)
				}
			case ContextFabricCohortMember:
				var back ContextFabricCohortMember
				if err := json.Unmarshal(encoded, &back); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if back.EvidenceRefIDs != nil {
					t.Fatalf("expected the omitted field to decode as nil, got %v", back.EvidenceRefIDs)
				}
				if err := back.Validate(); err != nil {
					t.Errorf("valid cohort member rejected after a JSON round trip: %v", err)
				}
			}
		})
	}
}

// TestRequiredEvidenceRefsStillRejectNil guards the other side of that fix:
// relaxing the OPTIONAL fields must not have relaxed the required ones,
// where a missing evidence list really is invalid.
func TestRequiredEvidenceRefsStillRejectNil(t *testing.T) {
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	driver := ContextFabricDriverJudgment{
		DriverID: "driver_12345678", Standing: ContextFabricDriverPrincipal, Category: "narrative",
		Title: "Title", Summary: "Summary.", AffectedSubjects: []ContextFabricSubjectRef{project},
		EvidenceRefIDs: nil, Derivation: ContextFabricDerivationRuleInferred,
		EpistemicStatus: ContextFabricEpistemicInferred, Confidence: 0.5, Current: true,
	}
	if err := driver.Validate(); err == nil {
		t.Error("driver with nil required evidence references was accepted")
	}
	finding := ContextFabricFinding{
		FindingID: "finding_12345678", Kind: "narrative", Summary: "Summary.", EvidenceRefIDs: nil,
	}
	if err := finding.Validate(); err == nil {
		t.Error("finding with nil required evidence references was accepted")
	}
}

// TestAnswerProjectionReusedShapesMatchTheCanonicalOnes closes the
// structural half of the self-contained-schema tradeoff.
//
// TestAnswerProjectionVocabulariesMatchTheCanonicalOnes guards the closed
// ENUMS. This guards every whole SHAPE the projection reuses rather than
// narrows. The Go DTO uses the canonical Go types for those, so the schema
// copies must stay byte-identical or the schema would describe something
// the Go type cannot produce.
//
// It ENUMERATES the shapes rather than listing them: any $defs name present
// in BOTH documents is, by definition, one the projection reuses, so a
// future reused shape is covered without anyone remembering to add it here.
// The first version of this test hardcoded three names and silently missed
// BoundSubjectReceipt (codex round-1 F8) -- a sampled list is exactly the
// wrong instrument for a drift gate.
//
// Its own reason to exist is a real drift: CHAOS-3782 widened the canonical
// VersionSet with model_identity and the projection's copy kept the older
// shape. The Go side was automatically correct (same type), so only the
// published schema was wrong -- the quietest possible failure.
func TestAnswerProjectionReusedShapesMatchTheCanonicalOnes(t *testing.T) {
	projection := loadSchemaDocument(t, "context_fabric_answer_projection.v1.schema.json")
	common := loadSchemaDocument(t, "context_fabric_common.v1.schema.json")

	projectionDefs, ok := projection["$defs"].(map[string]any)
	if !ok {
		t.Fatal("projection schema has no $defs")
	}
	commonDefs, ok := common["$defs"].(map[string]any)
	if !ok {
		t.Fatal("common schema has no $defs")
	}

	shared := make([]string, 0, len(projectionDefs))
	for name := range projectionDefs {
		if _, reused := commonDefs[name]; reused {
			shared = append(shared, name)
		}
	}
	sort.Strings(shared)

	// The intersection is PINNED, not merely non-empty (codex round-2 F5).
	// Enumerating alone still false-passes on a rename: the shape simply
	// vanishes from the intersection and nothing notices it stopped being
	// checked. Pinning makes both directions fail loudly -- a rename
	// shrinks the set, a newly reused shape grows it -- and each forces a
	// deliberate update rather than silent drift.
	expected := []string{"BoundSubjectReceipt", "ScalarValue", "SubjectRef", "VersionSet"}
	if !reflect.DeepEqual(shared, expected) {
		t.Fatalf("the set of reused shapes changed: got %v, pinned %v.\nA shape that vanished was renamed or stopped being reused; a new one must be added deliberately.", shared, expected)
	}
	t.Logf("verifying %d reused shapes: %v", len(shared), shared)
	for _, name := range shared {
		t.Run(name, func(t *testing.T) {
			if !reflect.DeepEqual(projectionDefs[name], commonDefs[name]) {
				t.Errorf("projected %s has drifted from the canonical definition:\n projected = %v\n canonical = %v",
					name, projectionDefs[name], commonDefs[name])
			}
		})
	}
}
