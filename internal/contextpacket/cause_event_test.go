package contextpacket_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestACauseItemNamesTheFailedDeploymentThatCarriedItAndWhen(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	first, second := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC), time.Date(2026, 9, 12, 9, 30, 0, 0, time.UTC)
	other := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-pr-exact", "pull_request", "1", "Exact", "merged", at),
		roleRow("ev-pr-merged", "pull_request", "2", "Merged", "merged", at),
		roleRow("ev-pr-first", "pull_request", "4", "First listed", "merged", at),
		roleRow("ev-pr-success", "pull_request", "7", "Success deployment", "merged", at),
		tieredDeploymentRow("ev-d-old", "d-old", "failure", "1", "exact_merge_sha", first),
		tieredDeploymentRow("ev-d-new", "d-new", "failure", "1", "exact_merge_sha", second),
		tieredDeploymentRow("ev-d-weaker", "d-weaker", "failure", "1", "merged_pr", second.Add(24*time.Hour)),
		tieredDeploymentRow("ev-d-merged", "d-merged", "failure", "2", "merged_pr", other),
		tieredDeploymentRow("ev-d-first", "d-first", "failure", "4", "first_listed", other),
		tieredDeploymentRow("ev-d-success", "d-ok", "success", "7", "exact_merge_sha", other),
	}
	_, packet := servedRoles(t, evidence)
	events := map[string]*contractsv1.CauseEvent{}
	for _, item := range packet.Items {
		events[item.EvidenceRefIDs[0]] = item.CauseEvent
	}
	want := map[string]struct {
		id string
		at time.Time
	}{
		"ev-pr-exact":  {"d-new", second},
		"ev-pr-merged": {"d-merged", other},
	}
	for ref, w := range want {
		got := events[ref]
		if got == nil || got.EntityType != "deployment" || got.EntityID != w.id || !got.OccurredAt.Equal(w.at) {
			t.Errorf("%s: cause_event = %+v, want deployment %s at %s (the best tier, the latest of it)", ref, got, w.id, w.at)
		}
	}
	for _, ref := range []string{"ev-pr-first", "ev-pr-success", "ev-d-new", "ev-d-old"} {
		if got, served := events[ref]; served && got != nil {
			t.Errorf("%s is not a linked cause yet carries cause_event %+v", ref, got)
		}
	}
}
