package panelharness

import (
	"encoding/base64"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// testBearerToken returns a shape-valid (auth.IsTokenShapeValid), but
// otherwise fake, bearer token for tests -- NewClient now rejects any
// value that doesn't have the real ACR credential shape (codex round 1,
// HIGH), so a plain placeholder string like "test-token" no longer works
// as a test fixture. discriminator lets different tests use visibly
// distinct (but still valid-shaped) tokens where a test needs to prove two
// panelists used DIFFERENT credentials.
func testBearerToken(discriminator byte) string {
	secret := make([]byte, 32)
	secret[0] = discriminator
	return auth.TokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
}

// minimalValidResult returns a contractsv1.ContextFabricInvestigationResult
// that satisfies ValidateStored() -- every test server in this package
// builds its response by copying this base and overriding only the fields
// the test actually cares about (StructureNeeds, ConfirmedStructure,
// Status, etc.), rather than a bare struct literal that ValidateStored
// would reject as a malformed "successful" response (codex round 1,
// MEDIUM: client.go now calls ValidateStored on every decoded result, so a
// test fixture that used to be an under-populated struct literal must be a
// genuinely valid one instead).
func minimalValidResult(resultID, requestID string) contractsv1.ContextFabricInvestigationResult {
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      resultID, RequestID: requestID,
		GeneratedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		Question:    "Was Ask Dev ready to ship?",
		Status:      contractsv1.ContextFabricInvestigationComplete,
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeSingleSubject, RequestedJudgment: "release_readiness",
			TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		},
		SubjectResolution:   contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{}},
		DirectJudgment:      "direct-judgment-placeholder", // required whenever Status is complete/partial (ValidateStored's own "answer-capable result" rule)
		DeterministicAnswer: "deterministic-answer-placeholder",
		StrongestPressures:  []string{},
		Drivers:             []contractsv1.ContextFabricDriverJudgment{},
		RemainingWork:       []contractsv1.ContextFabricFinding{},
		ReadinessGaps:       []contractsv1.ContextFabricFinding{},
		Paths:               []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:           []contractsv1.ContextFabricFinding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contractsv1.ContextFabricClaimedFact{},
		Warnings:            []string{},
		Coverage:            contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
	}
}
