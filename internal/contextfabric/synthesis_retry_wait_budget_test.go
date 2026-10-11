package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A caller's wait budget narrows the request context. The second synthesis
// is selected only when the remaining budget covers the retry reserve (a
// third of the request timeout in production); otherwise no second model call
// is started and the refusal names the deadline as the reason.
func TestTheSecondSynthesisIsSelectedOnlyWhenTheRemainingWaitBudgetCoversTheReserve(t *testing.T) {
	t.Parallel()
	const reserve = 20 * time.Second // request timeout 60 s / 3
	cases := []struct {
		name      string
		remaining time.Duration
		wantCalls int
	}{
		{"wait budget below the reserve", 10 * time.Second, 1},
		{"wait budget covers the reserve", 40 * time.Second, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			telemetry := &recordingTelemetry{}
			engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, reserve), &calls, telemetry)
			ctx, cancel := context.WithTimeout(context.Background(), tc.remaining)
			defer cancel()

			_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

			if calls != tc.wantCalls {
				t.Fatalf("synthesizer called %d times with %s of budget left, want %d", calls, tc.remaining, tc.wantCalls)
			}
			if tc.wantCalls == 2 {
				return
			}
			if !errors.Is(err, ErrAnswerExceedsBudget) {
				t.Fatalf("error = %v, want a planned refusal", err)
			}
			var declined RetryDeclinedReason
			for index := range telemetry.planNarrowings {
				if telemetry.planNarrowings[index].RefusalPlanned {
					declined = telemetry.planNarrowings[index].RetryDeclined
				}
			}
			if declined != RetryDeclinedInsufficientDeadline {
				t.Fatalf("RetryDeclined = %q, want %q", declined, RetryDeclinedInsufficientDeadline)
			}
		})
	}
}
