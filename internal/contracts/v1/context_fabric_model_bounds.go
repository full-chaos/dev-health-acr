package v1

// This file is the single source of truth for every length/count bound this
// package enforces against a MODEL-FACING context-fabric field -- a field
// the interpretation or synthesis model itself populates, as opposed to one
// ACR composes or supplies afterward (graph-resolved EvidenceRefIDs at the
// SubjectResolution/Cohort/RelationshipPath level, Coverage, Versions, or
// the server-composed DirectJudgment/CurrentState/DeterministicAnswer --
// RuntimeAnswerSynthesizer.Synthesize's compose* functions render and
// truncate those three server-side to fit their bound rather than ever
// validating the model's own text against it, so a prompt statement about
// them is advisory, not load-bearing).
//
// CHAOS-3770 F3: a validator bound on a model-facing field that the prompt
// never states is not a partial degradation -- every Validate()/
// ValidateAgainst() call below rejects the WHOLE interpretation or
// synthesis draft when one is exceeded, so an unstated bound is a silent,
// total, model-dependent failure rate. Three separate CHAOS-3770 live
// acceptance failures had exactly that shape, and a fourth (the
// interpretation/synthesis top-level collection caps: strongest_pressures,
// drivers, remaining_work, readiness_gaps, conflicts, limitations,
// warnings, evidence_ref_ids) shipped stated in the prompt but entirely
// untested, so removing the prompt statement would have shipped silently.
//
// The standing rule going forward: every numeric literal this file enforces
// against a model-facing field MUST be one of the named constants below,
// never an inline literal, and MUST have a corresponding entry in
// ContextFabricModelFacingBounds. genkitruntime.TestPromptsStateEveryModelFacingBound
// (a different package -- only it has the prompt strings) imports this
// registry, asserts every one of its entries has a wired test case that
// pins the SAME numeric value against the validator and asserts the prompt
// text names it, and separately asserts the registry has NO entry lacking
// such a case -- so a bound value can drift, or a whole new bound can be
// added, and either is caught mechanically rather than depending on a
// second, independently-maintained manual list staying in sync by hand.
const (
	// Interpretation (ContextFabricInterpretedQuestion, ContextFabricFactRequirement).
	ContextFabricRequestedJudgmentMaxLength             = 256
	ContextFabricSubjectTermsMaxCount                   = 100
	ContextFabricSubjectOrComparisonTermMaxLength       = 512
	ContextFabricComparisonTermsMaxCount                = 100
	ContextFabricClarificationReasonMaxLength           = 2000
	ContextFabricFactRequirementsMaxCount               = 64
	ContextFabricFactRequirementParameterValueMaxLength = 1024
	ContextFabricFactRequirementParameterKeyMaxLength   = 128
	// ContextFabricFactRequirementParametersMaxCount bounds how many
	// key/value entries the model may attach to ONE fact_requirements[]
	// entry's parameters object -- distinct from the per-key/per-value
	// LENGTH bounds above (CHAOS-3770 F3 residual, codex round 2: this was
	// an inline literal with no registry entry and no prompt statement).
	ContextFabricFactRequirementParametersMaxCount = 32

	// Synthesis: identifiers a model mints itself -- driver_id, finding_id,
	// and claim_id all share the same [min,max] shape (prompts.go states
	// them together: "at least 8 and at most 256 characters").
	ContextFabricModelMintedIDMinLength = 8
	ContextFabricModelMintedIDMaxLength = 256

	// Synthesis: driver (ContextFabricDriverJudgment).
	ContextFabricDriverTitleMaxLength           = 512
	ContextFabricDriverSummaryMaxLength         = 4000
	ContextFabricDriverQualificationMaxLength   = 2000
	ContextFabricDriverAffectedSubjectsMinCount = 1
	ContextFabricDriverAffectedSubjectsMaxCount = 250
	ContextFabricDriverPathIDsMaxCount          = 250
	ContextFabricDriverClaimedFactIDsMaxCount   = 250
	// ContextFabricIdentifierRefMaxLength bounds each individual path_id and
	// claimed_fact_id string a driver or finding lists (as opposed to the
	// COUNT bounds above) -- one shared bound, since both are the same
	// "reference to an ID this answer already returned or was given"
	// shape. Unlike an evidence_ref_id (excluded from this registry --
	// see the file doc comment's class (a): allowedEvidence's membership
	// check in SynthesisDraft.ValidateAgainst always rejects an
	// unrecognized evidence ref before its length is the operative
	// failure), a claimed_fact_id reference points at a claim the MODEL
	// itself minted earlier in the SAME draft, not at something ACR
	// independently supplied and bounded -- so this length is genuinely
	// model-facing and belongs in the registry (CHAOS-3784 round-2 R2-2:
	// enforced at validate_context_fabric_result.go's uniqueTrimmedStrings
	// calls but absent from here and from Diagnose* until this fix).
	ContextFabricIdentifierRefMaxLength = 256
	// ContextFabricEvidenceRefIDsMaxCount bounds evidence_ref_ids wherever a
	// model populates it directly: per-driver and per-finding.
	ContextFabricEvidenceRefIDsMaxCount = 500

	// Synthesis: finding (ContextFabricFinding).
	ContextFabricFindingKindMaxLength    = 128
	ContextFabricFindingSummaryMaxLength = 4000
	ContextFabricFindingSubjectsMaxCount = 250

	// Synthesis: claimed fact (ContextFabricClaimedFact).
	ContextFabricClaimedFieldMaxLength = 128

	// Synthesis: top-level synthesis draft / result collections the model
	// itself populates.
	ContextFabricStrongestPressuresMaxCount = 50
	ContextFabricStrongestPressureMaxLength = 2000
	ContextFabricDriversMaxCount            = 50
	ContextFabricRemainingWorkMaxCount      = 250
	ContextFabricReadinessGapsMaxCount      = 250
	ContextFabricConflictsMaxCount          = 250
	ContextFabricLimitationsMaxCount        = 250
	ContextFabricLimitationMaxLength        = 4000
	ContextFabricWarningsMaxCount           = 250
	ContextFabricWarningMaxLength           = 4000
	// ContextFabricClaimedFactsMaxCount bounds the synthesis draft's own
	// top-level claimed_facts list -- the model decides how many claims to
	// write (unlike driver/finding claimed_fact_ids, which only REFERENCE
	// entries in this list and are already covered above). Enforced by
	// validateClaimedFacts in validate_context_fabric_helpers.go (CHAOS-3770
	// F3 residual, codex round 2: inline literal, no registry entry, no
	// prompt statement).
	ContextFabricClaimedFactsMaxCount = 250
)

// ContextFabricModelFacingBound names one entry in the registry below.
type ContextFabricModelFacingBound struct {
	// Name is a short, stable, human-readable identifier for this bound --
	// used only in a completeness cross-check's failure messages, never
	// parsed.
	Name string
	// Limit is the enforced numeric value, always equal (by construction,
	// since both reference the same named constant above) to what the
	// corresponding Validate() method actually checks.
	Limit int
}

// ContextFabricModelFacingBounds is the exhaustive registry of every
// length/count limit this package enforces against a model-facing
// context-fabric field. See the file doc comment for the completeness
// guarantee this registry exists to provide.
var ContextFabricModelFacingBounds = []ContextFabricModelFacingBound{
	{"interpretation.requested_judgment.max_length", ContextFabricRequestedJudgmentMaxLength},
	{"interpretation.subject_terms.max_count", ContextFabricSubjectTermsMaxCount},
	{"interpretation.subject_term.max_length", ContextFabricSubjectOrComparisonTermMaxLength},
	{"interpretation.comparison_terms.max_count", ContextFabricComparisonTermsMaxCount},
	{"interpretation.clarification_reason.max_length", ContextFabricClarificationReasonMaxLength},
	{"interpretation.fact_requirements.max_count", ContextFabricFactRequirementsMaxCount},
	{"interpretation.fact_requirement.parameter_value.max_length", ContextFabricFactRequirementParameterValueMaxLength},
	{"interpretation.fact_requirement.parameter_key.max_length", ContextFabricFactRequirementParameterKeyMaxLength},
	{"interpretation.fact_requirement.parameters.max_count", ContextFabricFactRequirementParametersMaxCount},

	{"synthesis.driver.driver_id.max_length", ContextFabricModelMintedIDMaxLength},
	{"synthesis.driver.title.max_length", ContextFabricDriverTitleMaxLength},
	{"synthesis.driver.summary.max_length", ContextFabricDriverSummaryMaxLength},
	{"synthesis.driver.qualification.max_length", ContextFabricDriverQualificationMaxLength},
	{"synthesis.driver.affected_subjects.max_count", ContextFabricDriverAffectedSubjectsMaxCount},
	{"synthesis.driver.path_ids.max_count", ContextFabricDriverPathIDsMaxCount},
	{"synthesis.driver.path_ids.item_max_length", ContextFabricIdentifierRefMaxLength},
	{"synthesis.driver.claimed_fact_ids.max_count", ContextFabricDriverClaimedFactIDsMaxCount},
	{"synthesis.driver.claimed_fact_ids.item_max_length", ContextFabricIdentifierRefMaxLength},
	{"synthesis.driver.evidence_ref_ids.max_count", ContextFabricEvidenceRefIDsMaxCount},

	{"synthesis.finding.finding_id.max_length", ContextFabricModelMintedIDMaxLength},
	{"synthesis.finding.kind.max_length", ContextFabricFindingKindMaxLength},
	{"synthesis.finding.summary.max_length", ContextFabricFindingSummaryMaxLength},
	{"synthesis.finding.subjects.max_count", ContextFabricFindingSubjectsMaxCount},
	{"synthesis.finding.evidence_ref_ids.max_count", ContextFabricEvidenceRefIDsMaxCount},
	{"synthesis.finding.claimed_fact_ids.max_count", ContextFabricDriverClaimedFactIDsMaxCount},
	{"synthesis.finding.claimed_fact_ids.item_max_length", ContextFabricIdentifierRefMaxLength},

	{"synthesis.claimed_fact.claim_id.max_length", ContextFabricModelMintedIDMaxLength},
	{"synthesis.claimed_fact.field.max_length", ContextFabricClaimedFieldMaxLength},

	{"synthesis.strongest_pressures.max_count", ContextFabricStrongestPressuresMaxCount},
	{"synthesis.strongest_pressures.item_max_length", ContextFabricStrongestPressureMaxLength},
	{"synthesis.drivers.max_count", ContextFabricDriversMaxCount},
	{"synthesis.remaining_work.max_count", ContextFabricRemainingWorkMaxCount},
	{"synthesis.readiness_gaps.max_count", ContextFabricReadinessGapsMaxCount},
	{"synthesis.conflicts.max_count", ContextFabricConflictsMaxCount},
	{"synthesis.limitations.max_count", ContextFabricLimitationsMaxCount},
	{"synthesis.limitations.item_max_length", ContextFabricLimitationMaxLength},
	{"synthesis.warnings.max_count", ContextFabricWarningsMaxCount},
	{"synthesis.warnings.item_max_length", ContextFabricWarningMaxLength},
	// The synthesis draft's own top-level evidence_ref_ids -- distinct from
	// the per-driver/per-finding bounds above (a different Validate() call
	// site: ContextFabricInvestigationResult.Validate(), not
	// ContextFabricDriverJudgment.Validate()/ContextFabricFinding.Validate())
	// even though it shares the same numeric bound.
	{"synthesis.evidence_ref_ids.max_count", ContextFabricEvidenceRefIDsMaxCount},
	{"synthesis.claimed_facts.max_count", ContextFabricClaimedFactsMaxCount},
}
