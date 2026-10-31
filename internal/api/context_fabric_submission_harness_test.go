package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// The CHAOS-11912 harness: N App instances (one per store given) sharing one
// credential store, two principals of one organization with the same grant,
// one settable clock, and an investigator that counts executions and saves
// the way the engine does -- under the id its context reserved.

// submissionTestStore is what one App instance is given: the result store,
// its submission store and the engine's unsaved marker, one object.
type submissionTestStore interface {
	contextfabric.InvestigationResultStore
	contextfabric.SubmissionStore
	contextfabric.SubmissionUnsavedMarker
}

type submissionClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *submissionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *submissionClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// submissionRunMode is what the counting investigator does with a run.
type submissionRunMode int32

const (
	// runSave saves the answer under the reserved id and serves it.
	runSave submissionRunMode = iota
	// runUnsaved serves the answer without storing it and writes
	// done_unsaved, as the engine's payload-rejected terminal does.
	runUnsaved
	// runFail ends the run with an error and saves nothing.
	runFail
	// runSaveThenPanic saves, then dies before anything else happens: the
	// in-process stand-in for a crash between the save and the response.
	runSaveThenPanic
	// runBlockThenSave signals entered, waits for release, then saves.
	runBlockThenSave
)

type countingSubmissionInvestigator struct {
	store      submissionTestStore
	clock      *submissionClock
	mode       atomic.Int32
	executions atomic.Int64
	// reservedSeen counts runs whose context carried a reservation.
	reservedSeen atomic.Int64
	// lastPrincipal is the principal of the last run.
	lastPrincipal atomic.Value
	entered       chan struct{}
	release       chan struct{}
}

func newCountingSubmissionInvestigator(store submissionTestStore, clock *submissionClock) *countingSubmissionInvestigator {
	return &countingSubmissionInvestigator{store: store, clock: clock, entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (f *countingSubmissionInvestigator) Investigate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	f.executions.Add(1)
	f.lastPrincipal.Store(principal)
	resultID, _, reserved := contextfabric.MintReservedResultID(ctx)
	if reserved {
		f.reservedSeen.Add(1)
	} else {
		resultID = contextfabric.NewInvestigationResultID()
	}
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
	save := func() error {
		return f.store.Save(ctx, principal, result, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	}
	switch submissionRunMode(f.mode.Load()) {
	case runUnsaved:
		if _, err := f.store.MarkSubmissionUnsaved(ctx, principal, resultID, f.clock.Now()); err != nil {
			return contextfabric.InvestigationResult{}, err
		}
		return result, nil
	case runFail:
		return contextfabric.InvestigationResult{}, errors.Join(contextfabric.ErrUnavailable, errors.New("injected run failure"))
	case runSaveThenPanic:
		if err := save(); err != nil {
			return contextfabric.InvestigationResult{}, err
		}
		panic("injected crash after the save")
	case runBlockThenSave:
		f.entered <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			return contextfabric.InvestigationResult{}, ctx.Err()
		}
	}
	if err := save(); err != nil {
		return contextfabric.InvestigationResult{}, err
	}
	return result, nil
}

// submissionTestRequestID is a well-formed X-Request-ID (req_ and 32 hex),
// so the refusal goldens carry a fixed request id.
const submissionTestRequestID = "req_00000000000000000000000000011912"

type submissionHarness struct {
	t *testing.T
	// denied names entitlements the harness's entitlement service refuses
	// from now on (all are granted by default).
	denied sync.Map
	// clickhouseDown makes every App's ClickHouse data-store check fail.
	clickhouseDown atomic.Bool
	// grantWithdrawn makes the live graph grant every stored subject to
	// another repository, so the stored-result gate denies the test
	// credentials' reads from then on.
	grantWithdrawn atomic.Bool
	// graphDown makes the live graph fail every stored-subject decision, so
	// the stored-result gate answers unavailable (CHAOS-11948).
	graphDown atomic.Bool
	clock     *submissionClock
	apps      []*App
	// observed is each App\'s request observations.
	observed     []*snapshotSink
	logs         []*bytes.Buffer
	tokenA       string
	tokenB       string
	investigator *countingSubmissionInvestigator
	// credentials and devices are the shared credential and device
	// authorization stores, for tests that issue a user-approved credential.
	credentials *storage.CredentialLifecycle
	devices     storage.DeviceAuthorizationStore
}

// newSubmissionHarness builds one App per store. Every App shares the
// credential store; the investigator saves through stores[0] (one database
// for PostgreSQL, one map for memory).
func newSubmissionHarness(t *testing.T, stores ...submissionTestStore) *submissionHarness {
	t.Helper()
	return newSubmissionHarnessWithCredentials(t, nil, "org_1", stores...)
}

// newSubmissionHarnessWithCredentials is newSubmissionHarness over the given
// credential store (nil: a memory one). CHAOS-12093 passes a PostgreSQL store,
// the only one that can hold a browser-class row before an issuance path
// derives that class. orgID is the organization of the harness's credentials
// (a PostgreSQL credential row needs a UUID).
func newSubmissionHarnessWithCredentials(t *testing.T, credentials *storage.CredentialLifecycle, orgID string, stores ...submissionTestStore) *submissionHarness {
	t.Helper()
	clock := &submissionClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	audit := memory.NewAuditStore()
	if credentials == nil {
		credentials = newMemoryCredentialLifecycle(t, audit, clock.now)
	}
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	harness := &submissionHarness{
		t: t, clock: clock,
		tokenA:       issueOrgCredential(t, credentials, orgID, clock.now, []string{auth.ScopeContextRead}, []string{hostedTestRepository}),
		tokenB:       issueOrgCredential(t, credentials, orgID, clock.now, []string{auth.ScopeContextRead}, []string{hostedTestRepository}),
		investigator: newCountingSubmissionInvestigator(stores[0], clock),
		credentials:  credentials, devices: devices,
	}
	for _, store := range stores {
		logs := &bytes.Buffer{}
		manager, err := limits.NewManager(limits.Options{Now: clock.Now, PerOrgConcurrency: 64, Policies: limits.PolicySet{
			Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 10_000},
			Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 10_000, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		sink := &snapshotSink{}
		hooks := observability.NewHooks(sink, nil)
		app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 30 * time.Second}, Dependencies{
			Capabilities: StaticCapabilitiesProvider{Now: clock.Now, Value: hostedCapabilities()}, Limits: manager, Now: clock.Now, Observability: &hooks,
			Runtime: &RuntimeDependencies{
				Credentials: credentials, Audit: audit, Entitlements: EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
					_, refused := harness.denied.Load(entitlement)
					return !refused, nil
				}),
				Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
				DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
				DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(clock.Now)),
				ReadinessChecks:            exactRuntimeChecks(),
				DataStoreChecks: []ReadinessCheck{CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error {
					if harness.clickhouseDown.Load() {
						return errors.New("clickhouse is down")
					}
					return nil
				}}},
				Investigator:             harness.investigator,
				InvestigationResults:     store,
				StoredResultGate:         contextfabric.NewStoredResultGate(withdrawableGrantGraph{withdrawn: &harness.grantWithdrawn, down: &harness.graphDown}),
				InvestigationSubmissions: store,
				InvestigationThreads:     threadStoreOf(store),
				ResultFeedback:           feedbackStoreOf(store),
			},
		}, testLogger(logs))
		if err != nil {
			t.Fatal(err)
		}
		harness.apps = append(harness.apps, app)
		harness.observed = append(harness.observed, sink)
		harness.logs = append(harness.logs, logs)
	}
	return harness
}

// withdrawableGrantGraph is the harness's live graph: every stored subject is
// granted to the test credentials' repository until withdrawn, then to
// another one.
type withdrawableGrantGraph struct {
	subjectNodeGraph
	withdrawn *atomic.Bool
	down      *atomic.Bool
}

// threadStoreOf is the store's thread side, nil when it has none.
func threadStoreOf(store submissionTestStore) contextfabric.ThreadStore {
	threads, _ := store.(contextfabric.ThreadStore)
	return threads
}

func feedbackStoreOf(store submissionTestStore) contextfabric.ResultFeedbackStore {
	feedback, _ := store.(contextfabric.ResultFeedbackStore)
	return feedback
}

func (g withdrawableGrantGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	if g.down != nil && g.down.Load() {
		return nil, errors.Join(contextfabric.ErrUnavailable, errors.New("injected graph failure"))
	}
	grant := []string{hostedTestRepository}
	if g.withdrawn.Load() {
		grant = []string{"acme/another-repository"}
	}
	return subjectNodeGraph{grant: grant}.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

// post sends one create-investigation request to app index, as token, with
// the submission id (none when ""), and the question.
func (h *submissionHarness) post(app int, token, submissionID, question string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postThreadTo(h.apps[app].Handler(), token, submissionID, "", question)
}

// postTo is post through handler (for example one App's create handler built
// over a nil investigator).
func (h *submissionHarness) postTo(handler http.Handler, token, submissionID, question string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postThreadTo(handler, token, submissionID, "", question)
}

// postThread is post with an X-ACR-Thread-ID (none when "").
func (h *submissionHarness) postThread(app int, token, submissionID, threadID, question string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postThreadTo(h.apps[app].Handler(), token, submissionID, threadID, question)
}

// postThreadTo is postThread through handler.
func (h *submissionHarness) postThreadTo(handler http.Handler, token, submissionID, threadID, question string) *httptest.ResponseRecorder {
	h.t.Helper()
	body := investigationRequestBody()
	body.Question = question
	encoded, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	if submissionID != "" {
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, submissionID)
	}
	if threadID != "" {
		request.Header.Set(contractsv1.ContextFabricThreadIDHeader, threadID)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// status reads the submission status on app index, as token.
func (h *submissionHarness) status(app int, token, submissionID string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodGet, strings.Replace(ContextFabricSubmissionStatusPath, "{submission_id}", submissionID, 1), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

func (h *submissionHarness) get(app int, token, resultID string) *httptest.ResponseRecorder {
	h.t.Helper()
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, investigationResultRequest(h.t, token, resultID))
	return recorder
}

func servedResultID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		ResultID string `json:"result_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.ResultID == "" {
		t.Fatalf("decode result id: %v (body %s)", err, recorder.Body.String())
	}
	return body.ResultID
}

func errorEnvelopeOf(t *testing.T, recorder *httptest.ResponseRecorder) ErrorEnvelopeForTest {
	t.Helper()
	var envelope ErrorEnvelopeForTest
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body %s)", err, recorder.Body.String())
	}
	return envelope
}

// expectErrorCode asserts an error.v1 answer with status and code.
func expectErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status || errorEnvelopeOf(t, recorder).Error.Code != code {
		t.Fatalf("answer = %d %s, want %d %s", recorder.Code, recorder.Body.String(), status, code)
	}
}

// submissionErrorCode is the error.v1 code of an answer, or "" for a
// served (2xx) answer.
func submissionErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if recorder.Code < http.StatusBadRequest {
		return ""
	}
	return errorEnvelopeOf(t, recorder).Error.Code
}

// unconfiguredCreateHandler is App app's production create handler built over
// no investigator, behind the same request middleware App.Handler applies.
func (h *submissionHarness) unconfiguredCreateHandler(app int) http.Handler {
	return h.apps[app].InstrumentedHandler(h.apps[app].ContextFabricInvestigationHandler(nil))
}

// ErrorEnvelopeForTest is the error.v1 shape a test reads.
type ErrorEnvelopeForTest struct {
	Error struct {
		Code       string `json:"code"`
		HTTPStatus int    `json:"http_status"`
		Retryable  bool   `json:"retryable"`
	} `json:"error"`
}

// expectSubmissionRefusal asserts a 409 with code, retryable and, for
// in_progress, Retry-After.
func expectSubmissionRefusal(t *testing.T, recorder *httptest.ResponseRecorder, code string) {
	t.Helper()
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 %s (body %s)", recorder.Code, code, recorder.Body.String())
	}
	envelope := errorEnvelopeOf(t, recorder)
	wantRetryable := code == contractsv1.ErrorCodeSubmissionInProgress
	if envelope.Error.Code != code || envelope.Error.HTTPStatus != http.StatusConflict || envelope.Error.Retryable != wantRetryable {
		t.Fatalf("error = %+v, want %s retryable=%t", envelope.Error, code, wantRetryable)
	}
	retryAfter := recorder.Header().Get("Retry-After")
	if wantRetryable && retryAfter != "3" {
		t.Fatalf("Retry-After = %q, want 3", retryAfter)
	}
	if !wantRetryable && retryAfter != "" {
		t.Fatalf("Retry-After = %q on a non-retryable refusal", retryAfter)
	}
}

// waitFor polls condition until it holds, and fails after 10 s: a condition
// that never holds is a failure, never a pass on a timeout.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not hold within 10 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
