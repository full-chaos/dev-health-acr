package pginvestigation_test

import (
	"context"
	"database/sql"
	"errors"
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
	// The row carries no owner it was not saved by, and it does not complete
	// the record either: a done record names only a result bound to its own
	// owner (round-2 F1).
	unbound, err := store.Get(ctx, stranger, second.ReservedResultID)
	require.NoError(t, err)
	require.Nil(t, unbound.SubmissionOwner)
	require.Equal(t, contextfabric.SubmissionRunning, record.State)

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

// The canonical-owner table (CHAOS-12091), on PostgreSQL; memoryinvestigation
// runs the same table.
func TestSubmissionCanonicalOwnerParity(t *testing.T) {
	ctx := context.Background()
	paritytest.RunSubmissionCanonicalOwnerSuite(t, func(t *testing.T) paritytest.SubmissionStore {
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

// Round-1 F2: a Save whose result id already exists completes nothing until
// the existing row is proven to be this result in this organization. An id
// held by ANOTHER organization's row leaves the record running (the save
// fails); a verified same-organization replay completes it.
func TestASaveCompletesItsSubmissionOnlyAfterItsRowIsProven(t *testing.T) {
	ctx := context.Background()
	_, store, _ := twoSubmissionStores(t, ctx)
	owner := submissionPrincipal("user-collision")
	stranger := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-elsewhere", OrgID: "org-elsewhere"}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	saveAs(t, ctx, store, stranger, "result_collide_01")
	collided := admissionFor(t, owner, "submission-collide-01", "result_collide_01", now)
	_, ok, err := store.AdmitSubmission(ctx, collided, now)
	require.NoError(t, err)
	require.True(t, ok)
	err = store.Save(ctx, owner, validResult("result_collide_01"), nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	require.Error(t, err, "a result id another organization holds is not this result")
	record, err := store.GetSubmission(ctx, collided.Key, now)
	require.NoError(t, err)
	require.Equal(t, contextfabric.SubmissionRunning, record.State, "a failed save completed the record")
	require.Empty(t, record.ResultID)

	replayed := admissionFor(t, owner, "submission-replay-01", "result_replay_01", now)
	_, ok, err = store.AdmitSubmission(ctx, replayed, now)
	require.NoError(t, err)
	require.True(t, ok)
	result := validResult("result_replay_01")
	save := func() error {
		return store.Save(ctx, owner, result, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	}
	require.NoError(t, save())
	_, err = store.AbandonSubmission(ctx, replayed.Key, now) // done stays done
	require.NoError(t, err)
	require.NoError(t, save(), "an identical replay is accepted")
	record, err = store.GetSubmission(ctx, replayed.Key, now)
	require.NoError(t, err)
	require.Equal(t, contextfabric.SubmissionDone, record.State)
	require.Equal(t, "result_replay_01", record.ResultID)

	// A verified replay of a row ANOTHER principal of the organization saved
	// (byte-identical, so the immutability check passes) completes nothing:
	// the row is not bound to this record's owner (round-2 F1).
	sameOrgStranger := submissionPrincipal("user-same-org-stranger")
	foreign := admissionFor(t, owner, "submission-replay-02", "result_replay_02", now)
	_, ok, err = store.AdmitSubmission(ctx, foreign, now)
	require.NoError(t, err)
	require.True(t, ok)
	foreignResult := validResult("result_replay_02")
	saveResult := func(principal storage.Principal) error {
		return store.Save(ctx, principal, foreignResult, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	}
	require.NoError(t, saveResult(sameOrgStranger))
	require.NoError(t, saveResult(owner), "the identical bytes replay")
	record, err = store.GetSubmission(ctx, foreign.Key, now)
	require.NoError(t, err)
	require.Equal(t, contextfabric.SubmissionRunning, record.State, "a replay of a row the owner does not own completed the record")
}

// Round-2 F1, the store's side: an OwnedOnly key (a submission's turn) is
// offered a row bound to the reader's own submission only, never an
// unbound row.
func TestFindReusableOwnedOnlyOffersOnlyTheReadersBoundRows(t *testing.T) {
	ctx := context.Background()
	db, store, _ := twoSubmissionStores(t, ctx)
	owner := submissionPrincipal("user-owned-only")
	setCheckpointWatermark(t, ctx, db, owner.OrgID, "linear", "wm-1")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	unbound := reusableResult("result_owned_only_plain", owner.OrgID, "Is the plain owned-only question on track?")
	saveWithReuseSnapshot(t, ctx, store, owner, unbound)
	key := reuseKeyFor(unbound)
	_, ok, _, err := store.FindReusable(ctx, owner, key)
	require.NoError(t, err)
	require.True(t, ok, "control: a header-less turn reuses the unbound row")
	key.OwnedOnly = true
	_, ok, _, err = store.FindReusable(ctx, owner, key)
	require.NoError(t, err)
	require.False(t, ok, "a submission's turn must not be offered an unbound row")

	admission := admissionFor(t, owner, "submission-owned-only", "result_owned_only_bound", now)
	_, admitted, err := store.AdmitSubmission(ctx, admission, now)
	require.NoError(t, err)
	require.True(t, admitted)
	bound := reusableResult(admission.ReservedResultID, owner.OrgID, "Is the bound owned-only question on track?")
	saveWithReuseSnapshot(t, ctx, store, owner, bound)
	boundKey := reuseKeyFor(bound)
	boundKey.OwnedOnly = true
	found, ok, _, err := store.FindReusable(ctx, owner, boundKey)
	require.NoError(t, err)
	require.True(t, ok, "a submission's turn reuses its own principal's bound row")
	require.NotNil(t, found.SubmissionOwner)
}

// CHAOS-11914: the shared limits table, on PostgreSQL.
func TestSubmissionLimitsParity(t *testing.T) {
	ctx := context.Background()
	paritytest.RunSubmissionLimitsSuite(t, func(t *testing.T) paritytest.LimitsStore {
		store, err := pginvestigation.NewStore(newInvestigationTestDatabase(t, ctx))
		require.NoError(t, err)
		return store
	})
}

// Many concurrent admissions of DIFFERENT keys, over two pools, never admit
// more than the limits: the count and the insert are one transaction,
// serialized per organization.
func TestSubmissionLimitsHoldUnderConcurrentAdmissions(t *testing.T) {
	ctx := context.Background()
	db, storeA, storeB := twoSubmissionStores(t, ctx)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	limits := contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 2, MaxRunningPerOrg: 4, MonthlyPlatformRunAllowance: 1000}
	var (
		start, done sync.WaitGroup
		mu          sync.Mutex
		admitted    = map[string]int{}
		refusals    = map[contextfabric.SubmissionRefusal]int{}
		failures    []error
	)
	start.Add(1)
	for index := 0; index < 24; index++ {
		done.Add(1)
		store := storeA
		if index%2 == 1 {
			store = storeB
		}
		subject := fmt.Sprintf("user-load-%d", index%4)
		go func(index int, store *pginvestigation.Store, subject string) {
			defer done.Done()
			start.Wait()
			admission := admissionFor(t, submissionPrincipal(subject), fmt.Sprintf("submission-load-%04d", index), fmt.Sprintf("result_load_%04d", index), now)
			admission.Limits = limits
			_, ok, err := store.AdmitSubmission(ctx, admission, now)
			mu.Lock()
			defer mu.Unlock()
			var refused *contextfabric.SubmissionRefusedError
			switch {
			case errors.As(err, &refused):
				refusals[refused.Refusal]++
			case err != nil:
				failures = append(failures, err)
			case ok:
				admitted[subject]++
			}
		}(index, store, subject)
	}
	start.Done()
	done.Wait()
	require.Empty(t, failures)
	total := 0
	for subject, count := range admitted {
		require.LessOrEqual(t, count, 2, "principal %s over its limit", subject)
		total += count
	}
	require.Equal(t, 4, total, "exactly the org limit is admitted (admitted %v, refused %v)", admitted, refusals)
	require.Equal(t, 20, refusals[contextfabric.SubmissionRefusedPrincipalConcurrency]+refusals[contextfabric.SubmissionRefusedOrgConcurrency])
	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE state = 'running'`).Scan(&rows))
	require.Equal(t, 4, rows, "refused admissions wrote no record")
}

// Codex round 2 F3: the monthly allowance holds under concurrent PLATFORM
// admissions over two pools: with room under both concurrency limits, 24
// concurrent platform runs against an allowance of 5 admit exactly 5, refuse
// 19 as allowance_exhausted, and leave the aggregate at exactly 5 with 5
// running records (a refused admission writes neither).
func TestTheMonthlyAllowanceHoldsUnderConcurrentPlatformAdmissions(t *testing.T) {
	ctx := context.Background()
	db, storeA, storeB := twoSubmissionStores(t, ctx)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	limits := contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 16, MaxRunningPerOrg: 64, MonthlyPlatformRunAllowance: 5}
	var (
		start, done sync.WaitGroup
		mu          sync.Mutex
		admitted    int
		refusals    = map[contextfabric.SubmissionRefusal]int{}
		failures    []error
	)
	start.Add(1)
	for index := 0; index < 24; index++ {
		done.Add(1)
		store := storeA
		if index%2 == 1 {
			store = storeB
		}
		subject := fmt.Sprintf("user-allowance-%d", index%12)
		go func(index int, store *pginvestigation.Store, subject string) {
			defer done.Done()
			start.Wait()
			admission := admissionFor(t, submissionPrincipal(subject), fmt.Sprintf("submission-allowance-%04d", index), fmt.Sprintf("result_allowance_%04d", index), now)
			admission.Limits = limits
			admission.PlatformRun = true
			_, ok, err := store.AdmitSubmission(ctx, admission, now)
			mu.Lock()
			defer mu.Unlock()
			var refused *contextfabric.SubmissionRefusedError
			switch {
			case errors.As(err, &refused):
				refusals[refused.Refusal]++
			case err != nil:
				failures = append(failures, err)
			case ok:
				admitted++
			}
		}(index, store, subject)
	}
	start.Done()
	done.Wait()
	require.Empty(t, failures)
	require.Equal(t, 5, admitted, "exactly the allowance is admitted (refused %v)", refusals)
	require.Equal(t, map[contextfabric.SubmissionRefusal]int{contextfabric.SubmissionRefusedAllowanceExhausted: 19}, refusals)
	usage, found, err := storeA.ReadUsageMonthly(ctx, "org-submission", now)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 5, usage.PlatformRuns, "the aggregate counts the admitted platform runs only")
	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE state = 'running'`).Scan(&rows))
	require.Equal(t, 5, rows, "refused admissions wrote no record")
}

// An aggregate already at the allowance (written back-dated, as an operator
// or an earlier run would have) refuses the next platform run; deleting
// every submission record leaves the aggregate unchanged.
func TestTheUsageAggregateGatesAndOutlivesItsRecords(t *testing.T) {
	ctx := context.Background()
	db, store, _ := twoSubmissionStores(t, ctx)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	principal := submissionPrincipal("user-aggregate")
	_, err := db.ExecContext(ctx, `INSERT INTO acr.context_fabric_usage_monthly (org_id, month, platform_runs, updated_at) VALUES ($1, $2, 5, $3)`, principal.OrgID, contextfabric.UsageMonth(now), now.Add(-24*time.Hour))
	require.NoError(t, err)
	limited := admissionFor(t, principal, "submission-aggregate-1", "result_aggregate_1", now)
	limited.Limits, limited.PlatformRun = contextfabric.SubmissionLimits{MonthlyPlatformRunAllowance: 5}, true
	_, admitted, err := store.AdmitSubmission(ctx, limited, now)
	var refused *contextfabric.SubmissionRefusedError
	require.False(t, admitted)
	require.ErrorAs(t, err, &refused)
	require.Equal(t, contextfabric.SubmissionRefusedAllowanceExhausted, refused.Refusal)
	require.Equal(t, int64(5), refused.Count)
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Sub(now), refused.RetryAfter)

	allowed := admissionFor(t, principal, "submission-aggregate-2", "result_aggregate_2", now)
	allowed.PlatformRun = true // default allowance 1000
	_, admitted, err = store.AdmitSubmission(ctx, allowed, now)
	require.NoError(t, err)
	require.True(t, admitted)
	usage, found, err := store.ReadUsageMonthly(ctx, principal.OrgID, now)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(6), usage.PlatformRuns)
	require.Nil(t, usage.CostMicroUSD)

	_, err = db.ExecContext(ctx, `DELETE FROM acr.context_fabric_investigation_submissions WHERE org_id = $1`, principal.OrgID)
	require.NoError(t, err)
	usage, found, err = store.ReadUsageMonthly(ctx, principal.OrgID, now)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(6), usage.PlatformRuns, "deleting submission records changes the aggregate")
}
