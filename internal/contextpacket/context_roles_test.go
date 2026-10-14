package contextpacket_test

import (
	"context"
	"slices"
	"strings"
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

func deploymentRow(id, deploymentID, status, pullRequest string, at time.Time) contractsv1.EvidenceRef {
	ref := roleRow(id, "deployment", deploymentID, "production deployment", "status="+status, at)
	ref.Metadata = map[string]any{"deployment_status": status, "pull_request_number": pullRequest}
	return ref
}

func TestCheckoutShapeServesFailuresAsPressureAndTheirPullRequestsAsRelations(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-pr-feat-a", "pull_request", "41", "One-page checkout", "merged", at),
		roleRow("ev-pr-feat-b", "pull_request", "40", "Saved payment methods at checkout", "merged", at),
		roleRow("ev-pr-regress", "pull_request", "42", "Regression: delivery slots show past times", "merged", at),
		roleRow("ev-pr-hotfix", "pull_request", "43", "Hotfix: gift cards double-applied", "merged", at),
		roleRow("ev-c-feat-a", "commit", "aaaaaaa1111111111111111111111111111111aa", "One-page checkout", "One-page checkout", at),
		deploymentRow("ev-d-ok-1", "9001", "success", "41", at),
		deploymentRow("ev-d-fail-1", "9002", "failure", "42", at),
		deploymentRow("ev-d-fail-2", "9003", "failed", "43", at),
		roleRow("ev-ci-ok", "ci_pipeline_run", "run-1", "CI run-1", "success", at),
		roleRow("ev-ci-bad", "ci_pipeline_run", "run-2", "CI run-2", "failed", at),
	}
	roles, packet := servedRoles(t, evidence)
	for _, item := range packet.Items {
		if item.EvidenceRefIDs[0] == "ev-pr-regress" && (!strings.Contains(item.WhyIncluded, "pull request 42 for a failed deployment") || !strings.Contains(item.WhyIncluded, "match tier is not stored") || !strings.Contains(item.WhyIncluded, "not shown as the cause")) {
			t.Errorf("relation text = %q, want what is stored and no more", item.WhyIncluded)
		}
	}
	want := map[string]contractsv1.PacketCategory{
		"ev-pr-feat-a": contractsv1.CategoryEvidence, "ev-pr-feat-b": contractsv1.CategoryEvidence, "ev-c-feat-a": contractsv1.CategoryEvidence,
		"ev-pr-regress": contractsv1.CategoryEvidence, "ev-pr-hotfix": contractsv1.CategoryEvidence,
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
	if !slices.Contains(packet.Warnings, "cause_links_demoted:2:link_tier_not_stored") {
		t.Errorf("demoted links are not counted: %v", packet.Warnings)
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

func incidentEdge(id, deploymentID, incidentID, provenance string, at time.Time) contractsv1.EvidenceRef {
	edge := roleRow(id, "deployment_incident_edge", id, deploymentID+" linked to "+incidentID, "edge_id="+id, at)
	edge.Provenance = provenance
	edge.Metadata = map[string]any{"deployment_id": deploymentID, "incident_id": incidentID}
	return edge
}

func TestAnIncidentLinkedDeploymentNeedsANonHeuristicEdgeAndAnOpenIncident(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-pr-open", "pull_request", "77", "Slots", "merged", at),
		roleRow("ev-pr-heur", "pull_request", "78", "Heuristic edge", "merged", at),
		roleRow("ev-pr-gone", "pull_request", "79", "Incident not in set", "merged", at),
		roleRow("ev-pr-closed", "pull_request", "80", "Resolved incident", "merged", at),
		deploymentRow("ev-d-open", "dep-1", "success", "77", at),
		deploymentRow("ev-d-heur", "dep-2", "success", "78", at),
		deploymentRow("ev-d-gone", "dep-3", "success", "79", at),
		deploymentRow("ev-d-closed", "dep-4", "success", "80", at),
		roleRow("ev-inc-open", "incident", "inc-1", "checkout down", "status=investigating, severity=high", at),
		roleRow("ev-inc-heur", "incident", "inc-2", "heuristic incident", "status=investigating, severity=high", at),
		roleRow("ev-inc-closed", "incident", "inc-4", "old", "status=resolved, severity=low", at),
		incidentEdge("ev-e-open", "dep-1", "inc-1", "native", at),
		incidentEdge("ev-e-heur", "dep-2", "inc-2", "heuristic", at),
		incidentEdge("ev-e-gone", "dep-3", "inc-9", "native", at),
		incidentEdge("ev-e-closed", "dep-4", "inc-4", "native", at),
	}
	roles, packet := servedRoles(t, evidence)
	want := map[string]contractsv1.PacketCategory{
		"ev-pr-open": contractsv1.CategoryEvidence, "ev-pr-heur": contractsv1.CategoryEvidence,
		"ev-pr-gone": contractsv1.CategoryEvidence, "ev-pr-closed": contractsv1.CategoryEvidence,
	}
	for id, category := range want {
		if roles[id] != category {
			t.Errorf("%s served as %q, want %q", id, roles[id], category)
		}
	}
	related := map[string]string{}
	for _, item := range packet.Items {
		related[item.EvidenceRefIDs[0]] = item.WhyIncluded
	}
	if !strings.Contains(related["ev-pr-open"], "a deployment linked to an open incident") {
		t.Errorf("the incident-linked pull request carries no relation text: %q", related["ev-pr-open"])
	}
	for _, id := range []string{"ev-pr-heur", "ev-pr-gone", "ev-pr-closed"} {
		if strings.Contains(related[id], "match tier is not stored") {
			t.Errorf("%s was related through a link the rule rejects: %q", id, related[id])
		}
	}
	if !slices.Contains(packet.Warnings, "cause_links_demoted:1:link_tier_not_stored") {
		t.Errorf("exactly one demoted link must be counted: %v", packet.Warnings)
	}
}

func TestARevertPrefixSharedByTwoCommitsMarksNeither(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-c-one", "commit", "2222222aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "one", "one", at),
		roleRow("ev-c-two", "commit", "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "two", "two", at),
		roleRow("ev-c-sole", "commit", "4444444cccccccccccccccccccccccccccccccc", "sole", "sole", at),
		roleRow("ev-c-revert-1", "commit", "5555555dddddddddddddddddddddddddddddddd", "Revert", "This reverts commit 2222222.", at),
		roleRow("ev-c-revert-2", "commit", "6666666eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "Revert", "This reverts commit 4444444.", at),
	}
	roles, packet := servedRoles(t, evidence)
	if !slices.Contains(packet.Warnings, "revert_links_ambiguous:1") {
		t.Errorf("the ambiguous revert prefix is not counted: %v", packet.Warnings)
	}
	if roles["ev-c-one"] == contractsv1.CategoryCause || roles["ev-c-two"] == contractsv1.CategoryCause {
		t.Errorf("an ambiguous prefix marked a commit: %v", roles)
	}
	if roles["ev-c-sole"] != contractsv1.CategoryCause {
		t.Errorf("a unique prefix did not mark its commit: %v", roles)
	}
}

func TestFreeFormArtifactTextNeverChoosesARole(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	build := roleRow("ev-art-build", "build", "art-1", "build artifact", "succeeded; the log mentions error handling: error=0", at)
	build.SourceVersion = "ai_workflow_artifacts.v1"
	recommendation := roleRow("ev-art-rec", "recommendation", "art-2", "recommendation artifact", "do more", at)
	recommendation.SourceVersion = "ai_workflow_artifacts.v1"
	roles, _ := servedRoles(t, []contractsv1.EvidenceRef{build, recommendation})
	for _, id := range []string{"ev-art-build", "ev-art-rec"} {
		if roles[id] != contractsv1.CategoryEvidence {
			t.Errorf("%s served as %q, want evidence", id, roles[id])
		}
	}
}

func TestARevertIsACauseAndAnIncidentLinkedDeploymentsPullRequestIsOnlyARelation(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const reverted = "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-pr-carried", "pull_request", "77", "Slots", "merged", at),
		roleRow("ev-pr-other", "pull_request", "78", "Unrelated", "merged", at),
		roleRow("ev-c-reverted", "commit", reverted, "gift cards", "gift cards", at),
		roleRow("ev-c-revert", "commit", "3333333cccccccccccccccccccccccccccccccc", "Revert gift cards", "Revert gift cards\n\nThis reverts commit "+reverted+".", at),
		deploymentRow("ev-d-green", "dep-9", "success", "77", at),
		roleRow("ev-inc-open", "incident", "inc-9", "checkout down", "status=open, severity=high", at),
		incidentEdge("ev-e-link", "dep-9", "inc-9", "native", at),
	}
	roles, _ := servedRoles(t, evidence)
	if roles["ev-pr-carried"] != contractsv1.CategoryEvidence || roles["ev-c-reverted"] != contractsv1.CategoryCause {
		t.Errorf("roles = %v", roles)
	}
	if roles["ev-pr-other"] != contractsv1.CategoryEvidence || roles["ev-c-revert"] != contractsv1.CategoryEvidence {
		t.Errorf("roles = %v", roles)
	}
}

func TestAFailedDeploymentIsPressureAndItsStoredPullRequestIsOnlyARelation(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-pr-unlinked", "pull_request", "50", "Unrelated change", "merged", at),
		roleRow("ev-pr-linked", "pull_request", "51", "Linked change", "merged", at),
		deploymentRow("ev-d-nolink", "dep-a", "failure", "", at),
		deploymentRow("ev-d-link", "dep-b", "failure", "51", at),
	}
	roles, _ := servedRoles(t, evidence)
	want := map[string]contractsv1.PacketCategory{
		"ev-d-nolink": contractsv1.CategoryPressure, "ev-d-link": contractsv1.CategoryPressure,
		"ev-pr-unlinked": contractsv1.CategoryEvidence, "ev-pr-linked": contractsv1.CategoryEvidence,
	}
	for id, category := range want {
		if roles[id] != category {
			t.Errorf("%s served as %q, want %q", id, roles[id], category)
		}
	}
}

func TestAnArtifactRowNeverSourcesARevertOrAnIncidentLink(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	artifact := roleRow("ev-art-commit", "commit", "9999999ffffffffffffffffffffffffffffffffff", "artifact", "This reverts commit 1234567.", at)
	artifact.SourceVersion = "ai_workflow_artifacts.v1"
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-c-target", "commit", "1234567aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "target", "target", at),
		artifact,
	}
	roles, _ := servedRoles(t, evidence)
	if roles["ev-c-target"] == contractsv1.CategoryCause {
		t.Errorf("a free-form artifact row sourced a revert: %v", roles)
	}
}

func TestOnlyGitsOwnRevertLineMarksACommitAndTheCauseCitesTheReverter(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-c-negated", "commit", "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "negated target", "negated target", at),
		roleRow("ev-c-said-not", "commit", "5555555eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "not a revert", "Do not revert commit 1111111", at),
		roleRow("ev-c-target", "commit", "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "target", "target", at),
		roleRow("ev-c-reverter", "commit", "3333333cccccccccccccccccccccccccccccccc", "Revert target", "Revert target\n\nThis reverts commit 2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.", at),
	}
	roles, packet := servedRoles(t, evidence)
	if roles["ev-c-negated"] == contractsv1.CategoryCause {
		t.Errorf("a negated message marked a commit: %v", roles)
	}
	if roles["ev-c-target"] != contractsv1.CategoryCause {
		t.Fatalf("the reverted commit was not marked: %v", roles)
	}
	for _, item := range packet.Items {
		if item.EvidenceRefIDs[0] == "ev-c-target" && !slices.Contains(item.EvidenceRefIDs, "ev-c-reverter") {
			t.Errorf("the cause item does not cite the reverting commit: %v", item.EvidenceRefIDs)
		}
	}
}

func TestStatusTextIsMatchedWholeAndOnlyListedIncidentStatusesAreOpen(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-ci-text", "ci_pipeline_run", "run-1", "CI run-1", "success; error=0", at),
		roleRow("ev-ci-failed", "ci_pipeline_run", "run-2", "CI run-2", "Failed", at),
		roleRow("ev-i-listed", "incident", "inc-1", "listed", "status=acknowledged, severity=low", at),
		roleRow("ev-i-unlisted", "incident", "inc-2", "unlisted", "status=not_a_status, severity=low", at),
	}
	roles, _ := servedRoles(t, evidence)
	want := map[string]contractsv1.PacketCategory{
		"ev-ci-text": contractsv1.CategoryEvidence, "ev-ci-failed": contractsv1.CategoryPressure,
		"ev-i-listed": contractsv1.CategoryPressure, "ev-i-unlisted": contractsv1.CategoryEvidence,
	}
	for id, category := range want {
		if roles[id] != category {
			t.Errorf("%s served as %q, want %q", id, roles[id], category)
		}
	}
}

func TestEveryDeploymentLinkToOnePullRequestIsCountedAndFlaggedUncertain(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reserved := roleRow("ev-x-reserved", "check", "chk-1", "unrelated", "fine", at)
	reserved.Metadata = map[string]any{"acr_rule_id": "evidence.observed.cause.v1", "acr_claim_kind": "inferred"}
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-pr-42", "pull_request", "42", "Shared", "merged", at),
		deploymentRow("ev-d-link-a", "dep-a", "failure", "42", at),
		deploymentRow("ev-d-link-b", "dep-b", "failed", "42", at),
		reserved,
	}
	_, packet := servedRoles(t, evidence)
	if !slices.Contains(packet.Warnings, "cause_links_demoted:2:link_tier_not_stored") {
		t.Errorf("two deployment links to one pull request must count 2: %v", packet.Warnings)
	}
	for _, item := range packet.Items {
		switch item.EvidenceRefIDs[0] {
		case "ev-pr-42":
			if !item.Flags.Uncertain {
				t.Errorf("a demoted relation is not flagged uncertain")
			}
		case "ev-x-reserved":
			if item.RuleID == "evidence.observed.cause.v1" {
				t.Errorf("a row carried the reserved cause rule id through metadata")
			}
		}
	}
}

func TestUnavailableActionCategoryIsInCoverageAndPartialOnlyWhenRequested(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{roleRow("ev-c-one", "commit", "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "one", "one", at)}
	_, packet := servedRoles(t, evidence)
	listed := false
	for _, source := range packet.Coverage.SourcesUnavailable {
		listed = listed || (source.Source == "action_findings.v1" && source.Reason == contextpacket.ActionReasonNoStoredSource)
	}
	if !listed {
		t.Errorf("the unavailable action source is not in coverage: %+v", packet.Coverage)
	}
}

type fixedActionFindings struct {
	result contextpacket.ActionFindingsResult
}

func (f fixedActionFindings) ActionFindings(context.Context, storage.Principal, string) (contextpacket.ActionFindingsResult, error) {
	return f.result, nil
}

func dependencyRow(id, relationKey, blocker, status string, at time.Time) contractsv1.EvidenceRef {
	ref := roleRow(id, "work_item_dependency", id, "dependency "+id, "BLOCKS", at)
	ref.Metadata = map[string]any{"relation_key": relationKey, "blocker_work_item_id": blocker, "blocker_status": status}
	return ref
}

func TestOnlyAnActiveBlockersDependencyIsServedAsPressure(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		dependencyRow("ev-dep-open", "blocks:fwd", "PAY-12", "in_progress", at),
		dependencyRow("ev-dep-inv", "blocks:inv", "PAY-12", "blocked", at),
		dependencyRow("ev-dep-done", "blocks:fwd", "PAY-17", "done", at),
		dependencyRow("ev-dep-cancel", "blocks:fwd", "PAY-18", "canceled", at),
		dependencyRow("ev-dep-outside", "blocks:fwd", "OTHER-1", "", at),
		dependencyRow("ev-dep-rel", "relates_to:fwd", "", "in_progress", at),
	}
	roles, _ := servedRoles(t, evidence)
	want := map[string]contractsv1.PacketCategory{
		"ev-dep-open": contractsv1.CategoryPressure, "ev-dep-inv": contractsv1.CategoryPressure,
		"ev-dep-done": contractsv1.CategoryEvidence, "ev-dep-cancel": contractsv1.CategoryEvidence,
		"ev-dep-outside": contractsv1.CategoryEvidence, "ev-dep-rel": contractsv1.CategoryEvidence,
	}
	for id, category := range want {
		if roles[id] != category {
			t.Errorf("%s served as %q, want %q", id, roles[id], category)
		}
	}
}

func TestStoredTeamFindingsAreServedAsActionAndCarryNoUnavailableWarning(t *testing.T) {
	request := fixtureRequest("req-action", "main", "")
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{roleRow("ev-commit-1", "commit", "abcdef1", "work", "work", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))},
	}}
	options := fixedOptions()
	options.ActionFindings = fixedActionFindings{result: contextpacket.ActionFindingsResult{Teams: []contextpacket.TeamActionFindings{{
		TeamID: "CHECKOUT", EvidenceRefID: "acr:v1:team:CHECKOUT",
		Findings: []contextpacket.ActionFinding{{RuleID: "saturation", Severity: "high", Title: "Team is saturating. Reduce active work before adding scope", SuccessCriterion: "active work drops"}},
	}}}}
	packet, err := contextpacket.NewAssembler(store, options).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var action *contractsv1.ContextPacketItem
	for index := range packet.Items {
		if packet.Items[index].Category == contractsv1.CategoryAction {
			action = &packet.Items[index]
		}
	}
	if action == nil || !strings.Contains(action.Title, "saturating") {
		t.Fatalf("no stored-finding action item: %#v", packet.Items)
	}
	for _, warning := range packet.Warnings {
		if strings.HasPrefix(warning, "action_items_unavailable:") {
			t.Fatalf("unexpected warning %q", warning)
		}
	}
}

func TestUnreadableFindingsServeNoActionAndNameTheReason(t *testing.T) {
	request := fixtureRequest("req-action-none", "main", "")
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{roleRow("ev-commit-1", "commit", "abcdef1", "work", "work", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))},
	}}
	options := fixedOptions()
	options.ActionFindings = fixedActionFindings{result: contextpacket.ActionFindingsResult{Reason: contextpacket.ActionUnavailableNoGrant}}
	packet, err := contextpacket.NewAssembler(store, options).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, item := range packet.Items {
		if item.Category == contractsv1.CategoryAction {
			t.Fatalf("action served without a readable finding: %s", item.Title)
		}
	}
	if !slices.Contains(packet.Warnings, "action_items_unavailable:"+contextpacket.ActionUnavailableNoGrant) {
		t.Fatalf("warnings = %v", packet.Warnings)
	}
}

func TestOwningTeamWithNoFiredFindingServesNoActionAndNoWarning(t *testing.T) {
	request := fixtureRequest("req-action-empty", "main", "")
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{roleRow("ev-commit-1", "commit", "abcdef1", "work", "work", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))},
	}}
	options := fixedOptions()
	options.ActionFindings = fixedActionFindings{}
	packet, err := contextpacket.NewAssembler(store, options).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, item := range packet.Items {
		if item.Category == contractsv1.CategoryAction {
			t.Fatalf("action served with no finding: %s", item.Title)
		}
	}
	for _, warning := range packet.Warnings {
		if strings.HasPrefix(warning, "action_items_unavailable:") {
			t.Fatalf("warning %q for an owning team with nothing fired", warning)
		}
	}
}

func TestAnUnlistedStatusIsCountedAsUnknownNeitherPressureNorResolved(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := []contractsv1.EvidenceRef{
		roleRow("ev-i-weird", "incident", "inc-1", "weird", "status=not_a_status, severity=low", at),
		roleRow("ev-i-closed", "incident", "inc-2", "closed", "status=resolved, severity=low", at),
		roleRow("ev-i-open-1", "incident", "inc-3", "open", "status=open, severity=low", at),
		deploymentRow("ev-d-weird", "dep-1", "exploded", "", at),
		deploymentRow("ev-d-good-1", "dep-2", "success", "", at),
		deploymentRow("ev-d-failed", "dep-3", "failure", "", at),
	}
	roles, packet := servedRoles(t, evidence)
	if roles["ev-i-weird"] == contractsv1.CategoryPressure || roles["ev-d-weird"] == contractsv1.CategoryPressure {
		t.Errorf("an unlisted status became pressure: %v", roles)
	}
	for _, want := range []string{"status_unknown:incident:1", "status_unknown:deployment:1"} {
		if !slices.Contains(packet.Warnings, want) {
			t.Errorf("missing %q in %v", want, packet.Warnings)
		}
	}
}

func TestAnOpenIncidentMappedToTheRepositoryHeuristicallyIsNotPressure(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	native := roleRow("ev-i-native", "incident", "inc-1", "native mapping", "status=open, severity=high", at)
	heuristic := roleRow("ev-i-heuristic", "incident", "inc-2", "heuristic mapping", "status=open, severity=high", at)
	heuristic.Provenance = contractsv1.EvidenceProvenanceHeuristic
	roles, _ := servedRoles(t, []contractsv1.EvidenceRef{native, heuristic})
	if roles["ev-i-native"] != contractsv1.CategoryPressure {
		t.Errorf("a natively mapped open incident served as %q, want pressure", roles["ev-i-native"])
	}
	if roles["ev-i-heuristic"] == contractsv1.CategoryPressure {
		t.Errorf("a heuristically mapped open incident became pressure")
	}
}
