package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Fault names a deliberate deviation used by the harness self-test to prove the assertion
// layers actually reject bad agent behaviour. Production acceptance runs use FaultNone.
type Fault string

const (
	// FaultNone transcribes the live packet faithfully.
	FaultNone Fault = ""
	// FaultInventEvidence cites an evidence reference that no tool response returned.
	FaultInventEvidence Fault = "invent-evidence"
	// FaultInflateStatus reports a healthier packet status than the packet declared.
	FaultInflateStatus Fault = "inflate-status"
	// FaultFabricateFindings reports findings where the oracle requires none.
	FaultFabricateFindings Fault = "fabricate-findings"
	// FaultSkipEvidence never calls source_evidence.
	FaultSkipEvidence Fault = "skip-evidence"
	// FaultWrongScope reports a scope_resolution that disagrees with the live packet.
	FaultWrongScope Fault = "wrong-scope"
	// FaultUnsupportedClaim asserts an observed finding with no citation, bypassing the
	// refusal buildResult otherwise applies to an uncitable observation.
	FaultUnsupportedClaim Fault = "unsupported-claim"
	// FaultDowngradeClaimKind reports the oracle's required claim_id with a different
	// claim_kind than the oracle declares (e.g. "inferred" instead of "observed") and no
	// citations -- the downgrade is meant to dodge the observed-only checks entirely, not
	// merely fail one of them.
	FaultDowngradeClaimKind Fault = "downgrade-claim-kind"
)

func validFault(f Fault) bool {
	switch f {
	case FaultNone, FaultInventEvidence, FaultInflateStatus, FaultFabricateFindings, FaultSkipEvidence,
		FaultWrongScope, FaultUnsupportedClaim, FaultDowngradeClaimKind:
		return true
	}
	return false
}

// Scope mirrors the request scope the sidecar accepts. Empty fields are omitted so the
// sidecar performs its own resolution rather than receiving a blank filter.
type Scope struct {
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	TaskRef   string `json:"task_ref,omitempty"`
	// AsOf carries the fixture's pinned instant. Without it the client's request and the
	// driver's independent direct request are not the same request: the direct one is pinned
	// and the agent's is not, so the two only agree while the service happens to apply no
	// default time filter. The whole point of the cross-surface comparison is that they are
	// the same question asked twice.
	AsOf string `json:"as_of,omitempty"`
}

// PlannedFinding is the scripted half of a finding. The claim identity and wording are
// fixed by the plan so the oracle can assert them; the citations are not — EvidenceSelector
// chooses from references the live run actually returned.
type PlannedFinding struct {
	ClaimID          string `json:"claim_id"`
	ClaimKind        string `json:"claim_kind"`
	Summary          string `json:"summary"`
	EvidenceSelector string `json:"evidence_selector"`
}

// PlannedCheck mirrors the product's RequiredCheck shape, which is also what
// context_fabric_agent_result.v1 requires: a check is an identified, explained action, not a
// bare string.
type PlannedCheck struct {
	CheckID string `json:"check_id"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
}

// Plan is the scripted turn sequence for exactly one acceptance task.
//
// The split is deliberate: claim identities, wording and required checks are scripted so the
// oracle has something stable to assert, while packet status, scope resolution and every
// cited evidence reference are read back from the live tool responses. A broken ACR read
// path therefore cannot be masked by the script.
type Plan struct {
	SchemaVersion         string           `json:"schema_version"`
	TaskID                string           `json:"task_id"`
	Goal                  string           `json:"goal"`
	RepositorySlug        string           `json:"repository_slug"`
	Scope                 Scope            `json:"scope"`
	MinEvidenceExpansions int              `json:"min_evidence_expansions"`
	Findings              []PlannedFinding `json:"findings"`
	RecommendedChecks     []PlannedCheck   `json:"recommended_checks"`
	Fault                 Fault            `json:"fault,omitempty"`
}

func loadPlan(path string) (Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, fmt.Errorf("read plan: %w", err)
	}
	var plan Plan
	decoder := json.NewDecoder(newTrimReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode plan: %w", err)
	}
	if plan.SchemaVersion != "fullstack_model_plan.v1" {
		return Plan{}, fmt.Errorf("unsupported plan schema %q", plan.SchemaVersion)
	}
	if plan.TaskID == "" || plan.Goal == "" || plan.RepositorySlug == "" {
		return Plan{}, fmt.Errorf("plan requires task_id, goal and repository_slug")
	}
	if plan.MinEvidenceExpansions < 0 {
		return Plan{}, fmt.Errorf("min_evidence_expansions must not be negative")
	}
	if !validFault(plan.Fault) {
		return Plan{}, fmt.Errorf("unknown fault %q", plan.Fault)
	}
	for _, check := range plan.RecommendedChecks {
		if check.CheckID == "" || check.Label == "" || check.Reason == "" {
			return Plan{}, fmt.Errorf("every recommended check requires check_id, label and reason")
		}
	}
	for _, finding := range plan.Findings {
		switch finding.ClaimKind {
		case "observed", "inferred", "recommendation":
		default:
			return Plan{}, fmt.Errorf("finding %q has unknown claim kind %q", finding.ClaimID, finding.ClaimKind)
		}
		if finding.ClaimID == "" || finding.Summary == "" {
			return Plan{}, fmt.Errorf("every planned finding requires claim_id and summary")
		}
		if finding.ClaimKind == "observed" && finding.EvidenceSelector == "" {
			return Plan{}, fmt.Errorf("observed finding %q requires an evidence_selector", finding.ClaimID)
		}
	}
	return plan, nil
}

// contextArguments is the tool input for context_for_task. Scope is omitted entirely when
// the task pins nothing, so the request never sends an empty filter object.
func (p Plan) contextArguments() map[string]any {
	args := map[string]any{
		"goal":       p.Goal,
		"repository": map[string]any{"slug": p.RepositorySlug},
	}
	scope := map[string]any{}
	if p.Scope.Branch != "" {
		scope["branch"] = p.Scope.Branch
	}
	if p.Scope.CommitSHA != "" {
		scope["commit_sha"] = p.Scope.CommitSHA
	}
	if p.Scope.TaskRef != "" {
		scope["task_ref"] = p.Scope.TaskRef
	}
	if p.Scope.AsOf != "" {
		scope["as_of"] = p.Scope.AsOf
	}
	if len(scope) > 0 {
		args["scope"] = scope
	}
	return args
}
