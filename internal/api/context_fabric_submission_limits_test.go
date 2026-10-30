package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-11914: the shared Ask Dev limits through the real route. Each case
// asserts the execution count as well as the refusal.

func setSubmissionLimits(harness *submissionHarness, limits contextfabric.SubmissionLimits) {
	for _, app := range harness.apps {
		app.config.AskDevLimits = limits
	}
}

func expectLimitRefusal(t *testing.T, recorder *httptest.ResponseRecorder, code, limit string) {
	t.Helper()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 %s (body %s)", recorder.Code, code, recorder.Body.String())
	}
	if err := contractcheck.ValidateSerializedStrict("", "error.v1.schema.json", recorder.Body.Bytes()); err != nil {
		t.Fatalf("schema rejects the 429: %v", err)
	}
	var envelope struct {
		Error struct {
			Code      string         `json:"code"`
			Retryable bool           `json:"retryable"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code || !envelope.Error.Retryable {
		t.Fatalf("error = %+v, want retryable %s", envelope.Error, code)
	}
	if got, _ := envelope.Error.Details[contractsv1.ContextFabricConcurrencyLimitDetailsKey].(string); got != limit {
		t.Fatalf("details.limit = %q, want %q", got, limit)
	}
	if seconds, err := strconv.Atoi(recorder.Header().Get("Retry-After")); err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q", recorder.Header().Get("Retry-After"))
	}
}

// Principal A's third concurrent run is refused while the org has room; then
// principal B fills the org and B's next run is refused on the org limit.
// Neither refusal runs anything; a repeat of a running key stays in_progress.
func TestSubmissionConcurrencyLimitsAreSharedAcrossInstances(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	setSubmissionLimits(harness, contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 2, MaxRunningPerOrg: 3})
	harness.investigator.mode.Store(int32(runBlockThenSave))
	var done []chan struct{}
	run := func(app int, token, id string) {
		finished := make(chan struct{})
		done = append(done, finished)
		go func() { harness.post(app, token, id, submissionQuestion); close(finished) }()
		<-harness.investigator.entered
	}
	run(0, harness.tokenA, "submission-limit-a1")
	run(1, harness.tokenA, "submission-limit-a2")
	expectLimitRefusal(t, harness.post(0, harness.tokenA, "submission-limit-a3", submissionQuestion), contractsv1.ErrorCodeConcurrencyLimited, "principal")
	expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-limit-a1", submissionQuestion), contractsv1.ErrorCodeSubmissionInProgress)
	run(1, harness.tokenB, "submission-limit-b1")
	expectLimitRefusal(t, harness.post(0, harness.tokenB, "submission-limit-b2", submissionQuestion), contractsv1.ErrorCodeConcurrencyLimited, "org")
	if got := harness.investigator.executions.Load(); got != 3 {
		t.Fatalf("executions = %d, want 3", got)
	}
	if !strings.Contains(harness.logs[0].String(), `"outcome":"refused_concurrency_principal"`) || !strings.Contains(harness.logs[0].String(), `"outcome":"refused_concurrency_org"`) {
		t.Fatalf("refusals missing from the admission line:\n%s", harness.logs[0].String())
	}
	// A refused key was never admitted: it can be submitted again later.
	close(harness.investigator.release)
	for _, finished := range done {
		<-finished
	}
	harness.investigator.mode.Store(int32(runSave))
	servedResultID(t, harness.post(0, harness.tokenA, "submission-limit-a3", submissionQuestion))
}

// The monthly allowance counts admitted platform-model runs: the run past it
// is refused until the next month, and a repeat of an admitted key is not
// counted again.
func TestSubmissionMonthlyAllowanceIsEnforced(t *testing.T) {
	harness, store := newMemorySubmissionHarness(t, 2)
	setSubmissionLimits(harness, contextfabric.SubmissionLimits{MonthlyPlatformRunAllowance: 2})
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-allow-01", submissionQuestion))
	servedResultID(t, harness.post(1, harness.tokenB, "submission-allow-02", submissionQuestion))
	if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-allow-01", submissionQuestion)); again != first {
		t.Fatalf("replay = %s, want %s", again, first)
	}
	refused := harness.post(0, harness.tokenA, "submission-allow-03", submissionQuestion)
	expectLimitRefusal(t, refused, contractsv1.ErrorCodeAllowanceExhausted, "")
	wantRetry := int(contextfabric.AllowanceRetryAfter(harness.clock.Now()) / time.Second)
	if got := refused.Header().Get("Retry-After"); got != strconv.Itoa(wantRetry) {
		t.Fatalf("Retry-After = %s, want %d (the next UTC month)", got, wantRetry)
	}
	usage, found, err := store.ReadUsageMonthly(t.Context(), "org_1", harness.clock.Now())
	if err != nil || !found || usage.PlatformRuns != 2 {
		t.Fatalf("usage = %+v found %t err %v, want 2", usage, found, err)
	}
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
	harness.clock.Advance(time.Duration(wantRetry+1) * time.Second)
	servedResultID(t, harness.post(0, harness.tokenA, "submission-allow-04", submissionQuestion))
}

// The two 429 bodies, produced by the route itself, are the published
// goldens. ACR_WRITE_SUBMISSION_GOLDENS=1 rewrites them.
func TestSubmissionLimitGoldensAreTheRoutesOwnBodies(t *testing.T) {
	bodies := map[string][]byte{}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		setSubmissionLimits(harness, contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 1, MaxRunningPerOrg: 1})
		harness.investigator.mode.Store(int32(runBlockThenSave))
		finished := make(chan struct{})
		go func() { harness.post(0, harness.tokenA, "submission-golden-l1", submissionQuestion); close(finished) }()
		<-harness.investigator.entered
		bodies[contractsv1.ErrorCodeConcurrencyLimited] = harness.post(0, harness.tokenA, "submission-golden-l2", submissionQuestion).Body.Bytes()
		close(harness.investigator.release)
		<-finished
	}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		setSubmissionLimits(harness, contextfabric.SubmissionLimits{MonthlyPlatformRunAllowance: 1})
		harness.post(0, harness.tokenA, "submission-golden-l3", submissionQuestion)
		bodies[contractsv1.ErrorCodeAllowanceExhausted] = harness.post(0, harness.tokenA, "submission-golden-l4", submissionQuestion).Body.Bytes()
	}
	for _, code := range []string{contractsv1.ErrorCodeConcurrencyLimited, contractsv1.ErrorCodeAllowanceExhausted} {
		name := fmt.Sprintf("error_context_fabric_%s.v1.json", code)
		body := append(bytes.TrimRight(bodies[code], "\n"), '\n')
		if err := contractcheck.ValidateSerializedStrict("", "error.v1.schema.json", body); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := repopath.Path(t, "contracts", "examples", "v1", name)
		if os.Getenv("ACR_WRITE_SUBMISSION_GOLDENS") == "1" {
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		golden, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (regenerate with ACR_WRITE_SUBMISSION_GOLDENS=1)", name, err)
		}
		if !bytes.Equal(golden, body) {
			t.Fatalf("%s differs from the route's body:\n%s\nwant\n%s", name, body, golden)
		}
	}
}

// lastObservation is App app's most recent request observation.
func (h *submissionHarness) lastObservation(app int) observability.SupportSnapshot {
	h.t.Helper()
	sink := h.observed[app]
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.snapshots) == 0 {
		h.t.Fatalf("app %d observed no request", app)
	}
	return sink.snapshots[len(sink.snapshots)-1]
}

// Codex round 2 F2: request observability classifies both shared-limit
// refusals as rate-class denials (denial_class rate_limit), as it does the
// request rate limit, never as unknown; the admitted request before each is
// no denial.
func TestSharedLimitRefusalsAreObservedAsRateLimitDenials(t *testing.T) {
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		setSubmissionLimits(harness, contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 1, MaxRunningPerOrg: 1})
		harness.investigator.mode.Store(int32(runBlockThenSave))
		finished := make(chan struct{})
		go func() { harness.post(0, harness.tokenA, "submission-observe-1", submissionQuestion); close(finished) }()
		<-harness.investigator.entered
		expectLimitRefusal(t, harness.post(0, harness.tokenA, "submission-observe-2", submissionQuestion), contractsv1.ErrorCodeConcurrencyLimited, "principal")
		if got := harness.lastObservation(0).Denial; got != observability.DenialRateLimit {
			t.Fatalf("concurrency_limited observed as denial %q, want %q", got, observability.DenialRateLimit)
		}
		close(harness.investigator.release)
		<-finished
	}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		setSubmissionLimits(harness, contextfabric.SubmissionLimits{MonthlyPlatformRunAllowance: 1})
		servedResultID(t, harness.post(0, harness.tokenA, "submission-observe-3", submissionQuestion))
		if got := harness.lastObservation(0).Denial; got != observability.DenialNone {
			t.Fatalf("an admitted run observed as denial %q, want none", got)
		}
		refused := harness.post(0, harness.tokenA, "submission-observe-4", submissionQuestion)
		if refused.Code != http.StatusTooManyRequests || errorEnvelopeOf(t, refused).Error.Code != contractsv1.ErrorCodeAllowanceExhausted {
			t.Fatalf("answer = %d %s, want 429 allowance_exhausted", refused.Code, refused.Body.String())
		}
		if got := harness.lastObservation(0).Denial; got != observability.DenialRateLimit {
			t.Fatalf("allowance_exhausted observed as denial %q, want %q", got, observability.DenialRateLimit)
		}
	}
}
