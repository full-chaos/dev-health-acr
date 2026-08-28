package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
		{name: "context_fabric_investigation_result_historical.v1.json", target: &ContextFabricInvestigationResult{}},
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

// TestContextFabricDriverJudgmentValidateRejectsUnrecognizedCategory is the
// Go-level half of the H4 fix (CHAOS-3755 adversarial review): Category is
// a closed contract enum now, so an unrecognized value is rejected at
// ContextFabricDriverJudgment.Validate() directly, independent of any
// canonical-fact-claim reasoning layered on top in internal/contextfabric.
func TestContextFabricDriverJudgmentValidateRejectsUnrecognizedCategory(t *testing.T) {
	t.Parallel()
	project := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	driver := ContextFabricDriverJudgment{
		DriverID: "driver_12345678", Standing: ContextFabricDriverWithheld, Category: "not_a_real_category",
		Title: "Title", Summary: "Summary", AffectedSubjects: []ContextFabricSubjectRef{project},
		Derivation: ContextFabricDerivationRuleInferred, EpistemicStatus: ContextFabricEpistemicInferred,
		Confidence: 0.5, Qualification: "withheld", Current: true,
	}
	if err := driver.Validate(); err == nil || !strings.Contains(err.Error(), "driver judgment violates v1 bounds") {
		t.Fatalf("Validate() error = %v, want an unrecognized category to be rejected", err)
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

// TestContextFabricRelationshipTypeRejectsUnknownValueLoudly is
// AC-3779-1/AC-3779-3's binding test (CHAOS-3779, closing drift item D9 /
// the H4 lesson): both ContextFabricRelationshipProjection.Type and
// ContextFabricRelationshipEdge.Type must reject an unrecognized value
// with the NAMED ErrContextFabricUnknownRelationshipType error -- loudly,
// via errors.Is, not folded into a generic bounds-violation string a
// caller cannot distinguish from any other malformed field. Before
// CHAOS-3779 both fields were free strings of 1 to 128 bytes: a typo would
// have been accepted silently, exactly the H4 failure mode.
func TestContextFabricRelationshipTypeRejectsUnknownValueLoudly(t *testing.T) {
	t.Parallel()

	batch := validContextFabricProjectionBatch()
	from := ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_from", Label: "From"}
	to := ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_to", Label: "To"}
	projection := ContextFabricRelationshipProjection{
		RelationshipID: "relationship_12345678", Type: "NOT_A_REAL_TYPE", From: from, To: to,
		Derivation: ContextFabricDerivationCanonicalStructured, EpistemicStatus: ContextFabricEpistemicObserved,
		Authorization: ContextFabricAuthorizationScope{ProjectIDs: []string{"project_ask_dev"}}, EvidenceRefIDs: []string{"evidence_12345678"},
		ObservedAt: time.Now().UTC(), SourceVersion: "source-v1",
	}
	if err := projection.Validate(); !errors.Is(err, ErrContextFabricUnknownRelationshipType) {
		t.Fatalf("ContextFabricRelationshipProjection.Validate() error = %v, want errors.Is(err, ErrContextFabricUnknownRelationshipType)", err)
	}
	batch.Relationships = []ContextFabricRelationshipProjection{projection}
	if err := batch.Validate(); !errors.Is(err, ErrContextFabricUnknownRelationshipType) {
		t.Fatalf("ContextFabricProjectionBatch.Validate() with an unknown relationship type error = %v, want errors.Is(err, ErrContextFabricUnknownRelationshipType) (the unknown type must not be silently dropped from the batch)", err)
	}

	edge := ContextFabricRelationshipEdge{
		Type: "NOT_A_REAL_TYPE", From: from, To: to,
		Derivation: ContextFabricDerivationGraphAssociated, EpistemicStatus: ContextFabricEpistemicInferred,
		EvidenceRefIDs: []string{"evidence_12345678"},
	}
	if err := edge.Validate(); !errors.Is(err, ErrContextFabricUnknownRelationshipType) {
		t.Fatalf("ContextFabricRelationshipEdge.Validate() error = %v, want errors.Is(err, ErrContextFabricUnknownRelationshipType)", err)
	}

	// Every closed-vocabulary member -- the pre-existing six, plus
	// CHAOS-3779's BLOCKS/PART_OF/RELATES_TO/DUPLICATES, plus CHAOS-3802's
	// BELONGS_TO_PROJECT/OWNED_BY_TEAM -- must validate cleanly on both
	// fields. A closed enum that silently rejects a real member is as much
	// an H4-class defect as one that silently admits a fake one.
	members := []ContextFabricRelationshipType{
		ContextFabricRelationshipBelongsToRepository, ContextFabricRelationshipBelongsToPullRequest,
		ContextFabricRelationshipCorrelatedWithIncident, ContextFabricRelationshipRelatedTo,
		ContextFabricRelationshipDocumentedBy, ContextFabricRelationshipHasEpisode,
		ContextFabricRelationshipBlocks, ContextFabricRelationshipPartOf,
		ContextFabricRelationshipRelatesTo, ContextFabricRelationshipDuplicates,
		ContextFabricRelationshipBelongsToProject, ContextFabricRelationshipOwnedByTeam,
	}
	for _, member := range members {
		projection.Type = member
		if err := projection.Validate(); err != nil {
			t.Fatalf("ContextFabricRelationshipProjection.Validate() with Type=%q error = %v, want a closed-vocabulary member to validate", member, err)
		}
		edge.Type = member
		if err := edge.Validate(); err != nil {
			t.Fatalf("ContextFabricRelationshipEdge.Validate() with Type=%q error = %v, want a closed-vocabulary member to validate", member, err)
		}
	}
}

// TestContextFabricAuthorizationScopeRejectsSeparatorCharacter guards the v1
// port against a scope value that would corrupt a backend's delimited-string
// encoding (e.g. zepgraph's "|a|b|" scope encoding used '|' as its
// separator, before its CHAOS-3771 deletion): such a value must be rejected
// here, before any backend ever sees it.
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
		Type:           "RELATED_TO",
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
// adapter encoded with the same delimited-string scheme, before its
// CHAOS-3771 deletion.
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
// names: the zepgraph adapter's encodeScope used '|' as its delimited-
// string separator (before its CHAOS-3771 deletion), so a '|'-bearing
// alias/previous-name would corrupt that encoding and be silently dropped
// rather than stored, rather than failing loudly at the port.
func TestContextFabricEntityProjectionRejectsSeparatorCharacterInAliases(t *testing.T) {
	t.Parallel()
	for _, mutate := range []struct {
		name string
		fn   func(*ContextFabricEntityProjection)
	}{
		{"aliases", func(e *ContextFabricEntityProjection) { e.Aliases = []string{"Ask|Dev"} }},
		{"previous_names", func(e *ContextFabricEntityProjection) { e.PreviousNames = []string{"Old|Name"} }},
		// CHAOS-3884: provider_aliases follows the identical discipline.
		{"provider_aliases", func(e *ContextFabricEntityProjection) { e.ProviderAliases = []string{"github:Ask|Dev"} }},
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
		{"entity_provider_alias", func(b *ContextFabricProjectionBatch) { b.Entities[0].ProviderAliases = []string{"github:A|B"} }},
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
		Versions:            ContextFabricVersionSet{ServiceVersion: "acr-v1", ContractVersion: ContextFabricInvestigationResultSchema, Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1"},
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

// TestContextFabricResultSurvivesJSONRoundTripRevalidation is the probe for
// the Coverage.Validate relaxation that landed with the store-side
// validate-on-read work (CHAOS-3755 finding M2). Coverage.DegradedReasons
// is `omitempty` in Go and is NOT in the Coverage schema's required set
// (only sources and partial are), so a legitimately EMPTY, non-nil slice
// serializes to an omitted field and decodes back as nil. A validator that
// demanded non-nil there would reject the service's own valid output the
// moment anything re-read it -- which is exactly what
// InvestigationResultStore.Get now does on every read.
//
// This asserts the round trip, not the relaxed condition in isolation:
// re-tightening Coverage.Validate would make this fail, whereas a test
// that only built a nil-DegradedReasons value by hand would not prove the
// scenario that motivated the change.
func TestContextFabricResultSurvivesJSONRoundTripRevalidation(t *testing.T) {
	t.Parallel()
	original := validContextFabricContractResult()
	if err := original.Validate(); err != nil {
		t.Fatalf("fixture Validate() error = %v, want the fixture itself to be valid", err)
	}
	if original.Coverage.DegradedReasons == nil {
		t.Fatal("fixture Coverage.DegradedReasons is nil, want a non-nil empty slice so the round trip is meaningful")
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded ContextFabricInvestigationResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.Coverage.DegradedReasons != nil {
		t.Fatalf("Coverage.DegradedReasons = %#v after round trip, want nil -- omitempty should have dropped the empty slice, which is the whole point of this probe", decoded.Coverage.DegradedReasons)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("Validate() after JSON round trip error = %v, want a re-decoded valid result to stay valid", err)
	}
}

// TestContextFabricCoverageDegradedReasonsBounds pins BOTH directions of
// the Coverage.DegradedReasons relaxation, so the exact line a reviewer
// will challenge carries its own evidence.
//
// Direction 1 (why the relaxation is correct): degraded_reasons is
// `omitempty` in Go and is NOT in the Coverage schema's required set --
// only sources and partial are -- so absent and empty are both legal, and
// the nil a decode produces from an omitted field must validate. Demanding
// non-nil rejected the service's own valid output on every re-read.
//
// Direction 2 (what the relaxation did NOT give away): every other bound
// on the field still holds. Relaxing nil is not relaxing the field.
func TestContextFabricCoverageDegradedReasonsBounds(t *testing.T) {
	t.Parallel()
	tooMany := make([]string, 251)
	for i := range tooMany {
		tooMany[i] = "reason-" + strconv.Itoa(i)
	}
	cases := []struct {
		name    string
		reasons []string
		wantErr bool
	}{
		{"nil is accepted -- an omitted optional field decodes to this", nil, false},
		{"empty is accepted -- the in-Go form that serializes to omitted", []string{}, false},
		{"populated is accepted", []string{"clickhouse: degraded"}, false},
		{"over the 250 bound is still rejected", tooMany, true},
		{"duplicates are still rejected", []string{"same", "same"}, true},
		{"an over-long reason is still rejected", []string{strings.Repeat("x", 2001)}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			coverage := ContextFabricCoverage{Sources: []ContextFabricSourceObservation{}, DegradedReasons: testCase.reasons}
			err := coverage.Validate()
			if testCase.wantErr && err == nil {
				t.Fatal("Validate() error = nil, want the bound to still be enforced")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("Validate() error = %v, want accepted", err)
			}
		})
	}
}

// TestContextFabricCoverageSourcesStillRequiresNonNil is the companion
// guard: sources IS in the schema's required set, so relaxing
// degraded_reasons must not have relaxed it by association.
func TestContextFabricCoverageSourcesStillRequiresNonNil(t *testing.T) {
	t.Parallel()
	coverage := ContextFabricCoverage{Sources: nil, DegradedReasons: []string{}}
	if err := coverage.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want nil sources to remain invalid -- it is a required field")
	}
}

// TestContextFabricSourceStatePrunedIsAcceptedAndParityHolds guards the
// CHAOS-3783 enum widening from both sides. The Go validator must accept
// "pruned" (or the fact planner's coverage entries never survive
// InvestigationResult.Validate), and the JSON Schema enum must list exactly
// the same closed set (or a payload the Go side accepts is rejected on the
// wire, which is the parity break contract-first exists to prevent). The
// schema side is read from the file rather than restated here so a value
// added to only one of the two cannot pass.
func TestContextFabricSourceStatePrunedIsAcceptedAndParityHolds(t *testing.T) {
	t.Parallel()

	goStates := []ContextFabricSourceState{
		ContextFabricSourceAvailable, ContextFabricSourceStale, ContextFabricSourceUnavailable,
		ContextFabricSourceUnconfigured, ContextFabricSourceUnauthorized, ContextFabricSourceNoData,
		ContextFabricSourceTruncated, ContextFabricSourceConflicted, ContextFabricSourceNotApplicable,
		ContextFabricSourcePruned,
	}
	// Every non-available state already has to carry a reason -- the
	// contract's own form of the empty-states rule. pruned is no exception,
	// which is exactly what CHAOS-3783 needs: a pruned source can never be
	// an unexplained absence.
	for _, state := range goStates {
		coverage := ContextFabricCoverage{
			Sources:         []ContextFabricSourceObservation{{Source: "canonical_fact:workload", State: state, Reason: "reason"}},
			DegradedReasons: []string{},
		}
		if err := coverage.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want source state %q accepted", err, state)
		}
	}
	reasonless := ContextFabricCoverage{
		Sources:         []ContextFabricSourceObservation{{Source: "canonical_fact:workload", State: ContextFabricSourcePruned}},
		DegradedReasons: []string{},
	}
	if err := reasonless.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a reasonless pruned source rejected -- a prune must always be explainable")
	}

	invalid := ContextFabricCoverage{
		Sources:         []ContextFabricSourceObservation{{Source: "canonical_fact:workload", State: "skipped", Reason: "reason"}},
		DegradedReasons: []string{},
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an unknown source state to stay rejected -- the enum is closed")
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	want := make(map[string]struct{}, len(goStates))
	for _, state := range goStates {
		want[string(state)] = struct{}{}
	}
	enums := collectSourceStateEnums(schema)
	if len(enums) == 0 {
		t.Fatal("found no source-state enum in the schema -- the parity check would silently pass")
	}
	// Exact set equality, asserted in BOTH directions, plus explicit
	// duplicate rejection (codex round-7 F1).
	//
	// Checking length plus "every schema value is Go-known" is not parity: it
	// passes for a schema that DUPLICATES one value and drops another. Swap
	// "pruned" for a second copy of "available" and the count still matches
	// and every value is still Go-known -- while the schema silently no
	// longer admits the planner's own state, so JSON Schema validation
	// rejects real coverage output at runtime. Set equality both ways is what
	// closes it; the duplicate check is what keeps the SET comparison from
	// hiding a multiset difference.
	for _, enum := range enums {
		seen := make(map[string]struct{}, len(enum))
		for _, value := range enum {
			if _, duplicate := seen[value]; duplicate {
				t.Fatalf("schema source-state enum repeats %q -- a duplicate can mask a missing value from any count-based check", value)
			}
			seen[value] = struct{}{}
			if _, ok := want[value]; !ok {
				t.Fatalf("schema source-state enum has %q, which Go's validSourceState does not accept", value)
			}
		}
		// The direction the old assertion never covered: every Go-accepted
		// state must actually appear in the schema.
		for state := range want {
			if _, present := seen[state]; !present {
				t.Fatalf("Go accepts source state %q but the schema enum %v omits it -- the wire would reject a payload the Go side produces", state, enum)
			}
		}
	}
}

// collectSourceStateEnums walks the schema for every enum that looks like the
// source-state vocabulary (identified by two values only it carries together
// -- "not_applicable" alone also appears in CohortMember.outcome, so pruned
// is required too), so the parity assertion covers ALL copies of it --
// SourceObservation defines it once and Coverage inlines it again, and an
// edit to only one is exactly the drift this guards.
func collectSourceStateEnums(node any) [][]string {
	var found [][]string
	switch typed := node.(type) {
	case map[string]any:
		if rawEnum, ok := typed["enum"].([]any); ok {
			values := make([]string, 0, len(rawEnum))
			hasNotApplicable := false
			hasPruned := false
			for _, item := range rawEnum {
				value, ok := item.(string)
				if !ok {
					values = nil
					break
				}
				if value == "not_applicable" {
					hasNotApplicable = true
				}
				if value == "pruned" {
					hasPruned = true
				}
				values = append(values, value)
			}
			if hasNotApplicable && hasPruned && values != nil {
				found = append(found, values)
			}
		}
		for _, child := range typed {
			found = append(found, collectSourceStateEnums(child)...)
		}
	case []any:
		for _, child := range typed {
			found = append(found, collectSourceStateEnums(child)...)
		}
	}
	return found
}
