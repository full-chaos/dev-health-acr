package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func observationFixture(status string) Observation {
	return Observation{
		PacketStatus:    status,
		ScopeResolution: "exact_commit",
		Sightings: []EvidenceSighting{
			{EvidenceRefID: "ref-commit", EntityType: "commit", EntityID: "a1b2", Expanded: true},
			{EvidenceRefID: "ref-ci", EntityType: "ci_pipeline_run", EntityID: "checkout-e2e-run-4821"},
		},
		Warnings: []string{"no_evidence_found"},
	}
}

func planFixture(fault Fault) Plan {
	return Plan{
		SchemaVersion:         "fullstack_model_plan.v1",
		TaskID:                "task-001-checkout-flake-exact-commit",
		Goal:                  "Investigate the flaky checkout integration test on a pinned commit.",
		RepositorySlug:        "example-org/widget-service",
		Scope:                 Scope{CommitSHA: "a1b2"},
		MinEvidenceExpansions: 2,
		Findings: []PlannedFinding{
			{ClaimID: "finding:commit:a1b2", ClaimKind: "observed", Summary: "commit introduced a fixed-delay wait", EvidenceSelector: "entity_type:commit"},
			{ClaimID: "finding:ci:checkout-e2e-run-4821", ClaimKind: "observed", Summary: "CI retried the add-to-cart step", EvidenceSelector: "entity_type:ci_pipeline_run"},
		},
		RecommendedChecks: []PlannedCheck{{
			CheckID: "check.evidence.observed.cause.v1",
			Label:   "Re-run the checkout end-to-end suite",
			Reason:  "the packet cites a flaky retry on this commit",
		}},
		Fault: fault,
	}
}

// The scripted model must transcribe the live packet, not the plan: a plan that claims
// exact_commit cannot rescue a run whose packet resolved differently.
func TestBuildResultTranscribesLiveStatus(t *testing.T) {
	observed := observationFixture("complete")
	observed.ScopeResolution = "branch_filtered"
	result := buildResult(planFixture(FaultNone), observed)
	if result.PacketStatus != "complete" || result.ScopeResolution != "branch_filtered" {
		t.Fatalf("result did not transcribe the packet: %+v", result)
	}
	if result.SchemaVersion != agentResultSchema || result.TaskID != planFixture(FaultNone).TaskID {
		t.Fatalf("result identity = %+v", result)
	}
}

func TestBuildResultCitesOnlyObservedReferences(t *testing.T) {
	result := buildResult(planFixture(FaultNone), observationFixture("complete"))
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(result.Findings))
	}
	seen := map[string]bool{"ref-commit": true, "ref-ci": true}
	for _, finding := range result.Findings {
		if len(finding.EvidenceRefIDs) == 0 {
			t.Fatalf("observed finding %s cited nothing", finding.ClaimID)
		}
		for _, id := range finding.EvidenceRefIDs {
			if !seen[id] {
				t.Fatalf("finding %s cited unobserved %q", finding.ClaimID, id)
			}
		}
	}
}

func TestBuildResultRefusesUncitableObservations(t *testing.T) {
	observed := observationFixture("complete")
	observed.Sightings = observed.Sightings[:1] // drop the CI reference
	result := buildResult(planFixture(FaultNone), observed)
	for _, finding := range result.Findings {
		if finding.ClaimID == "finding:ci:checkout-e2e-run-4821" {
			t.Fatal("asserted an observation with no supporting evidence")
		}
	}
	if len(result.Assumptions) == 0 {
		t.Fatal("dropping an unsupported claim must be disclosed in assumptions")
	}
}

func TestBuildResultKeepsDegradedPacketsEmpty(t *testing.T) {
	for _, status := range []string{"empty", "degraded"} {
		result := buildResult(planFixture(FaultNone), observationFixture(status))
		if len(result.Findings) != 0 {
			t.Fatalf("status %s produced %d findings", status, len(result.Findings))
		}
		if result.PacketStatus != status {
			t.Fatalf("status %s was reported as %s", status, result.PacketStatus)
		}
		if len(result.Assumptions) == 0 {
			t.Fatalf("status %s did not disclose the degradation", status)
		}
	}
}

// Each fault is what the harness self-test uses to prove the assertion layers reject bad
// agent behaviour rather than merely passing a well-behaved script.
func TestFaultsProduceTheBehaviourTheSelfTestNeeds(t *testing.T) {
	invented := buildResult(planFixture(FaultInventEvidence), observationFixture("complete"))
	if !strings.Contains(encodeResult(invented), inventedEvidenceRefID) {
		t.Fatal("invent-evidence did not cite an unobserved reference")
	}

	inflated := buildResult(planFixture(FaultInflateStatus), observationFixture("degraded"))
	if inflated.PacketStatus != "complete" {
		t.Fatalf("inflate-status reported %q", inflated.PacketStatus)
	}

	fabricated := buildResult(planFixture(FaultFabricateFindings), observationFixture("partial"))
	if len(fabricated.Findings) == 0 {
		t.Fatal("fabricate-findings must report a finding where the oracle requires none")
	}

	// wrong-scope must trip agent_result_scope_resolution_matches_live_packet: the reported
	// scope_resolution must disagree with what the live packet actually resolved to.
	observed := observationFixture("complete")
	wrongScope := buildResult(planFixture(FaultWrongScope), observed)
	if wrongScope.ScopeResolution == observed.ScopeResolution {
		t.Fatalf("wrong-scope reported %q, the same as the live packet", wrongScope.ScopeResolution)
	}

	// unsupported-claim must trip observed_finding_has_citation[claim_id]: an observed
	// finding must be asserted with zero evidence_ref_ids, on a task-001-shaped packet where
	// every planned finding would otherwise have real citations available.
	unsupported := buildResult(planFixture(FaultUnsupportedClaim), observed)
	foundUncited := false
	for _, finding := range unsupported.Findings {
		if finding.ClaimKind == "observed" && len(finding.EvidenceRefIDs) == 0 {
			foundUncited = true
		}
	}
	if !foundUncited {
		t.Fatal("unsupported-claim did not assert any observed finding with zero evidence_ref_ids")
	}

	// downgrade-claim-kind must trip required_finding_claim_kind_matches[claim_id]: the
	// required claim_id must come back under a DIFFERENT claim_kind than planned (the oracle's
	// declared kind), with no citations, so the dodge is complete rather than partial.
	downgraded := buildResult(planFixture(FaultDowngradeClaimKind), observed)
	if len(downgraded.Findings) == 0 {
		t.Fatal("downgrade-claim-kind must still assert the required claim_id, just under a different kind")
	}
	first := downgraded.Findings[0]
	if first.ClaimKind == "observed" {
		t.Fatalf("downgrade-claim-kind did not change claim_kind away from the planned %q", first.ClaimKind)
	}
	if len(first.EvidenceRefIDs) != 0 {
		t.Fatalf("downgrade-claim-kind should report zero citations, got %v", first.EvidenceRefIDs)
	}
}

// TestFaultWrongScopeAlwaysDisagrees pins the "never a hardcoded substitute" property directly:
// for every possible observed scope_resolution, the fault's output must differ from it.
func TestFaultWrongScopeAlwaysDisagreesWithObserved(t *testing.T) {
	for _, observedResolution := range allScopeResolutions {
		observed := observationFixture("complete")
		observed.ScopeResolution = observedResolution
		result := buildResult(planFixture(FaultWrongScope), observed)
		if result.ScopeResolution == observedResolution {
			t.Fatalf("wrong-scope did not disagree when the live packet resolved %q", observedResolution)
		}
		found := false
		for _, candidate := range allScopeResolutions {
			found = found || candidate == result.ScopeResolution
		}
		if !found {
			t.Fatalf("wrong-scope substituted %q, not a valid scope_resolution enum member", result.ScopeResolution)
		}
	}
}

// TestFaultUnsupportedClaimStripsOnlyTheFirstObservedFinding pins the "predictable, single,
// targeted violation" design: with two observed findings available, only the first loses its
// citations, so the self-test can name one deterministic check.
func TestFaultUnsupportedClaimStripsOnlyTheFirstObservedFinding(t *testing.T) {
	result := buildResult(planFixture(FaultUnsupportedClaim), observationFixture("complete"))
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (both planned claims must still be asserted)", len(result.Findings))
	}
	if result.Findings[0].ClaimID != "finding:commit:a1b2" || len(result.Findings[0].EvidenceRefIDs) != 0 {
		t.Fatalf("first finding = %+v, want finding:commit:a1b2 with zero citations", result.Findings[0])
	}
	if result.Findings[1].ClaimID != "finding:ci:checkout-e2e-run-4821" || len(result.Findings[1].EvidenceRefIDs) == 0 {
		t.Fatalf("second finding = %+v, want finding:ci:checkout-e2e-run-4821 with its normal citation(s)", result.Findings[1])
	}
}

// TestFaultUnsupportedClaimPanicsRatherThanNoOp is the same discipline fabricate-findings
// needed on task-001 (see run_fault_self_test's comment): a fault with nothing to act on must
// fail loudly, never silently emit an answer indistinguishable from FaultNone.
func TestFaultUnsupportedClaimPanicsRatherThanNoOp(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the plan has no observed finding to strip a citation from")
		}
	}()
	plan := planFixture(FaultUnsupportedClaim)
	plan.Findings = []PlannedFinding{{ClaimID: "finding:x", ClaimKind: "inferred", Summary: "s"}}
	buildResult(plan, observationFixture("complete"))
}

// TestFaultDowngradeClaimKindAffectsOnlyTheFirstFinding pins the same "predictable, single,
// targeted violation" design as unsupported-claim: only the first finding is downgraded, and
// specifically to "inferred" (not left to whatever the enum sweep happens to pick), so the
// self-test can name one deterministic check.
func TestFaultDowngradeClaimKindAffectsOnlyTheFirstFinding(t *testing.T) {
	result := buildResult(planFixture(FaultDowngradeClaimKind), observationFixture("complete"))
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (both planned claims must still be asserted)", len(result.Findings))
	}
	if result.Findings[0].ClaimID != "finding:commit:a1b2" || result.Findings[0].ClaimKind != "inferred" || len(result.Findings[0].EvidenceRefIDs) != 0 {
		t.Fatalf("first finding = %+v, want finding:commit:a1b2 with claim_kind=inferred and zero citations", result.Findings[0])
	}
	if result.Findings[1].ClaimID != "finding:ci:checkout-e2e-run-4821" || result.Findings[1].ClaimKind != "observed" || len(result.Findings[1].EvidenceRefIDs) == 0 {
		t.Fatalf("second finding = %+v, want finding:ci:checkout-e2e-run-4821 unaffected, still observed with its normal citation(s)", result.Findings[1])
	}
}

// TestDowngradedClaimKindAlwaysDiffers pins the "never a hardcoded no-op" property: for every
// possible planned claim_kind, the substitute must actually differ from it.
func TestDowngradedClaimKindAlwaysDiffers(t *testing.T) {
	for _, planned := range allClaimKinds {
		got := downgradedClaimKind(planned)
		if got == planned {
			t.Fatalf("downgradedClaimKind(%q) did not differ from the input", planned)
		}
		found := false
		for _, candidate := range allClaimKinds {
			found = found || candidate == got
		}
		if !found {
			t.Fatalf("downgradedClaimKind(%q) = %q, not a valid claim_kind enum member", planned, got)
		}
	}
}

// TestFaultDowngradeClaimKindPanicsRatherThanNoOp is the same discipline as
// TestFaultUnsupportedClaimPanicsRatherThanNoOp: a fault with nothing to act on must fail
// loudly, never silently emit an answer indistinguishable from FaultNone.
func TestFaultDowngradeClaimKindPanicsRatherThanNoOp(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the plan has no finding to downgrade the claim_kind of")
		}
	}()
	plan := planFixture(FaultDowngradeClaimKind)
	plan.Findings = nil
	buildResult(plan, observationFixture("complete"))
}

func TestExpansionsNeverExceedWhatThePacketReturned(t *testing.T) {
	plan := planFixture(FaultNone)
	plan.MinEvidenceExpansions = 5
	if got := expansions(plan, observationFixture("complete")); got != 2 {
		t.Fatalf("expansions = %d, want 2", got)
	}
	if got := expansions(plan, observationFixture("empty")); got != 0 {
		t.Fatalf("degraded packets must not be expanded, got %d", got)
	}
	if got := expansions(planFixture(FaultSkipEvidence), observationFixture("complete")); got != 0 {
		t.Fatalf("skip-evidence expansions = %d", got)
	}
}

func TestEncodeResultEmitsBareJSON(t *testing.T) {
	encoded := encodeResult(buildResult(planFixture(FaultNone), observationFixture("complete")))
	if strings.HasPrefix(encoded, "```") || strings.Contains(encoded, "\n") {
		t.Fatalf("final answer was not a bare single-line JSON document: %q", encoded)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("final answer is not valid JSON: %v", err)
	}
	for _, key := range []string{"schema_version", "task_id", "packet_status", "scope_resolution", "findings", "recommended_checks", "assumptions"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("final answer is missing %q", key)
		}
	}
}

// The finding's wording must come from what the run returned, not from the plan. A harness
// whose answer text is copied from the oracle can pass with a broken read path.
func TestSummaryComesFromLiveEvidence(t *testing.T) {
	plan := Plan{
		TaskID: "task-x",
		Findings: []PlannedFinding{{
			ClaimID:          "finding:ci:run-1",
			ClaimKind:        "observed",
			Summary:          "PLANNED-WORDING",
			EvidenceSelector: "entity:ci/run-1",
		}},
	}
	observed := Observation{
		PacketStatus:    "partial",
		ScopeResolution: "exact_commit",
		Sightings: []EvidenceSighting{{
			EvidenceRefID: "ev1_live",
			EntityType:    "ci",
			EntityID:      "run-1",
			Label:         "CI checkout-e2e-run-4821",
			Expanded:      true,
		}},
	}
	result := buildResult(plan, observed)
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	summary := result.Findings[0].Summary
	if summary == "PLANNED-WORDING" {
		t.Fatal("the summary was copied from the plan instead of the live observation")
	}
	if !strings.Contains(summary, "CI checkout-e2e-run-4821") {
		t.Fatalf("summary %q does not carry the live display label", summary)
	}
}
