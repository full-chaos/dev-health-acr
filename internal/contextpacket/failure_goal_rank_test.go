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

func codeHealthEvidence() []contractsv1.EvidenceRef {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	hotspot := roleRow("ev-hotspot", "file_hotspot", "src/checkout.go", "src/checkout.go", "churn=40, complexity=12", at.Add(-72*time.Hour))
	complexity := roleRow("ev-complexity", "file_complexity", "src/cart.go", "src/cart.go", "complexity=30", at.Add(-73*time.Hour))
	commit := roleRow("ev-commit", "commit", "c1", "Update checkout copy", "1 file", at)
	pr := roleRow("ev-pr-redesign", "pull_request", "9", "Checkout redesign", "merged", at)
	deployment := tieredDeploymentRow("ev-dep-fail", "d9", "failed", "9", "exact_merge_sha", at.Add(-96*time.Hour))
	return []contractsv1.EvidenceRef{commit, pr, hotspot, complexity, deployment}
}

func TestCodeHealthGoalRanksHotspotAndComplexityRowsFirst(t *testing.T) {
	got := rankedIDs(t, "reduce churn in the checkout service", 10, codeHealthEvidence())
	if !slices.Equal(got[:2], []string{"ev-hotspot", "ev-complexity"}) {
		t.Fatalf("order = %v, want the hotspot and complexity rows first", got)
	}
}

func TestGoalWithoutACodeHealthWordIsNotLiftedByThatFit(t *testing.T) {
	got := rankedIDs(t, "update the checkout copy", 10, codeHealthEvidence())
	if want := []string{"ev-commit", "ev-pr-redesign", "ev-hotspot", "ev-complexity", "ev-dep-fail"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v: the matching commit first, then recency, with no code-health lift", got, want)
	}
}

func TestGoalNamingFailureAndCodeHealthFitsBothRoleSets(t *testing.T) {
	got := rankedIDs(t, "reduce failed deployments and churn", 10, codeHealthEvidence())
	top := got[:4]
	for _, id := range []string{"ev-dep-fail", "ev-pr-redesign", "ev-hotspot", "ev-complexity"} {
		if !slices.Contains(top, id) {
			t.Fatalf("top 4 = %v, want the failed deployment, its cause and both code-health rows (%s missing)", top, id)
		}
	}
}
