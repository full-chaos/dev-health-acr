package pginvestigation_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// validResult builds a minimal but FULLY VALID InvestigationResult (see
// paritytest.result -- this file needs its own copy since these tests
// exercise context/error-propagation behavior, not save/get parity, and
// live outside the shared table). Save now validates on write (CHAOS-3755
// finding M2), so every fixture in this file must satisfy
// InvestigationResult.Validate() or these tests would fail at the
// validation step before ever reaching the context/timeout behavior they
// actually test.
func validResult(resultID string) contextfabric.InvestigationResult {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-" + resultID, Label: "Project " + resultID}
	built := contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      resultID,
		RequestID:     "request-" + resultID,
		GeneratedAt:   time.Now().UTC(),
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
	}
	// From the PRODUCER, not by hand. The block's `state` is a closed
	// vocabulary whose Go zero value is not a member, so an omitted state
	// is an invalid fixture that reads as a complete one -- and this file's
	// own header already requires every fixture here to satisfy
	// InvestigationResult.Validate().
	built.Completeness = contextfabric.ComputeAnswerCompleteness(built)
	return built
}

func newInvestigationTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	return db
}

// TestStore_parity runs the shared contextfabric.InvestigationResultStore
// behavior table (save/get roundtrip, org scoping, immutability) against
// Postgres. memoryinvestigation's store_test.go runs the exact same table
// against the in-memory store, so the two implementations cannot silently
// drift apart. All cases share one container/database: every case uses a
// distinct result_id, so a fresh *pginvestigation.Store wrapping the same
// *sql.DB is an independent scope per case without needing per-case
// containers or truncation.
func TestStore_parity(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)

	paritytest.RunSuite(t,
		func(t *testing.T) contextfabric.InvestigationResultStore {
			store, err := pginvestigation.NewStore(db)
			require.NoError(t, err)
			return store
		},
		func(err error) bool { return errors.Is(err, pginvestigation.ErrNotFound) },
	)
}

func TestStore_saveAndGetReturnContextCanceledWithoutWrappingAsUnavailable(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	saveErr := store.Save(cancelled, storage.Principal{OrgID: "org-1"}, validResult("result-cancelled-save"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
	require.Error(t, saveErr)
	require.True(t, errors.Is(saveErr, context.Canceled), "save error should be context.Canceled, got %v", saveErr)
	require.False(t, errors.Is(saveErr, contextfabric.ErrUnavailable), "a canceled context is not a bounded dependency failure")

	// Seed a row (with a live context) so Get has something to reach for.
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-cancelled-get"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

	_, getErr := store.Get(cancelled, storage.Principal{OrgID: "org-1"}, "result-cancelled-get")
	require.Error(t, getErr)
	require.True(t, errors.Is(getErr, context.Canceled), "get error should be context.Canceled, got %v", getErr)
	require.False(t, errors.Is(getErr, contextfabric.ErrUnavailable))
}

func TestStore_saveAndGetReturnUnavailableOnDeadlineExceeded(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-deadline-seed"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

	expired, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	saveErr := store.Save(expired, storage.Principal{OrgID: "org-1"}, validResult("result-deadline-save"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
	require.Error(t, saveErr)
	require.True(t, errors.Is(saveErr, context.DeadlineExceeded), "save error should be context.DeadlineExceeded, got %v", saveErr)

	_, getErr := store.Get(expired, storage.Principal{OrgID: "org-1"}, "result-deadline-seed")
	require.Error(t, getErr)
	require.True(t, errors.Is(getErr, context.DeadlineExceeded), "get error should be context.DeadlineExceeded, got %v", getErr)
}

func TestStore_getUnknownResultIDIsIndistinguishableFromWrongOrg(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, storage.Principal{OrgID: "org-1"}, validResult("result-non-enumerating"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

	_, wrongOrgErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-non-enumerating")
	_, unknownIDErr := store.Get(ctx, storage.Principal{OrgID: "org-2"}, "result-does-not-exist")

	require.ErrorIs(t, wrongOrgErr, pginvestigation.ErrNotFound)
	require.ErrorIs(t, unknownIDErr, pginvestigation.ErrNotFound)
	require.Equal(t, unknownIDErr.Error(), wrongOrgErr.Error(), "wrong-org and truly-missing must produce the identical error")
}

// TestStore_getRejectsStoredResultThatFailsValidation is the M2 Get-guard
// probe (Codex adversarial + delta review, CHAOS-3755) for the production
// store specifically -- memoryinvestigation has its own white-box version
// (store_internal_test.go), but nothing exercised this store's copy of the
// same guard. Save validates on write, so the only way to reach Get's own
// validation is to write a row directly, bypassing Save -- exactly the
// state the guard exists for (a row that reached storage some other way).
func TestStore_getRejectsStoredResultThatFailsValidation(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	// Syntactically valid JSON, semantically invalid InvestigationResult:
	// missing schema_version, status, interpretation, coverage, and every
	// required collection. json.Unmarshal cannot catch this -- only
	// InvestigationResult.Validate() can.
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, "result-corrupt-pg", "org-1", []byte(`{"result_id":"result-corrupt-pg","question":"what happened?"}`), time.Now().UTC())
	require.NoError(t, err)

	_, getErr := store.Get(ctx, storage.Principal{OrgID: "org-1"}, "result-corrupt-pg")
	require.Error(t, getErr)
	require.Contains(t, getErr.Error(), "stored investigation result is invalid")
}

// TestStore_getRejectsStoredResultWithExplicitNullDegradedReasons is the P2
// fix (Codex delta review, CHAOS-3755): coverage.degraded_reasons is
// omitempty in Go and optional (not `null`) in the JSON Schema -- an
// explicit `null` collapses to the same Go nil slice an omitted field
// would decode to, so it must be caught on the raw stored bytes, not after
// json.Unmarshal has already erased the distinction. The row is planted
// with a direct INSERT (bypassing Save, which never produces explicit
// null through its own marshal path) to reach the state this guards
// against: a row written by some other path.
func TestStore_getRejectsStoredResultWithExplicitNullDegradedReasons(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	valid := validResult("result-explicit-null")
	encoded, err := json.Marshal(valid)
	require.NoError(t, err)
	tainted := bytes.Replace(encoded, []byte(`"sources":[]`), []byte(`"sources":[],"degraded_reasons":null`), 1)
	require.NotEqual(t, string(encoded), string(tainted), "test setup: expected substring not found in fixture JSON")

	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, valid.ResultID, "org-1", tainted, valid.GeneratedAt)
	require.NoError(t, err)

	_, getErr := store.Get(ctx, storage.Principal{OrgID: "org-1"}, valid.ResultID)
	require.Error(t, getErr)
	require.Contains(t, getErr.Error(), "degraded_reasons")
	require.Contains(t, getErr.Error(), "null")
}

// TestStore_explicitNullDegradedReasonsParity runs the SHARED explicit-null
// table against Postgres. pginvestigation and memoryinvestigation each
// carry their own copy of the raw-bytes check; this table is what stops
// the two from drifting apart.
func TestStore_explicitNullDegradedReasonsParity(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)

	paritytest.RunExplicitNullDegradedReasonsSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.RawSeed) {
		store, err := pginvestigation.NewStore(db)
		require.NoError(t, err)
		return store, func(t *testing.T, orgID, resultID string, payload []byte) {
			t.Helper()
			_, execErr := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, resultID, orgID, payload, time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC))
			require.NoError(t, execErr)
		}
	})
}

// resultWithConfirmedStructure builds a FULLY VALID InvestigationResult
// (validResult's own contract) additionally carrying one applied,
// receipt-sourced ConfirmedStructure entry for member -- CHAOS-3927 P4's
// own claim-bearing fixture. priorResultID/receiptID must each satisfy
// ContextFabricConfirmedStructureEntry.Validate()'s 8..256 char bound.
func resultWithConfirmedStructure(resultID string, member contextfabric.StructureNeedKind, priorResultID, receiptID string) contextfabric.InvestigationResult {
	result := validResult(resultID)
	result.ConfirmedStructure = []contextfabric.ConfirmedStructureEntry{
		{
			Member: member, AppliedValue: "pull_request", Source: "receipt",
			PriorResultID: priorResultID, ReceiptID: receiptID,
			Provenance: "clarification_confirmed", Disposition: "applied",
		},
	}
	return result
}

// TestStore_structureSupersessionClaims is CHAOS-3927 P4's own acceptance
// pin for the atomicity guarantee ErrStructureOfferSuperseded's doc
// comment describes: two Saves redeeming the SAME (org, prior_result_id,
// member) tuple under DIFFERENT result_ids -- the second must lose,
// atomically, with neither its result nor its claim persisted, while the
// first result and its claim remain completely intact.
// CHAOS-4003: table-ified over member -- expected_kind (the pre-existing
// pin) AND window (the member that ticket closed the staleness hole for),
// sharing ONE store/database instance across both t.Run subtests so the
// SAME assertions also prove the two members' claims are independent of
// each other in the REAL Postgres CHECK-constrained table (0023's own
// member vocabulary CHECK already listed 'window' -- this is the proof the
// Go write path actually exercises that value, not just that the
// constraint would permit it).
// CHAOS-4333: table-ified again over subject_candidate -- CHAOS-4012 added
// it as a 5th StructureNeedKind, but nothing widened this table's CHECK
// (migration 0023) to match until migration 0033. This subtest is the
// same class of pin CHAOS-4003 added for 'window': it fails the same way
// (a raw ck_acr_cf_structure_supersession_member_vocabulary violation,
// sanitized into contextfabric.ErrUnavailable by Store.Save) without 0033
// applied, confirmed live 2026-08-26 on the kiac acr-pilot cluster.
func TestStore_structureSupersessionClaims(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	principal := storage.Principal{OrgID: "org-supersession"}

	for _, member := range []contextfabric.StructureNeedKind{"expected_kind", "window", "subject_candidate"} {
		t.Run(string(member), func(t *testing.T) {
			priorResultID := "result-prior-structure-offer-" + string(member)

			// Before any Save, nothing is claimed.
			superseded, err := store.IsStructureSuperseded(ctx, principal.OrgID, priorResultID, member)
			require.NoError(t, err)
			require.False(t, superseded, "IsStructureSuperseded before any Save must be false")

			winner := resultWithConfirmedStructure("result-supersession-winner-"+string(member), member, priorResultID, "kindr_winner00000001")
			require.NoError(t, store.Save(ctx, principal, winner, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))

			superseded, err = store.IsStructureSuperseded(ctx, principal.OrgID, priorResultID, member)
			require.NoError(t, err)
			require.True(t, superseded, "IsStructureSuperseded after the winning Save must be true")

			loser := resultWithConfirmedStructure("result-supersession-loser-"+string(member), member, priorResultID, "kindr_loser000000001")
			saveErr := store.Save(ctx, principal, loser, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
			require.Error(t, saveErr, "a second Save redeeming the SAME (org, prior_result_id, member) must fail")
			var conflict *contextfabric.ErrStructureOfferSuperseded
			require.ErrorAs(t, saveErr, &conflict)
			require.Equal(t, []contextfabric.StructureNeedKind{member}, conflict.Members)

			// The loser's result must NOT have been persisted -- the whole
			// transaction (claim attempt AND result insert) rolled back together.
			_, getErr := store.Get(ctx, principal, loser.ResultID)
			require.ErrorIs(t, getErr, pginvestigation.ErrNotFound)

			// The winner is completely unaffected by the loser's failed attempt.
			stored, err := store.Get(ctx, principal, winner.ResultID)
			require.NoError(t, err)
			require.Equal(t, winner.ConfirmedStructure, stored.Result.ConfirmedStructure)

			// A THIRD Save that is a byte-for-byte replay of the winner (an
			// idempotent retry) must still succeed -- the claim it would attempt
			// is already held by the SAME result_id, matching the design brief's
			// own "receipts are NOT consumed by a failed round" symmetry the other
			// direction: a successful round replaying itself is not a conflict.
			require.NoError(t, store.Save(ctx, principal, winner, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0))
		})
	}
}

// TestStructureSupersessionClaimsMemberVocabularyParity is CHAOS-4333's own
// closing pin: ck_acr_cf_structure_supersession_member_vocabulary (migration
// 0023, widened by 0033) must allow EXACTLY the same set of values as
// contractsv1.ContextFabricStructureNeedKindVocabulary(), the single
// authoritative enum structureSupersessionClaims's callers draw `member`
// from. CHAOS-4333 happened because these silently drifted apart -- CHAOS-
// 4012 added subject_candidate to the Go enum without anyone widening this
// CHECK, and nothing caught it until a live 503. This test fails the same
// way the NEXT such addition would: read the constraint the migrations
// actually left in a real Postgres database (not the migration file's own
// source text, which would only prove the file says what it says, not
// what got applied), and diff it against the enum.
//
// Compares the FULL normalized predicate string pg_get_constraintdef
// returns, not just the quoted literals inside it (codex review, CHAOS-
// 4333: extracting only the literals would let a constraint like
// `CHECK (member IN (...) OR member = chr(120))` pass while an extra
// disjunct actually widens what's allowed beyond the enum). Postgres
// normalizes a `member IN (...)` CHECK into this exact
// `member = ANY (ARRAY[...])` shape -- confirmed empirically against the
// same postgres:18-alpine image this suite already runs everywhere else,
// with elements in the enum's own declared order (contractsv1's own
// array, which migration 0033 appends to rather than reorders).
//
// The lookup is scoped to this table's own conrelid, not conname alone
// (codex review, CHAOS-4333: constraint names are relation-scoped, so an
// unqualified name lookup could resolve a same-named constraint on a
// different table).
func TestStructureSupersessionClaimsMemberVocabularyParity(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)

	var constraintDef string
	err := db.QueryRowContext(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conname = 'ck_acr_cf_structure_supersession_member_vocabulary'
		   AND conrelid = 'acr.context_fabric_structure_supersession_claims'::regclass`,
	).Scan(&constraintDef)
	require.NoError(t, err, "ck_acr_cf_structure_supersession_member_vocabulary must exist on acr.context_fabric_structure_supersession_claims after migrations apply")

	vocabulary := contractsv1.ContextFabricStructureNeedKindVocabulary()
	quoted := make([]string, 0, len(vocabulary))
	for _, kind := range vocabulary {
		quoted = append(quoted, fmt.Sprintf("'%s'::text", kind))
	}
	expectedDef := fmt.Sprintf("CHECK ((member = ANY (ARRAY[%s])))", strings.Join(quoted, ", "))
	require.Equal(t, expectedDef, constraintDef,
		"ck_acr_cf_structure_supersession_member_vocabulary must allow EXACTLY contractsv1.ContextFabricStructureNeedKindVocabulary(), in its declared order -- no fewer, no more, no other predicate shape")
}

// TestMigration0025_BackfillsClaimsForPreMigrationConfirmedStructure is
// CHAOS-3927 P4's own codex-adversarial-review acceptance pin (HIGH
// finding): migration 0023 created the claim table EMPTY, so any
// InvestigationResult already carrying an applied, receipt-sourced
// ConfirmedStructure entry from BEFORE 0023 existed had no claim
// protecting its redeemed offer. Migration 0025 backfills exactly that.
//
// This test simulates the pre-migration state directly: it INSERTs a raw
// investigation_results row (bypassing Store.Save entirely -- Save today
// would always mint the claim itself; this row represents one that was
// written by an OLDER binary, before the claim table's write path
// existed) carrying a confirmed_structure payload, with deliberately NO
// matching claim row. It then re-executes 0025's own migration SQL
// directly (the migration already ran once, against an empty table, when
// newInvestigationTestDatabase set up the database -- re-running its
// idempotent INSERT ... ON CONFLICT DO NOTHING against THIS row is exactly
// what a real backfill deploy does against real pre-existing data) and
// asserts the claim now exists.
//
// It also plants three sibling rows whose confirmed_structure is NOT a
// JSON array (null, a scalar, an object -- codex round-2 adversarial
// review, MEDIUM finding) alongside the valid legacy row, proving the
// backfill tolerates every malformed shape without aborting for the whole
// table.
func TestMigration0025_BackfillsClaimsForPreMigrationConfirmedStructure(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store, err := pginvestigation.NewStore(db)
	require.NoError(t, err)

	principal := storage.Principal{OrgID: "org-backfill"}
	priorResultID := "result-prior-legacy-structure-001"
	const member = contextfabric.StructureNeedKind("subject_anchor")

	// A confirmed-structure result, persisted WITHOUT going through the
	// claim-aware Save path -- a raw INSERT is the only way to reproduce
	// "this row predates the claim table" against a database that already
	// has migration 0023's write-side behavior live in Store.Save.
	legacy := resultWithConfirmedStructure("result-legacy-confirmed-001", member, priorResultID, "ancr_legacy00000001")
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, legacy.ResultID, principal.OrgID, payload, legacy.GeneratedAt)
	require.NoError(t, err)

	// Confirm the premise: no claim exists yet for this legacy row.
	superseded, err := store.IsStructureSuperseded(ctx, principal.OrgID, priorResultID, member)
	require.NoError(t, err)
	require.False(t, superseded, "premise: a raw-inserted legacy row must carry no claim before the backfill runs")

	// codex round-2 adversarial review, MEDIUM finding: siblings whose
	// confirmed_structure is NOT a JSON array -- explicit null, a scalar,
	// and an object -- must never abort the backfill for every OTHER row.
	// These raw payloads deliberately violate Go's own contract (they
	// could never be produced by contextfabric.InvestigationResult's own
	// json.Marshal) -- exactly the "reached storage some other way"
	// posture Get's own defensive checks already assume elsewhere in this
	// package.
	for _, malformed := range []struct {
		resultID string
		payload  string
	}{
		{"result-malformed-null-0001", `{"confirmed_structure": null}`},
		{"result-malformed-scalar001", `{"confirmed_structure": "not-an-array"}`},
		{"result-malformed-object001", `{"confirmed_structure": {"member": "expected_kind"}}`},
	} {
		_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)`, malformed.resultID, principal.OrgID, []byte(malformed.payload), legacy.GeneratedAt)
		require.NoError(t, err)
	}

	// Re-run 0025's own migration SQL directly against this now-populated
	// table -- the embedded copy is the SAME file the runner already
	// applied once; re-executing its idempotent INSERT is what a real
	// deploy's backfill does against real pre-existing rows.
	backfillSQL, err := migrations.Files.ReadFile("0025_context_fabric_structure_supersession_backfill.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(backfillSQL))
	require.NoError(t, err)

	superseded, err = store.IsStructureSuperseded(ctx, principal.OrgID, priorResultID, member)
	require.NoError(t, err)
	require.True(t, superseded, "the backfill must claim (org, prior_result_id, member) for the pre-existing confirmed-structure row")

	// A NEW result attempting to redeem the SAME tuple must now be
	// rejected -- exactly the double-redemption the finding described,
	// now closed.
	racer := resultWithConfirmedStructure("result-racer-post-backfill-01", member, priorResultID, "ancr_racer000000001")
	racerErr := store.Save(ctx, principal, racer, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0)
	var conflict *contextfabric.ErrStructureOfferSuperseded
	require.ErrorAs(t, racerErr, &conflict)
	require.Equal(t, []contextfabric.StructureNeedKind{member}, conflict.Members)
}

// TestMigration0027_ClearsReuseColumnsForPreExistingStructureBearingRows is
// CHAOS-3977 P5's own cleanup-migration pin (codex adversarial review
// round 2/3, medium finding, repro-confirmed and fixed): a row written by
// a PRE-FIX binary could carry populated reuse-key columns alongside a
// non-nil structure_needs or non-empty confirmed_structure in its own
// payload -- reuseColumnsFor's own fix (this same ticket) only governs
// FUTURE Saves, so migration 0027 is the one-time retroactive cleanup.
// This test reproduces the pre-fix shape with a raw INSERT (the only way
// to construct it against a database that already has the fixed
// reuseColumnsFor live), re-runs 0027's own SQL, and asserts every reuse
// column is cleared for BOTH structure_needs-bearing and
// confirmed_structure-bearing rows -- and that an UNRELATED, ordinary
// reusable row is left completely untouched.
func TestMigration0027_ClearsReuseColumnsForPreExistingStructureBearingRows(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	orgID := "org-migration-0027"
	now := time.Now().UTC()

	insertLegacyRow := func(resultID string, payload string, reusable bool) {
		var questionHash, contractVersion, projectionVersion, modelIdentity any
		var sourceWatermarks any
		var invalidationEpoch any
		if reusable {
			questionHash, contractVersion, projectionVersion, modelIdentity = contextfabric.QuestionHash(resultID), "contract-v1", "projection-v1", "model-v1"
			sourceWatermarks = []byte(`{"source-a":"watermark-1"}`)
			invalidationEpoch = int64(0)
		}
		_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results
    (result_id, org_id, payload, generated_at, question_hash, contract_version, projection_version, model_identity, source_watermarks, invalidation_epoch, time_axis_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			resultID, orgID, []byte(payload), now, questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch, "current")
		require.NoError(t, err)
	}

	// A structure_needs-bearing row, pre-fix-shaped (reuse columns
	// populated as if reuseColumnsFor had never excluded it).
	insertLegacyRow("result-0027-structure-needs", `{"structure_needs":{"missing":["expected_kind"]}}`, true)
	// A confirmed_structure-bearing row, same pre-fix shape.
	insertLegacyRow("result-0027-confirmed-structure", `{"confirmed_structure":[{"member":"expected_kind","applied_value":"pull_request","source":"receipt","provenance":"clarification_confirmed","disposition":"applied"}]}`, true)
	// An ORDINARY reusable row -- must be left completely untouched.
	insertLegacyRow("result-0027-ordinary", `{}`, true)
	// A structure_needs-bearing row that was ALREADY correctly excluded
	// (reuse columns NULL) -- must stay NULL, not error.
	insertLegacyRow("result-0027-already-excluded", `{"structure_needs":{"missing":["expected_kind"]}}`, false)

	backfillSQL, err := migrations.Files.ReadFile("0029_context_fabric_structure_bearing_reuse_cleanup.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(backfillSQL))
	require.NoError(t, err)

	assertReuseColumnsNull := func(resultID string) {
		t.Helper()
		var questionHash, sourceWatermarks sql.NullString
		var invalidationEpoch sql.NullInt64
		err := db.QueryRowContext(ctx, `
SELECT question_hash, source_watermarks::text, invalidation_epoch FROM acr.context_fabric_investigation_results WHERE result_id = $1`, resultID).
			Scan(&questionHash, &sourceWatermarks, &invalidationEpoch)
		require.NoError(t, err)
		require.False(t, questionHash.Valid, "result %s: question_hash must be NULL after the cleanup", resultID)
		require.False(t, sourceWatermarks.Valid, "result %s: source_watermarks must be NULL after the cleanup", resultID)
		require.False(t, invalidationEpoch.Valid, "result %s: invalidation_epoch must be NULL after the cleanup", resultID)
	}
	assertReuseColumnsPopulated := func(resultID string) {
		t.Helper()
		var questionHash sql.NullString
		err := db.QueryRowContext(ctx, `
SELECT question_hash FROM acr.context_fabric_investigation_results WHERE result_id = $1`, resultID).Scan(&questionHash)
		require.NoError(t, err)
		require.True(t, questionHash.Valid, "result %s: an ordinary reusable row must be left untouched", resultID)
	}

	assertReuseColumnsNull("result-0027-structure-needs")
	assertReuseColumnsNull("result-0027-confirmed-structure")
	assertReuseColumnsNull("result-0027-already-excluded")
	assertReuseColumnsPopulated("result-0027-ordinary")
}
