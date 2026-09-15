package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type retrySelectionProbe struct {
	EngineTelemetry
	selected []PlanNarrowingEvent
}

func (p *retrySelectionProbe) RecordSynthesisRetrySelection(_ context.Context, _ storage.Principal, event PlanNarrowingEvent) {
	p.selected = append(p.selected, event)
}

func TestSynthesisRetrySelectionUsesTheTriggerBeforeTheAttempt(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		members, claims, limit int
		reserve                time.Duration
		retry, fails, nilSink  bool
	}{
		{"fits", 2, 1, 50, time.Second, false, false, false},
		{"no_reserve", 6, 2, 12, 0, false, false, false},
		{"nothing_to_narrow", 1, 20, 4, time.Second, false, false, false},
		{"successful_retry", 6, 2, 12, time.Second, true, false, false},
		{"failed_retry", 6, 2, 12, time.Second, true, true, false},
		{"nil_sink", 6, 2, 12, time.Second, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			calls := 0
			finalTelemetry := &recordingTelemetry{}
			probe := &retrySelectionProbe{EngineTelemetry: finalTelemetry}
			engine := budgetStageEngine(t, budgetStageCohort(tc.members), tc.claims, budgetStageOptions(tc.limit, tc.reserve), &calls)
			engine.telemetry = probe
			if tc.nilSink {
				engine.telemetry = nil
			}
			base := engine.synthesizer
			failed := errors.New("controlled second synthesis failure")
			engine.synthesizer = synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
				want := 0
				if calls == 1 && !tc.nilSink {
					want = 1
				}
				if len(probe.selected) != want {
					t.Errorf("before synthesis %d: selected events=%d want=%d", calls+1, len(probe.selected), want)
				}
				if calls == 1 && tc.fails {
					calls++
					return InvestigationResult{}, failed
				}
				return base.Synthesize(ctx, principal, input)
			})
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
			wantCalls, wantEvents := 1, 0
			if tc.retry {
				wantCalls = 2
				if !tc.nilSink {
					wantEvents = 1
				}
			}
			if calls != wantCalls || len(probe.selected) != wantEvents {
				t.Fatalf("calls=%d selection events=%d want=%d/%d error=%v", calls, len(probe.selected), wantCalls, wantEvents, err)
			}
			if tc.fails && !errors.Is(err, failed) {
				t.Fatalf("retry failure=%v", err)
			}
			if tc.retry && !tc.fails && err != nil {
				t.Fatal(err)
			}
			for _, event := range probe.selected {
				if event.Stage != contractsv1.ContextFabricPlanNarrowingAssembledResult || event.Overrun != contractsv1.ContextFabricBudgetOverrunItems || event.MeasuredItems <= event.MaxItems || event.MaxItems != tc.limit || event.MeasuredBytes <= 0 {
					t.Errorf("trigger measurement=%+v", event)
				}
				if event.Before != tc.members || event.After != tc.members/2 || !event.DeadlineReserved || event.RetryAttempted || event.RetryFit || event.RetryFailed || event.RefusalPlanned {
					t.Errorf("selection asserted an unexecuted result: %+v", event)
				}
				if !tc.fails {
					final, measureErr := contractsv1.MeasureContextFabricResponse(result)
					if measureErr != nil || final.Items.Budgeted() > tc.limit || event.MeasuredItems <= final.Items.Budgeted() {
						t.Errorf("initial measurement=%d final=%+v error=%v", event.MeasuredItems, final, measureErr)
					}
				}
			}
			if !tc.nilSink {
				finalEvents := 0
				for _, event := range finalTelemetry.planNarrowings {
					if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
						finalEvents++
					}
				}
				if finalEvents != 1 {
					t.Errorf("ordinary final narrowing events=%d want1", finalEvents)
				}
			}
		})
	}
}
