package genkitruntime

import (
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestPromptsStateEveryModelFacingBound closes a defect class rather than a
// defect. Three separate CHAOS-3770 live-acceptance failures had the same
// shape: contracts/v1 enforces a bound on model output, the system prompt
// never states it, and the model -- having no way to infer it -- produces
// output that is rejected in full. Interpretation v3 was the fact-kind
// vocabulary, synthesis v4 was the standing/derivation/epistemic/claim-kind
// vocabularies, interpretation v4 was the 256-character requested_judgment
// cap that cost gpt-5-mini 5 of 12 investigations.
//
// Each case pins one bound with three independent assertions:
//
//	atLimit  -- a value exactly AT the limit validates, so the limit in this
//	            table is not merely <= the real one.
//	over     -- a value one past the limit is rejected, so the limit in this
//	            table is not merely >= the real one.
//	mentions -- the prompt text names the field and the limit.
//
// Together, atLimit and over pin the table's limit to the validator's exactly.
// So if someone widens or tightens a bound in contracts/v1 without updating
// the prompt, this test fails: the table no longer matches the validator, and
// correcting the table forces the prompt statement to be corrected with it.
//
// That alone was not enough (CHAOS-3770 F3 codex review): this table used to
// be maintained purely by hand, so a bound the validator enforced but nobody
// ever added a case for shipped unstated with the test still green -- exactly
// what happened to the top-level collection caps (strongest_pressures,
// drivers, remaining_work, readiness_gaps, conflicts, limitations, warnings)
// and the top-level evidence_ref_ids cap. TestModelFacingBoundRegistryIsFullyCovered
// closes that: modelFacingBounds() below must cover every single entry in
// contracts/v1.ContextFabricModelFacingBounds -- the validator-side registry
// those Validate() methods themselves read their numeric literals from (see
// that file's doc comment) -- so a bound can no longer be silently absent
// from this table at all, let alone from the prompt.
func TestPromptsStateEveryModelFacingBound(t *testing.T) {
	for _, testCase := range modelFacingBounds() {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.atLimit(); err != nil {
				t.Fatalf("a value at the documented limit was rejected (%v); the limit in this table is smaller than the validator's, so the prompt is understating it", err)
			}
			if err := testCase.over(); err == nil {
				t.Fatal("a value past the documented limit was accepted; the limit in this table is larger than the validator's, so the prompt is overstating it")
			}
			for _, mention := range testCase.mentions {
				if !strings.Contains(testCase.prompt, mention) {
					t.Errorf("the prompt never states %q, so the model cannot honour this bound", mention)
				}
			}
		})
	}
}

// TestModelFacingBoundRegistryIsFullyCovered is the mechanical completeness
// half of the oracle: every entry contracts/v1.ContextFabricModelFacingBounds
// declares must have a matching, correctly-valued case in modelFacingBounds()
// here. A registry entry with no matching case fails loudly by name, rather
// than the omission passing silently the way a purely hand-maintained table
// would. Combined with TestPromptsStateEveryModelFacingBound's atLimit/over/
// mentions checks on every covered case, this is what makes "a NEW validator
// bound cannot ship unstated" an enforced property instead of a convention:
// widening contracts/v1's registry without adding the matching wired case
// here fails this test; adding the case without a prompt statement fails the
// case's own mentions assertion above.
func TestModelFacingBoundRegistryIsFullyCovered(t *testing.T) {
	byName := make(map[string]boundCase, len(modelFacingBounds()))
	for _, testCase := range modelFacingBounds() {
		if _, exists := byName[testCase.registryName]; exists {
			t.Fatalf("modelFacingBounds() has more than one case for registry entry %q", testCase.registryName)
		}
		byName[testCase.registryName] = testCase
	}
	seen := make(map[string]struct{}, len(contractsv1.ContextFabricModelFacingBounds))
	for _, bound := range contractsv1.ContextFabricModelFacingBounds {
		seen[bound.Name] = struct{}{}
		testCase, ok := byName[bound.Name]
		if !ok {
			t.Errorf("contracts/v1 registers model-facing bound %q (limit %d) with no matching case in modelFacingBounds() -- add one, wired against contractsv1.ContextFabricModelFacingBounds so it cannot drift, before this bound can ship", bound.Name, bound.Limit)
			continue
		}
		if testCase.limit != bound.Limit {
			t.Errorf("modelFacingBounds() case %q pins limit %d, but contracts/v1 registers %d for %q -- the case must read the bound from contractsv1.ContextFabricModelFacingBounds (or the constant it is built from), not a bare literal", testCase.name, testCase.limit, bound.Limit, bound.Name)
		}
	}
	for name := range byName {
		if _, exists := seen[name]; !exists {
			t.Errorf("modelFacingBounds() has a case for registry name %q, which contracts/v1.ContextFabricModelFacingBounds does not declare -- rename it to match or remove it", name)
		}
	}
}

type boundCase struct {
	name string
	// registryName is the contractsv1.ContextFabricModelFacingBound.Name
	// this case is wired against -- see TestModelFacingBoundRegistryIsFullyCovered.
	registryName string
	// limit is the numeric value this case pins, always read from a
	// contractsv1 constant (never a bare literal) so it cannot drift from
	// what the validator itself enforces.
	limit    int
	prompt   string
	mentions []string
	atLimit  func() error
	over     func() error
}

func modelFacingBounds() []boundCase {
	return []boundCase{
		{
			name: "interpretation/requested_judgment max length", registryName: "interpretation.requested_judgment.max_length",
			limit: contractsv1.ContextFabricRequestedJudgmentMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"requested_judgment", "256"},
			atLimit: func() error {
				return interpretationWithJudgment(contractsv1.ContextFabricRequestedJudgmentMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithJudgment(contractsv1.ContextFabricRequestedJudgmentMaxLength + 1).Validate()
			},
		},
		{
			name: "interpretation/subject_terms max count", registryName: "interpretation.subject_terms.max_count",
			limit: contractsv1.ContextFabricSubjectTermsMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"subject_terms", "100"},
			atLimit: func() error {
				return interpretationWithSubjectTerms(contractsv1.ContextFabricSubjectTermsMaxCount).Validate()
			},
			over: func() error {
				return interpretationWithSubjectTerms(contractsv1.ContextFabricSubjectTermsMaxCount + 1).Validate()
			},
		},
		{
			name: "interpretation/subject term max length", registryName: "interpretation.subject_term.max_length",
			limit: contractsv1.ContextFabricSubjectOrComparisonTermMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"512"},
			atLimit: func() error {
				return interpretationWithTermLength(contractsv1.ContextFabricSubjectOrComparisonTermMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithTermLength(contractsv1.ContextFabricSubjectOrComparisonTermMaxLength + 1).Validate()
			},
		},
		{
			name: "interpretation/comparison_terms max count", registryName: "interpretation.comparison_terms.max_count",
			limit: contractsv1.ContextFabricComparisonTermsMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"comparison_terms", "100"},
			atLimit: func() error {
				return interpretationWithComparisonTerms(contractsv1.ContextFabricComparisonTermsMaxCount).Validate()
			},
			over: func() error {
				return interpretationWithComparisonTerms(contractsv1.ContextFabricComparisonTermsMaxCount + 1).Validate()
			},
		},
		{
			name: "interpretation/clarification_reason max length", registryName: "interpretation.clarification_reason.max_length",
			limit: contractsv1.ContextFabricClarificationReasonMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"clarification_reason", "2000"},
			atLimit: func() error {
				return interpretationWithClarification(contractsv1.ContextFabricClarificationReasonMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithClarification(contractsv1.ContextFabricClarificationReasonMaxLength + 1).Validate()
			},
		},
		{
			// The registered fact-kind vocabulary has only 20 members
			// (contracts/v1's closed ContextFabricFactKind set), so a
			// FactRequirements list of exactly ContextFabricFactRequirementsMaxCount
			// (64) with every Kind distinct -- required, since
			// ContextFabricInterpretedQuestion.Validate rejects a
			// duplicate Kind -- cannot be constructed at all; 20 distinct
			// kinds is the real achievable ceiling. atLimit therefore
			// proves the count bound does not block that real ceiling (a
			// tightened bound below 20 would still fail this assertion);
			// over proves the documented 64 is still the enforced count
			// ceiling, using cycled (duplicate) kinds -- valid here only
			// because the length check in
			// ContextFabricInterpretedQuestion.Validate short-circuits
			// before the per-Kind uniqueness loop ever runs once the
			// count itself already exceeds the bound.
			name: "interpretation/fact_requirements max count", registryName: "interpretation.fact_requirements.max_count",
			limit: contractsv1.ContextFabricFactRequirementsMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"At most 64 fact_requirements"},
			atLimit: func() error {
				return interpretationWithDistinctFactRequirements(len(contextFabricAllFactKinds)).Validate()
			},
			over: func() error {
				return interpretationWithCycledFactRequirements(contractsv1.ContextFabricFactRequirementsMaxCount + 1).Validate()
			},
		},
		{
			name: "interpretation/fact requirement parameter value max length", registryName: "interpretation.fact_requirement.parameter_value.max_length",
			limit: contractsv1.ContextFabricFactRequirementParameterValueMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"parameters", "1024"},
			atLimit: func() error {
				return interpretationWithParameterValue(contractsv1.ContextFabricFactRequirementParameterValueMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithParameterValue(contractsv1.ContextFabricFactRequirementParameterValueMaxLength + 1).Validate()
			},
		},
		{
			name: "interpretation/fact requirement parameter key max length", registryName: "interpretation.fact_requirement.parameter_key.max_length",
			limit: contractsv1.ContextFabricFactRequirementParameterKeyMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"128"},
			atLimit: func() error {
				return interpretationWithParameterKey(contractsv1.ContextFabricFactRequirementParameterKeyMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithParameterKey(contractsv1.ContextFabricFactRequirementParameterKeyMaxLength + 1).Validate()
			},
		},
		{
			// CHAOS-3770 F3 residual (codex round 2): distinct from the
			// per-key/per-value LENGTH bounds above -- this is how many
			// parameter entries ONE fact_requirements[] item may carry.
			name: "interpretation/fact requirement parameters max count", registryName: "interpretation.fact_requirement.parameters.max_count",
			limit: contractsv1.ContextFabricFactRequirementParametersMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"parameters", "32"},
			atLimit: func() error {
				return interpretationWithParameterCount(contractsv1.ContextFabricFactRequirementParametersMaxCount).Validate()
			},
			over: func() error {
				return interpretationWithParameterCount(contractsv1.ContextFabricFactRequirementParametersMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/driver_id max length", registryName: "synthesis.driver.driver_id.max_length",
			limit: contractsv1.ContextFabricModelMintedIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"driver_id", "256"},
			atLimit:  func() error { return driverWithID(contractsv1.ContextFabricModelMintedIDMaxLength).Validate() },
			over:     func() error { return driverWithID(contractsv1.ContextFabricModelMintedIDMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/driver title max length", registryName: "synthesis.driver.title.max_length",
			limit: contractsv1.ContextFabricDriverTitleMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"title", "512"},
			atLimit:  func() error { return driverWithTitle(contractsv1.ContextFabricDriverTitleMaxLength).Validate() },
			over:     func() error { return driverWithTitle(contractsv1.ContextFabricDriverTitleMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/driver summary max length", registryName: "synthesis.driver.summary.max_length",
			limit: contractsv1.ContextFabricDriverSummaryMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"summary", "4000"},
			atLimit:  func() error { return driverWithSummary(contractsv1.ContextFabricDriverSummaryMaxLength).Validate() },
			over:     func() error { return driverWithSummary(contractsv1.ContextFabricDriverSummaryMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/driver qualification max length", registryName: "synthesis.driver.qualification.max_length",
			limit: contractsv1.ContextFabricDriverQualificationMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"qualification", "2000"},
			atLimit: func() error {
				return driverWithQualification(contractsv1.ContextFabricDriverQualificationMaxLength).Validate()
			},
			over: func() error {
				return driverWithQualification(contractsv1.ContextFabricDriverQualificationMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/driver affected_subjects max count", registryName: "synthesis.driver.affected_subjects.max_count",
			limit: contractsv1.ContextFabricDriverAffectedSubjectsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"affected_subjects", "250"},
			atLimit: func() error {
				return driverWithSubjects(contractsv1.ContextFabricDriverAffectedSubjectsMaxCount).Validate()
			},
			over: func() error {
				return driverWithSubjects(contractsv1.ContextFabricDriverAffectedSubjectsMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/driver path_ids max count", registryName: "synthesis.driver.path_ids.max_count",
			limit: contractsv1.ContextFabricDriverPathIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids", "250"},
			atLimit:  func() error { return driverWithPathIDs(contractsv1.ContextFabricDriverPathIDsMaxCount).Validate() },
			over:     func() error { return driverWithPathIDs(contractsv1.ContextFabricDriverPathIDsMaxCount + 1).Validate() },
		},
		{
			name: "synthesis/driver claimed_fact_ids max count", registryName: "synthesis.driver.claimed_fact_ids.max_count",
			limit: contractsv1.ContextFabricDriverClaimedFactIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"claimed_fact_ids", "250"},
			atLimit: func() error {
				return driverWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount).Validate()
			},
			over: func() error {
				return driverWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-2 R2-2: enforced (validate_context_fabric_result.go's
			// uniqueTrimmedStrings(d.PathIDs, ContextFabricIdentifierRefMaxLength))
			// and already stated in the prompt ("each at most 256 characters"),
			// but had no registry entry and no case here until this fix.
			name: "synthesis/driver path_id item max length", registryName: "synthesis.driver.path_ids.item_max_length",
			limit: contractsv1.ContextFabricIdentifierRefMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids and at most 250 claimed_fact_ids, each at most 256"},
			atLimit: func() error {
				return driverWithPathIDLength(contractsv1.ContextFabricIdentifierRefMaxLength).Validate()
			},
			over: func() error {
				return driverWithPathIDLength(contractsv1.ContextFabricIdentifierRefMaxLength + 1).Validate()
			},
		},
		{
			// See the matching comment on the path_id item-length case above.
			name: "synthesis/driver claimed_fact_id item max length", registryName: "synthesis.driver.claimed_fact_ids.item_max_length",
			limit: contractsv1.ContextFabricIdentifierRefMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids and at most 250 claimed_fact_ids, each at most 256"},
			atLimit: func() error {
				return driverWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength).Validate()
			},
			over: func() error {
				return driverWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/driver evidence_ref_ids max count", registryName: "synthesis.driver.evidence_ref_ids.max_count",
			limit: contractsv1.ContextFabricEvidenceRefIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids", "500"},
			atLimit: func() error {
				return driverWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount).Validate()
			},
			over: func() error {
				return driverWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/finding_id max length", registryName: "synthesis.finding.finding_id.max_length",
			limit: contractsv1.ContextFabricModelMintedIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"finding_id", "256"},
			atLimit:  func() error { return findingWithID(contractsv1.ContextFabricModelMintedIDMaxLength).Validate() },
			over:     func() error { return findingWithID(contractsv1.ContextFabricModelMintedIDMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/finding kind max length", registryName: "synthesis.finding.kind.max_length",
			limit: contractsv1.ContextFabricFindingKindMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"kind is at most 128"},
			atLimit:  func() error { return findingWithKind(contractsv1.ContextFabricFindingKindMaxLength).Validate() },
			over:     func() error { return findingWithKind(contractsv1.ContextFabricFindingKindMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/finding summary max length", registryName: "synthesis.finding.summary.max_length",
			limit: contractsv1.ContextFabricFindingSummaryMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"summary", "4000"},
			atLimit:  func() error { return findingWithSummary(contractsv1.ContextFabricFindingSummaryMaxLength).Validate() },
			over: func() error {
				return findingWithSummary(contractsv1.ContextFabricFindingSummaryMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/finding subjects max count", registryName: "synthesis.finding.subjects.max_count",
			limit: contractsv1.ContextFabricFindingSubjectsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"subjects", "250"},
			atLimit:  func() error { return findingWithSubjects(contractsv1.ContextFabricFindingSubjectsMaxCount).Validate() },
			over: func() error {
				return findingWithSubjects(contractsv1.ContextFabricFindingSubjectsMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/finding evidence_ref_ids max count", registryName: "synthesis.finding.evidence_ref_ids.max_count",
			limit: contractsv1.ContextFabricEvidenceRefIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids", "500"},
			atLimit: func() error {
				return findingWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount).Validate()
			},
			over: func() error {
				return findingWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/finding claimed_fact_ids max count", registryName: "synthesis.finding.claimed_fact_ids.max_count",
			limit: contractsv1.ContextFabricDriverClaimedFactIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"claimed_fact_ids", "250"},
			atLimit: func() error {
				return findingWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount).Validate()
			},
			over: func() error {
				return findingWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-2 R2-2: see the matching driver-side case
			// above -- Finding.Validate() enforces this the same way
			// (uniqueTrimmedStrings(f.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength)),
			// and the SAME prompt sentence covers both driver and finding
			// (prompts.go states them together).
			name: "synthesis/finding claimed_fact_id item max length", registryName: "synthesis.finding.claimed_fact_ids.item_max_length",
			limit: contractsv1.ContextFabricIdentifierRefMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids and at most 250 claimed_fact_ids, each at most 256"},
			atLimit: func() error {
				return findingWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength).Validate()
			},
			over: func() error {
				return findingWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/claim_id max length", registryName: "synthesis.claimed_fact.claim_id.max_length",
			limit: contractsv1.ContextFabricModelMintedIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"claim_id", "256"},
			atLimit:  func() error { return claimWithID(contractsv1.ContextFabricModelMintedIDMaxLength).Validate() },
			over:     func() error { return claimWithID(contractsv1.ContextFabricModelMintedIDMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/claimed fact field max length", registryName: "synthesis.claimed_fact.field.max_length",
			limit: contractsv1.ContextFabricClaimedFieldMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"field", "128"},
			atLimit:  func() error { return claimWithField(contractsv1.ContextFabricClaimedFieldMaxLength).Validate() },
			over:     func() error { return claimWithField(contractsv1.ContextFabricClaimedFieldMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/strongest_pressures max count", registryName: "synthesis.strongest_pressures.max_count",
			limit: contractsv1.ContextFabricStrongestPressuresMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"at most 50 strongest_pressures"},
			atLimit: func() error {
				return resultWithStrongestPressures(contractsv1.ContextFabricStrongestPressuresMaxCount).Validate()
			},
			over: func() error {
				return resultWithStrongestPressures(contractsv1.ContextFabricStrongestPressuresMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/strongest_pressures item max length", registryName: "synthesis.strongest_pressures.item_max_length",
			limit: contractsv1.ContextFabricStrongestPressureMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"strongest_pressures", "2000"},
			atLimit: func() error {
				return resultWithStrongestPressureLength(contractsv1.ContextFabricStrongestPressureMaxLength).Validate()
			},
			over: func() error {
				return resultWithStrongestPressureLength(contractsv1.ContextFabricStrongestPressureMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/drivers max count", registryName: "synthesis.drivers.max_count",
			limit: contractsv1.ContextFabricDriversMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"at most 50 drivers"},
			atLimit:  func() error { return resultWithDrivers(contractsv1.ContextFabricDriversMaxCount).Validate() },
			over:     func() error { return resultWithDrivers(contractsv1.ContextFabricDriversMaxCount + 1).Validate() },
		},
		{
			name: "synthesis/remaining_work max count", registryName: "synthesis.remaining_work.max_count",
			limit: contractsv1.ContextFabricRemainingWorkMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"remaining_work", "250"},
			atLimit: func() error {
				return resultWithFindings("remaining_work", contractsv1.ContextFabricRemainingWorkMaxCount).Validate()
			},
			over: func() error {
				return resultWithFindings("remaining_work", contractsv1.ContextFabricRemainingWorkMaxCount+1).Validate()
			},
		},
		{
			name: "synthesis/readiness_gaps max count", registryName: "synthesis.readiness_gaps.max_count",
			limit: contractsv1.ContextFabricReadinessGapsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"readiness_gaps", "250"},
			atLimit: func() error {
				return resultWithFindings("readiness_gaps", contractsv1.ContextFabricReadinessGapsMaxCount).Validate()
			},
			over: func() error {
				return resultWithFindings("readiness_gaps", contractsv1.ContextFabricReadinessGapsMaxCount+1).Validate()
			},
		},
		{
			name: "synthesis/conflicts max count", registryName: "synthesis.conflicts.max_count",
			limit: contractsv1.ContextFabricConflictsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"conflicts", "250"},
			atLimit: func() error {
				return resultWithFindings("conflicts", contractsv1.ContextFabricConflictsMaxCount).Validate()
			},
			over: func() error {
				return resultWithFindings("conflicts", contractsv1.ContextFabricConflictsMaxCount+1).Validate()
			},
		},
		{
			name: "synthesis/limitations max count", registryName: "synthesis.limitations.max_count",
			limit: contractsv1.ContextFabricLimitationsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"limitations", "250"},
			atLimit:  func() error { return resultWithLimitations(contractsv1.ContextFabricLimitationsMaxCount).Validate() },
			over: func() error {
				return resultWithLimitations(contractsv1.ContextFabricLimitationsMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/limitations item max length", registryName: "synthesis.limitations.item_max_length",
			limit: contractsv1.ContextFabricLimitationMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"limitation", "4000"},
			atLimit: func() error {
				return resultWithLimitationLength(contractsv1.ContextFabricLimitationMaxLength).Validate()
			},
			over: func() error {
				return resultWithLimitationLength(contractsv1.ContextFabricLimitationMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/warnings max count", registryName: "synthesis.warnings.max_count",
			limit: contractsv1.ContextFabricWarningsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"warnings", "250"},
			atLimit:  func() error { return resultWithWarnings(contractsv1.ContextFabricWarningsMaxCount).Validate() },
			over:     func() error { return resultWithWarnings(contractsv1.ContextFabricWarningsMaxCount + 1).Validate() },
		},
		{
			name: "synthesis/warnings item max length", registryName: "synthesis.warnings.item_max_length",
			limit: contractsv1.ContextFabricWarningMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"warning", "4000"},
			atLimit:  func() error { return resultWithWarningLength(contractsv1.ContextFabricWarningMaxLength).Validate() },
			over:     func() error { return resultWithWarningLength(contractsv1.ContextFabricWarningMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/evidence_ref_ids (top-level) max count", registryName: "synthesis.evidence_ref_ids.max_count",
			limit: contractsv1.ContextFabricEvidenceRefIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids", "500"},
			atLimit: func() error {
				return resultWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount).Validate()
			},
			over: func() error {
				return resultWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3770 F3 residual (codex round 2): the model decides how
			// MANY claimed facts to write (unlike claimed_fact_ids on a
			// driver/finding, which only reference entries in this list and
			// are already covered above).
			name: "synthesis/claimed_facts max count", registryName: "synthesis.claimed_facts.max_count",
			limit: contractsv1.ContextFabricClaimedFactsMaxCount, prompt: synthesisSystemPrompt,
			// A single precise phrase, not independent "claimed_facts"/"250"
			// substrings: both already appear elsewhere in this prompt for
			// unrelated reasons (the claimed_fact_ids reference bound, the
			// closed-vocabulary sentence), so loose substrings alone would
			// pass without an actual dedicated statement of THIS bound.
			mentions: []string{"at most 250 claimed_facts"},
			atLimit: func() error {
				return resultWithClaimedFacts(contractsv1.ContextFabricClaimedFactsMaxCount).Validate()
			},
			over: func() error {
				return resultWithClaimedFacts(contractsv1.ContextFabricClaimedFactsMaxCount + 1).Validate()
			},
		},
	}
}

func filler(n int) string { return strings.Repeat("a", n) }

func boundsSubject(index int) contractsv1.ContextFabricSubjectRef {
	return contractsv1.ContextFabricSubjectRef{
		Kind:        contractsv1.ContextFabricSubjectProject,
		CanonicalID: fmt.Sprintf("project_%04d", index),
		Label:       fmt.Sprintf("Project %04d", index),
	}
}

func baseInterpretation() contractsv1.ContextFabricInterpretedQuestion {
	return contractsv1.ContextFabricInterpretedQuestion{
		Shape:             contractsv1.ContextFabricShapeOpen,
		RequestedJudgment: "actual status and current drivers",
		TimeContext:       contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		FactRequirements:  []contractsv1.ContextFabricFactRequirement{},
	}
}

func interpretationWithJudgment(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.RequestedJudgment = filler(length)
	return question
}

func interpretationWithSubjectTerms(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.SubjectTerms = uniqueTerms(count)
	return question
}

func interpretationWithComparisonTerms(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.ComparisonTerms = uniqueTerms(count)
	return question
}

func interpretationWithTermLength(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.SubjectTerms = []string{filler(length)}
	return question
}

func interpretationWithClarification(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.ClarificationNeeded = true
	question.ClarificationReason = filler(length)
	return question
}

// contextFabricAllFactKinds is every kind in contracts/v1's closed
// ContextFabricFactKind vocabulary -- 20 as of this writing, deliberately
// fewer than ContextFabricFactRequirementsMaxCount (64), which is why
// interpretationWithDistinctFactRequirements and
// interpretationWithCycledFactRequirements below are two different
// constructors rather than one.
var contextFabricAllFactKinds = []contractsv1.ContextFabricFactKind{
	contractsv1.ContextFabricFactIdentity, contractsv1.ContextFabricFactMembership, contractsv1.ContextFabricFactStatus,
	contractsv1.ContextFabricFactActualCompletion, contractsv1.ContextFabricFactWork, contractsv1.ContextFabricFactBlockers,
	contractsv1.ContextFabricFactRequiredChildren, contractsv1.ContextFabricFactPullRequests, contractsv1.ContextFabricFactReviews,
	contractsv1.ContextFabricFactContinuousIntegration, contractsv1.ContextFabricFactDeployments, contractsv1.ContextFabricFactIncidents,
	contractsv1.ContextFabricFactMetrics, contractsv1.ContextFabricFactHealth, contractsv1.ContextFabricFactWorkload,
	contractsv1.ContextFabricFactInvestment, contractsv1.ContextFabricFactReadiness, contractsv1.ContextFabricFactOperationalDeficiencies,
	contractsv1.ContextFabricFactSourceHealth, contractsv1.ContextFabricFactEvidence,
}

// interpretationWithDistinctFactRequirements builds count DISTINCT
// fact_requirement kinds, and panics if count exceeds the vocabulary's
// size -- ContextFabricInterpretedQuestion.Validate rejects a duplicate
// kind, so this constructor can only ever reach as many requirements as
// there are legal kinds.
func interpretationWithDistinctFactRequirements(count int) contractsv1.ContextFabricInterpretedQuestion {
	if count > len(contextFabricAllFactKinds) {
		panic(fmt.Sprintf("interpretationWithDistinctFactRequirements(%d): only %d distinct fact kinds exist", count, len(contextFabricAllFactKinds)))
	}
	question := baseInterpretation()
	requirements := make([]contractsv1.ContextFabricFactRequirement, 0, count)
	for i := 0; i < count; i++ {
		requirements = append(requirements, contractsv1.ContextFabricFactRequirement{Kind: contextFabricAllFactKinds[i]})
	}
	question.FactRequirements = requirements
	return question
}

// interpretationWithCycledFactRequirements builds count fact_requirements,
// cycling through the vocabulary (so kinds repeat once count exceeds its
// size). It is valid ONLY for a count that already exceeds
// ContextFabricFactRequirementsMaxCount: ContextFabricInterpretedQuestion.Validate's
// length check short-circuits the whole boolean expression before the
// per-Kind uniqueness loop below it ever runs, so the repeated kinds this
// produces never actually reach that loop.
func interpretationWithCycledFactRequirements(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	requirements := make([]contractsv1.ContextFabricFactRequirement, 0, count)
	for i := 0; i < count; i++ {
		requirements = append(requirements, contractsv1.ContextFabricFactRequirement{Kind: contextFabricAllFactKinds[i%len(contextFabricAllFactKinds)]})
	}
	question.FactRequirements = requirements
	return question
}

func interpretationWithParameterValue(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: map[string]string{"scope": filler(length)},
	}}
	return question
}

func interpretationWithParameterKey(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: map[string]string{filler(length): "scope"},
	}}
	return question
}

// interpretationWithParameterCount builds ONE fact_requirements[] entry
// carrying count distinct parameter keys -- see
// "interpretation/fact requirement parameters max count".
func interpretationWithParameterCount(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	params := make(map[string]string, count)
	for i := 0; i < count; i++ {
		params[fmt.Sprintf("key_%04d", i)] = "value"
	}
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: params,
	}}
	return question
}

func uniqueTerms(count int) []string {
	terms := make([]string, 0, count)
	for i := 0; i < count; i++ {
		terms = append(terms, fmt.Sprintf("term_%04d", i))
	}
	return terms
}

// baseDriver is deliberately in the relationship category: relationship and
// narrative are the two categories that require no claimed fact, so these
// cases isolate the bound under test from value-level closure.
func baseDriver() contractsv1.ContextFabricDriverJudgment {
	return contractsv1.ContextFabricDriverJudgment{
		DriverID:         "driver_00000001",
		Standing:         contractsv1.ContextFabricDriverPrincipal,
		Category:         string(contractsv1.ContextFabricDriverCategoryRelationship),
		Title:            "Release acceptance is open",
		Summary:          "The release acceptance work item is still open.",
		AffectedSubjects: []contractsv1.ContextFabricSubjectRef{boundsSubject(0)},
		PathIDs:          []string{"path_00000001"},
		EvidenceRefIDs:   []string{},
		Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
		Confidence:       0.5,
	}
}

// driverN returns a valid driver identical to baseDriver() except for a
// unique DriverID -- for exercising a RESULT-level collection count bound
// (Drivers), where each entry must independently validate AND all DriverIDs
// in the list must be unique (validateDrivers).
func driverN(index int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.DriverID = fmt.Sprintf("driver_%08d", index)
	driver.PathIDs = []string{fmt.Sprintf("path_%08d", index)}
	return driver
}

func driverWithID(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.DriverID = filler(length)
	return driver
}

func driverWithTitle(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Title = filler(length)
	return driver
}

func driverWithSummary(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Summary = filler(length)
	return driver
}

func driverWithQualification(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Qualification = filler(length)
	return driver
}

func driverWithSubjects(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	subjects := make([]contractsv1.ContextFabricSubjectRef, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, boundsSubject(i))
	}
	driver.AffectedSubjects = subjects
	return driver
}

func driverWithPathIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.PathIDs = uniqueTerms(count)
	return driver
}

func driverWithClaimedFactIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.ClaimedFactIDs = uniqueTerms(count)
	return driver
}

// driverWithPathIDLength and driverWithClaimedFactIDLength are the
// per-item length counterparts of driverWithPathIDs/driverWithClaimedFactIDs
// above (which vary COUNT): a single path_id/claimed_fact_id of the given
// length, isolating the item-length bound from the count bound
// (CHAOS-3784 round-2 R2-2).
func driverWithPathIDLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.PathIDs = []string{filler(length)}
	return driver
}

func driverWithClaimedFactIDLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.ClaimedFactIDs = []string{filler(length)}
	return driver
}

func evidenceRefs(count int) []string {
	refs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		refs = append(refs, fmt.Sprintf("evidence_%08d", i))
	}
	return refs
}

func driverWithEvidenceRefIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.EvidenceRefIDs = evidenceRefs(count)
	return driver
}

func baseFinding() contractsv1.ContextFabricFinding {
	return contractsv1.ContextFabricFinding{
		FindingID: "finding_00000001",
		Kind:      string(contractsv1.ContextFabricDriverCategoryNarrative),
		Summary:   "Release acceptance remains open.",
		Subjects:  []contractsv1.ContextFabricSubjectRef{boundsSubject(0)},
		// Unlike a driver, a finding may not have an empty evidence list:
		// boundedEvidenceRefs is called with allowEmpty=false for findings
		// and allowEmpty=true for drivers.
		EvidenceRefIDs: []string{"evidence_00000001"},
	}
}

// findingN returns a valid finding identical to baseFinding() except for a
// unique FindingID -- see driverN.
func findingN(index int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.FindingID = fmt.Sprintf("finding_%08d", index)
	return finding
}

func findingWithID(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.FindingID = filler(length)
	return finding
}

func findingWithKind(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Kind = filler(length)
	return finding
}

func findingWithSummary(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Summary = filler(length)
	return finding
}

func findingWithSubjects(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	subjects := make([]contractsv1.ContextFabricSubjectRef, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, boundsSubject(i))
	}
	finding.Subjects = subjects
	return finding
}

func findingWithEvidenceRefIDs(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.EvidenceRefIDs = evidenceRefs(count)
	return finding
}

func findingWithClaimedFactIDs(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.ClaimedFactIDs = uniqueTerms(count)
	return finding
}

// findingWithClaimedFactIDLength is driverWithClaimedFactIDLength's
// finding-side counterpart (CHAOS-3784 round-2 R2-2).
func findingWithClaimedFactIDLength(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.ClaimedFactIDs = []string{filler(length)}
	return finding
}

func claimWithID(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: filler(length),
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   "status",
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}

func claimWithField(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: "claim_00000001",
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   filler(length),
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}

// baseResult builds a minimally valid ContextFabricInvestigationResult --
// every collection non-nil and empty, every required sub-object valid -- so
// that varying exactly ONE collection's size (below) isolates the bound
// under test from every other one ContextFabricInvestigationResult.Validate
// enforces.
func baseResult() contractsv1.ContextFabricInvestigationResult {
	project := boundsSubject(0)
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_12345678",
		RequestID:     "request_12345678",
		GeneratedAt:   time.Now().UTC(),
		Status:        contractsv1.ContextFabricInvestigationComplete,
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution:  contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{project}},
		DirectJudgment:     "Ask Dev is not release-ready.",
		CurrentState:       "Required work remains.",
		StrongestPressures: []string{},
		Drivers:            []contractsv1.ContextFabricDriverJudgment{},
		RemainingWork:      []contractsv1.ContextFabricFinding{},
		ReadinessGaps:      []contractsv1.ContextFabricFinding{},
		Paths:              []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:          []contractsv1.ContextFabricFinding{},
		Limitations:        []string{},
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []contractsv1.ContextFabricClaimedFact{},
		Coverage:           contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}, DegradedReasons: []string{}},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "test",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1",
		},
		DeterministicAnswer: "Ask Dev is not release-ready because required work remains.",
		Warnings:            []string{},
	}
}

func resultWithStrongestPressures(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.StrongestPressures = uniqueTerms(count)
	return result
}

func resultWithStrongestPressureLength(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.StrongestPressures = []string{filler(length)}
	return result
}

func resultWithDrivers(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	drivers := make([]contractsv1.ContextFabricDriverJudgment, 0, count)
	for i := 0; i < count; i++ {
		drivers = append(drivers, driverN(i))
	}
	result.Drivers = drivers
	return result
}

func resultWithFindings(field string, count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	findings := make([]contractsv1.ContextFabricFinding, 0, count)
	for i := 0; i < count; i++ {
		findings = append(findings, findingN(i))
	}
	switch field {
	case "remaining_work":
		result.RemainingWork = findings
	case "readiness_gaps":
		result.ReadinessGaps = findings
	case "conflicts":
		result.Conflicts = findings
	default:
		panic("resultWithFindings: unknown field " + field)
	}
	return result
}

func resultWithLimitations(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Limitations = uniqueTerms(count)
	return result
}

func resultWithLimitationLength(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Limitations = []string{filler(length)}
	return result
}

func resultWithWarnings(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Warnings = uniqueTerms(count)
	return result
}

func resultWithWarningLength(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Warnings = []string{filler(length)}
	return result
}

func resultWithEvidenceRefIDs(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.EvidenceRefIDs = evidenceRefs(count)
	return result
}

// claimN returns a valid claimed fact identical in shape to claimWithField's
// baseline except for a unique ClaimID -- for exercising the RESULT-level
// claimed_facts count bound, where every entry must independently validate
// AND all ClaimIDs in the list must be unique (validateClaimedFacts).
func claimN(index int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: fmt.Sprintf("claim_%08d", index),
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   "status",
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}

func resultWithClaimedFacts(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	claims := make([]contractsv1.ContextFabricClaimedFact, 0, count)
	for i := 0; i < count; i++ {
		claims = append(claims, claimN(i))
	}
	result.ClaimedFacts = claims
	return result
}
