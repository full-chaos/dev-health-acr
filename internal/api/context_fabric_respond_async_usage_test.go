package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The accept-first run owns its request's limit claim: it charges its real
// usage and closes the claim once at its own end, whether or not anyone reads
// the result.

func (h *submissionHarness) runPrincipal(t *testing.T) storage.Principal {
	t.Helper()
	principal, ok := h.investigator.lastPrincipal.Load().(storage.Principal)
	if !ok {
		t.Fatal("no run principal recorded")
	}
	return principal
}

func contextUsage(t *testing.T, app *App, principal storage.Principal) limits.UsageCounters {
	t.Helper()
	totals, err := app.limits.Usage(limitSubjectOf(principal), limits.RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
	return totals.Credential
}

// terminalOf waits for the run's log terminal on the store, without a status
// read (a read takes a claim of its own).
func terminalOf(t *testing.T, runs contextfabric.RunStore, run contextfabric.RunKey) *contextfabric.RunTerminal {
	t.Helper()
	var terminal *contextfabric.RunTerminal
	waitFor(t, func() bool {
		event, err := runs.RunTerminalOf(context.Background(), run)
		if err != nil {
			t.Fatal(err)
		}
		if event != nil {
			terminal = event.Terminal
		}
		return terminal != nil
	})
	return terminal
}

func acceptedStatus(t *testing.T, body []byte) contractsv1.ContextFabricSubmissionStatus {
	t.Helper()
	var status contractsv1.ContextFabricSubmissionStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("202 body %s: %v", body, err)
	}
	return status
}

func TestRespondAsyncRunChargesItsUsageUnread(t *testing.T) {
	baseline, _, _ := newLeaseHarness(t, 1)
	baselineServer := baseline.serveApp(0)
	response, body := baseline.respondAsync(baselineServer, baseline.tokenA, "submission-usage-syn", func(r *http.Request) { r.Header.Del(contractsv1.ContextFabricPreferHeader) })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("synchronous create = %d %s", response.StatusCode, body)
	}
	want := contextUsage(t, baseline.apps[0], baseline.runPrincipal(t))
	if want.Bytes == 0 || want.Tokens == 0 {
		t.Fatalf("synchronous usage %+v, want a non-zero charge", want)
	}

	harness, store, _ := newLeaseHarness(t, 1)
	server := harness.serveApp(0)
	response, body = harness.respondAsync(server, harness.tokenA, "submission-usage-asy", nil)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("accept-first create = %d %s", response.StatusCode, body)
	}
	accepted := acceptedStatus(t, body)
	terminal := terminalOf(t, store, contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.ReservedResultID})
	if terminal.Kind != contextfabric.RunTerminalResult {
		t.Fatalf("terminal %+v, want a result", terminal)
	}
	principal := harness.runPrincipal(t)
	waitFor(t, func() bool { return contextUsage(t, harness.apps[0], principal).Completed == 1 })
	got := contextUsage(t, harness.apps[0], principal)
	// The two answers differ only in ids and timestamps, whose encoded
	// length can differ by a few bytes; the charge is the served answer's
	// own measure (tokens = ceil(bytes / 4)).
	if got.Items != want.Items || got.Bytes < want.Bytes-32 || got.Bytes > want.Bytes+32 || got.Tokens != (got.Bytes+3)/4 || got.Admitted != 1 || got.Completed != 1 {
		t.Fatalf("unread accept-first usage %+v, want the synchronous charge %+v on one closed claim", got, want)
	}
}

func TestRespondAsyncRunHoldsTheOrgConcurrencySlot(t *testing.T) {
	harness, _, _ := newLeaseHarness(t, 1)
	manager, err := limits.NewManager(limits.Options{Now: harness.clock.Now, PerOrgConcurrency: 1, Policies: limits.PolicySet{
		Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 10_000},
		Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 10_000, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	harness.apps[0].limits = manager
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	if response, body := harness.respondAsync(server, harness.tokenA, "submission-usage-on1", nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("first create = %d %s", response.StatusCode, body)
	}
	waitEntered(t, harness.investigator.entered)
	response, body := harness.respondAsync(server, harness.tokenA, "submission-usage-on2", nil)
	if code, _ := errorCodeOf(t, body); response.StatusCode != http.StatusTooManyRequests || code != "rate_limited" {
		t.Fatalf("second create while the first runs = %d %s, want 429 rate_limited", response.StatusCode, body)
	}
	close(harness.investigator.release)
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

// itemPaddingInvestigator serves its inner run's result with claimed facts
// added, so the answer measures over a small item budget.
type itemPaddingInvestigator struct{ inner contextfabric.Investigator }

func (p itemPaddingInvestigator) Investigate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	result, err := p.inner.Investigate(ctx, principal, request)
	if err != nil {
		return result, err
	}
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	ready := false
	for i := 0; i < 8; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: "claim_" + strconv.Itoa(2000+i), Kind: contractsv1.ContextFabricFactStatus,
			Subject: subject, Field: "release_ready", Value: contractsv1.ContextFabricScalarValue{Boolean: &ready},
		})
	}
	result.Completeness.ClaimedFactsCount = len(result.ClaimedFacts)
	return result, nil
}

func TestRespondAsyncRunMeetsTheItemBudget(t *testing.T) {
	harness, store, _ := newLeaseHarness(t, 1)
	harness.useInvestigator(itemPaddingInvestigator{inner: harness.investigator})
	harness.apps[0].config.MaxItems = 1
	server := harness.serveApp(0)
	response, body := harness.respondAsync(server, harness.tokenA, "submission-usage-isy", func(r *http.Request) { r.Header.Del(contractsv1.ContextFabricPreferHeader) })
	if code, _ := errorCodeOf(t, body); response.StatusCode != http.StatusRequestEntityTooLarge || code != "budget_refusal" {
		t.Fatalf("synchronous create over the item budget = %d %s, want 413 budget_refusal", response.StatusCode, body)
	}
	response, body = harness.respondAsync(server, harness.tokenA, "submission-usage-ias", nil)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("accept-first create = %d %s", response.StatusCode, body)
	}
	accepted := acceptedStatus(t, body)
	terminal := terminalOf(t, store, contextfabric.RunKey{OrgID: "org_1", ReservedResultID: accepted.ReservedResultID})
	if terminal.Kind != contextfabric.RunTerminalError || terminal.Error == nil || terminal.Error.Error.Code != "budget_refusal" {
		t.Fatalf("accept-first terminal %+v, want error budget_refusal", terminal)
	}
}

func TestRespondAsyncLostRunClosesItsClaimOnceAtItsEnd(t *testing.T) {
	harness, stalled, logs := lostAsyncRun(t, 2, "submission-usage-lst")
	principal := harness.runPrincipal(t)
	if got := contextUsage(t, harness.apps[0], principal); got.Admitted != 1 || got.Completed != 0 {
		t.Fatalf("holder usage while its run is held %+v, want 1 admitted, 0 completed (the claim still open)", got)
	}
	status := harness.statusBody(1, harness.tokenA, "submission-usage-lst")
	if status.Terminal == nil || status.Terminal.ErrorCode != contractsv1.ErrorCodeRunLost {
		t.Fatalf("status %+v, want run_lost", status)
	}
	stalled.resume()
	effective := logs[0].waitLine(t, "context fabric investigation cancel effective", nil)
	if effective["cause"] != "lease_lost" {
		t.Fatalf("cancel effective %v, want lease_lost", effective)
	}
	waitFor(t, func() bool { return contextUsage(t, harness.apps[0], principal).Completed == 1 })
	if err := harness.apps[0].DrainDetachedRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := contextUsage(t, harness.apps[0], principal); got.Admitted != 1 || got.Completed != 1 || got.Items != 0 {
		t.Fatalf("holder usage after its lost run ended %+v, want its one claim closed once with no usage", got)
	}
}

// The two-replica late read on Postgres: a holder that stalled, both
// expiries past, two replicas reading at once. One terminal, one append, and
// the holder's claim closes once when its run ends.
func TestRespondAsyncLateReadTakeoverRaceOnPostgres(t *testing.T) {
	dsn, admin := runLeasePostgres(t)
	harness, logs := newPostgresLeaseHarness(t, dsn, 3)
	stalled := &stallingRunStore{RunStore: harness.apps[0].runtime.InvestigationRuns}
	harness.apps[0].runtime.InvestigationRuns = stalled
	harness.investigator.mode.Store(int32(runBlockThenSave))
	server := harness.serveApp(0)
	t.Cleanup(stalled.resume)
	response, body := harness.respondAsync(server, harness.tokenA, "submission-usage-pgr", nil)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	accepted := acceptedStatus(t, body)
	waitEntered(t, harness.investigator.entered)
	principal := harness.runPrincipal(t)
	stalled.stall()
	harness.clock.Advance(10 * time.Minute)
	var wg sync.WaitGroup
	statuses := make([]contractsv1.ContextFabricSubmissionStatus, 2)
	codes := make([]int, 2)
	for index, app := range []int{1, 2} {
		wg.Add(1)
		go func(index, app int) {
			defer wg.Done()
			recorder := harness.status(app, harness.tokenA, "submission-usage-pgr")
			codes[index] = recorder.Code
			_ = json.Unmarshal(recorder.Body.Bytes(), &statuses[index])
		}(index, app)
	}
	wg.Wait()
	for index, status := range statuses {
		if codes[index] != http.StatusOK || status.State != contractsv1.ContextFabricSubmissionAbandoned || status.Terminal == nil || status.Terminal.ErrorCode != contractsv1.ErrorCodeRunLost {
			t.Fatalf("late read %d = %d %+v / %+v, want abandoned + run_lost", index, codes[index], status, status.Terminal)
		}
	}
	if got := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE org_id = $1 AND reserved_result_id = $2 AND terminal_kind IS NOT NULL`, "org_1", accepted.ReservedResultID); got != 1 {
		t.Fatalf("%d terminal rows, want exactly 1", got)
	}
	appended := 0
	for _, buffer := range logs[1:] {
		for _, line := range buffer.lines(ContextFabricRunTakeoverLogMessage) {
			if line["terminal_appended"] == true {
				appended++
			}
		}
	}
	if appended != 1 {
		t.Fatalf("%d appending takeover lines over the two reads, want exactly 1", appended)
	}
	if got := contextUsage(t, harness.apps[0], principal); got.Admitted != 1 || got.Completed != 0 {
		t.Fatalf("holder usage while its run is held %+v, want its claim still open", got)
	}
	stalled.resume()
	waitFor(t, func() bool { return contextUsage(t, harness.apps[0], principal).Completed == 1 })
	if err := harness.apps[0].DrainDetachedRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := contextUsage(t, harness.apps[0], principal); got.Admitted != 1 || got.Completed != 1 {
		t.Fatalf("holder usage after its lost run ended %+v, want its one claim closed once", got)
	}
	if got := countRows(t, admin, `SELECT count(*) FROM acr.context_fabric_investigation_run_events WHERE org_id = $1 AND reserved_result_id = $2 AND terminal_kind IS NOT NULL`, "org_1", accepted.ReservedResultID); got != 1 {
		t.Fatalf("%d terminal rows after the holder ended, want still exactly 1", got)
	}
}
