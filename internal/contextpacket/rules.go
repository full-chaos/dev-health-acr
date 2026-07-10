package contextpacket

import (
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type rule struct {
	id           string
	category     contractsv1.PacketCategory
	claim        contractsv1.ClaimKind
	severity     contractsv1.Severity
	categoryRank int
}

func evidenceRule(ref contractsv1.EvidenceRef) rule {
	base := evidenceCategoryRule(ref)
	if outcome, ok := explicitRuleOutcome(ref.Metadata); ok {
		base.id, base.claim = outcome.id, outcome.claim
	}
	return base
}

func evidenceCategoryRule(ref contractsv1.EvidenceRef) rule {
	switch strings.ToLower(ref.Source.EntityType) {
	case "check", "check_run", "workflow_run", "build", "incident", "alert":
		return rule{"evidence.observed.pressure.v1", contractsv1.CategoryPressure, contractsv1.ClaimObserved, contractsv1.SeverityHigh, 1}
	case "commit", "change", "diff":
		return rule{"evidence.observed.cause.v1", contractsv1.CategoryCause, contractsv1.ClaimObserved, contractsv1.SeverityWarning, 2}
	case "recommendation", "review_action", "task":
		return rule{"evidence.observed.action.v1", contractsv1.CategoryAction, contractsv1.ClaimObserved, contractsv1.SeverityWarning, 4}
	case "state", "deployment", "release":
		return rule{"evidence.observed.state.v1", contractsv1.CategoryState, contractsv1.ClaimObserved, contractsv1.SeverityInfo, 0}
	default:
		return rule{"evidence.observed.reference.v1", contractsv1.CategoryEvidence, contractsv1.ClaimObserved, contractsv1.SeverityInfo, 3}
	}
}

type ruleOutcome struct {
	id    string
	claim contractsv1.ClaimKind
}

func explicitRuleOutcome(metadata map[string]any) (ruleOutcome, bool) {
	ruleID, ruleIDOK := metadata["acr_rule_id"].(string)
	claim, claimOK := metadata["acr_claim_kind"].(string)
	if !ruleIDOK || !claimOK || strings.TrimSpace(ruleID) == "" || len(ruleID) > 240 {
		return ruleOutcome{}, false
	}
	switch contractsv1.ClaimKind(claim) {
	case contractsv1.ClaimInferred, contractsv1.ClaimRecommendation:
		return ruleOutcome{id: ruleID, claim: contractsv1.ClaimKind(claim)}, true
	default:
		return ruleOutcome{}, false
	}
}

func actionsFor(items []contractsv1.ContextPacketItem) ([]contractsv1.RequiredCheck, []contractsv1.RecommendedStep) {
	rules := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item.RuleID] {
			seen[item.RuleID] = true
			rules = append(rules, item.RuleID)
		}
	}
	sort.Strings(rules)
	checks, steps := make([]contractsv1.RequiredCheck, 0, len(rules)), make([]contractsv1.RecommendedStep, 0, len(rules))
	for _, ruleID := range rules {
		checks = append(checks, contractsv1.RequiredCheck{CheckID: "check." + ruleID, Label: "Validate retrieved evidence", Reason: "Retrieved content is untrusted.", RuleID: ruleID})
		steps = append(steps, contractsv1.RecommendedStep{StepID: "step." + ruleID, Label: "Investigate retrieved evidence", Reason: "Use cited evidence before changing code.", RuleID: ruleID})
	}
	return checks, steps
}
