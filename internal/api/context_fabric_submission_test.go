package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-11912 on the in-memory store: every repeat-key outcome, each through
// the real route and asserting the execution count, not only the status.

func newMemorySubmissionHarness(t *testing.T, apps int) (*submissionHarness, *memoryinvestigation.Store) {
	t.Helper()
	var store *memoryinvestigation.Store
	var clock *submissionClock
	stores := make([]submissionTestStore, apps)
	// The store's own Save stamps completions with the harness clock.
	store = memoryinvestigation.NewStoreWithClock(func() time.Time { return clock.Now() })
	for index := range stores {
		stores[index] = store
	}
	harness := newSubmissionHarness(t, stores...)
	clock = harness.clock
	return harness, store
}

const submissionQuestion = "Most of the work is closed, so why is Ask Dev still not ready to ship?"

func TestSubmissionDoneRepeatReplaysTheStoredResultWithoutRunning(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-done-0001", submissionQuestion))
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	if harness.investigator.reservedSeen.Load() != 1 {
		t.Fatal("the run's context carried no reservation")
	}
	// The same key on either instance replays the same result.
	for app := 0; app < 2; app++ {
		if again := servedResultID(t, harness.post(app, harness.tokenA, "submission-done-0001", submissionQuestion)); again != first {
			t.Fatalf("instance %d replayed %q, want %q", app, again, first)
		}
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions after two repeats = %d, want 1", got)
	}
	if !strings.Contains(harness.logs[1].String(), `"outcome":"replayed"`) {
		t.Fatalf("the replay is not on the admission line:\n%s", harness.logs[1].String())
	}
	// A changed request under the same key is refused and does not run.
	expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-done-0001", submissionQuestion+" And the drivers?"), contractsv1.ErrorCodeSubmissionMismatch)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions after a mismatch = %d, want 1", got)
	}
	// The same submission id under ANOTHER principal is another key.
	servedResultID(t, harness.post(0, harness.tokenB, "submission-done-0001", submissionQuestion+" And the drivers?"))
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2: another principal's key is its own submission", got)
	}
}

func TestSubmissionRunningRepeatIsInProgress(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	done := make(chan string, 1)
	go func() {
		recorder := harness.post(0, harness.tokenA, "submission-running-01", submissionQuestion)
		done <- recorder.Body.String()
	}()
	<-harness.investigator.entered
	expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-running-01", submissionQuestion), contractsv1.ErrorCodeSubmissionInProgress)
	close(harness.investigator.release)
	<-done
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

func TestSubmissionFailedRunIsAbandonedAtOnce(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.investigator.mode.Store(int32(runFail))
	if recorder := harness.post(0, harness.tokenA, "submission-failed-001", submissionQuestion); recorder.Code < 500 {
		t.Fatalf("a failed run answered %d (body %s)", recorder.Code, recorder.Body.String())
	}
	harness.investigator.mode.Store(int32(runSave))
	expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-failed-001", submissionQuestion), contractsv1.ErrorCodeSubmissionAbandoned)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	if !strings.Contains(harness.logs[0].String(), `"completion":"abandoned_on_error"`) {
		t.Fatalf("the abandon is not on the completion line:\n%s", harness.logs[0].String())
	}
}

// Lead ruling 2026-10-29 (plan deviation: abandon at once on a run error):
// a same-key retry after an abandon on a known run error gets exactly the
// answer a retry after an abandon at expiry (a pod that died mid-run) gets:
// 409 submission_abandoned, not retryable, no Retry-After, byte-identical
// bodies. The client is not meant to tell them apart: the plan (§2.7.5,
// recovery) maps both to "Interrupted — ask again". Neither runs again.
func TestAnAbandonOnErrorAndAnAbandonAtExpiryAnswerARetryAlike(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	harness.investigator.mode.Store(int32(runFail))
	harness.post(0, harness.tokenA, "submission-abandon-e", submissionQuestion)
	onError := harness.post(1, harness.tokenA, "submission-abandon-e", submissionQuestion)
	expectSubmissionRefusal(t, onError, contractsv1.ErrorCodeSubmissionAbandoned)

	// The stand-in for a pod that died mid-run: the run never finishes
	// while its record passes expires_at (run budget + 30 s).
	harness.investigator.mode.Store(int32(runBlockThenSave))
	done := make(chan struct{})
	go func() {
		defer close(done)
		harness.post(0, harness.tokenA, "submission-abandon-x", submissionQuestion)
	}()
	<-harness.investigator.entered
	harness.clock.Advance(24 * time.Hour)
	atExpiry := harness.post(1, harness.tokenA, "submission-abandon-x", submissionQuestion)
	close(harness.investigator.release)
	<-done
	expectSubmissionRefusal(t, atExpiry, contractsv1.ErrorCodeSubmissionAbandoned)
	if onError.Code != atExpiry.Code || onError.Body.String() != atExpiry.Body.String() || onError.Header().Get("Retry-After") != atExpiry.Header().Get("Retry-After") {
		t.Fatalf("a retry after an abandon on error = %d %s, after an abandon at expiry = %d %s: they must be identical", onError.Code, onError.Body.String(), atExpiry.Code, atExpiry.Body.String())
	}
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2 (one per key)", got)
	}
}

func TestSubmissionUnsavedRunIsUnsavedOnRetry(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.investigator.mode.Store(int32(runUnsaved))
	servedResultID(t, harness.post(0, harness.tokenA, "submission-unsaved-01", submissionQuestion))
	harness.investigator.mode.Store(int32(runSave))
	expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-unsaved-01", submissionQuestion), contractsv1.ErrorCodeSubmissionUnsaved)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// A record whose run is still going past its budget plus the grace is
// abandoned on read, and the key never runs again; a save that lands after
// still completes it, bound to its owner.
func TestSubmissionPastItsExpiryIsAbandonedAndALateSaveCompletesIt(t *testing.T) {
	harness, store := newMemorySubmissionHarness(t, 1)
	harness.investigator.mode.Store(int32(runBlockThenSave))
	late := make(chan string, 1)
	go func() {
		late <- harness.post(0, harness.tokenA, "submission-expired-01", submissionQuestion).Body.String()
	}()
	<-harness.investigator.entered
	expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-expired-01", submissionQuestion), contractsv1.ErrorCodeSubmissionInProgress)
	// The request budget is 30 s and the grace 30 s: two minutes is past.
	harness.clock.Advance(2 * time.Minute)
	expectSubmissionRefusal(t, harness.post(0, harness.tokenA, "submission-expired-01", submissionQuestion), contractsv1.ErrorCodeSubmissionAbandoned)
	close(harness.investigator.release)
	<-late
	replayed := servedResultID(t, harness.post(0, harness.tokenA, "submission-expired-01", submissionQuestion))
	stored, err := store.Get(context.Background(), principalOfResult(t, harness), replayed)
	if err != nil || stored.SubmissionOwner == nil {
		t.Fatalf("the late save is unbound: %+v, %v", stored.SubmissionOwner, err)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// principalOfResult is the principal the counting investigator last ran as.
func principalOfResult(t *testing.T, harness *submissionHarness) storage.Principal {
	t.Helper()
	principal, ok := harness.investigator.lastPrincipal.Load().(storage.Principal)
	if !ok {
		t.Fatal("the investigator never ran")
	}
	return principal
}

func TestSubmissionHeaderIsValidatedBeforeAdmission(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	for _, ids := range [][]string{{"short"}, {"has a space in it"}, {"submission-one-0001", "submission-two-0001"}} {
		request := investigationRequest(t, harness.tokenA)
		for _, id := range ids {
			request.Header.Add(contractsv1.ContextFabricSubmissionIDHeader, id)
		}
		recorder := httptest.NewRecorder()
		harness.apps[0].Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%v: status = %d, want 400", ids, recorder.Code)
		}
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
}

// Without a submission store a request that names a submission is refused,
// never run unbound; one that names none is unchanged.
func TestSubmissionWithoutAStoreIsRefusedNotRunUnbound(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.apps[0].runtime.InvestigationSubmissions = nil
	recorder := harness.post(0, harness.tokenA, "submission-nostore-01", submissionQuestion)
	if recorder.Code != http.StatusServiceUnavailable || errorEnvelopeOf(t, recorder).Error.Code != "store_unavailable" {
		t.Fatalf("status = %d body %s, want 503 store_unavailable", recorder.Code, recorder.Body.String())
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	if harness.investigator.reservedSeen.Load() != 0 {
		t.Fatal("a request with no submission ran under a reservation")
	}
}

// INV-1 on the result-by-id surface: a submission result is the unknown-id
// answer for another principal of the same org with the same grant; a
// result of a request with no submission keeps organization-bearer reads.
func TestASubmissionResultIsReadByItsOwnerOnly(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	bound := servedResultID(t, harness.post(0, harness.tokenA, "submission-owner-0001", submissionQuestion))
	if recorder := harness.get(1, harness.tokenA, bound); recorder.Code != http.StatusOK {
		t.Fatalf("owner read = %d", recorder.Code)
	}
	denied := harness.get(1, harness.tokenB, bound)
	unknown := harness.get(1, harness.tokenB, "result_never_existed_01")
	if denied.Code != http.StatusNotFound || !sameErrorBody(t, denied, unknown) {
		t.Fatalf("another principal read = %d %s, want the unknown-id answer %d %s", denied.Code, denied.Body.String(), unknown.Code, unknown.Body.String())
	}
	if !strings.Contains(harness.logs[1].String(), `"reason":"owner_mismatch"`) {
		t.Fatalf("the refusal is not on the authorization line:\n%s", harness.logs[1].String())
	}
	unbound := servedResultID(t, harness.post(0, harness.tokenA, "", submissionQuestion))
	if recorder := harness.get(1, harness.tokenB, unbound); recorder.Code != http.StatusOK {
		t.Fatalf("an unbound result read by another principal = %d, want 200", recorder.Code)
	}
}

// sameErrorBody compares two error bodies with request_id removed.
func sameErrorBody(t *testing.T, a, b *httptest.ResponseRecorder) bool {
	t.Helper()
	strip := func(raw []byte) string {
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		delete(decoded, "request_id")
		out, _ := json.Marshal(decoded)
		return string(out)
	}
	return strip(a.Body.Bytes()) == strip(b.Body.Bytes())
}

// The four 409 bodies, produced by the route itself, are the published
// goldens and validate against error.v1. ACR_WRITE_SUBMISSION_GOLDENS=1
// rewrites them.
func TestSubmissionRefusalGoldensAreTheRoutesOwnBodies(t *testing.T) {
	bodies := map[string][]byte{}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		servedResultID(t, harness.post(0, harness.tokenA, "submission-golden-01", submissionQuestion))
		bodies[contractsv1.ErrorCodeSubmissionMismatch] = harness.post(0, harness.tokenA, "submission-golden-01", submissionQuestion+" Why?").Body.Bytes()
	}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.investigator.mode.Store(int32(runBlockThenSave))
		done := make(chan struct{})
		go func() { harness.post(0, harness.tokenA, "submission-golden-02", submissionQuestion); close(done) }()
		<-harness.investigator.entered
		bodies[contractsv1.ErrorCodeSubmissionInProgress] = harness.post(0, harness.tokenA, "submission-golden-02", submissionQuestion).Body.Bytes()
		close(harness.investigator.release)
		<-done
	}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.investigator.mode.Store(int32(runUnsaved))
		harness.post(0, harness.tokenA, "submission-golden-03", submissionQuestion)
		bodies[contractsv1.ErrorCodeSubmissionUnsaved] = harness.post(0, harness.tokenA, "submission-golden-03", submissionQuestion).Body.Bytes()
	}
	{
		harness, _ := newMemorySubmissionHarness(t, 1)
		harness.investigator.mode.Store(int32(runFail))
		harness.post(0, harness.tokenA, "submission-golden-04", submissionQuestion)
		bodies[contractsv1.ErrorCodeSubmissionAbandoned] = harness.post(0, harness.tokenA, "submission-golden-04", submissionQuestion).Body.Bytes()
	}
	for _, code := range contractsv1.ContextFabricSubmissionErrorCodes() {
		name := "error_context_fabric_" + code + ".v1.json"
		body := bytes.TrimRight(bodies[code], "\n")
		body = append(body, '\n')
		if err := contractcheck.ValidateSerializedStrict("", "error.v1.schema.json", body); err != nil {
			t.Fatalf("%s: schema rejects the body: %v", name, err)
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

// Round-1 F1: consumer.surface changes what the engine does (MCP-only window
// commit, reuse bypass, structure authority), so the same submission id with
// another surface is a different request: submission_mismatch, never a
// replay. Name and version are who is asking, not what: they do not change
// the digest.
func TestSubmissionDigestBindsTheConsumerSurface(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	post := func(surface, name string) *httptest.ResponseRecorder {
		body := investigationRequestBody()
		body.Question = submissionQuestion
		body.Consumer.Surface, body.Consumer.Name = surface, name
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
		request.Header.Set("Authorization", "Bearer "+harness.tokenA)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, "submission-surface-01")
		recorder := httptest.NewRecorder()
		harness.apps[0].Handler().ServeHTTP(recorder, request)
		return recorder
	}
	first := servedResultID(t, post("workbench", "context-fabric-workbench"))
	expectSubmissionRefusal(t, post("mcp", "context-fabric-workbench"), contractsv1.ErrorCodeSubmissionMismatch)
	if again := servedResultID(t, post("workbench", "another-client-name")); again != first {
		t.Fatalf("a changed consumer name replayed %s, want %s", again, first)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// Round-3 F1: a repeat of an admitted submission runs no model, so the
// service-model gate (a decision about EXECUTING on the deployment's model)
// does not decide it: after the model entitlement is withdrawn, the owner's
// retry of a done submission still replays the stored result, re-authorized
// by the stored-result gate like a read by id, and a retry of a running one
// is still in_progress. A NEW submission is refused by the model gate before
// anything is admitted.
func TestASubmissionRepeatIsAnsweredBeforeTheServiceModelGate(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-modelgate-1", submissionQuestion))
	harness.denied.Store("acr_service_model", true)
	harness.denied.Store("platform_model_fallback", true)
	if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-modelgate-1", submissionQuestion)); again != first {
		t.Fatalf("replay = %s, want %s", again, first)
	}
	expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-modelgate-1", submissionQuestion+" Why?"), contractsv1.ErrorCodeSubmissionMismatch)
	refused := harness.post(0, harness.tokenA, "submission-modelgate-2", submissionQuestion)
	if refused.Code != http.StatusForbidden || errorEnvelopeOf(t, refused).Error.Code != "feature_not_enabled" {
		t.Fatalf("a new submission without the model entitlement = %d %s, want 403 feature_not_enabled", refused.Code, refused.Body.String())
	}
	// The refused new submission admitted nothing: with the entitlement
	// back, the same key runs.
	harness.denied.Delete("acr_service_model")
	harness.denied.Delete("platform_model_fallback")
	servedResultID(t, harness.post(0, harness.tokenA, "submission-modelgate-2", submissionQuestion))
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
}

// Confirmation pass C1: a repeat of an admitted submission executes nothing
// and reads only the submission and result stores, like a read by id, so the
// ClickHouse readiness gate does not decide it. With ClickHouse down the
// owner's retry of a done submission still replays its stored result and a
// changed request is still a mismatch. A NEW submission still meets the gate
// and admits nothing there.
func TestASubmissionRepeatIsAnsweredBeforeTheDataStoreReadinessGate(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-ready-1", submissionQuestion))
	harness.clickhouseDown.Store(true)
	if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-ready-1", submissionQuestion)); again != first {
		t.Fatalf("replay with ClickHouse down = %s, want %s", again, first)
	}
	expectSubmissionRefusal(t, harness.post(1, harness.tokenA, "submission-ready-1", submissionQuestion+" Why?"), contractsv1.ErrorCodeSubmissionMismatch)
	expectErrorCode(t, harness.post(0, harness.tokenA, "submission-ready-2", submissionQuestion), http.StatusServiceUnavailable, "store_unavailable")
	harness.clickhouseDown.Store(false)
	// The refused new submission admitted nothing: with ClickHouse back,
	// the key runs once.
	servedResultID(t, harness.post(0, harness.tokenA, "submission-ready-2", submissionQuestion))
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
}

// Confirmation pass C2: the same for the create handler built over no
// investigator (the runtime is not configured): a repeat still replays or
// is still a mismatch; a new submission is 503 upstream_unavailable and
// admits nothing.
func TestASubmissionRepeatIsAnsweredBeforeTheInvestigatorGate(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-ready-1", submissionQuestion))
	unconfigured := harness.unconfiguredCreateHandler(1)
	if again := servedResultID(t, harness.postTo(unconfigured, harness.tokenA, "submission-ready-1", submissionQuestion)); again != first {
		t.Fatalf("replay with no investigator = %s, want %s", again, first)
	}
	expectSubmissionRefusal(t, harness.postTo(unconfigured, harness.tokenA, "submission-ready-1", submissionQuestion+" Why?"), contractsv1.ErrorCodeSubmissionMismatch)
	expectErrorCode(t, harness.postTo(unconfigured, harness.tokenA, "submission-ready-3", submissionQuestion), http.StatusServiceUnavailable, "upstream_unavailable")
	servedResultID(t, harness.post(0, harness.tokenA, "submission-ready-3", submissionQuestion))
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
}

// Lead ruling 2026-10-29, condition 2 (INV-1, North Star 18): answered
// before the readiness gates, a done replay is still authorized live by the
// stored-result gate, as GET by id is. After the grant is withdrawn, the
// owner's replay gets exactly the answer GET by id gets (the existence-
// neutral not-found), whether the runtime is ready, ClickHouse is down, or
// no investigator is configured; with the grant back it replays again.
func TestASubmissionReplayIsAuthorizedLiveLikeAReadByID(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-live-01", submissionQuestion))
	harness.grantWithdrawn.Store(true)
	byID := harness.get(1, harness.tokenA, first)
	if byID.Code != http.StatusNotFound {
		t.Fatalf("GET by id after the grant was withdrawn = %d %s, want 404", byID.Code, byID.Body.String())
	}
	unconfigured := harness.unconfiguredCreateHandler(1)
	for _, condition := range []struct {
		name   string
		before func()
		post   func() *httptest.ResponseRecorder
	}{
		{"ready", func() {}, func() *httptest.ResponseRecorder {
			return harness.post(1, harness.tokenA, "submission-live-01", submissionQuestion)
		}},
		{"clickhouse down", func() { harness.clickhouseDown.Store(true) }, func() *httptest.ResponseRecorder {
			return harness.post(1, harness.tokenA, "submission-live-01", submissionQuestion)
		}},
		{"no investigator", func() { harness.clickhouseDown.Store(false) }, func() *httptest.ResponseRecorder {
			return harness.postTo(unconfigured, harness.tokenA, "submission-live-01", submissionQuestion)
		}},
	} {
		condition.before()
		replay := condition.post()
		if replay.Code != byID.Code || errorEnvelopeOf(t, replay).Error.Code != errorEnvelopeOf(t, byID).Error.Code {
			t.Fatalf("%s: replay after the grant was withdrawn = %d %s, want GET by id's %d %s", condition.name, replay.Code, replay.Body.String(), byID.Code, byID.Body.String())
		}
	}
	harness.grantWithdrawn.Store(false)
	if again := servedResultID(t, harness.post(1, harness.tokenA, "submission-live-01", submissionQuestion)); again != first {
		t.Fatalf("replay with the grant back = %s, want %s", again, first)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// Lead ruling 2026-10-29, condition 2: the key includes the owner, so another
// principal's request on A's submission id is a new key of its own and never
// reaches A's record. Whatever the runtime state, principal B gets the same
// answer on A's id as on an id nobody used (no 503-versus-404 or other
// difference that tells B the id exists), never A's result; and GET by id of
// A's result answers B not found in every state.
func TestANonOwnerCannotTellAnotherPrincipalsSubmissionExists(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 2)
	first := servedResultID(t, harness.post(0, harness.tokenA, "submission-owner-01", submissionQuestion))
	unconfigured := harness.unconfiguredCreateHandler(1)
	for index, condition := range []struct {
		name  string
		setup func()
		post  func(submissionID string) *httptest.ResponseRecorder
	}{
		{"clickhouse down", func() { harness.clickhouseDown.Store(true) }, func(id string) *httptest.ResponseRecorder {
			return harness.post(1, harness.tokenB, id, submissionQuestion)
		}},
		{"no investigator", func() { harness.clickhouseDown.Store(false) }, func(id string) *httptest.ResponseRecorder {
			return harness.postTo(unconfigured, harness.tokenB, id, submissionQuestion)
		}},
		{"ready", func() {}, func(id string) *httptest.ResponseRecorder {
			return harness.post(1, harness.tokenB, id, submissionQuestion)
		}},
	} {
		condition.setup()
		onA := condition.post("submission-owner-01")
		unused := condition.post(fmt.Sprintf("submission-unused-%02d", index))
		if onA.Code != unused.Code || submissionErrorCode(t, onA) != submissionErrorCode(t, unused) {
			t.Fatalf("%s: B on A's id = %d %s, B on an unused id = %d %s: they must not differ", condition.name, onA.Code, onA.Body.String(), unused.Code, unused.Body.String())
		}
		if strings.Contains(onA.Body.String(), first) {
			t.Fatalf("%s: B was served A's result %s", condition.name, first)
		}
		if byID := harness.get(1, harness.tokenB, first); byID.Code != http.StatusNotFound {
			t.Fatalf("%s: B's GET of A's result = %d %s, want 404", condition.name, byID.Code, byID.Body.String())
		}
	}
	// A ran once; B ran once on A's id and once on the unused id, both only
	// when the runtime was ready.
	if got := harness.investigator.executions.Load(); got != 3 {
		t.Fatalf("executions = %d, want 3", got)
	}
}
