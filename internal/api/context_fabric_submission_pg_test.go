package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// CHAOS-11912 integration: two App instances, each with its own connection
// pool and *pginvestigation.Store, over one real PostgreSQL. Every case
// asserts the execution count and the stored record, not only a status.

func submissionPostgres(t *testing.T) (dsn string, admin *sql.DB) {
	t.Helper()
	chfixture.RequireContainers(t)
	ctx := context.Background()
	container, err := startterm.PostgresRun(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44", tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	dsn, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin = openSubmissionPool(t, dsn)
	runner, err := migrations.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(ctx, admin); err != nil {
		t.Fatal(fmt.Errorf("apply production migrations: %w", err))
	}
	return dsn, admin
}

func openSubmissionPool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := runtimepostgres.Open(context.Background(), runtimepostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// newPostgresSubmissionHarness is two App instances over two pools.
func newPostgresSubmissionHarness(t *testing.T, dsn string) *submissionHarness {
	t.Helper()
	var stores []submissionTestStore
	for index := 0; index < 2; index++ {
		store, err := pginvestigation.NewStore(openSubmissionPool(t, dsn))
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	return newSubmissionHarness(t, stores...)
}

type submissionRow struct {
	state    string
	resultID sql.NullString
	count    int
}

func readSubmissionRow(t *testing.T, db *sql.DB, submissionID string) submissionRow {
	t.Helper()
	var row submissionRow
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE submission_id = $1`, submissionID).Scan(&row.count); err != nil {
		t.Fatal(err)
	}
	if row.count == 0 {
		return row
	}
	if err := db.QueryRowContext(context.Background(), `SELECT state, result_id FROM acr.context_fabric_investigation_submissions WHERE submission_id = $1`, submissionID).Scan(&row.state, &row.resultID); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestSubmissionOnPostgresAcrossTwoInstances(t *testing.T) {
	dsn, admin := submissionPostgres(t)

	// Same key on both instances at once: one execution.
	t.Run("race_runs_once", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		harness.investigator.mode.Store(int32(runBlockThenSave))
		const contenders = 8
		var (
			start, done sync.WaitGroup
			mu          sync.Mutex
			codes       = map[string]int{}
			winner      *httptest.ResponseRecorder
		)
		start.Add(1)
		for index := 0; index < contenders; index++ {
			done.Add(1)
			go func(index int) {
				defer done.Done()
				start.Wait()
				recorder := harness.post(index%2, harness.tokenA, "submission-pg-race-01", submissionQuestion)
				mu.Lock()
				defer mu.Unlock()
				if recorder.Code == http.StatusOK {
					winner = recorder
					codes["200"]++
					return
				}
				codes[errorEnvelopeOf(t, recorder).Error.Code]++
			}(index)
		}
		start.Done()
		<-harness.investigator.entered
		// Every contender but the runner is answered while the runner is
		// still blocked: in_progress, never a second run.
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return codes[contractsv1.ErrorCodeSubmissionInProgress] == contenders-1
		})
		close(harness.investigator.release)
		done.Wait()
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions = %d, want exactly 1 (codes %v)", got, codes)
		}
		if codes["200"] != 1 || codes[contractsv1.ErrorCodeSubmissionInProgress] != contenders-1 {
			t.Fatalf("codes = %v, want one 200 and %d in_progress", codes, contenders-1)
		}
		resultID := servedResultID(t, winner)
		row := readSubmissionRow(t, admin, "submission-pg-race-01")
		if row.count != 1 || row.state != "done" || row.resultID.String != resultID {
			t.Fatalf("record = %+v, want one done record naming %s", row, resultID)
		}
		if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-pg-race-01", submissionQuestion)); again != resultID {
			t.Fatalf("replay = %s, want %s", again, resultID)
		}
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions after the replay = %d, want 1", got)
		}
	})

	t.Run("mismatch_across_instances", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		servedResultID(t, harness.post(0, harness.tokenA, "submission-pg-mismatch", submissionQuestion))
		expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-pg-mismatch", submissionQuestion+" Since when?"), contractsv1.ErrorCodeSubmissionMismatch)
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions = %d, want 1", got)
		}
	})

	// The run dies after its save commits and before anything else happens:
	// the save completed the record in its own statement, so the retry on the
	// other instance replays the saved result.
	t.Run("crash_between_save_and_response", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		harness.investigator.mode.Store(int32(runSaveThenPanic))
		if recorder := harness.post(0, harness.tokenA, "submission-pg-crash-01", submissionQuestion); recorder.Code != http.StatusInternalServerError {
			t.Fatalf("crashed run answered %d (body %s)", recorder.Code, recorder.Body.String())
		}
		row := readSubmissionRow(t, admin, "submission-pg-crash-01")
		if row.state != "done" || !row.resultID.Valid {
			t.Fatalf("record after the crash = %+v, want done", row)
		}
		harness.investigator.mode.Store(int32(runSave))
		if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-pg-crash-01", submissionQuestion)); again != row.resultID.String {
			t.Fatalf("retry = %s, want the saved %s", again, row.resultID.String)
		}
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions = %d, want 1", got)
		}
	})

	// The completion is lost (the record is left running though its result
	// is stored): the retry reconciles it on read and replays.
	t.Run("lost_completion_is_reconciled_on_read", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		saved := servedResultID(t, harness.post(0, harness.tokenA, "submission-pg-lost-001", submissionQuestion))
		if _, err := admin.ExecContext(context.Background(), `UPDATE acr.context_fabric_investigation_submissions SET state = 'running', result_id = NULL WHERE submission_id = 'submission-pg-lost-001'`); err != nil {
			t.Fatal(err)
		}
		if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-pg-lost-001", submissionQuestion)); again != saved {
			t.Fatalf("retry = %s, want %s", again, saved)
		}
		if row := readSubmissionRow(t, admin, "submission-pg-lost-001"); row.state != "done" || row.resultID.String != saved {
			t.Fatalf("record = %+v", row)
		}
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions = %d, want 1", got)
		}
	})

	// The not-stored branch, the response lost, a retry on the other
	// instance: submission_unsaved, one execution.
	t.Run("unsaved_response_lost_retry", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		harness.investigator.mode.Store(int32(runUnsaved))
		_ = harness.post(0, harness.tokenA, "submission-pg-unsaved1", submissionQuestion)
		harness.investigator.mode.Store(int32(runSave))
		expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-pg-unsaved1", submissionQuestion), contractsv1.ErrorCodeSubmissionUnsaved)
		if row := readSubmissionRow(t, admin, "submission-pg-unsaved1"); row.state != "done_unsaved" || row.resultID.Valid {
			t.Fatalf("record = %+v, want done_unsaved with no result", row)
		}
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions = %d, want 1", got)
		}
	})

	t.Run("failed_run_abandoned", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		harness.investigator.mode.Store(int32(runFail))
		_ = harness.post(0, harness.tokenA, "submission-pg-failed01", submissionQuestion)
		harness.investigator.mode.Store(int32(runSave))
		expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-pg-failed01", submissionQuestion), contractsv1.ErrorCodeSubmissionAbandoned)
		if got := harness.investigator.executions.Load(); got != 1 {
			t.Fatalf("executions = %d, want 1", got)
		}
	})

	// CHAOS-11913: the status read on the OTHER instance names the result
	// and the thread; another principal gets the unknown-id answer.
	t.Run("status_across_instances", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		served := servedResultID(t, harness.postThread(0, harness.tokenA, "submission-pg-status01", "thread-pg-11913-status", submissionQuestion))
		status := decodeSubmissionStatus(t, harness.status(1, harness.tokenA, "submission-pg-status01"))
		if status.State != contractsv1.ContextFabricSubmissionDone || status.ResultID != served || status.ThreadID != "thread-pg-11913-status" {
			t.Fatalf("status = %+v", status)
		}
		other := harness.status(1, harness.tokenB, "submission-pg-status01")
		unknown := harness.status(1, harness.tokenB, "submission-pg-unknown01")
		if other.Code != http.StatusNotFound || !sameErrorBody(t, other, unknown) {
			t.Fatalf("another principal = %d %s", other.Code, other.Body.String())
		}
	})

	// INV-1, per surface: the stored row carries its owner, and every
	// surface's decision on it refuses another principal of the same org
	// and grant -- result_by_id through the real route on the other
	// instance, the engine-side surfaces through the gate on the carrier the
	// same PostgreSQL read returns.
	t.Run("owner_denials_per_surface", func(t *testing.T) {
		harness := newPostgresSubmissionHarness(t, dsn)
		bound := servedResultID(t, harness.post(0, harness.tokenA, "submission-pg-owner-01", submissionQuestion))
		if recorder := harness.get(1, harness.tokenA, bound); recorder.Code != http.StatusOK {
			t.Fatalf("owner read on the other instance = %d", recorder.Code)
		}
		denied := harness.get(1, harness.tokenB, bound)
		unknown := harness.get(1, harness.tokenB, "result_never_existed_02")
		if denied.Code != http.StatusNotFound || !sameErrorBody(t, denied, unknown) {
			t.Fatalf("another principal read = %d %s", denied.Code, denied.Body.String())
		}
		owner := principalOfResult(t, harness)
		other := owner
		other.Subject = owner.Subject + "-other"
		store, err := pginvestigation.NewStore(admin)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := store.Get(context.Background(), other, bound)
		if err != nil || stored.SubmissionOwner == nil {
			t.Fatalf("PostgreSQL read lost the owner: %+v %v", stored.SubmissionOwner, err)
		}
		gate := testStoredResultGate(harness.investigator)
		for _, surface := range contextfabric.StoredResultSurfaceVocabulary() {
			if decision := gate.Authorize(context.Background(), other, stored, surface); decision.Decision != contextfabric.StoredResultDenied || decision.Reason != contextfabric.StoredResultReasonOwnerMismatch {
				t.Fatalf("%s: decision = %s/%s, want denied/owner_mismatch", surface, decision.Decision, decision.Reason)
			}
			if decision := gate.Authorize(context.Background(), owner, stored, surface); !decision.Admitted() {
				t.Fatalf("%s: the owner is refused: %+v", surface, decision)
			}
		}
	})
}
