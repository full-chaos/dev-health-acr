package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

// The implicit namespace is the server's: a client's X-ACR-Submission-ID in
// it is refused before anything is admitted.
func TestAClientSubmissionIDInTheImplicitNamespaceIsRefused(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	refused := harness.post(0, harness.tokenA, contextfabric.ImplicitSubmissionIDPrefix+"0123456789abcdef", submissionQuestion)
	if refused.Code != http.StatusBadRequest || errorEnvelopeOf(t, refused).Error.Code != "invalid_request" {
		t.Fatalf("answer = %d %s, want 400 invalid_request", refused.Code, refused.Body.String())
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if !contextfabric.IsImplicitSubmissionID(contextfabric.NewImplicitSubmissionID()) || !contextfabric.ValidSubmissionID(contextfabric.NewImplicitSubmissionID()) {
		t.Fatal("a minted implicit id is not a valid submission id in the implicit namespace")
	}
}
