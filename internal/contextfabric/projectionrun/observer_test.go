package projectionrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Codex round-4 F3, RED->GREEN: the observer must log a CLASSIFICATION, never
// the error's own text. Outcome.Err is whatever a source, checkpoint store, or
// backend returned; a ClickHouse driver error or Postgres checkpoint error
// arrives unbounded and would put raw dependency text into production
// telemetry.
func TestR4_F3_ObserverNeverLogsRawErrorText(t *testing.T) {
	const secret = "dial tcp 10.1.2.3:9000: connect: connection refused (user=svc_acr password=hunter2)"
	var buffer bytes.Buffer
	observer := SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	observer.ObserveProjectionOutcome(Outcome{
		OrgID: "org_1", Source: "devhealth", Duration: 3 * time.Second,
		Err: fmt.Errorf("read batch: %w", errors.New(secret)),
	})

	logged := buffer.String()
	for _, leak := range []string{"hunter2", "10.1.2.3", "password", "connection refused", "dial tcp"} {
		if strings.Contains(logged, leak) {
			t.Fatalf("raw dependency error text leaked into telemetry (%q): %s", leak, logged)
		}
	}
	// It must still say SOMETHING actionable.
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logged)), &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if record["failure_class"] != failureClassUnclassified {
		t.Fatalf("an unrecognized error must classify as %q, got %v", failureClassUnclassified, record["failure_class"])
	}
	if record["org_id"] != "org_1" {
		t.Fatalf("the organization must still be identified, got %v", record["org_id"])
	}
}

// exceptionDetailLeakCanaries lists exception-detail substrings that must
// never reach telemetry for TestQueryBudgetExceededObservesDistinctlyAndBoundedly.
// CHAOS-3877: deliberately distinctive (each contains a letter, never a
// bare run of digits) so none of them can collide with an incidental
// substring of slog's own RFC3339Nano timestamp, which is digits-only. A
// bare "307" canary previously matched the nanosecond fraction of an
// entirely unrelated timestamp (observed .930737748Z on a real CI run) and
// failed the test for a reason that had nothing to do with a leak.
var exceptionDetailLeakCanaries = []string{"17987654", "Limit for read exceeded", "exception code 307"}

// structuredLeakCheck parses a single JSON log line and reports which
// exceptionDetailLeakCanaries appear in its STRUCTURED fields -- msg and
// every attr -- with the "time" field dropped first. CHAOS-3877: the
// rendered line also carries slog's own timestamp, which is content this
// observer never controls and which can incidentally contain digits
// matching a canary (a bare "307" canary previously matched the nanosecond
// fraction of an entirely unrelated timestamp, observed .930737748Z on a
// real CI run); checking the whole line makes the test depend on the clock.
// Dropping "time" before the substring check removes that dependency while
// still catching a real leak in ANY field this observer logs today or adds
// later -- there is no allow-list of "safe" field names to fall out of
// date. Pure (no *testing.T): requireNoExceptionDetailLeak below is the
// assertion wrapper production tests use; TestChaos3877_* below call this
// directly so they can inspect a leak being found without failing.
func structuredLeakCheck(t testing.TB, logged string) (record map[string]any, leaked []string) {
	t.Helper()
	if err := json.Unmarshal([]byte(strings.TrimSpace(logged)), &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	structured := make(map[string]any, len(record))
	for k, v := range record {
		if k == "time" {
			continue
		}
		structured[k] = v
	}
	structuredBody, err := json.Marshal(structured)
	if err != nil {
		t.Fatalf("re-marshal structured fields: %v", err)
	}
	structuredText := string(structuredBody)
	for _, leak := range exceptionDetailLeakCanaries {
		if strings.Contains(structuredText, leak) {
			leaked = append(leaked, leak)
		}
	}
	return record, leaked
}

// requireNoExceptionDetailLeak is structuredLeakCheck plus the Fatalf a
// production test wants: fail loud the moment any canary is found.
func requireNoExceptionDetailLeak(t *testing.T, logged string) map[string]any {
	t.Helper()
	record, leaked := structuredLeakCheck(t, logged)
	if len(leaked) > 0 {
		t.Fatalf("raw exception detail leaked into telemetry (%q): %s", leaked, logged)
	}
	return record
}

// TestQueryBudgetExceededObservesDistinctlyAndBoundedly is CHAOS-3848's
// part-2 closure test at the observer boundary: a mocked ClickHouse
// TOO_MANY_BYTES (Code 307) failure, wrapped exactly as
// devhealthsource.tableReadError wraps one, must log failure_class =
// "query_budget_exceeded" -- not "dependency_unavailable" -- and the
// exception's own numeric code and message text must still never reach the
// log line (F3's guarantee applies to this failure class exactly as it does
// to every other one).
func TestQueryBudgetExceededObservesDistinctlyAndBoundedly(t *testing.T) {
	var buffer bytes.Buffer
	observer := SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	observer.ObserveProjectionOutcome(Outcome{
		OrgID: "org_1", Source: "devhealth", Duration: time.Second,
		Err: fmt.Errorf("read pull_requests (clickhouse exception code 307): %w", contextfabric.ErrQueryBudgetExceeded),
	})

	logged := buffer.String()
	record := requireNoExceptionDetailLeak(t, logged)
	if record["failure_class"] != failureClassBudgetExceeded {
		t.Fatalf("failure_class = %v, want %q", record["failure_class"], failureClassBudgetExceeded)
	}
	if record["failure_class"] == failureClassUnavailable {
		t.Fatal("a query-budget failure must not classify as dependency_unavailable")
	}
}

// TestChaos3877_StructuredLeakCheckSurvivesTimestampCollision is the
// red-first half of the fix's own proof: a log line shaped exactly like the
// real CI failure (a "time" field whose nanosecond fraction contains the
// canary digits "307", and otherwise clean structured fields) must NOT be
// reported as a leak. Fed through the OLD whole-line check this line trips
// a false failure; requireNoExceptionDetailLeak (the fix) must pass it.
func TestChaos3877_StructuredLeakCheckSurvivesTimestampCollision(t *testing.T) {
	const logged = `{"time":"2026-08-17T03:31:12.930737748Z","level":"ERROR","msg":"context_fabric: projection tick failed; checkpoint held for replay","org_id":"org_1","source":"devhealth","duration_ms":1000,"failure_class":"query_budget_exceeded"}`

	// Anti-vacuity: the fixture's timestamp really does carry the collision
	// digits (this is byte-for-byte the .930737748Z observed in CI), so the
	// pre-fix whole-line check -- strings.Contains(logged, "307") -- really
	// does trip on this exact line even though nothing leaked.
	if !strings.Contains(logged, "307") {
		t.Fatal("fixture's timestamp must contain the collision digits '307' (from .930737748Z) for this test to mean anything")
	}

	record := requireNoExceptionDetailLeak(t, logged) // must NOT fail
	if record["failure_class"] != failureClassBudgetExceeded {
		t.Fatalf("failure_class = %v, want %q", record["failure_class"], failureClassBudgetExceeded)
	}
}

// TestChaos3877_StructuredLeakCheckStillCatchesRealLeaks is the green-stays-red
// half: a real leak -- the canary landing in msg, where a genuine
// dependency-error mistake would put it -- must still fail
// requireNoExceptionDetailLeak. Proves the switch to structured-only
// checking did not also silence real leaks, only the timestamp collision.
func TestChaos3877_StructuredLeakCheckStillCatchesRealLeaks(t *testing.T) {
	subTests := []struct {
		name   string
		logged string
	}{
		{
			name:   "leak in msg",
			logged: `{"time":"2026-08-17T03:31:12.000000000Z","level":"ERROR","msg":"context_fabric: projection tick failed: exception code 307","org_id":"org_1","source":"devhealth","duration_ms":1000,"failure_class":"query_budget_exceeded"}`,
		},
		{
			name:   "leak in an attr",
			logged: `{"time":"2026-08-17T03:31:12.000000000Z","level":"ERROR","msg":"context_fabric: projection tick failed; checkpoint held for replay","org_id":"org_1","source":"devhealth","duration_ms":1000,"failure_class":"query_budget_exceeded","debug_detail":"Limit for read exceeded"}`,
		},
	}
	for _, st := range subTests {
		t.Run(st.name, func(t *testing.T) {
			_, leaked := structuredLeakCheck(t, st.logged)
			if len(leaked) == 0 {
				t.Fatalf("structuredLeakCheck did not catch a real leak (%s): %s", st.name, st.logged)
			}
		})
	}
}

// The vocabulary is closed and each known failure maps to its own class, so
// "unclassified" genuinely means "a failure this vocabulary does not name"
// rather than "classification is not wired".
func TestR4_F3_KnownFailuresClassifyDistinctly(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.Canceled, failureClassCanceled},
		{context.DeadlineExceeded, failureClassCanceled},
		{contextfabric.ErrProjectionConflict, failureClassConflict},
		{ErrOrgLocked, failureClassLocked},
		{contextfabric.ErrProjectionSourceVersionChanged, failureClassRebuildNeeded},
		{contextfabric.ErrRateLimited, failureClassRateLimited},
		{contextfabric.ErrQueryBudgetExceeded, failureClassBudgetExceeded},
		{contextfabric.ErrUnavailable, failureClassUnavailable},
		{contextfabric.ErrInvalidResult, failureClassInvalidResult},
		{errors.New("something new"), failureClassUnclassified},
		{nil, ""},
	}
	for _, testCase := range cases {
		// Wrapped, because a real tick failure always arrives wrapped.
		err := testCase.err
		if err != nil {
			err = fmt.Errorf("tick: %w", err)
		}
		if got := classifyOutcomeError(err); got != testCase.want {
			t.Fatalf("classifyOutcomeError(%v) = %q, want %q", testCase.err, got, testCase.want)
		}
	}
}

// A successful tick logs at debug and carries no failure class.
func TestR4_F3_SuccessfulTickLogsNoFailureClass(t *testing.T) {
	var buffer bytes.Buffer
	observer := SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	observer.ObserveProjectionOutcome(Outcome{OrgID: "org_1", Source: "devhealth", Duration: time.Second})
	if strings.Contains(buffer.String(), "failure_class") {
		t.Fatalf("a successful tick must carry no failure class: %s", buffer.String())
	}
}

// SELF-FOUND (lane-3778, pre-round-5): the table above proves that an UNKNOWN
// error classifies as unclassified, but it does not prove classification keys
// on sentinel IDENTITY rather than on error TEXT -- every case in it is
// textually unlike the sentinels. A switch from errors.Is to substring
// matching would keep all of it green.
//
// These probes close that: each error's text is BYTE-IDENTICAL to a sentinel's
// while not being that sentinel. Identity-based classification rejects them
// all; any text-based classification would misclassify every one.
func TestR5_SelfFound_ClassifierKeysOnIdentityNotErrorText(t *testing.T) {
	// Derived from the classifier's OWN table, not a parallel hand-list
	// (codex round 7). A sentinel added to failureClasses is probed here
	// automatically; there is no second list to forget to update.
	if len(failureClasses) == 0 {
		t.Fatal("failureClasses is empty; this test would be vacuous")
	}
	for _, entry := range failureClasses {
		sentinel := entry.sentinel
		impostor := errors.New(sentinel.Error())
		// Anti-vacuity, both directions: the impostor must carry the
		// sentinel's exact text, and must NOT be the sentinel.
		if impostor.Error() != sentinel.Error() {
			t.Fatalf("impostor for %v does not carry its text; the probe is vacuous", sentinel)
		}
		if errors.Is(impostor, sentinel) {
			t.Fatalf("impostor for %v IS the sentinel; the probe is vacuous", sentinel)
		}
		if got := classifyOutcomeError(fmt.Errorf("tick: %w", impostor)); got != failureClassUnclassified {
			t.Fatalf("an error merely TEXT-EQUAL to sentinel %v classified as %q; "+
				"classification must key on identity, not text", sentinel, got)
		}
	}
}
