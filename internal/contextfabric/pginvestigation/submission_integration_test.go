package pginvestigation_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-11912: the submission store on real PostgreSQL. Each assertion is on
// the state the store exists to reach (the record, the stored owner, the
// number of admissions), never only on a nil error.

// twoSubmissionStores is two Stores over two connection pools to one
// database: two app instances.
func twoSubmissionStores(t *testing.T, ctx context.Context) (*sql.DB, *pginvestigation.Store, *pginvestigation.Store) {
	t.Helper()
	server := sharedPostgresFixture(t)
	dsn := server.dsnFor(server.createDatabase(t, ctx))
	var stores []*pginvestigation.Store
	var first *sql.DB
	for index := 0; index < 2; index++ {
		db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		if first == nil {
			first = db
		}
		store, err := pginvestigation.NewStore(db, pginvestigation.WithAnswerReuse(time.Hour))
		require.NoError(t, err)
		stores = append(stores, store)
	}
	return first, stores[0], stores[1]
}

func submissionPrincipal(subject string) storage.Principal {
	return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: subject, OrgID: "org-submission"}
}

func admissionFor(t *testing.T, principal storage.Principal, submissionID, reserved string, now time.Time) contextfabric.SubmissionAdmission {
	t.Helper()
	key, err := contextfabric.SubmissionKeyFor(principal, submissionID)
	require.NoError(t, err)
	return contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 7), ReservedResultID: reserved, ExpiresAt: now.Add(time.Minute)}
}

func saveAs(t *testing.T, ctx context.Context, store *pginvestigation.Store, principal storage.Principal, resultID string) {
	t.Helper()
	require.NoError(t, store.Save(ctx, principal, validResult(resultID), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
}

// Of many concurrent admissions of one key, over two pools, exactly one
// inserts; every other one reads that record and its reserved id.
func TestSubmissionAdmissionRaceAdmitsExactlyOne(t *testing.T) {
	ctx := context.Background()
	db, storeA, storeB := twoSubmissionStores(t, ctx)
	principal := submissionPrincipal("user-race")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	const contenders = 16
	var (
		start    sync.WaitGroup
		done     sync.WaitGroup
		mu       sync.Mutex
		admitted []string
		records  []contextfabric.SubmissionRecord
		failures []error
	)
	start.Add(1)
	for index := 0; index < contenders; index++ {
		done.Add(1)
		store := storeA
		if index%2 == 1 {
			store = storeB
		}
		go func(index int, store *pginvestigation.Store) {
			defer done.Done()
			start.Wait()
			record, ok, err := store.AdmitSubmission(ctx, admissionFor(t, principal, "submission-race-0001", fmt.Sprintf("result_race_%04d", index), now), now)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			records = append(records, record)
			if ok {
				admitted = append(admitted, record.ReservedResultID)
			}
		}(index, store)
	}
	start.Done()
	done.Wait()
	require.Empty(t, failures)
	require.Len(t, admitted, 1, "exactly one admission of the key")
	require.Len(t, records, contenders)
	for _, record := range records {
		require.Equal(t, admitted[0], record.ReservedResultID, "every contender reads the winner's record")
		require.Equal(t, contextfabric.SubmissionRunning, record.State)
	}
	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE submission_id = 'submission-race-0001'`).Scan(&rows))
	require.Equal(t, 1, rows, "one record for the key")
}

// Save completes the submission whose reserved id it saves, in its own
// statement, and binds the row to that owner; another principal's save of
// the same id binds nothing.
func TestSaveCompletesItsSubmissionAndBindsTheOwner(t *testing.T) {
	ctx := context.Background()
	_, store, other := twoSubmissionStores(t, ctx)
	owner := submissionPrincipal("user-owner")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	admission := admissionFor(t, owner, "submission-save-0001", "result_save_bound_01", now)
	_, ok, err := store.AdmitSubmission(ctx, admission, now)
	require.NoError(t, err)
	require.True(t, ok)

	saveAs(t, ctx, other, owner, admission.ReservedResultID)
	record, err := store.GetSubmission(ctx, admission.Key, now)
	require.NoError(t, err)
	require.Equal(t, contextfabric.SubmissionDone, record.State)
	require.Equal(t, admission.ReservedResultID, record.ResultID)
	stored, err := store.Get(ctx, owner, admission.ReservedResultID)
	require.NoError(t, err)
	require.NotNil(t, stored.SubmissionOwner)
	require.Equal(t, contextfabric.SubmissionOwner{AuthenticationMethod: string(storage.AuthenticationMethodWebAssertion), Subject: "user-owner"}, *stored.SubmissionOwner)

	// The bound row is the unknown-id answer for another principal of the
	// org on every surface the gate decides.
	stranger := submissionPrincipal("user-stranger")
	for _, surface := range contextfabric.StoredResultSurfaceVocabulary() {
		decision := contextfabric.NewStoredResultGate(nil).Authorize(ctx, stranger, stored, surface)
		require.Equal(t, contextfabric.StoredResultDenied, decision.Decision, surface)
		require.Equal(t, contextfabric.StoredResultReasonOwnerMismatch, decision.Reason, surface)
	}

	// Another principal saving a reserved id binds nothing and completes
	// nothing.
	second := admissionFor(t, owner, "submission-save-0002", "result_save_bound_02", now)
	_, ok, err = store.AdmitSubmission(ctx, second, now)
	require.NoError(t, err)
	require.True(t, ok)
	saveAs(t, ctx, store, stranger, second.ReservedResultID)
	record, err = store.GetSubmission(ctx, second.Key, now)
	require.NoError(t, err)
	// The stored row does complete the record by the reconcile rule (the
	// reserved id exists); what it must not do is carry an owner it was not
	// saved by.
	unbound, err := store.Get(ctx, stranger, second.ReservedResultID)
	require.NoError(t, err)
	require.Nil(t, unbound.SubmissionOwner)
	require.Equal(t, contextfabric.SubmissionDone, record.State)

	// A save with no reservation is unbound.
	saveAs(t, ctx, store, owner, "result_save_plain_01")
	plain, err := store.Get(ctx, owner, "result_save_plain_01")
	require.NoError(t, err)
	require.Nil(t, plain.SubmissionOwner)
}

// A running record whose reserved result is stored (the completion was lost:
// a save by a build without it, or a rolled-back completion) is completed on
// read and by the sweeper.
func TestARunningRecordWithAStoredResultIsCompletedOnReadAndBySweep(t *testing.T) {
	ctx := context.Background()
	db, store, _ := twoSubmissionStores(t, ctx)
	owner := submissionPrincipal("user-reconcile")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, path := range []string{"read", "sweep"} {
		admission := admissionFor(t, owner, "submission-reconcile-"+path, "result_reconcile_"+path, now)
		_, ok, err := store.AdmitSubmission(ctx, admission, now)
		require.NoError(t, err)
		require.True(t, ok)
		saveAs(t, ctx, store, owner, admission.ReservedResultID)
		// Lose the completion.
		_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_investigation_submissions SET state = 'running', result_id = NULL WHERE submission_id = $1`, admission.Key.SubmissionID)
		require.NoError(t, err)
		if path == "sweep" {
			sweep, err := store.SweepSubmissions(ctx, now)
			require.NoError(t, err)
			require.Equal(t, int64(1), sweep.Completed)
			require.Equal(t, int64(0), sweep.Abandoned)
		}
		record, err := store.GetSubmission(ctx, admission.Key, now)
		require.NoError(t, err)
		require.Equal(t, contextfabric.SubmissionDone, record.State, path)
		require.Equal(t, admission.ReservedResultID, record.ResultID, path)
	}
}

// The shared submission behavior table, on PostgreSQL; memoryinvestigation
// runs the same table, so the adapters cannot disagree.
func TestSubmissionParity(t *testing.T) {
	ctx := context.Background()
	paritytest.RunSubmissionSuite(t, func(t *testing.T) paritytest.SubmissionStore {
		store, err := pginvestigation.NewStore(newInvestigationTestDatabase(t, ctx))
		require.NoError(t, err)
		return store
	})
}

// Reuse never crosses principals: a row bound to A's submission is a reuse
// candidate for A only; an unbound row stays a candidate for everyone in the
// organization.
func TestFindReusableNeverOffersAnotherPrincipalsSubmissionResult(t *testing.T) {
	ctx := context.Background()
	db, store, _ := twoSubmissionStores(t, ctx)
	owner := submissionPrincipal("user-reuse-owner")
	stranger := submissionPrincipal("user-reuse-stranger")
	setCheckpointWatermark(t, ctx, db, owner.OrgID, "linear", "wm-1")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	admission := admissionFor(t, owner, "submission-reuse-0001", "result_reuse_bound01", now)
	_, ok, err := store.AdmitSubmission(ctx, admission, now)
	require.NoError(t, err)
	require.True(t, ok)
	bound := reusableResult(admission.ReservedResultID, owner.OrgID, "Is the bound question on track?")
	saveWithReuseSnapshot(t, ctx, store, owner, bound)

	found, ok, _, err := store.FindReusable(ctx, owner, reuseKeyFor(bound))
	require.NoError(t, err)
	require.True(t, ok, "the owner reuses its own answer")
	require.NotNil(t, found.SubmissionOwner)
	_, ok, reason, err := store.FindReusable(ctx, stranger, reuseKeyFor(bound))
	require.NoError(t, err)
	require.False(t, ok, "another principal must not be offered the bound answer")
	require.Equal(t, contextfabric.ReuseMissNoCandidate, reason)

	unbound := reusableResult("result_reuse_plain01", owner.OrgID, "Is the plain question on track?")
	saveWithReuseSnapshot(t, ctx, store, owner, unbound)
	found, ok, _, err = store.FindReusable(ctx, stranger, reuseKeyFor(unbound))
	require.NoError(t, err)
	require.True(t, ok, "an unbound answer keeps organization reuse")
	require.Nil(t, found.SubmissionOwner)
}
