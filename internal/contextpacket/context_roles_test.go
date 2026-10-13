package contextpacket_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func roleRow(id, entityType, entityID, label, citation string, at time.Time) contractsv1.EvidenceRef {
	ref := testEvidence(id, "dev_health", at)
	ref.Source.EntityType, ref.Source.EntityID, ref.Source.DisplayLabel, ref.Citation = entityType, entityID, label, citation
	return ref
}

func servedRoles(t *testing.T, evidence []contractsv1.EvidenceRef) (map[string]contractsv1.PacketCategory, contractsv1.ContextPacket) {
	t.Helper()
	request := fixtureRequest("req-roles", "main", "")
	request.Goal = "checkout"
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 20, 4000, 65536
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      evidence,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	roles := map[string]contractsv1.PacketCategory{}
	for _, item := range packet.Items {
		roles[item.EvidenceRefIDs[0]] = item.Category
	}
	return roles, packet
}

func TestCheckoutShapeServesFailuresAsPressureAndOnlyLinkedChangesAsCause(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const featureA, featureB, regression, hotfix = "aaaaaaa1111111111111111111111111111111aa", "bbbbbbb2222222222222222222222222222222bb", "ccccccc3333333333333333333333333333333cc", "ddddddd4444444444444444444444444444444dd"
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-c-feat-a", "commit", featureA, "One-page checkout", "One-page checkout", at),
		roleRow("ev-c-feat-b", "commit", featureB, "Saved payment methods at checkout", "Saved payment methods at checkout", at),
		roleRow("ev-c-regress", "commit", regression, "Regression: delivery slots show past times", "Regression: delivery slots show past times", at),
		roleRow("ev-c-hotfix", "commit", hotfix, "Hotfix: gift cards double-applied", "Hotfix: gift cards double-applied", at),
		roleRow("ev-d-ok-1", "deployment", "dep-1", "production deployment", "status=success, release_ref="+featureA, at),
		roleRow("ev-d-fail-1", "deployment", "dep-2", "production deployment", "status=failed, release_ref="+regression, at),
		roleRow("ev-d-fail-2", "deployment", "dep-3", "production deployment", "status=failed, release_ref="+hotfix, at),
		roleRow("ev-ci-ok", "ci_pipeline_run", "run-1", "CI run-1", "success", at),
		roleRow("ev-ci-bad", "ci_pipeline_run", "run-2", "CI run-2", "failed", at),
	}
	roles, packet := servedRoles(t, evidence)
	want := map[string]contractsv1.PacketCategory{
		"ev-c-feat-a": contractsv1.CategoryEvidence, "ev-c-feat-b": contractsv1.CategoryEvidence,
		"ev-c-regress": contractsv1.CategoryCause, "ev-c-hotfix": contractsv1.CategoryCause,
		"ev-d-ok-1": contractsv1.CategoryState, "ev-d-fail-1": contractsv1.CategoryPressure, "ev-d-fail-2": contractsv1.CategoryPressure,
		"ev-ci-ok": contractsv1.CategoryEvidence, "ev-ci-bad": contractsv1.CategoryPressure,
	}
	for id, category := range want {
		if roles[id] != category {
			t.Errorf("%s served as %q, want %q", id, roles[id], category)
		}
	}
	for _, item := range packet.Items {
		if item.Category == contractsv1.CategoryAction {
			t.Errorf("action item served without a stored source: %s", item.Title)
		}
	}
	if !slices.Contains(packet.Warnings, contextpacket.ActionItemsUnavailableWarning) {
		t.Errorf("no coverage line for absent action items: %v", packet.Warnings)
	}
}

func TestACommitIsNotCauseByTimeOrderOrOpenIncidentAlone(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-c-before-incident", "commit", "eeeeeee5555555555555555555555555555555ee", "refactor", "refactor", at.Add(-time.Hour)),
		roleRow("ev-i-open", "incident", "inc-1", "checkout down", "status=investigating, severity=high", at),
		roleRow("ev-i-closed", "incident", "inc-2", "old outage", "status=resolved, severity=low", at),
		roleRow("ev-i-unknown", "incident", "inc-3", "no status", "status=, severity=", at),
	}
	roles, _ := servedRoles(t, evidence)
	if roles["ev-c-before-incident"] != contractsv1.CategoryEvidence {
		t.Errorf("commit served as %q, want evidence", roles["ev-c-before-incident"])
	}
	if roles["ev-i-open"] != contractsv1.CategoryPressure || roles["ev-i-closed"] == contractsv1.CategoryPressure || roles["ev-i-unknown"] == contractsv1.CategoryPressure {
		t.Errorf("incident roles = %v", roles)
	}
}

func TestIncidentLinkedDeploymentAndRevertMakeTheCarriedChangeACause(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const carried, reverted = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-c-carried", "commit", carried, "slots", "slots", at),
		roleRow("ev-c-reverted", "commit", reverted, "gift cards", "gift cards", at),
		roleRow("ev-c-revert", "commit", "3333333cccccccccccccccccccccccccccccccc", "Revert gift cards", "Revert gift cards\n\nThis reverts commit "+reverted+".", at),
		roleRow("ev-d-green", "deployment", "dep-9", "production deployment", "status=success, release_ref="+carried, at),
		roleRow("ev-e-link", "deployment_incident_edge", "edge-1", "dep-9 linked to inc-9", "edge_id=edge-1", at),
	}
	roles, _ := servedRoles(t, evidence)
	if roles["ev-c-carried"] != contractsv1.CategoryCause || roles["ev-c-reverted"] != contractsv1.CategoryCause {
		t.Errorf("roles = %v", roles)
	}
	if roles["ev-c-revert"] != contractsv1.CategoryEvidence {
		t.Errorf("the reverting commit served as %q, want evidence", roles["ev-c-revert"])
	}
}
