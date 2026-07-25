package main

import (
	"encoding/json"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const agentResultSchema = "context_fabric_agent_result.v1"

// The invented reference used by the invent-evidence self-test. It is syntactically valid
// and deliberately never seeded, so a suite that accepts it is not checking citations.
const inventedEvidenceRefID = "acr:v1:commit:0000000000000000000000000000000000000000"

// allScopeResolutions mirrors contracts/jsonschema/v1/context_packet.v1.schema.json's
// resolved_scope.resolution enum (also internal/contracts/v1/types.go's ScopeResolution), so
// the wrong-scope self-test can pick a substitute value programmatically rather than
// hardcoding one -- a hardcoded substitute would silently stop being wrong the day a fixture
// or scope shape changed the packet's genuine resolution to match it.
var allScopeResolutions = []string{
	string(contractsv1.ScopeExactCommit),
	string(contractsv1.ScopeBranchFiltered),
	string(contractsv1.ScopeRepoFallback),
	string(contractsv1.ScopeUnresolved),
}

// wrongScopeResolution returns a valid scope_resolution enum member other than observed, so
// FaultWrongScope's substitution is guaranteed to actually disagree with the live packet
// regardless of what it resolved to.
func wrongScopeResolution(observed string) string {
	for _, candidate := range allScopeResolutions {
		if candidate != observed {
			return candidate
		}
	}
	// Unreachable with four distinct enum members and one observed value; fail loudly rather
	// than silently emitting a no-op fault if the enum ever shrinks to a single member.
	panic("wrongScopeResolution: no alternate scope_resolution enum member available")
}

// allClaimKinds mirrors context_fabric_agent_result.v1's claim_kind enum
// (testdata/fullstack/v1/schema/context_fabric_agent_result.v1.schema.json), so
// downgradedClaimKind can pick a substitute programmatically rather than hardcoding one.
var allClaimKinds = []string{"observed", "inferred", "recommendation"}

// downgradedClaimKind returns a claim_kind enum member other than planned. FaultDowngradeClaimKind
// wants specifically a demotion away from "observed" -- that is the kind the harness schema's
// evidence_ref_ids minItems:1 conditional and the assertion tool's observed-only checks key
// off of -- so "inferred" is preferred when it differs from planned; any other member still
// guarantees an actual behavioural difference if planned itself is already "inferred".
func downgradedClaimKind(planned string) string {
	if planned != "inferred" {
		return "inferred"
	}
	for _, candidate := range allClaimKinds {
		if candidate != planned {
			return candidate
		}
	}
	panic("downgradedClaimKind: no alternate claim_kind enum member available")
}

type finding struct {
	ClaimID        string   `json:"claim_id"`
	ClaimKind      string   `json:"claim_kind"`
	Summary        string   `json:"summary"`
	EvidenceRefIDs []string `json:"evidence_ref_ids"`
}

type recommendedCheck struct {
	CheckID string `json:"check_id"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
}

type agentResult struct {
	SchemaVersion     string             `json:"schema_version"`
	TaskID            string             `json:"task_id"`
	PacketStatus      string             `json:"packet_status"`
	ScopeResolution   string             `json:"scope_resolution"`
	Findings          []finding          `json:"findings"`
	RecommendedChecks []recommendedCheck `json:"recommended_checks"`
	Assumptions       []string           `json:"assumptions"`
}

func degraded(status string) bool { return status == "empty" || status == "degraded" }

func recommendedChecks(plan Plan) []recommendedCheck {
	out := make([]recommendedCheck, 0, len(plan.RecommendedChecks))
	for _, check := range plan.RecommendedChecks {
		out = append(out, recommendedCheck{CheckID: check.CheckID, Label: check.Label, Reason: check.Reason})
	}
	return out
}

// selectEvidence resolves a planned selector against references the run actually observed.
//
// Selectors address the stable entity identity, never the reference string: the wire
// evidence_ref_id is an opaque signed token that differs per request, so it cannot be
// pattern matched. Supported forms:
//
//	all | ""                 every reference seen
//	expanded                 only references the run actually expanded
//	entity_type:<type>       every reference pointing at that entity type
//	entity:<type>/<id>       one specific entity
func selectEvidence(selector string, observed Observation) []string {
	matched := []string{}
	for _, sighting := range observed.Sightings {
		if matchesSelector(selector, sighting) {
			matched = append(matched, sighting.EvidenceRefID)
		}
	}
	return matched
}

func matchesSelector(selector string, sighting EvidenceSighting) bool {
	switch {
	case selector == "" || selector == "all":
		return true
	case selector == "expanded":
		return sighting.Expanded
	case strings.HasPrefix(selector, "entity_type:"):
		return sighting.EntityType == strings.TrimPrefix(selector, "entity_type:")
	case strings.HasPrefix(selector, "entity:"):
		wanted := strings.TrimPrefix(selector, "entity:")
		entityType, entityID, found := strings.Cut(wanted, "/")
		return found && sighting.EntityType == entityType && sighting.EntityID == entityID
	default:
		return false
	}
}

// buildResult assembles the final answer. Status, scope resolution and every citation come
// from the live observation; only claim identity and wording come from the plan.
func buildResult(plan Plan, observed Observation) agentResult {
	result := agentResult{
		SchemaVersion:     agentResultSchema,
		TaskID:            plan.TaskID,
		PacketStatus:      observed.PacketStatus,
		ScopeResolution:   observed.ScopeResolution,
		Findings:          []finding{},
		RecommendedChecks: recommendedChecks(plan),
		Assumptions:       []string{},
	}

	if plan.Fault == FaultInflateStatus {
		result.PacketStatus = "complete"
	}
	if plan.Fault == FaultWrongScope {
		result.ScopeResolution = wrongScopeResolution(observed.ScopeResolution)
	}

	fabricate := plan.Fault == FaultFabricateFindings
	if degraded(observed.PacketStatus) && !fabricate {
		// A degraded or empty packet is reported as such rather than filled in from
		// background knowledge; the degradation itself is the finding.
		result.Assumptions = append(result.Assumptions,
			"the context packet reported status "+observed.PacketStatus+"; no evidence was available for this scope")
		result.Assumptions = append(result.Assumptions, observed.Warnings...)
		return result
	}

	// unsupportedClaimPending/downgradeClaimKindPending apply their fault to only the first
	// eligible finding, not every one of them: a predictable, single, targeted violation is
	// easier to reason about (and to name a check for) than a fault that happens to distort
	// every finding in the answer. Only one of plan.Fault's values can ever be active at once,
	// so at most one of these two ever actually fires.
	unsupportedClaimPending := plan.Fault == FaultUnsupportedClaim
	downgradeClaimKindPending := plan.Fault == FaultDowngradeClaimKind
	for _, planned := range plan.Findings {
		citations := selectEvidence(planned.EvidenceSelector, observed)
		claimKind := planned.ClaimKind
		unsupported := unsupportedClaimPending && planned.ClaimKind == "observed"
		if unsupported {
			// Deliberately bypass the refusal below: assert the claim anyway with no
			// evidence_ref_ids. buildResult's own doc comment says every citation comes
			// from the live observation; this fault is the one deliberate exception, and it
			// exists only to prove the assertion layer rejects exactly this.
			citations = []string{}
			unsupportedClaimPending = false
		}
		downgrade := downgradeClaimKindPending
		if downgrade {
			// Report the required claim_id under a different claim_kind AND with no
			// citations -- the fault this is meant to prove is that a downgrade lets an
			// agent dodge the observed-only checks (no_invented_evidence_ids,
			// observed_finding_has_citation) entirely, not merely fail one of them, so
			// stopping at just the kind change would not exercise that.
			claimKind = downgradedClaimKind(planned.ClaimKind)
			citations = []string{}
			downgradeClaimKindPending = false
		}
		if planned.ClaimKind == "observed" && len(citations) == 0 && !fabricate && !unsupported && !downgrade {
			// Refuse to assert an observation the run cannot cite.
			result.Assumptions = append(result.Assumptions,
				"no returned evidence supported claim "+planned.ClaimID)
			continue
		}
		result.Findings = append(result.Findings, finding{
			ClaimID:        planned.ClaimID,
			ClaimKind:      claimKind,
			Summary:        summarize(planned, citations, observed),
			EvidenceRefIDs: citations,
		})
	}
	if unsupportedClaimPending {
		// The plan had no observed finding to strip citations from, so the fault would have
		// been a silent no-op -- exactly the "rejection for an unrelated reason" failure mode
		// fabricate-findings originally hit on task-001 (see run_fault_self_test). Fail loudly
		// rather than emit an answer indistinguishable from FaultNone.
		panic("FaultUnsupportedClaim: plan has no observed finding to strip a citation from")
	}
	if downgradeClaimKindPending {
		panic("FaultDowngradeClaimKind: plan has no finding to downgrade the claim_kind of")
	}

	// A no-findings oracle has no planned finding to distort, so the fault invents one outright.
	// This models an agent that promotes background or wider-scope context into an unsupported claim.
	if fabricate && len(result.Findings) == 0 {
		result.Findings = append(result.Findings, finding{
			ClaimID:   "fault-fabricated-finding",
			ClaimKind: "observed",
			Summary:   "self-test claim asserted for a scope the run returned no evidence for",
		})
	}

	if plan.Fault == FaultInventEvidence {
		if len(result.Findings) == 0 {
			result.Findings = append(result.Findings, finding{
				ClaimID:   "fault-invented-citation",
				ClaimKind: "observed",
				Summary:   "self-test claim citing a reference the run never returned",
			})
		}
		last := len(result.Findings) - 1
		result.Findings[last].EvidenceRefIDs = append(result.Findings[last].EvidenceRefIDs, inventedEvidenceRefID)
	}

	return result
}

func encodeResult(result agentResult) string {
	// The prompt requires a bare JSON document, so this is emitted without a code fence and
	// without surrounding prose.
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"schema_version":"` + agentResultSchema + `","error":"encode failed"}`
	}
	return string(encoded)
}

// summarize writes the finding's prose from what the run returned for the evidence it cites,
// falling back to the planned wording only when the live result carried no describable
// identity. Wording taken wholesale from the plan would make the answer say exactly what the
// oracle expects regardless of what the tools returned, which is the one thing this harness
// must not do.
func summarize(planned PlannedFinding, citations []string, observed Observation) string {
	for _, id := range citations {
		for _, sighting := range observed.Sightings {
			if sighting.EvidenceRefID != id {
				continue
			}
			switch {
			case sighting.Label != "" && sighting.EntityType != "":
				return sighting.EntityType + " " + sighting.Label + " supports " + planned.ClaimID
			case sighting.EntityType != "" && sighting.EntityID != "":
				return sighting.EntityType + " " + sighting.EntityID + " supports " + planned.ClaimID
			}
		}
	}
	return planned.Summary
}
