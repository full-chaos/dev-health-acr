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
}

func mustLoadSelectionRow(t *testing.T, ctx context.Context, db *sql.DB, priorResultID string) storedSelectionRow {
	t.Helper()
	var row storedSelectionRow
	var pipelineJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context
FROM acr.context_fabric_structure_selections WHERE prior_result_id = $1`, priorResultID).Scan(
		&row.SelectionID, &row.OrgID, &row.CapturedAt, &row.QuestionHash, &row.PriorResultID, &row.Member,
		&row.SelectedReceiptID, &row.SelectedAppliedValue, &row.Accepted, &row.SelectionMode, &row.SelectionProvenance,
		&row.OfferedRaw, &pipelineJSON,
	))
	require.NotEmpty(t, row.SelectionID, "selection_id must be a real application-generated primary key, not left blank")
	require.NoError(t, json.Unmarshal(row.OfferedRaw, &row.Offered))
	require.NoError(t, json.Unmarshal(pipelineJSON, &row.PipelineContext))
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
		ModelOutputSchemaVersion: event.VersionAuthorities.ModelOutputSchemaVersion,
	}, row.PipelineContext)
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
