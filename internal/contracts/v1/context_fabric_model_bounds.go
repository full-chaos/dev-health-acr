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
//
// CHAOS-3784 round-3 R3-1: a bound is not safely excludable from this
// registry merely because some OTHER check (a membership/grounding/value-
// equality check in SynthesisDraft.ValidateAgainst) would "obviously"
// reject the same violating value for a different reason -- Go evaluates a
// struct's own Validate() call BEFORE any of ValidateAgainst's grounding
// checks in every single caller site in this codebase (the per-item loops
// in internal/contextfabric/model_runtime.go always call
// driver.Validate()/finding.Validate()/claim.Validate() first, THEN check
// membership/labels/value-equality), so a length bound enforced INSIDE a
// Validate() method always wins the race against a grounding check enforced
// OUTSIDE it, regardless of which one an exclusion rationale assumed would
// fire first. ContextFabricIdentifierRefMaxLength (path_id/claimed_fact_id
// item length, added CHAOS-3784 round-2 R2-2) was reasoned about
// correctly; the ORIGINAL CHAOS-3770 v7 prompt-doc rationale for excluding
// evidence_ref_id/SubjectRef.CanonicalID/SubjectRef.Label/ClaimedFact.Value
// ("a membership/grounding check already rejects an unrecognized value
// before its length is the operative failure reason") was not -- all four
// are enforced inside a Validate() call that runs strictly before the
// membership/grounding check the rationale relied on, so an
// simultaneously-too-long-AND-ungrounded value from the model is rejected
// on the LENGTH violation, unreported, exactly like the path_id/
// claimed_fact_id case R2-2 fixed. All four are now registered below.
//
// CHAOS-3784 round-4: R3-1's fix (registering the four fields above) was
// necessary but not sufficient -- DiagnoseContextFabric*Bound still SCANNED
// each struct independently for any registrable violation, so an EARLIER
// non-registrable rejection (an invalid enum, a too-short value, a missing
// required field, a grounding/claim-binding failure) sitting before a
// LATER genuine bound violation in Validate()/ValidateAgainst()'s own
// clause order could still be masked by that later, unrelated bound --
// reporting a name for something the validator never actually rejected
// on. The fix is structural, not another case-by-case exclusion:
// DiagnoseContextFabric*Bound (context_fabric_bound_diagnosis.go) and
// internal/contextfabric's diagnoseSynthesisDraftBound are now literal,
// clause-by-clause MIRRORS of their Validate()/ValidateAgainst()
// counterparts' own left-to-right, short-circuit order -- including every
// non-bound clause -- and return at the FIRST clause that fails, naming a
// bound only when that first failure is one.
//
// This makes the exclusion list below an ORDER-PROOF guarantee, not an
// assumption about which check happens to run first: a rejection whose
// first-failing clause is anything below (an enum, a min-side length, an
// uniqueness/duplicate check, a grounding/claim-binding rule, a missing
// required field) yields NO violated_bound BY CONSTRUCTION -- the mirror
// stops there and never reaches a later clause, registered or not -- not
// because each case was individually reasoned about and found safe.
//
// CHAOS-3784 round-5: this guarantee holds only as long as each mirror's
// clause order is kept in sync with its Validate()/ValidateAgainst()
// counterpart by hand, since no mechanical check can compare two
// independently-written control-flow bodies for order equality (unlike
// ContextFabricModelFacingBounds itself, which IS mechanically checked
// against Diagnose* by TestContextFabricModelFacingBoundRegistryDiagnosisCoverage);
// this residual is accepted deliberately, not overlooked -- the shared
// clause helpers (diagnoseLengthBound and friends) cover every length/
// uniqueness clause BODY so only the call-site ORDER can drift, and the
// paired regression tests alongside each mirror
// (TestDiagnose*MatchesValidateStatementOrder/*StopsAtFirstFailingClause*)
// exist specifically to catch that drift for every bound it would affect,
// each time a Validate()/ValidateAgainst() clause order changes.
//   - ContextFabricFactRequirement.Subjects: the model has no wire field to
//     populate it through at all (factRequirementOutput in
//     genkitruntime/runtime.go carries only kind/parameters) -- toDomain()
//     never assigns it, so it is always nil regardless of what the model
//     returns. Structurally unreachable from model output, full stop --
//     the one exclusion that doesn't depend on clause order at all.
//   - Every MINIMUM-side length/count bound (ContextFabricModelMintedIDMinLength,
//     ContextFabricDriverAffectedSubjectsMinCount, SubjectRef.CanonicalID/
//     Label's min of 1, evidence_ref_id's min of 8): this registry's
//     one-entry-per-field convention (see e.g. driver_id/finding_id/
//     claim_id sharing a single "max_length" entry for their [8,256] shape)
//     names only the MAXIMUM side (CHAOS-3784 round-1 F3), so the mirror's
//     length-clause helper reports no name for a min-side failure -- there
//     is nothing to misattribute.
//   - Enum/range/logical checks (Standing, Category, Derivation,
//     EpistemicStatus, Confidence's [0,1] range, ContextFabricTimeContext's
//     axis-shaped timestamp rules, and every "requires a claim"/"requires a
//     qualification"/uniqueness/grounding/claim-binding rule): none of
//     these are length or count bounds, the sole scope of this registry
//     per the file doc comment above -- the mirror still evaluates them,
//     in Validate()'s own order, purely to know WHERE to stop.
const (
	// Interpretation (ContextFabricInterpretedQuestion, ContextFabricFactRequirement).
	ContextFabricRequestedJudgmentMaxLength = 256
	// The published schema bounds subject_terms and comparison_terms at 50
	// each; Go said 100 (CHAOS-3746 round 5).
	ContextFabricSubjectTermsMaxCount             = 50
	ContextFabricSubjectOrComparisonTermMaxLength = 512
	ContextFabricComparisonTermsMaxCount          = 50
	ContextFabricClarificationReasonMaxLength     = 2000
	// ContextFabricFactRequirementsMaxCount is DERIVED from the fact-kind
	// vocabulary, because that vocabulary is what actually caps the list:
	// ContextFabricInterpretedQuestion.validate rejects a duplicate Kind, so
	// no valid interpretation can carry more requirements than there are
	// legal kinds. The schema said 50 and Go said 64 (codex round-9 F1) --
	// two different numbers, neither of them reachable, and the disagreement
	// stayed invisible because a probe that duplicated requirements was
	// rejected for uniqueness at BOTH N and N+1 and scored as a pass.
	//
	// A number above the vocabulary size is not a loose bound, it is a
	// FALSE PROMISE: the schema told a client a 50-entry list was acceptable
	// when the service rejects every list past 20. Deriving the bound makes
	// the published contract state the truth, and makes it follow the
	// vocabulary automatically if a kind is ever added or pruned.
	ContextFabricFactRequirementsMaxCount = ContextFabricFactKindCount
	// Matches the published schema (was 1024, which the schema never
	// admitted -- CHAOS-3746 round 4).
	ContextFabricFactRequirementParameterValueMaxLength = 1000
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
	// shape: a claimed_fact_id reference points at a claim the MODEL
	// itself minted earlier in the SAME draft, not at something ACR
	// independently supplied and bounded -- so this length is genuinely
	// model-facing and belongs in the registry (CHAOS-3784 round-2 R2-2:
	// enforced at validate_context_fabric_result.go's uniqueTrimmedStrings
	// calls but absent from here and from Diagnose* until that fix).
	ContextFabricIdentifierRefMaxLength = 256
	// ContextFabricEvidenceRefIDsMaxCount bounds evidence_ref_ids wherever a
	// model populates it directly: per-driver and per-finding.
	// The RESULT-LEVEL evidence index; the published schema allows 500 here.
	ContextFabricEvidenceRefIDsMaxCount = 500
	// ContextFabricEvidenceRefIDMaxLength bounds each individual
	// evidence_ref_id string (as opposed to the COUNT bound above), matching
	// boundedEvidenceRefs's stringLengthBetween(value, 8, 256)
	// (validate_context_fabric_helpers.go). See the file doc comment
	// (CHAOS-3784 round-3 R3-1): the original exclusion rationale for this
	// bound was order-contradicted, same as path_id/claimed_fact_id item
	// length was before round-2 R2-2.
	ContextFabricEvidenceRefIDMaxLength = 256
	// Evidence references nested inside a driver, finding, or relationship
	// path. The published schema bounds these at 200 while Go enforced the
	// result-level 500, so a document could pass validation and still
	// violate the contract (CHAOS-3746 round 4).
	ContextFabricNestedEvidenceRefIDsMaxCount = 200

	// Synthesis: finding (ContextFabricFinding).
	ContextFabricFindingKindMaxLength    = 128
	ContextFabricFindingSummaryMaxLength = 4000
	ContextFabricFindingSubjectsMaxCount = 250

	// ContextFabricSubjectRefCanonicalIDMaxLength and
	// ContextFabricSubjectRefLabelMaxLength bound every model-minted
	// ContextFabricSubjectRef's CanonicalID/Label -- driver.affected_subjects,
	// finding.subjects, and claimed_fact.subject all share this shape
	// (SubjectRef.Validate(), validate_context_fabric_result.go). See the
	// file doc comment (CHAOS-3784 round-3 R3-1): order-contradicted
	// exclusion, same class as evidence_ref_id above.
	ContextFabricSubjectRefCanonicalIDMaxLength = 256
	ContextFabricSubjectRefLabelMaxLength       = 512

	// Synthesis: claimed fact (ContextFabricClaimedFact).
	ContextFabricClaimedFieldMaxLength = 128
	// ContextFabricClaimedFactValueMaxLength bounds a claimed fact's string
	// value (ContextFabricScalarValue.Validate(), the String variant only --
	// Integer/Number/Boolean/Null carry no length). See the file doc
	// comment (CHAOS-3784 round-3 R3-1): order-contradicted exclusion, same
	// class as evidence_ref_id above.
	ContextFabricClaimedFactValueMaxLength = 4000
	// ContextFabricClaimedFactMaxRows and ContextFabricClaimedFactRowMaxFields
	// bound a claimed/projected fact's OPTIONAL renderable table
	// (CHAOS-4347, ContextFabricClaimedFactRow) -- see
	// contracts/jsonschema/v1/context_fabric_common.v1.schema.json's
	// ClaimedFact.rows/ClaimedFactRow $defs, which carry these same
	// numbers as maxItems/maxProperties. This is NOT in
	// ContextFabricModelFacingBounds below: no synthesis prompt states
	// these numbers yet, because nothing populates Rows from the model
	// today -- it is a producer-facing capability (devhealthfacts fact
	// providers), not a synthesis output field.
	ContextFabricClaimedFactMaxRows      = 64
	ContextFabricClaimedFactRowMaxFields = 32

	// ContextFabricClaimedFactCombinedCellsMax and
	// ContextFabricClaimedFactCombinedContentBytesMax bound Rows and
	// TimeSeriesRows TOGETHER (CHAOS-4785). Each collection is
	// independently bounded at ContextFabricClaimedFactMaxRows x
	// ContextFabricClaimedFactRowMaxFields cells, so a fact populating
	// BOTH at their legal per-table maximum -- a real, producible shape
	// since CHAOS-4682 (§5.1 P2): ContextFabricClaimedFact's own doc
	// comment on TimeSeriesRows says a dual-table fact carries a legacy
	// table AND a time_series table at once -- reaches roughly
	// 2 x 64 x 32 x (128+4000) =~ 16.9M content bytes against a service
	// ceiling of 1 MiB (ContextFabricSerializedBytesMax). Verified
	// UNPRODUCED by any of today's five TimeSeriesRows producers
	// (devhealthfacts health/flow/readiness/workload/metrics): the
	// largest ClaimedFact actually observed in real data
	// (kiac/dh_0830, org 70d529e0, 2026-09-02) is 16,246 serialized
	// bytes -- so these bounds are additive defense in depth, not a
	// response to an active failure.
	//
	// CombinedCellsMax is set to exactly ONE table's own legal cell
	// budget (MaxRows x RowMaxFields): a dual-table fact may spend the
	// SAME total cell budget a single legacy table always could, never
	// double it.
	//
	// CombinedContentBytesMax is ContextFabricSerializedBytesMin (8192,
	// the smallest a caller may ever configure MaxSerializedBytes to)
	// x 32 = 262,144 bytes: >=16x the largest fact ever produced against
	// real data (16,246 bytes, so no observed shape is at risk), while
	// staying far below the 1 MiB response ceiling so ONE fact can never
	// consume it alone. Measured against claimedFactRowContentBytes'
	// ACTUAL json.Marshal size of each row (validate_context_fabric_
	// helpers.go), the same basis the 16,246-byte real-data measurement
	// used (Postgres octet_length of the stored serialized JSON) -- not
	// an approximation of raw string content, which a codex round proved
	// defeatable (HTML-escaped characters inflate up to 6x on the wire).
	ContextFabricClaimedFactCombinedCellsMax        = ContextFabricClaimedFactMaxRows * ContextFabricClaimedFactRowMaxFields
	ContextFabricClaimedFactCombinedContentBytesMax = ContextFabricSerializedBytesMin * 32

	// Synthesis: top-level synthesis draft / result collections the model
	// itself populates.
	ContextFabricStrongestPressuresMaxCount = 50
	ContextFabricStrongestPressureMaxLength = 2000
	ContextFabricDriversMaxCount            = 50
	ContextFabricRemainingWorkMaxCount      = 250
	ContextFabricReadinessGapsMaxCount      = 250
	ContextFabricConflictsMaxCount          = 250
	// These are MODEL-FACING: the synthesis prompt states them, so they
	// must equal what validation actually enforces on a write, or the
	// prompt invites the model to produce output the validator then
	// rejects. They were 250 x 4000 while the published JSON Schema said
	// 100 x 2000 (codex round-3 P2-4); the schema is the wire-contract
	// source of truth, so these moved to it.
	//
	// The looser historical values survive only as the stored-read
	// allowance in validate_context_fabric_result.go, which exists so
	// immutable rows written before the correction stay readable.
	// The result-level answer text. This is the SINGLE source: the
	// composers in internal/contextfabric/model_runtime.go truncate to
	// these, the result validator enforces them, and the synthesis prompt
	// states them. Before CHAOS-3746 round 7 the composers carried their
	// own copies at 8000/16000 while writes accepted 4000/12000, so a
	// valid synthesis with enough driver titles could compose a judgment
	// the engine then rejected -- an invalid-result failure entirely of
	// ACR's own making.
	ContextFabricDirectJudgmentMaxLength      = 4000
	ContextFabricCurrentStateMaxLength        = 4000
	ContextFabricDeterministicAnswerMaxLength = 12000

	ContextFabricLimitationsMaxCount = 100
	ContextFabricLimitationMaxLength = 2000
	ContextFabricWarningsMaxCount    = 100
	ContextFabricWarningMaxLength    = 2000
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
	{"synthesis.driver.affected_subjects.item_canonical_id_max_length", ContextFabricSubjectRefCanonicalIDMaxLength},
	{"synthesis.driver.affected_subjects.item_label_max_length", ContextFabricSubjectRefLabelMaxLength},
	{"synthesis.driver.path_ids.max_count", ContextFabricDriverPathIDsMaxCount},
	{"synthesis.driver.path_ids.item_max_length", ContextFabricIdentifierRefMaxLength},
	{"synthesis.driver.evidence_ref_ids.max_count", ContextFabricNestedEvidenceRefIDsMaxCount},
	{"synthesis.driver.evidence_ref_ids.item_max_length", ContextFabricEvidenceRefIDMaxLength},
	{"synthesis.driver.claimed_fact_ids.max_count", ContextFabricDriverClaimedFactIDsMaxCount},
	{"synthesis.driver.claimed_fact_ids.item_max_length", ContextFabricIdentifierRefMaxLength},

	{"synthesis.finding.finding_id.max_length", ContextFabricModelMintedIDMaxLength},
	{"synthesis.finding.kind.max_length", ContextFabricFindingKindMaxLength},
	{"synthesis.finding.summary.max_length", ContextFabricFindingSummaryMaxLength},
	{"synthesis.finding.subjects.max_count", ContextFabricFindingSubjectsMaxCount},
	{"synthesis.finding.subjects.item_canonical_id_max_length", ContextFabricSubjectRefCanonicalIDMaxLength},
	{"synthesis.finding.subjects.item_label_max_length", ContextFabricSubjectRefLabelMaxLength},
	{"synthesis.finding.evidence_ref_ids.max_count", ContextFabricNestedEvidenceRefIDsMaxCount},
	{"synthesis.finding.evidence_ref_ids.item_max_length", ContextFabricEvidenceRefIDMaxLength},
	{"synthesis.finding.claimed_fact_ids.max_count", ContextFabricDriverClaimedFactIDsMaxCount},
	{"synthesis.finding.claimed_fact_ids.item_max_length", ContextFabricIdentifierRefMaxLength},

	{"synthesis.claimed_fact.claim_id.max_length", ContextFabricModelMintedIDMaxLength},
	{"synthesis.claimed_fact.field.max_length", ContextFabricClaimedFieldMaxLength},
	{"synthesis.claimed_fact.subject.canonical_id_max_length", ContextFabricSubjectRefCanonicalIDMaxLength},
	{"synthesis.claimed_fact.subject.label_max_length", ContextFabricSubjectRefLabelMaxLength},
	{"synthesis.claimed_fact.value.max_length", ContextFabricClaimedFactValueMaxLength},

	{"synthesis.strongest_pressures.max_count", ContextFabricStrongestPressuresMaxCount},
	{"synthesis.strongest_pressures.item_max_length", ContextFabricStrongestPressureMaxLength},
	{"synthesis.drivers.max_count", ContextFabricDriversMaxCount},
	{"synthesis.remaining_work.max_count", ContextFabricRemainingWorkMaxCount},
	{"synthesis.readiness_gaps.max_count", ContextFabricReadinessGapsMaxCount},
	{"synthesis.conflicts.max_count", ContextFabricConflictsMaxCount},
	{"synthesis.direct_judgment.max_length", ContextFabricDirectJudgmentMaxLength},
	{"synthesis.current_state.max_length", ContextFabricCurrentStateMaxLength},
	{"synthesis.deterministic_answer.max_length", ContextFabricDeterministicAnswerMaxLength},
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
