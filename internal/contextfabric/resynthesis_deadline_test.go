package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// blockingRetrySynthesizer lets the first synthesis draw through and blocks
// every later one until its context ends, the way a slow model call does.
type blockingRetrySynthesizer struct {
	AnswerSynthesizer
	calls   atomic.Int32
	retryAt atomic.Int64
}

func (b *blockingRetrySynthesizer) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	if b.calls.Add(1) == 1 {
		return b.AnswerSynthesizer.Synthesize(ctx, principal, input)
	}
	<-ctx.Done()
	b.retryAt.Store(time.Now().UnixNano())
	return InvestigationResult{}, ctx.Err()
}

func (b *blockingRetrySynthesizer) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	return memberCutDegradeCapable{b.AnswerSynthesizer}.ComposeDegraded(ctx, principal, input, failure)
}

// A valid first-pass answer is not thrown away for a narrowing retry that
// then loses the request deadline: the first pass's facts are served, labelled
// as a deadline outcome, and the retry ended before the request did.
func TestANarrowingRetryThatLosesTheDeadlineServesTheFirstPassFacts(t *testing.T) {
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, 2*time.Second), &calls, telemetry)
	blocking := &blockingRetrySynthesizer{AnswerSynthesizer: engine.synthesizer}
	engine.synthesizer = blocking

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if err != nil {
		t.Fatalf("error = %v, want the first-pass facts served", err)
	}
	assertFactsOnlyLabelled(t, result, contractsv1.ContextFabricSynthesisFailureDeadline)
	if got := blocking.calls.Load(); got != 2 {
		t.Fatalf("model calls = %d, want the first draw and one blocked retry", got)
	}
	if end := time.Unix(0, blocking.retryAt.Load()); !end.Before(deadline) {
		t.Fatalf("retry ended at %v, want before the request deadline %v", end, deadline)
	}
	if served := factsOnlyServedEvent(telemetry); served == nil || !served.RetryAttempted || served.RetryDeclined != RetryDeclinedInsufficientDeadline {
		t.Fatalf("served event %+v, want facts-only served after a retry that ran", served)
	}
}

// The Info line states what the caller got: when the facts-only answer does
// not fit the budget the caller still gets the retry's error, and the line says
// the facts were not served.
func TestTheDeadlineLogSaysWhenTheFirstPassFactsWereNotServed(t *testing.T) {
	var sink bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })
	calls := 0
	options := budgetStageOptions(12, 2*time.Second)
	options.MaxSerializedBytes = 3000
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, options, &calls)
	engine.synthesizer = &blockingRetrySynthesizer{AnswerSynthesizer: engine.synthesizer}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if err == nil {
		t.Fatal("error = nil, want the retry's deadline error when the facts do not fit")
	}
	if line := sink.String(); !strings.Contains(line, "narrowing retry lost the deadline") || !strings.Contains(line, `"facts_only_served":false`) {
		t.Fatalf("log = %s, want the deadline line with facts_only_served false", line)
	}
}
