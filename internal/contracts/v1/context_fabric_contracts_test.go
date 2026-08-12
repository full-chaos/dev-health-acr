package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestContextFabricGoldenContractsDecodeAndValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target interface{ Validate() error }
	}{
		{name: "context_fabric_investigation_request.v1.json", target: &ContextFabricInvestigationRequest{}},
		{name: "context_fabric_investigation_result.v1.json", target: &ContextFabricInvestigationResult{}},
		{name: "context_fabric_projection_batch.v1.json", target: &ContextFabricProjectionBatch{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := contextFabricGolden(t, test.name)
			if err := decodeContextFabricStrict(encoded, test.target); err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			if err := test.target.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestContextFabricRequestStrictDecodeRejectsUnknownTrailingAndInvalidNull(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(validContextFabricContractRequest())
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(encoded, []byte(`"question":`), []byte(`"unknown":true,"question":`), 1)
	if err := decodeContextFabricStrict(unknown, &ContextFabricInvestigationRequest{}); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if err := decodeContextFabricStrict(append(encoded, []byte(` {}`)...), &ContextFabricInvestigationRequest{}); !errors.Is(err, errContextFabricTrailingJSON) {
		t.Fatalf("trailing JSON error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["question"] = nil
	nullQuestion, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var request ContextFabricInvestigationRequest
	if err := decodeContextFabricStrict(nullQuestion, &request); err != nil {
		t.Fatalf("null question should decode before semantic validation: %v", err)
	}
	if err := request.Validate(); err == nil {
		t.Fatal("invalid null question was accepted")
	}
}

func TestContextFabricResultRequiresEvidenceClosedDriver(t *testing.T) {
	t.Parallel()

	result := validContextFabricContractResult()
	result.Drivers = []ContextFabricDriverJudgment{{
		DriverID: "driver_12345678", Standing: ContextFabricDriverPrincipal, Category: "readiness", Title: "Readiness gap", Summary: "The product is not ready.",
		AffectedSubjects: []ContextFabricSubjectRef{{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}},
		PathIDs:          []string{}, EvidenceRefIDs: []string{}, Derivation: ContextFabricDerivationCanonicalStructured, EpistemicStatus: ContextFabricEpistemicObserved, Confidence: 0.9, Current: true,
	}}
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "evidence closure") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestContextFabricProjectionRequiresCompleteEnumerationAndScalarOneOf(t *testing.T) {
	t.Parallel()

	batch := validContextFabricProjectionBatch()
	batch.FullSnapshot = true
	batch.CompleteEnumeration = false
	if err := batch.Validate(); err == nil || !strings.Contains(err.Error(), "complete enumeration") {
		t.Fatalf("Validate() error = %v", err)
	}

	text := "value"
	boolean := true
	if err := (ContextFabricScalarValue{String: &text, Boolean: &boolean}).Validate(); err == nil {
		t.Fatal("scalar with multiple values was accepted")
	}
	if err := (ContextFabricScalarValue{}).Validate(); err == nil {
		t.Fatal("empty scalar was accepted")
	}
}

// TestContextFabricAuthorizationScopeRejectsSeparatorCharacter guards the v1
// port against a scope value that would corrupt a backend's delimited-string
// encoding (e.g. zepgraph's "|a|b|" scope encoding uses '|' as its
// separator): such a value must be rejected here, before any backend ever
// sees it.
func TestContextFabricAuthorizationScopeRejectsSeparatorCharacter(t *testing.T) {
	t.Parallel()
	for _, scope := range []ContextFabricAuthorizationScope{
		{RepositorySlugs: []string{"full-chaos/private|leak"}},
		{ProjectIDs: []string{"project_a|project_b"}},
		{TeamIDs: []string{"team_a|team_b"}},
	} {
		if err := scope.Validate(); err == nil {
			t.Fatalf("Validate() accepted a '|'-bearing scope value: %#v", scope)
		}
	}
	// A clean scope with the same shape must still be accepted.
	if err := (ContextFabricAuthorizationScope{RepositorySlugs: []string{"full-chaos/private"}}).Validate(); err != nil {
		t.Fatalf("Validate() rejected a clean scope: %v", err)
	}
}

// TestContextFabricReservedOrganizationScopeRejectedOutsideOrganizationSubject
// is CHAOS-3753 codex finding W2's contract-level regression test: only an
// entity/content/episode projection whose Subject.Kind is
// ContextFabricSubjectOrganization may emit a ProjectIDs value inside
// ContextFabricReservedOrganizationScopePrefix's namespace -- any other
// producer doing so (a real project ID from an upstream data source that
// happened to collide with the reserved prefix) must be rejected at the
// contract boundary, not merely by a doc-comment convention downstream
// producers might forget to check.
func TestContextFabricReservedOrganizationScopeRejectedOutsideOrganizationSubject(t *testing.T) {
	t.Parallel()
	reserved := ContextFabricReservedOrganizationScopePrefix + "org-1"

	// The one legitimate producer: an Organization-kind subject.
	batch := validContextFabricProjectionBatch()
	batch.Entities[0].Subject.Kind = ContextFabricSubjectOrganization
	batch.Entities[0].Subject.CanonicalID = "organization:org-1"
	batch.Entities[0].Authorization = ContextFabricAuthorizationScope{ProjectIDs: []string{reserved}}
	if err := batch.Validate(); err != nil {
		t.Fatalf("Validate() rejected the legitimate organization-scope producer: %v", err)
	}
	// The published JSON Schema must agree: it must not over-reject the one
	// legitimate producer just because it also rejects everyone else.
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	if err := contractcheck.ValidateSerialized("", "context_fabric_projection_batch.v1.schema.json", encoded); err != nil {
		t.Fatalf("JSON Schema rejected the legitimate organization-scope producer that Go accepts -- schema/Go validation drift: %v", err)
	}

	// Every other subject kind must be rejected.
	batch = validContextFabricProjectionBatch()
	batch.Entities[0].Subject.Kind = ContextFabricSubjectProject
	batch.Entities[0].Authorization = ContextFabricAuthorizationScope{ProjectIDs: []string{reserved}}
	if err := batch.Validate(); err == nil || !strings.Contains(err.Error(), "reserved organization-scope") {
		t.Fatalf("Validate() accepted a non-organization subject using the reserved scope namespace: %v", err)
	}

	// A relationship -- no single subject to exempt -- must always be
	// rejected, even though its From subject happens to be an organization.
	relationship := ContextFabricRelationshipProjection{
		RelationshipID: "relationship_reserved_probe",
		Type:           "BELONGS_TO",
		From:           ContextFabricSubjectRef{Kind: ContextFabricSubjectOrganization, CanonicalID: "organization:org-1", Label: "org-1"},
		To:             ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"},
		Derivation:     ContextFabricDerivationCanonicalStructured, EpistemicStatus: ContextFabricEpistemicObserved,
		Authorization:  ContextFabricAuthorizationScope{ProjectIDs: []string{reserved}},
		EvidenceRefIDs: []string{"evidence_12345678"}, ObservedAt: time.Now().UTC(), SourceVersion: "source-v1",
	}
	if err := relationship.Validate(); err == nil || !strings.Contains(err.Error(), "reserved organization-scope") {
		t.Fatalf("Validate() accepted a relationship using the reserved scope namespace: %v", err)
	}
}

// TestContextFabricBoundedEvidenceRefsRejectsSeparatorCharacter guards
// against the same corruption for evidence ref IDs, which the zepgraph
// adapter encodes with the same delimited-string scheme.
func TestContextFabricBoundedEvidenceRefsRejectsSeparatorCharacter(t *testing.T) {
	t.Parallel()
	batch := validContextFabricProjectionBatch()
	batch.Entities[0].EvidenceRefIDs = []string{"evidence_a|b_1234"}
	if err := batch.Validate(); err == nil {
		t.Fatal("Validate() accepted a '|'-bearing evidence ref ID")
	}
}

// TestContextFabricEntityProjectionRejectsSeparatorCharacterInAliases
// guards against the same corruption for entity aliases and previous
// names: the zepgraph adapter's encodeScope uses '|' as its delimited-
// string separator, so a '|'-bearing alias/previous-name would corrupt
// that encoding and be silently dropped rather than stored, rather than
// failing loudly at the port.
func TestContextFabricEntityProjectionRejectsSeparatorCharacterInAliases(t *testing.T) {
	t.Parallel()
	for _, mutate := range []struct {
		name string
		fn   func(*ContextFabricEntityProjection)
	}{
		{"aliases", func(e *ContextFabricEntityProjection) { e.Aliases = []string{"Ask|Dev"} }},
		{"previous_names", func(e *ContextFabricEntityProjection) { e.PreviousNames = []string{"Old|Name"} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			batch := validContextFabricProjectionBatch()
			mutate.fn(&batch.Entities[0])
			if err := batch.Validate(); err == nil {
				t.Fatalf("Validate() accepted a '|'-bearing %s value", mutate.name)
			}
		})
	}
}

// TestContextFabricSeparatorRejectionMatchesBetweenGoAndJSONSchema is the
// round-3 regression: the Go validators reject '|' in authorization scope
// values, evidence ref IDs, aliases, and previous names, but the published
// JSON Schema must reject the exact same values -- a schema-valid payload
// must never be a Go-invalid one. Each case marshals a batch that Go
// rejects and asserts the JSON Schema rejects the same serialized bytes.
func TestContextFabricSeparatorRejectionMatchesBetweenGoAndJSONSchema(t *testing.T) {
	t.Parallel()
	for _, mutate := range []struct {
		name string
		fn   func(*ContextFabricProjectionBatch)
	}{
		{"entity_alias", func(b *ContextFabricProjectionBatch) { b.Entities[0].Aliases = []string{"A|B"} }},
		{"entity_previous_name", func(b *ContextFabricProjectionBatch) { b.Entities[0].PreviousNames = []string{"A|B"} }},
		{"entity_authorization_repository_slug", func(b *ContextFabricProjectionBatch) {
			b.Entities[0].Authorization.RepositorySlugs = []string{"team-a/repo|team-b/repo"}
		}},
		{"entity_authorization_project_id", func(b *ContextFabricProjectionBatch) {
			b.Entities[0].Authorization = ContextFabricAuthorizationScope{ProjectIDs: []string{"project_a|project_b"}}
		}},
		{"entity_authorization_team_id", func(b *ContextFabricProjectionBatch) {
			b.Entities[0].Authorization = ContextFabricAuthorizationScope{TeamIDs: []string{"team_a|team_b"}}
		}},
		{"entity_evidence_ref_id", func(b *ContextFabricProjectionBatch) { b.Entities[0].EvidenceRefIDs = []string{"evidence_a|b_1234"} }},
		// validContextFabricProjectionBatch's one entity is a
		// ContextFabricSubjectProject, not an organization -- CHAOS-3753
		// codex finding W2: a non-organization subject must never be able to
		// emit a ProjectIDs value inside the reserved organization-scope
		// namespace, in Go or in the published schema.
		{"entity_authorization_reserved_organization_scope", func(b *ContextFabricProjectionBatch) {
			b.Entities[0].Authorization = ContextFabricAuthorizationScope{ProjectIDs: []string{ContextFabricReservedOrganizationScopePrefix + "org-1"}}
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			batch := validContextFabricProjectionBatch()
			mutate.fn(&batch)
			if err := batch.Validate(); err == nil {
				t.Fatalf("Go Validate() accepted a '|'-bearing %s value", mutate.name)
			}
			encoded, err := json.Marshal(batch)
			if err != nil {
				t.Fatalf("marshal batch: %v", err)
			}
			if err := contractcheck.ValidateSerialized("", "context_fabric_projection_batch.v1.schema.json", encoded); err == nil {
				t.Fatalf("JSON Schema accepted a '|'-bearing %s value that Go rejects -- schema/Go validation drift", mutate.name)
			}
		})
	}
}

// TestContextFabricProjectionBatchRejectsDuplicateEntitySubject guards
// against a batch projecting the same subject twice: a backend that upserts
// entities by subject key would silently apply only the last record (its
// aliases, authorization scope, evidence) while a receipt still reports
// every entity as applied.
func TestContextFabricProjectionBatchRejectsDuplicateEntitySubject(t *testing.T) {
	t.Parallel()
	batch := validContextFabricProjectionBatch()
	subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_dup", Label: "Duplicate"}
	batch.Entities = []ContextFabricEntityProjection{
		{
			Subject: subject, Aliases: []string{"FirstAlias"},
			Authorization:  ContextFabricAuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
			EvidenceRefIDs: []string{"evidence_first_1234"}, ObservedAt: batch.GeneratedAt, SourceVersion: "ops-v1",
		},
		{
			Subject: subject, Aliases: []string{"SecondAlias"},
			Authorization:  ContextFabricAuthorizationScope{RepositorySlugs: []string{"team-b/repo"}},
			EvidenceRefIDs: []string{"evidence_second_1234"}, ObservedAt: batch.GeneratedAt, SourceVersion: "ops-v1",
		},
	}
	if err := batch.Validate(); err == nil {
		t.Fatal("Validate() accepted a batch projecting the same subject twice")
	}
}

// TestContextFabricProjectionBatchRejectsDuplicateRelationshipID guards
// against a batch reusing the same RelationshipID: a backend that upserts
// edges by relationship ID would silently overwrite the earlier edge's
// target/authorization/evidence with the later one's, orphaning the
// earlier target.
func TestContextFabricProjectionBatchRejectsDuplicateRelationshipID(t *testing.T) {
	t.Parallel()
	batch := validContextFabricProjectionBatch()
	batch.Entities = []ContextFabricEntityProjection{}
	from := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a", Label: "A"}
	to1 := ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_b", Label: "B"}
	to2 := ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_c", Label: "C"}
	batch.Relationships = []ContextFabricRelationshipProjection{
		{
			RelationshipID: "relationship_dup1", Type: "BLOCKS", From: from, To: to1,
			Derivation: ContextFabricDerivationCanonicalStructured, EpistemicStatus: ContextFabricEpistemicObserved,
			Authorization:  ContextFabricAuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
			EvidenceRefIDs: []string{"evidence_one_1234"}, ObservedAt: batch.GeneratedAt, SourceVersion: "ops-v1",
		},
		{
			RelationshipID: "relationship_dup1", Type: "BLOCKS", From: from, To: to2,
			Derivation: ContextFabricDerivationCanonicalStructured, EpistemicStatus: ContextFabricEpistemicObserved,
			Authorization:  ContextFabricAuthorizationScope{RepositorySlugs: []string{"team-b/repo"}},
			EvidenceRefIDs: []string{"evidence_two_1234"}, ObservedAt: batch.GeneratedAt, SourceVersion: "ops-v1",
		},
	}
	if err := batch.Validate(); err == nil {
		t.Fatal("Validate() accepted a batch reusing the same relationship ID")
	}
}

func contextFabricGolden(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "examples", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

var errContextFabricTrailingJSON = errors.New("trailing JSON")

func decodeContextFabricStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errContextFabricTrailingJSON
		}
		return err
	}
	return nil
}

func validContextFabricContractRequest() ContextFabricInvestigationRequest {
	return ContextFabricInvestigationRequest{
		SchemaVersion: ContextFabricInvestigationRequestSchema,
		RequestID:     "request_12345678",
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		TimeContext:   ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
		Options: ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10,
			MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: ContextFabricConsumerInfo{Name: "workbench", Version: "0.1.0", Surface: "workbench"},
	}
}

func validContextFabricContractResult() ContextFabricInvestigationResult {
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return ContextFabricInvestigationResult{
		SchemaVersion: ContextFabricInvestigationResultSchema,
		ResultID:      "result_12345678",
		RequestID:     "request_12345678",
		GeneratedAt:   time.Now().UTC(),
		Status:        ContextFabricInvestigationComplete,
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		Interpretation: ContextFabricInterpretedQuestion{
			Shape: ContextFabricShapeOpen, RequestedJudgment: "status_and_drivers", TimeContext: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
			FactRequirements: []ContextFabricFactRequirement{{Kind: ContextFabricFactStatus}},
		},
		SubjectResolution:   ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{}, Committed: []ContextFabricSubjectRef{project}},
		DirectJudgment:      "Ask Dev is not release-ready.",
		CurrentState:        "Required work remains.",
		StrongestPressures:  []string{},
		Drivers:             []ContextFabricDriverJudgment{},
		RemainingWork:       []ContextFabricFinding{},
		ReadinessGaps:       []ContextFabricFinding{},
		Paths:               []ContextFabricRelationshipPath{},
		Conflicts:           []ContextFabricFinding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []ContextFabricClaimedFact{},
		Coverage:            ContextFabricCoverage{Sources: []ContextFabricSourceObservation{}, DegradedReasons: []string{}},
		Versions:            ContextFabricVersionSet{ServiceVersion: "acr-v1", ContractVersion: ContextFabricInvestigationResultSchema, Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1"},
		DeterministicAnswer: "Ask Dev is not release-ready because required work remains.",
		Warnings:            []string{},
	}
}

func validContextFabricProjectionBatch() ContextFabricProjectionBatch {
	text := "linear"
	return ContextFabricProjectionBatch{
		SchemaVersion: ContextFabricProjectionBatchSchema, BatchID: "batch_12345678", OrgID: "org_1", Source: "dev-health-ops", SourceVersion: "source-v1", Cursor: "cursor_1", NextCursor: "cursor_2", GeneratedAt: time.Now().UTC(),
		Entities: []ContextFabricEntityProjection{{
			Subject:    ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"},
			Properties: map[string]ContextFabricScalarValue{"provider": {String: &text}}, Authorization: ContextFabricAuthorizationScope{ProjectIDs: []string{"project_ask_dev"}}, EvidenceRefIDs: []string{"evidence_12345678"}, ObservedAt: time.Now().UTC(), SourceVersion: "source-v1",
		}},
		Relationships: []ContextFabricRelationshipProjection{}, Contents: []ContextFabricContentProjection{}, Episodes: []ContextFabricEpisodeProjection{}, Tombstones: []ContextFabricProjectionTombstone{},
	}
}
