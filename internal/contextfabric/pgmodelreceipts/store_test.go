package pgmodelreceipts_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelreceipts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// unreachableDB builds a *sql.DB that never actually dials -- pgx's
// stdlib.OpenDB is lazy, so this is safe to use for guard-clause tests that
// must return before ever issuing a query.
func unreachableDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg, err := pgx.ParseConfig("postgres://unreachable:5432/db")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return stdlib.OpenDB(*cfg)
}

func validReceipt() contextfabric.ModelExecutionReceipt {
	now := time.Now().UTC()
	return contextfabric.ModelExecutionReceipt{
		Operation:        contextfabric.ModelOperationInterpret,
		Provider:         "acme-gateway",
		Model:            "acme-large",
		ModelVersion:     "v1",
		PromptVersion:    "v1",
		SchemaVersion:    "v1",
		EvaluatorVersion: "v1",
		StartedAt:        now,
		CompletedAt:      now.Add(time.Second),
		Attempts:         1,
		InputDigest:      "ae1608896372720b6ebb58261e0c0092c608324b0804bc99267c1753990faaa8",
		Outcome:          "success",
	}
}

func TestNewStore_rejectsNilDB(t *testing.T) {
	if _, err := pgmodelreceipts.NewStore(nil); err == nil {
		t.Fatal("NewStore accepted a nil database")
	}
}

// TestRecordModelExecution_rejectsMissingOrg locks that org scoping is
// enforced before any query reaches the database -- the receipt sink must
// never write an org-less row.
func TestRecordModelExecution_rejectsMissingOrg(t *testing.T) {
	store, err := pgmodelreceipts.NewStore(unreachableDB(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	err = store.RecordModelExecution(context.Background(), storage.Principal{}, validReceipt())
	if err == nil {
		t.Fatal("RecordModelExecution accepted a request with no organization")
	}
}

// TestRecordModelExecution_rejectsInvalidReceipt locks that
// ModelExecutionReceipt.Validate() runs before any query reaches the
// database, so a malformed receipt can never produce a partially-written
// or malformed durable row.
func TestRecordModelExecution_rejectsInvalidReceipt(t *testing.T) {
	store, err := pgmodelreceipts.NewStore(unreachableDB(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	invalid := validReceipt()
	invalid.Outcome = ""
	err = store.RecordModelExecution(context.Background(), storage.Principal{OrgID: "org-a"}, invalid)
	if err == nil {
		t.Fatal("RecordModelExecution accepted an invalid receipt")
	}
}

// TestRecordModelExecution_databaseFailureSatisfiesContextFabricErrUnavailable
// is the Codex round-1 F2 probe, permanently locked: a receipt-sink error
// reaches internal/api/context_fabric_routes.go's writeContextFabricError
// through RuntimeQuestionInterpreter/RuntimeAnswerSynthesizer, which
// classifies ONLY on contextfabric.Err*/context.* -- never on
// storage.ErrUnavailable. A database failure here must therefore satisfy
// errors.Is(err, contextfabric.ErrUnavailable), or the whole investigation
// silently falls through to a generic 500 instead of the declared 503
// upstream_unavailable.
func TestRecordModelExecution_databaseFailureSatisfiesContextFabricErrUnavailable(t *testing.T) {
	store, err := pgmodelreceipts.NewStore(unreachableDB(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	recordErr := store.RecordModelExecution(ctx, storage.Principal{OrgID: "org-a"}, validReceipt())
	if recordErr == nil {
		t.Fatal("RecordModelExecution succeeded against an unreachable database")
	}
	if !errors.Is(recordErr, context.DeadlineExceeded) && !errors.Is(recordErr, contextfabric.ErrUnavailable) {
		t.Fatalf("err = %v, want it to satisfy context.DeadlineExceeded or contextfabric.ErrUnavailable", recordErr)
	}
}
