package contextpacket_test

import (
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func blockerVersusFailureEvidence() []contractsv1.EvidenceRef {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	return []contractsv1.EvidenceRef{
		dependencyRow("ev-blocker-1", "blocks:fwd", "PAY-1", "in_progress", at),
		dependencyRow("ev-blocker-2", "blocks:fwd", "PAY-2", "in_progress", at.Add(-time.Hour)),
		deploymentRow("ev-dep-failed", "dfail", "failed", "7", at.Add(-72*time.Hour)),
		deploymentRow("ev-dep-ok-1", "dok1", "success", "8", at.Add(-2*time.Hour)),
		roleRow("ev-commit-one", "commit", "c1", "Update checkout copy", "1 file", at.Add(-3*time.Hour)),
		roleRow("ev-pr-one", "pull_request", "20", "Checkout redesign", "merged", at.Add(-4*time.Hour)),
	}
}

func TestFailureGoalKeepsTheFailedDeploymentUnderASmallBudgetNotTheBlockers(t *testing.T) {
	got := rankedIDs(t, "regressions", 4, blockerVersusFailureEvidence())
	if !slices.Contains(got, "ev-dep-failed") {
		t.Fatalf("served = %v, want the failed deployment kept: a blocker is pressure but not a failure", got)
	}
}
