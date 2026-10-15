package contextpacket_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func rankedIDs(t *testing.T, goal string, maxItems int, evidence []contractsv1.EvidenceRef) []string {
	t.Helper()
	request := fixtureRequest("req-goal-fit", "main", "")
	request.Goal = goal
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = maxItems, 8000, 200000
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      evidence, QueryVersion: contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return itemEvidenceIDs(packet)
}

// The Demo checkout-api shape: seven successful deployments that name their
// release, two failed deployments whose pull requests are promoted causes, and
// a failed CI run.
func demoFailureEvidence() []contractsv1.EvidenceRef {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var evidence []contractsv1.EvidenceRef
	for i := 0; i < 7; i++ {
		row := tieredDeploymentRow(fmt.Sprintf("ev-dep-ok-%d", i), fmt.Sprintf("ok%d", i), "success", fmt.Sprint(10+i), "exact_merge_sha", at.Add(-time.Duration(i)*time.Hour))
		row.Citation = fmt.Sprintf("status=success, release_ref=rel-%d, release_ref_confidence=1", i)
		evidence = append(evidence, row)
		evidence = append(evidence, roleRow(fmt.Sprintf("ev-pr-ok-%d", i), "pull_request", fmt.Sprint(10+i), "Checkout change", "merged", at.Add(-time.Duration(i)*time.Hour)))
	}
	for i := 0; i < 2; i++ {
		row := tieredDeploymentRow(fmt.Sprintf("ev-dep-fail-%d", i), fmt.Sprintf("bad%d", i), "failed", fmt.Sprint(1+i), "exact_merge_sha", at.Add(-time.Duration(30+i)*time.Hour))
		row.Citation = fmt.Sprintf("status=failed, deployment_id=bad%d", i)
		evidence = append(evidence, row)
		evidence = append(evidence, roleRow(fmt.Sprintf("ev-pr-cause-%d", i), "pull_request", fmt.Sprint(1+i), "Change that shipped", "merged", at.Add(-time.Duration(30+i)*time.Hour)))
	}
	return append(evidence, roleRow("ev-ci-fail", "check", "ci1", "CI run", "failed", at.Add(-50*time.Hour)))
}

func TestFailureGoalRanksFailuresAndTheirCausesAboveUnrelatedSuccesses(t *testing.T) {
	got := rankedIDs(t, "Reduce failed deployments and CI failures in the checkout API release pipeline", 12, demoFailureEvidence())
	top := got[:5]
	want := []string{"ev-dep-fail-0", "ev-dep-fail-1", "ev-pr-cause-0", "ev-pr-cause-1", "ev-ci-fail"}
	for _, id := range want {
		if !slices.Contains(top, id) {
			t.Fatalf("top 5 = %v, want the failed deployments, their promoted causes and the CI failure (%v)", top, want)
		}
	}
}

func TestGoalWordsFitCitationValuesAndLabelsNeverFieldNames(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	byKeyName := roleRow("ev-key-name", "deployment", "d1", "production deployment", "status=success, release_ref=abc123", at)
	byLabel := roleRow("ev-label", "pull_request", "1", "Release notes draft", "merged", at.Add(-48*time.Hour))
	byValue := roleRow("ev-value", "deployment", "d2", "production deployment", "status=success, release_ref=release-2026-10", at.Add(-72*time.Hour))
	other := roleRow("ev-other", "pull_request", "2", "Unrelated change", "merged", at)
	got := rankedIDs(t, "release", 10, []contractsv1.EvidenceRef{other, byKeyName, byLabel, byValue})
	if !slices.Equal(got[:2], []string{"ev-label", "ev-value"}) && !slices.Equal(got[:2], []string{"ev-value", "ev-label"}) {
		t.Fatalf("order = %v, want the label and the value that contain the word ahead of the row that only has release_ref=", got)
	}
}
