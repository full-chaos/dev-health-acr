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

func itemTokens(item contractsv1.ContextPacketItem) int {
	return (len(item.Title) + len(item.Summary) + len(item.WhyIncluded) + len(item.RuleID) + 3) / 4
}

func tierFillers(at time.Time) []contractsv1.EvidenceRef {
	var rows []contractsv1.EvidenceRef
	for _, id := range []string{"a", "b", "c", "d"} {
		rows = append(rows, roleRow("ev-fill-"+id, "ci_pipeline_run", "run-"+id, "checkout "+strings.Repeat(id, 500), "failed", at))
	}
	return rows
}

// servedUnderBudget assembles twice: once with room to spare to price every
// item, then with a token budget that holds the goal-naming filler rows, which rank ahead of the causes, plus
// exactly the priced items named in admit.
func servedUnderBudget(t *testing.T, evidence []contractsv1.EvidenceRef, admit ...string) contractsv1.ContextPacket {
	t.Helper()
	assemble := func(tokens int) contractsv1.ContextPacket {
		request := fixtureRequest("req-cause-tier", "main", "")
		request.Goal = "checkout"
		request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 20, tokens, 65536
		store := testStore{bundle: storage.EvidenceBundle{
			ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
			Evidence:      evidence,
		}}
		packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}
		return packet
	}
	cost, total := map[string]int{}, 0
	for _, item := range assemble(16000).Items {
		cost[item.EvidenceRefIDs[0]] = itemTokens(item)
		if strings.HasPrefix(item.EvidenceRefIDs[0], "ev-fill-") {
			total += itemTokens(item)
		}
	}
	for _, id := range admit {
		total += cost[id]
	}
	if total < 500 {
		t.Fatalf("budget %d is under the request minimum of 500", total)
	}
	return assemble(total)
}

func servedIDs(packet contractsv1.ContextPacket) []string {
	var ids []string
	for _, item := range packet.Items {
		ids = append(ids, item.EvidenceRefIDs[0])
	}
	return ids
}

func TestABudgetMissOnAStrongerCauseTierDoesNotAdmitAWeakerOne(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := append(tierFillers(at),
		roleRow("ev-pr-merged", "pull_request", "2", "Merged", "merged", at),
		roleRow("ev-pr-pushed", "pull_request", "3", "Pushed", "merged", at),
		tieredDeploymentRow("ev-d-merged", "d2", "failure", "2", "merged_pr", at),
		tieredDeploymentRow("ev-d-pushed", "d3", "failure", "3", "pushed", at),
	)
	packet := servedUnderBudget(t, evidence, "ev-pr-pushed")
	ids := servedIDs(packet)
	if slices.Contains(ids, "ev-pr-merged") || slices.Contains(ids, "ev-pr-pushed") {
		t.Errorf("served %v: the merged_pr cause missed the budget, so the weaker pushed cause must not be admitted", ids)
	}
	if !packet.Budget.Truncated || !slices.Contains(packet.Warnings, "context_truncated") {
		t.Errorf("truncated=%v warnings=%v, want context_truncated", packet.Budget.Truncated, packet.Warnings)
	}
}

func TestABudgetMissOnACauseStillAdmitsAShorterItemOfAnotherKind(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := append(tierFillers(at),
		roleRow("ev-pr-merged", "pull_request", "2", "Merged", "merged", at),
		tieredDeploymentRow("ev-d-merged", "d2", "failure", "2", "merged_pr", at),
		roleRow("ev-pr-short", "pull_request", "9", "Short", "merged", at),
	)
	ids := servedIDs(servedUnderBudget(t, evidence, "ev-pr-short"))
	if slices.Contains(ids, "ev-pr-merged") || !slices.Contains(ids, "ev-pr-short") {
		t.Errorf("served %v, want the short non-cause item kept and the long cause skipped", ids)
	}
}

func TestABudgetMissOnACauseStillAdmitsALaterCauseOfTheSameTier(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evidence := append(tierFillers(at),
		roleRow("ev-pr-long", "pull_request", "1000000", strings.Repeat("L", 60), "merged", at.Add(time.Hour)),
		roleRow("ev-pr-brief", "pull_request", "2", "B", "merged", at),
		tieredDeploymentRow("ev-d-long", "d1", "failure", "1000000", "merged_pr", at),
		tieredDeploymentRow("ev-d-brief", "d2", "failure", "2", "merged_pr", at),
	)
	ids := servedIDs(servedUnderBudget(t, evidence, "ev-pr-brief"))
	if slices.Contains(ids, "ev-pr-long") || !slices.Contains(ids, "ev-pr-brief") {
		t.Errorf("served %v, want the shorter same-tier cause kept after the longer one missed", ids)
	}
}

func TestABudgetMissOnATieredCauseStillAdmitsAnUntieredCause(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const reverted = "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	evidence := append(tierFillers(at),
		roleRow("ev-pr-merged", "pull_request", "2", "Merged", "merged", at),
		tieredDeploymentRow("ev-d-merged", "d2", "failure", "2", "merged_pr", at),
		roleRow("ev-c-reverted", "commit", reverted, "gift", "gift", at),
		roleRow("ev-c-revert", "commit", "3333333cccccccccccccccccccccccccccccccc", "Revert", "Revert\n\nThis reverts commit "+reverted+".", at),
	)
	ids := servedIDs(servedUnderBudget(t, evidence, "ev-c-reverted"))
	if slices.Contains(ids, "ev-pr-merged") || !slices.Contains(ids, "ev-c-reverted") {
		t.Errorf("served %v, want the untiered revert cause kept after the tiered cause missed", ids)
	}
}
