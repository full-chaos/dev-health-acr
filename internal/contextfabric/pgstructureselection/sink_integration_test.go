package pgstructureselection

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// newStructureSelectionTestDatabase mirrors pgclarification's own
// newClarificationTestDatabase exactly: a fresh testcontainers Postgres,
// migrated to head (through 0024, this package's own migration), scoped to
// one test via t.Cleanup.
func newStructureSelectionTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
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
	require.NoError(t, runner.Up(ctx, db))
	return db
}

// validEvent returns a well-formed StructureSelectionEvent, scoped to
// orgID, mirroring pgclarification's own validEvent helper.
func validEvent(orgID string) contextfabric.StructureSelectionEvent {
	offered := []contextfabric.StructureOfferedOption{
		{ReceiptID: "kindr_committed000001", AppliedValue: "pull_request", OfferSource: "engine", Rank: 0},
		{ReceiptID: "kindr_committed000002", AppliedValue: "work_item", OfferSource: "engine", Rank: 1},
	}
	return contextfabric.StructureSelectionEvent{
		OrgID: orgID, CapturedAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		QuestionHash:  contextfabric.QuestionHash("Was Ask Dev ready to ship?"),
		PriorResultID: "result_structure_offer_00001", Member: "expected_kind",
		Offered: offered, Selected: offered[0], Accepted: true,
		SelectionMode: "human_panel", SelectionProvenance: "web_assertion",
		ProjectionVersion: "projection-v1", ModelIdentities: []string{"openai-compatible/gpt-5-nano"},
		RetrievalIdentity: contextfabric.ReuseRetrievalIdentity{EmbedRetrievalIdentity: "none", RetrievalPolicyVersion: "rp1"},
		PromptVersions: contextfabric.ReusePromptVersions{
			InterpretationPromptVersion: "context-fabric-interpretation.v7", SynthesisPromptVersion: "context-fabric-synthesis.v9",
		},
		VersionAuthorities: contextfabric.ReuseVersionAuthorities{
			QueryVersion: "devhealthfacts.clickhouse.v1", CanonicalServiceVersion: "context-fabric-facts.v1", ModelOutputSchemaVersion: "context-fabric-model-output.v1",
			IdentityNormalizationVersion: "graphrank.alias-normalize.v1", WindowInferenceVersion: "win_v1",
		},
	}
}

type storedSelectionRow struct {
	SelectionID          string
	OrgID                string
	CapturedAt           time.Time
	QuestionHash         string
	PriorResultID        string
	Member               string
	SelectedReceiptID    string
	SelectedAppliedValue string
	Accepted             bool
	SelectionMode        string
	SelectionProvenance  string
	OfferedRaw           []byte
	Offered              []contextfabric.StructureOfferedOption
	PipelineContext      pipelineContextPayload
	Consensus            *contextfabric.ConsensusEvidence
}

func mustLoadSelectionRow(t *testing.T, ctx context.Context, db *sql.DB, priorResultID string) storedSelectionRow {
	t.Helper()
	var row storedSelectionRow
	var pipelineJSON []byte
	var consensusJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context, consensus_evidence
FROM acr.context_fabric_structure_selections WHERE prior_result_id = $1`, priorResultID).Scan(
		&row.SelectionID, &row.OrgID, &row.CapturedAt, &row.QuestionHash, &row.PriorResultID, &row.Member,
		&row.SelectedReceiptID, &row.SelectedAppliedValue, &row.Accepted, &row.SelectionMode, &row.SelectionProvenance,
		&row.OfferedRaw, &pipelineJSON, &consensusJSON,
	))
	require.NotEmpty(t, row.SelectionID, "selection_id must be a real application-generated primary key, not left blank")
	require.NoError(t, json.Unmarshal(row.OfferedRaw, &row.Offered))
	require.NoError(t, json.Unmarshal(pipelineJSON, &row.PipelineContext))
	if consensusJSON != nil {
		row.Consensus = &contextfabric.ConsensusEvidence{}
		require.NoError(t, json.Unmarshal(consensusJSON, row.Consensus))
	}
	return row
}

// TestInsertContext_PersistsEveryField is the write-path proof: every field
// on a well-formed event lands in the row exactly, including the full (not
// just-the-selection) offered option set and the composed pipeline_context
// blob -- mirrors pgclarification's own TestInsertContext_PersistsEveryField.
func TestInsertContext_PersistsEveryField(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-structure-persist")
	require.NoError(t, sink.insertContext(ctx, event))

	row := mustLoadSelectionRow(t, ctx, db, event.PriorResultID)
	require.Equal(t, event.OrgID, row.OrgID)
	require.True(t, event.CapturedAt.Equal(row.CapturedAt), "captured_at = %v, want %v", row.CapturedAt, event.CapturedAt)
	require.Equal(t, event.QuestionHash, row.QuestionHash)
	require.Equal(t, event.Member, row.Member)
	require.Equal(t, event.Selected.ReceiptID, row.SelectedReceiptID)
	require.Equal(t, event.Selected.AppliedValue, row.SelectedAppliedValue)
	require.Equal(t, event.Accepted, row.Accepted)
	require.Equal(t, event.SelectionMode, row.SelectionMode)
	require.Equal(t, event.SelectionProvenance, row.SelectionProvenance)
	require.Equal(t, event.Offered, row.Offered, "offered must persist the COMPLETE option set, not just the selection")
	require.Equal(t, pipelineContextPayload{
		ProjectionVersion: event.ProjectionVersion, ModelIdentities: event.ModelIdentities,
		EmbedRetrievalIdentity: event.RetrievalIdentity.EmbedRetrievalIdentity, RetrievalPolicyVersion: event.RetrievalIdentity.RetrievalPolicyVersion,
		InterpretationPromptVersion: event.PromptVersions.InterpretationPromptVersion, SynthesisPromptVersion: event.PromptVersions.SynthesisPromptVersion,
		QueryVersion: event.VersionAuthorities.QueryVersion, CanonicalServiceVersion: event.VersionAuthorities.CanonicalServiceVersion,
		ModelOutputSchemaVersion:     event.VersionAuthorities.ModelOutputSchemaVersion,
		IdentityNormalizationVersion: event.VersionAuthorities.IdentityNormalizationVersion,
		WindowInferenceVersion:       event.VersionAuthorities.WindowInferenceVersion,
	}, row.PipelineContext)
	require.Nil(t, row.Consensus, "consensus_evidence must stay NULL for a non-3860 (human_panel) event")
}

// TestInsertContext_PersistsConsensusEvidenceWhenPresent is the CHAOS-3860
// P6 precondition fix's own write-path proof (migration 0026): a
// well-formed agent_receipt event carrying ConsensusEvidence round-trips
// its panel model identities and parallel agreement bits exactly.
func TestInsertContext_PersistsConsensusEvidenceWhenPresent(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-structure-consensus")
	event.SelectionMode = "agent_receipt"
	event.Consensus = &contextfabric.ConsensusEvidence{
		PanelModelIdentities: []string{"anthropic/sol-max", "anthropic/luna", "openai-compatible/opus"},
		AgreementBits:        []bool{true, true, false},
	}
	require.NoError(t, sink.insertContext(ctx, event))

	row := mustLoadSelectionRow(t, ctx, db, event.PriorResultID)
	require.NotNil(t, row.Consensus)
	require.Equal(t, *event.Consensus, *row.Consensus)
}

// TestInsertContext_RejectsConsensusEvidenceOutsideAgentReceiptMode proves
// validateEvent enforces migration 0026's own CHECK constraint in Go
// first, with a clearer error than a raw constraint violation, and leaves
// no row behind.
func TestInsertContext_RejectsConsensusEvidenceOutsideAgentReceiptMode(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-structure-consensus-reject")
	event.Consensus = &contextfabric.ConsensusEvidence{PanelModelIdentities: []string{"anthropic/sol-max"}, AgreementBits: []bool{true}}
	require.Error(t, sink.insertContext(ctx, event), "consensus evidence on a human_panel event must be rejected before any insert")

	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_structure_selections WHERE prior_result_id = $1`, event.PriorResultID).Scan(&count))
	require.Equal(t, 0, count)
}

// TestInsertContext_RejectsSingleMemberOrDuplicateConsensus proves the
// codex round-1 tightening: a panel is plural by definition, so a
// single-entry (or duplicate-identity) ConsensusEvidence payload is
// rejected in Go before any insert is attempted, matching migration
// 0026's own ck_acr_cf_structure_selections_consensus_panel_size CHECK.
func TestInsertContext_RejectsSingleMemberOrDuplicateConsensus(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	singleMember := validEvent("org-structure-consensus-single")
	singleMember.SelectionMode = "agent_receipt"
	singleMember.Consensus = &contextfabric.ConsensusEvidence{PanelModelIdentities: []string{"anthropic/sol-max"}, AgreementBits: []bool{true}}
	require.Error(t, sink.insertContext(ctx, singleMember), "a single-entry payload cannot represent a panel")

	duplicateMember := validEvent("org-structure-consensus-duplicate")
	duplicateMember.SelectionMode = "agent_receipt"
	duplicateMember.Consensus = &contextfabric.ConsensusEvidence{
		PanelModelIdentities: []string{"anthropic/sol-max", "anthropic/sol-max"},
		AgreementBits:        []bool{true, false},
	}
	require.Error(t, sink.insertContext(ctx, duplicateMember), "a panel's members must be distinct")

	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_structure_selections WHERE prior_result_id IN ($1, $2)`,
		singleMember.PriorResultID, duplicateMember.PriorResultID).Scan(&count))
	require.Equal(t, 0, count)
}

// TestInsertContext_RejectsMalformedEventBeforeAnyInsert proves
// validateEvent runs BEFORE the INSERT is attempted: a malformed event
// (unknown member) must error, and must leave NO row behind -- mirrors
// pgclarification's own equivalent validation-first proof.
func TestInsertContext_RejectsMalformedEventBeforeAnyInsert(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-structure-reject")
	event.Member = "window" // not in this table's closed vocabulary (window rides its own event type)
	require.Error(t, sink.insertContext(ctx, event))

	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_structure_selections WHERE prior_result_id = $1`, event.PriorResultID).Scan(&count))
	require.Equal(t, 0, count, "a rejected event must never leave a partial row behind")
}

// TestConsensusPanelSizeCheck_RejectsAtDatabaseLevelDirectly is the
// defense-in-depth proof migration 0027 exists for: a raw SQL INSERT that
// bypasses validateEvent entirely (no Go-side gate at all) must still be
// rejected by acr.context_fabric_structure_selections_consensus_is_valid_panel
// -- proving the DB CHECK, not only the Sink's own Go validation, actually
// enforces >=2 distinct panel model identities. Exercises exactly the two
// codex round-2 findings: a single-entry payload, a duplicate-identity
// payload, and (length-check-only would have missed this) a payload with a
// well-formed consensus_evidence object that never carries the
// panel_model_identities key at all.
func TestConsensusPanelSizeCheck_RejectsAtDatabaseLevelDirectly(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)

	insertRaw := func(t *testing.T, priorResultID, consensusJSON string) error {
		t.Helper()
		_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context, consensus_evidence)
VALUES (gen_random_uuid()::text, 'org-raw-consensus', now(), repeat('a', 64), $1, 'expected_kind', 'kindr_x', 'pull_request', true, 'agent_receipt', 'credential_mcp', '[]'::jsonb, '{}'::jsonb, $2::jsonb)`,
			priorResultID, consensusJSON)
		return err
	}

	require.Error(t, insertRaw(t, "raw-single", `{"panel_model_identities": ["anthropic/sol-max"], "agreement_bits": [true]}`), "single-entry payload must be rejected at the DB level")
	require.Error(t, insertRaw(t, "raw-duplicate", `{"panel_model_identities": ["anthropic/sol-max", "anthropic/sol-max"], "agreement_bits": [true, false]}`), "duplicate identities must be rejected at the DB level")
	require.Error(t, insertRaw(t, "raw-missing-key", `{"agreement_bits": [true, false]}`), "a missing panel_model_identities key must be rejected, not silently pass a NULL comparison")
	require.NoError(t, insertRaw(t, "raw-valid", `{"panel_model_identities": ["anthropic/sol-max", "anthropic/luna"], "agreement_bits": [true, false]}`), "a genuinely well-formed 2-member payload must still be accepted")

	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_structure_selections WHERE org_id = 'org-raw-consensus'`).Scan(&count))
	require.Equal(t, 1, count, "only the one valid raw insert should have landed")
}

// TestSink_RecordSelectionDeliversThroughTheBackgroundWorker is the
// end-to-end proof that RecordSelection (the public
// contextfabric.StructureSelectionSink method) actually reaches Postgres
// through the bounded queue and background worker, not just insertContext
// called directly.
func TestSink_RecordSelectionDeliversThroughTheBackgroundWorker(t *testing.T) {
	ctx := context.Background()
	db := newStructureSelectionTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-structure-worker")
	sink.RecordSelection(ctx, event)

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, sink.Close(closeCtx), "Close must drain the queued event before returning")

	row := mustLoadSelectionRow(t, ctx, db, event.PriorResultID)
	require.Equal(t, event.OrgID, row.OrgID)
	metrics := sink.Metrics()
	require.Equal(t, int64(1), metrics.Delivered)
	require.Equal(t, int64(0), metrics.DeliveryFailures)
}
