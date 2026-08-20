package memoryinvestigation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestStore_parity runs the shared contextfabric.InvestigationResultStore
// behavior table (save/get roundtrip, org scoping, immutability) against
// the in-memory store. pginvestigation's integration test runs the exact
// same table against Postgres, so the two implementations cannot silently
// drift apart.
func TestStore_parity(t *testing.T) {
	paritytest.RunSuite(t,
		func(t *testing.T) contextfabric.InvestigationResultStore { return memoryinvestigation.NewStore() },
		func(err error) bool { return errors.Is(err, memoryinvestigation.ErrNotFound) },
	)
}

func TestStore_getDefensiveCopyDoesNotLeakStoredState(t *testing.T) {
	ctx := context.Background()
	store := memoryinvestigation.NewStore()
	principal := storage.Principal{OrgID: "org-1"}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-defensive-copy", Label: "Defensive Copy"}
	original := contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      "result-defensive-copy",
		RequestID:     "request-defensive-copy",
		GeneratedAt:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Status:        contextfabric.InvestigationComplete,
		Question:      "original question",
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		SubjectResolution:   contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
		DirectJudgment:      "original judgment",
		DeterministicAnswer: "original deterministic answer",
		StrongestPressures:  []string{"pressure-1"},
		Drivers:             []contextfabric.DriverJudgment{},
		RemainingWork:       []contextfabric.Finding{},
		ReadinessGaps:       []contextfabric.Finding{},
		Paths:               []contextfabric.RelationshipPath{},
		Conflicts:           []contextfabric.Finding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}},
		Versions: contextfabric.VersionSet{
			ServiceVersion: "test", ContractVersion: contextfabric.InvestigationResultSchemaV1, Backend: "test",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
		},
		Warnings: []string{},
	}
	if err := store.Save(ctx, principal, original, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0); err != nil {
		t.Fatalf("save: %v", err)
	}

	first, err := store.Get(ctx, principal, original.ResultID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	first.Result.StrongestPressures[0] = "mutated"
	first.Result.Question = "mutated question"

	second, err := store.Get(ctx, principal, original.ResultID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if second.Result.Question != "original question" {
		t.Fatalf("stored question mutated: got %q", second.Result.Question)
	}
	if second.Result.StrongestPressures[0] != "pressure-1" {
		t.Fatalf("stored strongest_pressures mutated: got %q", second.Result.StrongestPressures[0])
	}
}

func TestStore_saveRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := memoryinvestigation.NewStore()
	err := store.Save(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationResult{ResultID: "result-cancelled"}, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("save: want context.Canceled, got %v", err)
	}
}

// resultWithConfirmedStructure mirrors pginvestigation's own test helper of
// the same name (its doc comment covers the fixture contract): a FULLY
// VALID InvestigationResult additionally carrying one applied,
// receipt-sourced ConfirmedStructure entry for member.
func resultWithConfirmedStructure(resultID string, member contextfabric.StructureNeedKind, priorResultID, receiptID string) contextfabric.InvestigationResult {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-" + resultID, Label: "Project " + resultID}
	return contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      resultID,
		RequestID:     "request-" + resultID,
		GeneratedAt:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Status:        contextfabric.InvestigationComplete,
		Question:      "question for " + resultID,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		SubjectResolution:   contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
		DirectJudgment:      "judgment for " + resultID,
		DeterministicAnswer: "deterministic answer for " + resultID,
		StrongestPressures:  []string{},
		Drivers:             []contextfabric.DriverJudgment{},
		RemainingWork:       []contextfabric.Finding{},
		ReadinessGaps:       []contextfabric.Finding{},
		Paths:               []contextfabric.RelationshipPath{},
		Conflicts:           []contextfabric.Finding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}},
		Versions: contextfabric.VersionSet{
			ServiceVersion: "test", ContractVersion: contextfabric.InvestigationResultSchemaV1, Backend: "test",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
		},
		Warnings: []string{},
		ConfirmedStructure: []contextfabric.ConfirmedStructureEntry{
			{
				Member: member, AppliedValue: "pull_request", Source: "receipt",
				PriorResultID: priorResultID, ReceiptID: receiptID,
				Provenance: "clarification_confirmed", Disposition: "applied",
			},
		},
	}
}

// TestStore_structureSupersessionClaims mirrors pginvestigation's own
// TestStore_structureSupersessionClaims exactly (CHAOS-3927 P4) -- the two
// stores must agree, byte for byte in outcome, on the SAME race: two
// Saves redeeming the identical (org, prior_result_id, member) tuple under
// different result_ids, the second must lose atomically.
func TestStore_structureSupersessionClaims(t *testing.T) {
	ctx := context.Background()
	store := memoryinvestigation.NewStore()
	principal := storage.Principal{OrgID: "org-supersession"}
	priorResultID := "result-prior-structure-offer-001"
	const member = contextfabric.StructureNeedKind("expected_kind")

	superseded, err := store.IsStructureSuperseded(ctx, principal.OrgID, priorResultID, member)
	if err != nil {
		t.Fatalf("IsStructureSuperseded (before any Save): %v", err)
	}
	if superseded {
		t.Fatal("IsStructureSuperseded before any Save must be false")
	}

	winner := resultWithConfirmedStructure("result-supersession-winner-001", member, priorResultID, "kindr_winner00000001")
	if err := store.Save(ctx, principal, winner, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0); err != nil {
		t.Fatalf("save winner: %v", err)
	}

	superseded, err = store.IsStructureSuperseded(ctx, principal.OrgID, priorResultID, member)
	if err != nil {
		t.Fatalf("IsStructureSuperseded (after winning Save): %v", err)
	}
	if !superseded {
		t.Fatal("IsStructureSuperseded after the winning Save must be true")
	}

	loser := resultWithConfirmedStructure("result-supersession-loser-0001", member, priorResultID, "kindr_loser000000001")
	saveErr := store.Save(ctx, principal, loser, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
	if saveErr == nil {
		t.Fatal("a second Save redeeming the SAME (org, prior_result_id, member) must fail")
	}
	var conflict *contextfabric.ErrStructureOfferSuperseded
	if !errors.As(saveErr, &conflict) {
		t.Fatalf("saveErr = %v, want *contextfabric.ErrStructureOfferSuperseded", saveErr)
	}
	if len(conflict.Members) != 1 || conflict.Members[0] != member {
		t.Fatalf("conflict.Members = %+v, want [%q]", conflict.Members, member)
	}

	if _, err := store.Get(ctx, principal, loser.ResultID); !errors.Is(err, memoryinvestigation.ErrNotFound) {
		t.Fatalf("Get(loser) error = %v, want ErrNotFound (the loser must never persist)", err)
	}

	stored, err := store.Get(ctx, principal, winner.ResultID)
	if err != nil {
		t.Fatalf("get winner: %v", err)
	}
	if len(stored.Result.ConfirmedStructure) != 1 || stored.Result.ConfirmedStructure[0].Member != member {
		t.Fatalf("winner.ConfirmedStructure = %+v, unaffected by the loser expected", stored.Result.ConfirmedStructure)
	}

	// An idempotent replay of the winner itself must still succeed.
	if err := store.Save(ctx, principal, winner, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0); err != nil {
		t.Fatalf("idempotent replay of winner: %v", err)
	}
}
