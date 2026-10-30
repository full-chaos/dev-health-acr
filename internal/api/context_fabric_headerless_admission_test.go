package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-11997: a create WITHOUT X-ACR-Submission-ID that runs on the
// deployment's model is admitted under a server-minted key and counts
// against the shared Ask Dev limits like a submission; its answer stays
// organization-bearer.

// The codex round-2 repro of #1756 (limits 1/1/1, two header-less creates →
// 200, 200, executions 2, no usage row) now refuses the second create.
func TestAHeaderlessPlatformCreateCountsAgainstTheMonthlyAllowance(t *testing.T) {
	harness, store := newMemorySubmissionHarness(t, 2)
	setSubmissionLimits(harness, contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 1, MaxRunningPerOrg: 1, MonthlyPlatformRunAllowance: 1})
	servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	refused := harness.post(1, harness.tokenB, "", submissionQuestion)
	expectLimitRefusal(t, refused, contractsv1.ErrorCodeAllowanceExhausted, "")
	usage, found, err := store.ReadUsageMonthly(t.Context(), "org_1", harness.clock.Now())
	if err != nil || !found || usage.PlatformRuns != 1 {
		t.Fatalf("usage = %+v found %t err %v, want 1 platform run", usage, found, err)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	if !strings.Contains(harness.logs[0].String(), `"outcome":"admitted_implicit"`) || !strings.Contains(harness.logs[1].String(), `"implicit":true`) {
		t.Fatalf("the implicit admission and its refusal are not on the admission line:\n%s\n%s", harness.logs[0].String(), harness.logs[1].String())
	}
}

// A running header-less create holds a concurrency slot; it is released when
// the run ends (served or failed), so the next create runs.
func TestAHeaderlessPlatformCreateHoldsAndReleasesAConcurrencySlot(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	setSubmissionLimits(harness, contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 1, MaxRunningPerOrg: 4})
	harness.investigator.mode.Store(int32(runBlockThenSave))
	finished := make(chan struct{})
	go func() { harness.post(0, harness.tokenA, "", submissionQuestion); close(finished) }()
	<-harness.investigator.entered
	// Another header-less create, and a submission, of the same principal
	// on the other instance: both over the principal limit.
	expectLimitRefusal(t, harness.post(1, harness.tokenA, "", submissionQuestion), contractsv1.ErrorCodeConcurrencyLimited, "principal")
	expectLimitRefusal(t, harness.post(1, harness.tokenA, "submission-mixed-01", submissionQuestion), contractsv1.ErrorCodeConcurrencyLimited, "principal")
	close(harness.investigator.release)
	<-finished
	harness.investigator.mode.Store(int32(runSave))
	servedResultID(t, harness.post(1, harness.tokenA, "", submissionQuestion))
	harness.investigator.mode.Store(int32(runFail))
	if failed := harness.post(0, harness.tokenA, "", submissionQuestion); failed.Code < 500 {
		t.Fatalf("a failed run answered %d", failed.Code)
	}
	harness.investigator.mode.Store(int32(runSave))
	servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	if got := harness.investigator.executions.Load(); got != 4 {
		t.Fatalf("executions = %d, want 4", got)
	}
	logs := harness.logs[0].String() + harness.logs[1].String()
	if !strings.Contains(logs, `"completion":"implicit_released"`) || !strings.Contains(logs, `"completion":"abandoned_on_error"`) {
		t.Fatalf("the implicit records were not released:\n%s", logs)
	}
	if strings.Contains(logs, `"defect"`) {
		t.Fatalf("an implicit release was logged as a defect:\n%s", logs)
	}
}

// The answer of a header-less create is not bound to its principal: the run
// received no reservation, and another principal of the organization reads
// the result by id, exactly as before.
func TestAHeaderlessAnswerStaysOrganizationBearer(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	resultID := servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	if got := harness.investigator.reservedSeen.Load(); got != 0 {
		t.Fatalf("the header-less run received %d reservations, want none", got)
	}
	if read := harness.get(1, harness.tokenB, resultID); read.Code != http.StatusOK {
		t.Fatalf("another principal's read by id = %d %s, want 200", read.Code, read.Body.String())
	}
}

// Without a submission store (a runtime composed without one; the hosted
// runtime always has one) a header-less create runs uncounted, as before, and
// says so at Warn on the admission line.
func TestAHeaderlessCreateWithoutAStoreRunsAndSaysSo(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.apps[0].runtime.InvestigationSubmissions = nil
	servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	logs := harness.logs[0].String()
	if !strings.Contains(logs, `"level":"WARN"`) || !strings.Contains(logs, `"outcome":"store_unavailable"`) || !strings.Contains(logs, `"error_class":"store_not_configured"`) {
		t.Fatalf("the uncounted header-less create is not on a Warn admission line:\n%s", logs)
	}
}

// admissionRecordingStore is the memory store, recording every admission's
// key, so a test can name the server-minted key of an implicit admission.
type admissionRecordingStore struct {
	*memoryinvestigation.Store
	mu   sync.Mutex
	keys []contextfabric.SubmissionKey
}

func (s *admissionRecordingStore) AdmitSubmission(ctx context.Context, admission contextfabric.SubmissionAdmission, now time.Time) (contextfabric.SubmissionRecord, bool, error) {
	s.mu.Lock()
	s.keys = append(s.keys, admission.Key)
	s.mu.Unlock()
	return s.Store.AdmitSubmission(ctx, admission, now)
}

// An implicit record is admitted under a minted, valid key and is released
// after its run: final, naming no result (its answer is not its record's).
func TestAnImplicitRecordIsReleasedAndNamesNoResult(t *testing.T) {
	var clock *submissionClock
	store := &admissionRecordingStore{Store: memoryinvestigation.NewStoreWithClock(func() time.Time { return clock.Now() })}
	harness := newSubmissionHarness(t, store)
	clock = harness.clock
	servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	if len(store.keys) != 1 || !contextfabric.IsImplicitSubmissionID(store.keys[0].SubmissionID) || !contextfabric.ValidSubmissionID(store.keys[0].SubmissionID) {
		t.Fatalf("admissions %+v, want one implicit key", store.keys)
	}
	record, err := store.GetSubmission(t.Context(), store.keys[0], harness.clock.Now())
	if err != nil || record.State != contextfabric.SubmissionDoneUnsaved || record.ResultID != "" {
		t.Fatalf("the implicit record = %+v (%v), want released (done_unsaved, no result)", record, err)
	}
}

// Round-1 P1: the prefix is an operator marker, not a reserved namespace. A
// client submission id that starts with it is an ordinary submission, as it
// was before this change: admitted, replayed on a retry, its status served.
func TestAClientSubmissionIDWithTheImplicitPrefixIsAnOrdinarySubmission(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	id := contextfabric.ImplicitSubmissionIDPrefix + "client-0001"
	first := servedResultID(t, harness.post(0, harness.tokenA, id, submissionQuestion))
	if again := servedResultID(t, harness.post(1, harness.tokenA, id, submissionQuestion)); again != first {
		t.Fatalf("replay = %s, want %s", again, first)
	}
	if status := harness.status(1, harness.tokenA, id); status.Code != http.StatusOK || !strings.Contains(status.Body.String(), first) {
		t.Fatalf("status = %d %s, want 200 naming %s", status.Code, status.Body.String(), first)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}
