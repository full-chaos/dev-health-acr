package mcp

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func init() {
	registerOutputVocabularies(
		outputVocabulary{
			name:           "AnswerCompleteness.terminal_reason",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"AnswerCompleteness.terminal_reason"},
			// schema: "clarification_reason_disclosed", "degraded_reason_disclosed", "limitation_disclosed", "warning_disclosed", "undisclosed"
			goSet:      []string{string(contractsv1.ContextFabricTerminalReasonClarificationDisclosed), string(contractsv1.ContextFabricTerminalReasonDegradedDisclosed), string(contractsv1.ContextFabricTerminalReasonLimitationDisclosed), string(contractsv1.ContextFabricTerminalReasonWarningDisclosed), string(contractsv1.ContextFabricTerminalReasonUndisclosed)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "AnswerCompleteness.terminal_status",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"AnswerCompleteness.terminal_status", "context_fabric_answer_projection.v1.status", "context_fabric_investigation_result.v1.status"},
			// schema: "complete", "partial", "degraded", "clarification_required", "no_match"
			goSet:      []string{string(contractsv1.ContextFabricInvestigationComplete), string(contractsv1.ContextFabricInvestigationPartial), string(contractsv1.ContextFabricInvestigationDegraded), string(contractsv1.ContextFabricInvestigationClarificationRequired), string(contractsv1.ContextFabricInvestigationNoMatch)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "AnswerCompleteness.state",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"AnswerCompleteness.state"},
			// schema: "not_derived", "complete", "partial", "degraded"
			goSet:      []string{string(contractsv1.ContextFabricAnswerCompletenessNotDerived), string(contractsv1.ContextFabricAnswerCompletenessComplete), string(contractsv1.ContextFabricAnswerCompletenessPartial), string(contractsv1.ContextFabricAnswerCompletenessDegraded)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "AnswerCompleteness.refusal_basis",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"AnswerCompleteness.refusal_basis", "context_fabric_investigation_result.v1.refusal_basis"},
			// schema: "member_kind_unservable", "frame_invariant_violated", "unspecified", "continuation_context_unverifiable", "declared_kind_unmatched", "organization_scope_unsupported", "subject_identity_unconfirmed"
			goSet:      []string{string(contractsv1.ContextFabricRefusalBasisMemberKindUnservable), string(contractsv1.ContextFabricRefusalBasisFrameInvariantViolated), string(contractsv1.ContextFabricRefusalBasisUnspecified), string(contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable), string(contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched), string(contractsv1.ContextFabricRefusalBasisOrganizationScopeUnsupported), string(contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "status_correction.replaced_status",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_status_correction.go:34 typed as the frozen ContextFabricInvestigationStatus
			sites: []string{"AnswerCompleteness.status_correction.replaced_status", "context_fabric_investigation_result.v1.status_correction.replaced_status"},
			// schema: "complete", "partial", "degraded"
			goSet:      []string{string(contractsv1.ContextFabricCohortDataComplete), string(contractsv1.ContextFabricCohortDataPartial), string(contractsv1.ContextFabricCohortDataDegraded)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "status_correction.direction",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_status_correction.go:5 "The vocabulary is exactly the pairs the authority is ever allowed to serve"
			sites: []string{"AnswerCompleteness.status_correction.direction", "context_fabric_investigation_result.v1.status_correction.direction"},
			// schema: "complete_to_partial", "complete_to_degraded", "partial_to_degraded", "degraded_to_partial"
			goSet:      []string{string(contractsv1.ContextFabricStatusCorrectionCompleteToPartial), string(contractsv1.ContextFabricStatusCorrectionCompleteToDegraded), string(contractsv1.ContextFabricStatusCorrectionPartialToDegraded), string(contractsv1.ContextFabricStatusCorrectionDegradedToPartial)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "status_correction.deciding_outcome",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_requirement_outcome.go:36 "CLOSED, five members."
			sites: []string{"AnswerCompleteness.status_correction.deciding_outcome", "PlanRequirementOutcomeRow.outcome", "context_fabric_investigation_result.v1.status_correction.deciding_outcome"},
			// schema: "satisfied", "narrowed", "unavailable", "not_applicable", "not_attempted"
			goSet:      []string{string(contractsv1.ContextFabricRequirementSatisfied), string(contractsv1.ContextFabricRequirementNarrowed), string(contractsv1.ContextFabricRequirementUnavailable), string(contractsv1.ContextFabricRequirementNotApplicable), string(contractsv1.ContextFabricRequirementNotAttempted)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "CoverageDetail.code",
			kind:           vocabAdditive,
			clientValidate: "none: CoverageDetail is checked on the write path only; a stored or served read never checks it",
			sites:          []string{"CoverageDetail.code"},
			// schema: "fact_unconfigured", "fact_no_declaring_producer", "fact_table_shape_undeclared", "fact_scope_unexpanded", "fact_read_failed", "fact_provider_reported", "fact_pruned", "fact_narrowed", "graph_endpoint_lookup_failed", "graph_exact_name_candidates_truncated", "graph_project_deployments_unlinked", "work_item_repository_unlinked", "graph_cohort_denied_by_authorization", "graph_unknown_relationship_type", "graph_validity_unbounded", "reuse_auxiliary_refs_stripped", "answer_terminated_before_attempt", "population_truncated", "requirement_read_not_planned", "read_population_unverified", "fact_read_origin_state", "kind_census_truncated", "graph_walk_cut_before_member", "computed_population_absent", "requirement_not_evaluable", "graph_no_member_found", "synthesis_input_bounded", "synthesis_model_failed", "synthesis_not_run", "fact_score_withheld"
			goSet:      []string{string(contractsv1.ContextFabricCoverageDetailFactUnconfigured), string(contractsv1.ContextFabricCoverageDetailFactNoDeclaringProducer), string(contractsv1.ContextFabricCoverageDetailFactTableShapeUndeclared), string(contractsv1.ContextFabricCoverageDetailFactScopeUnexpanded), string(contractsv1.ContextFabricCoverageDetailFactReadFailed), string(contractsv1.ContextFabricCoverageDetailFactProviderReported), string(contractsv1.ContextFabricCoverageDetailFactPruned), string(contractsv1.ContextFabricCoverageDetailFactNarrowed), string(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed), string(contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated), string(contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked), string(contractsv1.ContextFabricCoverageDetailWorkItemRepositoryUnlinked), string(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization), string(contractsv1.ContextFabricCoverageDetailGraphUnknownRelationshipType), string(contractsv1.ContextFabricCoverageDetailGraphValidityUnbounded), string(contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped), string(contractsv1.ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt), string(contractsv1.ContextFabricCoverageDetailPopulationTruncated), string(contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned), string(contractsv1.ContextFabricCoverageDetailReadPopulationUnverified), string(contractsv1.ContextFabricCoverageDetailFactReadOriginState), string(contractsv1.ContextFabricCoverageDetailKindCensusTruncated), string(contractsv1.ContextFabricCoverageDetailGraphWalkCutBeforeMember), string(contractsv1.ContextFabricCoverageDetailComputedPopulationAbsent), string(contractsv1.ContextFabricCoverageDetailRequirementNotEvaluable), string(contractsv1.ContextFabricCoverageDetailGraphNoMemberFound), string(contractsv1.ContextFabricCoverageDetailSynthesisInputBounded), string(contractsv1.ContextFabricCoverageDetailSynthesisModelFailed), string(contractsv1.ContextFabricCoverageDetailSynthesisNotRun), string(contractsv1.ContextFabricCoverageDetailFactScoreWithheld)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "Cohort.kind",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"Cohort.kind", "CoverageDetail.kind", "ProjectedCohort.kind", "SubjectRef.kind"},
			// schema: "organization", "team", "project", "repository", "work_item", "pull_request", "deployment", "incident", "document", "decision", "episode", "metric", "pull_request_review", "ci_pipeline_run", "work_item_ref"
			goSet:      []string{string(contractsv1.ContextFabricSubjectOrganization), string(contractsv1.ContextFabricSubjectTeam), string(contractsv1.ContextFabricSubjectProject), string(contractsv1.ContextFabricSubjectRepository), string(contractsv1.ContextFabricSubjectWorkItem), string(contractsv1.ContextFabricSubjectPullRequest), string(contractsv1.ContextFabricSubjectDeployment), string(contractsv1.ContextFabricSubjectIncident), string(contractsv1.ContextFabricSubjectDocument), string(contractsv1.ContextFabricSubjectDecision), string(contractsv1.ContextFabricSubjectEpisode), string(contractsv1.ContextFabricSubjectMetric), string(contractsv1.ContextFabricSubjectPullRequestReview), string(contractsv1.ContextFabricSubjectCIRun), string(contractsv1.ContextFabricSubjectWorkItemRef)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "SubjectKind",
			kind: vocabFrozen,
			why:  "request-echo",
			// Go: the $defs entry and SubjectHint.kind are reachable from canonical requests, whose Go validation is closed
			sites: []string{"SubjectHint.kind", "SubjectKind"},
			// schema: "organization", "team", "project", "repository", "work_item", "pull_request", "deployment", "incident", "document", "decision", "episode", "metric", "pull_request_review", "ci_pipeline_run", "work_item_ref"
			goSet:      []string{string(contractsv1.ContextFabricSubjectOrganization), string(contractsv1.ContextFabricSubjectTeam), string(contractsv1.ContextFabricSubjectProject), string(contractsv1.ContextFabricSubjectRepository), string(contractsv1.ContextFabricSubjectWorkItem), string(contractsv1.ContextFabricSubjectPullRequest), string(contractsv1.ContextFabricSubjectDeployment), string(contractsv1.ContextFabricSubjectIncident), string(contractsv1.ContextFabricSubjectDocument), string(contractsv1.ContextFabricSubjectDecision), string(contractsv1.ContextFabricSubjectEpisode), string(contractsv1.ContextFabricSubjectMetric), string(contractsv1.ContextFabricSubjectPullRequestReview), string(contractsv1.ContextFabricSubjectCIRun), string(contractsv1.ContextFabricSubjectWorkItemRef)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "CoverageDetail.failure_class",
			kind:           vocabAdditive,
			clientValidate: "none: CoverageDetail is checked on the write path only; a stored or served read never checks it",
			sites:          []string{"CoverageDetail.failure_class"},
			// schema: "model_output_invalid", "synthesis_rejected", "model_receipt_unrecorded", "model_call_not_completed"
			goSet:      []string{string(contractsv1.ContextFabricSynthesisFailureModelOutputInvalid), string(contractsv1.ContextFabricSynthesisFailureRejected), string(contractsv1.ContextFabricSynthesisFailureReceiptUnrecorded), string(contractsv1.ContextFabricSynthesisFailureCallNotCompleted), string(contractsv1.ContextFabricSynthesisFailureDeadline), string(contractsv1.ContextFabricSynthesisFailureNotNarrowable)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "CoverageDetail.retry_hint",
			kind:           vocabAdditive,
			clientValidate: "none: CoverageDetail is checked on the write path only; a stored or served read never checks it",
			sites:          []string{"CoverageDetail.retry_hint"},
			// schema: "retry_may_succeed", "retry_unlikely_to_help"
			goSet:      []string{string(contractsv1.ContextFabricRetryMaySucceed), string(contractsv1.ContextFabricRetryUnlikelyToHelp)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "EffectiveEvidenceWindow.confidence",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"EffectiveEvidenceWindow.confidence", "InterpretedQuestion.window_confidence"},
			// schema: "high", "low"
			goSet:      []string{string(contractsv1.ContextFabricWindowConfidenceHigh), string(contractsv1.ContextFabricWindowConfidenceLow)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "EffectiveEvidenceWindow.provenance",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"EffectiveEvidenceWindow.provenance"},
			// schema: "inferred_default", "question_stated", "clarification_confirmed"
			goSet:      []string{string(contractsv1.ContextFabricWindowInferredDefault), string(contractsv1.ContextFabricWindowQuestionStated), string(contractsv1.ContextFabricWindowClarificationConfirmed)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "EffectiveEvidenceWindow.window_class",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"EffectiveEvidenceWindow.window_class", "InterpretedQuestion.window_class", "WindowExpandOption.window_class"},
			// schema: "trend_assessment", "recent_activity_lookup", "state_snapshot", "explicit_window"
			goSet:      []string{string(contractsv1.ContextFabricWindowClassTrendAssessment), string(contractsv1.ContextFabricWindowClassRecentActivityLookup), string(contractsv1.ContextFabricWindowClassStateSnapshot), string(contractsv1.ContextFabricWindowClassExplicitWindow)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "PriorSubjectReceiptDisposition",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_types.go:1004 "the SAME design brief §2.1 rule ContextFabricStructureDisposition already applies" (silent-drop closure)
			sites: []string{"PriorSubjectReceiptDisposition"},
			// schema: "applied", "skipped_unloadable", "skipped_no_match", "skipped_stale_graph_epoch", "skipped_failed_reauth"
			goSet:      []string{string(contractsv1.ContextFabricPriorSubjectReceiptApplied), string(contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable), string(contractsv1.ContextFabricPriorSubjectReceiptSkippedNoMatch), string(contractsv1.ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch), string(contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "ProjectedCandidate.state",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"ProjectedCandidate.state", "SubjectCandidate.state"},
			// schema: "committed", "proposed", "ambiguous", "unresolved"
			goSet:      []string{string(contractsv1.ContextFabricResolutionCommitted), string(contractsv1.ContextFabricResolutionProposed), string(contractsv1.ContextFabricResolutionAmbiguous), string(contractsv1.ContextFabricResolutionUnresolved)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "Cohort.score_meaning",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"Cohort.score_meaning", "ProjectedCohort.score_meaning"},
			// schema: "attention"
			goSet:      []string{string(contractsv1.ContextFabricCohortScoreMeaningAttention)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "CohortMember.data_completeness",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMember.data_completeness", "ProjectedCohortMember.data_completeness"},
			// schema: "complete", "partial", "degraded"
			goSet:      []string{string(contractsv1.ContextFabricCohortDataComplete), string(contractsv1.ContextFabricCohortDataPartial), string(contractsv1.ContextFabricCohortDataDegraded)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "missing_signals.items",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMember.missing_signals.items", "CohortMemberDriver.signal", "ProjectedCohortMember.missing_signals.items"},
			// schema: "investment_mix", "health.compounding_risk", "operational_deficiencies.severity", "readiness.coverage_gap", "workload.forecast_pressure"
			goSet:      []string{contextfabric.RankingSignalInvestmentMix, contextfabric.RankingSignalHealthRisk, contextfabric.RankingSignalDeficiencySeverity, contextfabric.RankingSignalReadinessGap, contextfabric.RankingSignalWorkloadPressure},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "CohortMember.outcome",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMember.outcome", "ProjectedCohortMember.outcome"},
			// schema: "qualified", "provisional", "insufficient_evidence", "not_applicable"
			goSet:      []string{string(contractsv1.ContextFabricCohortOutcomeQualified), string(contractsv1.ContextFabricCohortOutcomeProvisional), string(contractsv1.ContextFabricCohortOutcomeInsufficientEvidence), string(contractsv1.ContextFabricCohortOutcomeNotApplicable)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "ranking_basis.items",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMember.ranking_basis.items", "ProjectedCohortMember.ranking_basis.items"},
			// schema: "investment_mix", "health.compounding_risk", "operational_deficiencies.severity", "readiness.coverage_gap", "workload.forecast_pressure", "investment_mix.reactive_share_high", "investment_mix.deliberate_share_low", "investment_mix.mix_concentrated", "investment_mix.mix_shift_toward_operational", "investment_mix.mix_shift_toward_feature", "investment_mix.mix_shift_other"
			goSet:      []string{contextfabric.RankingSignalInvestmentMix, contextfabric.RankingSignalHealthRisk, contextfabric.RankingSignalDeficiencySeverity, contextfabric.RankingSignalReadinessGap, contextfabric.RankingSignalWorkloadPressure, contextfabric.DriverReactiveShareHigh, contextfabric.DriverDeliberateShareLow, contextfabric.DriverMixConcentrated, contextfabric.DriverMixShiftTowardOperational, contextfabric.DriverMixShiftTowardFeature, contextfabric.DriverMixShiftOther},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.state",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"Coverage.sources.items.state", "DriverJudgment.source_state", "ProjectedCoverage.state", "ProjectedDriver.source_state", "SourceObservation.state"},
			// schema: "available", "stale", "unavailable", "unconfigured", "unauthorized", "no_data", "truncated", "conflicted", "not_applicable", "pruned"
			goSet:      []string{string(contractsv1.ContextFabricSourceAvailable), string(contractsv1.ContextFabricSourceStale), string(contractsv1.ContextFabricSourceUnavailable), string(contractsv1.ContextFabricSourceUnconfigured), string(contractsv1.ContextFabricSourceUnauthorized), string(contractsv1.ContextFabricSourceNoData), string(contractsv1.ContextFabricSourceTruncated), string(contractsv1.ContextFabricSourceConflicted), string(contractsv1.ContextFabricSourceNotApplicable), string(contractsv1.ContextFabricSourcePruned)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "DriverJudgment.category",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"DriverJudgment.category", "Finding.kind", "ProjectedDriver.category"},
			// schema: "status", "actual_completion", "work", "blockers", "reviews", "continuous_integration", "deployments", "incidents", "health", "workload", "investment", "readiness", "operational_deficiency", "source_health", "relationship", "narrative"
			goSet:      []string{string(contractsv1.ContextFabricDriverCategoryStatus), string(contractsv1.ContextFabricDriverCategoryCompletion), string(contractsv1.ContextFabricDriverCategoryWork), string(contractsv1.ContextFabricDriverCategoryBlockers), string(contractsv1.ContextFabricDriverCategoryReviews), string(contractsv1.ContextFabricDriverCategoryCI), string(contractsv1.ContextFabricDriverCategoryDeployments), string(contractsv1.ContextFabricDriverCategoryIncidents), string(contractsv1.ContextFabricDriverCategoryHealth), string(contractsv1.ContextFabricDriverCategoryWorkload), string(contractsv1.ContextFabricDriverCategoryInvestment), string(contractsv1.ContextFabricDriverCategoryReadiness), string(contractsv1.ContextFabricDriverCategoryDeficiency), string(contractsv1.ContextFabricDriverCategorySourceHealth), string(contractsv1.ContextFabricDriverCategoryRelationship), string(contractsv1.ContextFabricDriverCategoryNarrative)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "DriverJudgment.standing",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"DriverJudgment.standing", "ProjectedDriver.standing"},
			// schema: "principal", "contributing", "symptom", "context", "withheld"
			goSet:      []string{string(contractsv1.ContextFabricDriverPrincipal), string(contractsv1.ContextFabricDriverContributing), string(contractsv1.ContextFabricDriverSymptom), string(contractsv1.ContextFabricDriverContext), string(contractsv1.ContextFabricDriverWithheld)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "ClaimedFact.kind",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"ClaimedFact.kind", "ProjectedFact.kind"},
			// schema: "identity", "membership", "status", "actual_completion", "work", "blockers", "required_children", "pull_requests", "reviews", "continuous_integration", "deployments", "incidents", "metrics", "health", "workload", "investment", "readiness", "operational_deficiencies", "source_health", "evidence", "flow", "landscape", "cardinality"
			goSet:      []string{string(contractsv1.ContextFabricFactIdentity), string(contractsv1.ContextFabricFactMembership), string(contractsv1.ContextFabricFactStatus), string(contractsv1.ContextFabricFactActualCompletion), string(contractsv1.ContextFabricFactWork), string(contractsv1.ContextFabricFactBlockers), string(contractsv1.ContextFabricFactRequiredChildren), string(contractsv1.ContextFabricFactPullRequests), string(contractsv1.ContextFabricFactReviews), string(contractsv1.ContextFabricFactContinuousIntegration), string(contractsv1.ContextFabricFactDeployments), string(contractsv1.ContextFabricFactIncidents), string(contractsv1.ContextFabricFactMetrics), string(contractsv1.ContextFabricFactHealth), string(contractsv1.ContextFabricFactWorkload), string(contractsv1.ContextFabricFactInvestment), string(contractsv1.ContextFabricFactReadiness), string(contractsv1.ContextFabricFactOperationalDeficiencies), string(contractsv1.ContextFabricFactSourceHealth), string(contractsv1.ContextFabricFactEvidence), string(contractsv1.ContextFabricFactFlow), string(contractsv1.ContextFabricFactLandscape), string(contractsv1.ContextFabricFactSecurityAlerts), string(contractsv1.ContextFabricFactCommitment), string(contractsv1.ContextFabricFactFileHotspots), string(contractsv1.ContextFabricFactCardinality)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "ClaimedFactTable.shape",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"ClaimedFactTable.shape", "ProjectedFactTable.shape"},
			// schema: "time_series", "breakdown", "ranking"
			goSet:      []string{string(contractsv1.ContextFabricFactTableShapeTimeSeries), string(contractsv1.ContextFabricFactTableShapeBreakdown), string(contractsv1.ContextFabricFactTableShapeRanking)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "ProjectionBudget.cohort_member_selection_basis",
			kind: vocabFrozen,
			why:  "protocol",
			// Go: context_fabric_answer_plan.go:178 "the CLOSED vocabulary of orders a narrowing step may take"
			sites: []string{"ProjectionBudget.cohort_member_selection_basis"},
			// schema: "largest_group_round_robin", "overlap_aware_set_cover"
			goSet:      []string{string(contractsv1.ContextFabricNarrowingBasisLargestGroupRoundRobin), string(contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover)},
			exhaustive: false,
		},
		outputVocabulary{
			name: "RelativeWindowID",
			kind: vocabFrozen,
			why:  "request-echo",
			// Go: context_fabric_window_types.go:18 "A caller may NAME one of these identifiers on a request"
			sites: []string{"RelativeWindowID"},
			// schema: "trailing_30d", "trailing_90d", "trailing_365d", "all_time"
			goSet:      []string{string(contractsv1.ContextFabricRelativeWindowTrailing30D), string(contractsv1.ContextFabricRelativeWindowTrailing90D), string(contractsv1.ContextFabricRelativeWindowTrailing365D), string(contractsv1.ContextFabricRelativeWindowAllTime)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "RenderAxisKind",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"RenderAxisKind"},
			// schema: "category", "time"
			goSet:      []string{string(contractsv1.ContextFabricRenderAxisCategory), string(contractsv1.ContextFabricRenderAxisTime)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "RenderKind",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"RenderKind"},
			// schema: "series", "table", "quadrant", "treemap", "sunburst", "sankey", "burndown", "forecast"
			goSet:      []string{string(contractsv1.ContextFabricRenderKindSeries), string(contractsv1.ContextFabricRenderKindTable), string(contractsv1.ContextFabricRenderKindQuadrant), string(contractsv1.ContextFabricRenderKindTreemap), string(contractsv1.ContextFabricRenderKindSunburst), string(contractsv1.ContextFabricRenderKindSankey), string(contractsv1.ContextFabricRenderKindBurndown), string(contractsv1.ContextFabricRenderKindForecast)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "RenderPointSource.kind",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"RenderPointSource.kind"},
			// schema: "cohort_member_score", "cohort_driver_weight_contributed", "claimed_fact_row"
			goSet:      []string{string(contractsv1.ContextFabricRenderSourceCohortMemberScore), string(contractsv1.ContextFabricRenderSourceCohortDriverWeight), string(contractsv1.ContextFabricRenderSourceClaimedFactRow)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "RenderPresentation",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"RenderPresentation"},
			// schema: "bars", "stacked_bars", "line"
			goSet:      []string{string(contractsv1.ContextFabricRenderPresentationBars), string(contractsv1.ContextFabricRenderPresentationStackedBars), string(contractsv1.ContextFabricRenderPresentationLine)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "RenderShapeRule",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"RenderShapeRule"},
			// schema: "cohort_attention_score", "cohort_driver_contribution", "dated_fact_trend"
			goSet:      []string{string(contractsv1.ContextFabricRenderRuleCohortAttentionScore), string(contractsv1.ContextFabricRenderRuleCohortDriverContribution), string(contractsv1.ContextFabricRenderRuleDatedFactTrend)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "StructureDisposition",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_structure_types.go:385 "the closed vocabulary for what happened to one carried structure member (design brief §2.1's silent-drop closure"
			sites: []string{"StructureDisposition"},
			// schema: "applied", "vetoed_unresolved", "vetoed_conflict", "vetoed_stale", "superseded_by_caller", "not_evaluated"
			goSet:      []string{string(contractsv1.ContextFabricStructureDispositionApplied), string(contractsv1.ContextFabricStructureDispositionVetoedUnresolved), string(contractsv1.ContextFabricStructureDispositionVetoedConflict), string(contractsv1.ContextFabricStructureDispositionVetoedStale), string(contractsv1.ContextFabricStructureDispositionSupersededByCaller), string(contractsv1.ContextFabricStructureDispositionNotEvaluated)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "StructureNeedKind",
			kind: vocabFrozen,
			why:  "protocol",
			// Go: context_fabric_structure_types.go:285 "a NEW closed-enum value is unrecoverable for any v1-pinned consumer"
			sites: []string{"StructureNeedKind"},
			// schema: "expected_kind", "subject_anchor", "subject_handle", "window", "subject_candidate"
			goSet:      []string{string(contractsv1.ContextFabricStructureNeedExpectedKind), string(contractsv1.ContextFabricStructureNeedSubjectAnchor), string(contractsv1.ContextFabricStructureNeedSubjectHandle), string(contractsv1.ContextFabricStructureNeedWindow), string(contractsv1.ContextFabricStructureNeedSubjectCandidate)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "StructureOfferSource",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"StructureOfferSource"},
			// schema: "engine", "prior"
			goSet:      []string{string(contractsv1.ContextFabricStructureOfferEngine), string(contractsv1.ContextFabricStructureOfferPrior)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "StructureProvenance",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"StructureProvenance"},
			// schema: "inferred_default", "question_stated", "clarification_confirmed", "engine_committed"
			goSet:      []string{string(contractsv1.ContextFabricStructureInferredDefault), string(contractsv1.ContextFabricStructureQuestionStated), string(contractsv1.ContextFabricStructureClarificationConfirmed), string(contractsv1.ContextFabricStructureEngineCommitted)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "StructureSource",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"StructureSource"},
			// schema: "receipt", "explicit", "explicit_unattributed", "carried"
			goSet:      []string{string(contractsv1.ContextFabricStructureSourceReceipt), string(contractsv1.ContextFabricStructureSourceExplicit), string(contractsv1.ContextFabricStructureSourceExplicitUnattributed), string(contractsv1.ContextFabricStructureSourceCarried)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "1.axis",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"TemporalLabel.effective.allOf.1.axis", "TemporalLabel.requested.allOf.1.axis"},
			// schema: "valid_time", "observed_time", "range"
			goSet:      []string{string(contractsv1.ContextFabricTemporalValidTime), string(contractsv1.ContextFabricTemporalObservedTime), string(contractsv1.ContextFabricTemporalRange)},
			exhaustive: false,
		},
		outputVocabulary{
			name:  "TemporalLabel.grain",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"TemporalLabel.grain"},
			// schema: "instant", "day", "none"
			goSet:      []string{string(contractsv1.ContextFabricGrainInstant), string(contractsv1.ContextFabricGrainDay), string(contractsv1.ContextFabricGrainNone)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "1.axis#2",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"TimeContext.oneOf.1.axis"},
			// schema: "valid_time", "observed_time"
			goSet:      []string{string(contractsv1.ContextFabricTemporalValidTime), string(contractsv1.ContextFabricTemporalObservedTime)},
			exhaustive: false,
		},
		outputVocabulary{
			name:  "TimeContext.axis",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"TimeContext.axis"},
			// schema: "current", "valid_time", "observed_time", "range"
			goSet:      []string{string(contractsv1.ContextFabricTemporalCurrent), string(contractsv1.ContextFabricTemporalValidTime), string(contractsv1.ContextFabricTemporalObservedTime), string(contractsv1.ContextFabricTemporalRange)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "VersionSet.interpretation_source",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"VersionSet.interpretation_source", "VersionSet.synthesis_source"},
			// schema: "server", "client"
			goSet:      []string{string(contractsv1.ContextFabricSynthesisModeServer), string(contractsv1.ContextFabricSynthesisModeClient)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "PlanRequirementOutcomeRow.stage",
			kind: vocabFrozen,
			why:  "protocol",
			// Go: context_fabric_requirement_outcome.go:117 "Widening the shipped vocabulary instead was refused"
			sites: []string{"PlanRequirementOutcomeRow.stage", "RequirementRefinement.stage"},
			// schema: "planning", "assembled_result", "projection", "reuse"
			goSet:      []string{string(contractsv1.ContextFabricOutcomeStagePlanning), string(contractsv1.ContextFabricOutcomeStageAssembledResult), string(contractsv1.ContextFabricOutcomeStageProjection), string(contractsv1.ContextFabricOutcomeStageReuse)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "PlanRequirementOutcomeRow.impact",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_requirement_outcome.go:188 "CLOSED, four members."
			sites: []string{"PlanRequirementOutcomeRow.impact"},
			// schema: "none", "scope", "depth", "dimension"
			goSet:      []string{string(contractsv1.ContextFabricAnswerImpactNone), string(contractsv1.ContextFabricAnswerImpactScope), string(contractsv1.ContextFabricAnswerImpactDepth), string(contractsv1.ContextFabricAnswerImpactDimension)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "BudgetOverrun",
			kind: vocabFrozen,
			why:  "protocol",
			// Go: context_fabric_response_budget.go:152 "the CLOSED vocabulary for which axis a result exceeded"
			sites: []string{"BudgetOverrun", "PlanRequirementOutcomeRow.cause_overrun", "RequirementRefinement.overrun"},
			// schema: "fits", "items", "bytes"
			goSet:      []string{string(contractsv1.ContextFabricBudgetFits), string(contractsv1.ContextFabricBudgetOverrunItems), string(contractsv1.ContextFabricBudgetOverrunBytes)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "NarrowingBasis",
			kind: vocabFrozen,
			why:  "protocol",
			// Go: context_fabric_answer_plan.go:178 "the CLOSED vocabulary of orders a narrowing step may take"
			sites: []string{"NarrowingBasis", "RequirementRefinement.basis"},
			// schema: "canonical_id_lexical", "largest_group_round_robin", "attention_rank", "overlap_aware_set_cover", "link_strength_then_id"
			goSet:      []string{string(contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical), string(contractsv1.ContextFabricNarrowingBasisLargestGroupRoundRobin), string(contractsv1.ContextFabricNarrowingBasisAttentionRank), string(contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover), string(contractsv1.ContextFabricNarrowingBasisLinkStrengthThenID)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "semantic_reading.status",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_semantic_reading.go:22 "One member: a present field only ever says the reading was unavailable."
			sites: []string{"context_fabric_answer_projection.v1.semantic_reading.status", "context_fabric_investigation_result.v1.semantic_reading.status"},
			// schema: "unavailable"
			goSet:      []string{string(contractsv1.ContextFabricSemanticReadingUnavailable)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "semantic_reading.reason",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"context_fabric_answer_projection.v1.semantic_reading.reason", "context_fabric_investigation_result.v1.semantic_reading.reason"},
			// schema: "semantic_state_absent", "semantic_state_unreadable"
			goSet:      []string{string(contractsv1.ContextFabricSemanticReadingStateAbsent), string(contractsv1.ContextFabricSemanticReadingStateUnreadable)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "fact_kinds.items",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"AnswerPlan.fact_kinds.items", "FactRequirement.kind", "PlanRequirement.fact_kinds.items", "PlanRequirement.input_fact_kinds.items"},
			// schema: "identity", "membership", "status", "actual_completion", "work", "blockers", "required_children", "pull_requests", "reviews", "continuous_integration", "deployments", "incidents", "metrics", "health", "workload", "investment", "readiness", "operational_deficiencies", "source_health", "evidence", "flow", "landscape"
			goSet:      []string{string(contractsv1.ContextFabricFactIdentity), string(contractsv1.ContextFabricFactMembership), string(contractsv1.ContextFabricFactStatus), string(contractsv1.ContextFabricFactActualCompletion), string(contractsv1.ContextFabricFactWork), string(contractsv1.ContextFabricFactBlockers), string(contractsv1.ContextFabricFactRequiredChildren), string(contractsv1.ContextFabricFactPullRequests), string(contractsv1.ContextFabricFactReviews), string(contractsv1.ContextFabricFactContinuousIntegration), string(contractsv1.ContextFabricFactDeployments), string(contractsv1.ContextFabricFactIncidents), string(contractsv1.ContextFabricFactMetrics), string(contractsv1.ContextFabricFactHealth), string(contractsv1.ContextFabricFactWorkload), string(contractsv1.ContextFabricFactInvestment), string(contractsv1.ContextFabricFactReadiness), string(contractsv1.ContextFabricFactOperationalDeficiencies), string(contractsv1.ContextFabricFactSourceHealth), string(contractsv1.ContextFabricFactEvidence), string(contractsv1.ContextFabricFactFlow), string(contractsv1.ContextFabricFactLandscape), string(contractsv1.ContextFabricFactSecurityAlerts), string(contractsv1.ContextFabricFactCommitment), string(contractsv1.ContextFabricFactFileHotspots)},
			exhaustive: false,
		},
		outputVocabulary{
			name:           "PlanRequirement.obligation",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.obligation"},
			// schema: "state", "completion", "readiness", "health", "principal_drivers", "ranking", "remaining_work", "evidence", "coverage", "count", "allocation_breakdown", "trend_series", "period_delta"
			goSet:      []string{string(contextfabric.ObligationState), string(contextfabric.ObligationCompletion), string(contextfabric.ObligationReadiness), string(contextfabric.ObligationHealth), string(contextfabric.ObligationPrincipalDrivers), string(contextfabric.ObligationRanking), string(contextfabric.ObligationRemainingWork), string(contextfabric.ObligationEvidence), string(contextfabric.ObligationCoverage), string(contextfabric.ObligationCount), string(contextfabric.ObligationAllocationBreakdown), string(contextfabric.ObligationTrendSeries), string(contextfabric.ObligationPeriodDelta)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.role",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.role"},
			// schema: "subject", "member", "group", "operand"
			goSet:      []string{string(contextfabric.SubjectRoleSubject), string(contextfabric.SubjectRoleMember), string(contextfabric.SubjectRoleGroup), string(contextfabric.SubjectRoleOperand)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.kind",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.kind"},
			// schema: "read", "computed", "answer_contract"
			goSet:      []string{string(contextfabric.ObligationKindRead), string(contextfabric.ObligationKindComputed), string(contextfabric.ObligationKindAnswerContract)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.step",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.step"},
			// schema: "rank_cohort", "membership_cardinality"
			goSet:      []string{string(contextfabric.ComputedStepRankCohort), string(contextfabric.ComputedStepMembershipCardinality)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.step_execution",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.step_execution"},
			// schema: "server_executed", "declared_only"
			goSet:      []string{string(contextfabric.ComputedStepServerExecuted), string(contextfabric.ComputedStepDeclaredOnly)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.input_class",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.input_class"},
			// schema: "fact_kinds", "resolved_member_set"
			goSet:      []string{string(contextfabric.ComputedInputFactKinds), string(contextfabric.ComputedInputResolvedMemberSet)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.scope",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.scope"},
			// schema: "single_subject", "each_operand", "each_member", "each_group"
			goSet:      []string{string(contextfabric.CompletionScopeSingleSubject), string(contextfabric.CompletionScopeEachOperand), string(contextfabric.CompletionScopeEachMember), string(contextfabric.CompletionScopeEachGroup)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.quantifier",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.quantifier"},
			// schema: "at_least_one", "corroborated", "exact", "all", "none"
			goSet:      []string{string(contextfabric.CompletionQuantifierAtLeastOne), string(contextfabric.CompletionQuantifierCorroborated), string(contextfabric.CompletionQuantifierExact), string(contextfabric.CompletionQuantifierAll), string(contextfabric.CompletionQuantifierNone)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "PlanRequirement.unavailable",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"PlanRequirement.unavailable"},
			// schema: "subject_kind_unsupported", "no_declaring_producer", "table_shape_undeclared", "computed_population_absent"
			goSet:      []string{string(contextfabric.RequirementReasonSubjectKindUnsupported), string(contextfabric.RequirementReasonNoDeclaringProducer), string(contextfabric.RequirementReasonTableShapeUndeclared), string(contextfabric.RequirementReasonComputedPopulationAbsent)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "ClaimedFact.operation",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"ClaimedFact.operation"},
			// schema: "sum", "difference", "ratio"
			goSet:      []string{string(contractsv1.ContextFabricDerivationSum), string(contractsv1.ContextFabricDerivationDifference), string(contractsv1.ContextFabricDerivationRatio)},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "CohortMemberDriver.concentration_method",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMemberDriver.concentration_method"},
			// schema: "max_share"
			goSet:      []string{contextfabric.ConcentrationMethodMaxShare},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "threshold_labels.items",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMemberDriver.threshold_labels.items"},
			// schema: "investment_mix.reactive_share_high", "investment_mix.deliberate_share_low", "investment_mix.mix_concentrated", "investment_mix.mix_shift_toward_operational", "investment_mix.mix_shift_toward_feature", "investment_mix.mix_shift_other"
			goSet:      []string{contextfabric.DriverReactiveShareHigh, contextfabric.DriverDeliberateShareLow, contextfabric.DriverMixConcentrated, contextfabric.DriverMixShiftTowardOperational, contextfabric.DriverMixShiftTowardFeature, contextfabric.DriverMixShiftOther},
			exhaustive: true,
		},
		outputVocabulary{
			name:  "CohortMemberDriver.window",
			kind:  vocabFrozen,
			why:   "scoring",
			sites: []string{"CohortMemberDriver.window"},
			// schema: "current", "current_vs_prior"
			goSet:      []string{string(contractsv1.ContextFabricCohortMemberDriverWindowCurrent), string(contractsv1.ContextFabricCohortMemberDriverWindowCurrentVsPrior)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "CommitDecisionDigest.identity_lookup",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"CommitDecisionDigest.identity_lookup"},
			// schema: "complete", "incomplete", "graph_lag", "not_run_time_axis", "not_wired"
			goSet:      []string{contextfabric.IdentityLookupTokenComplete, contextfabric.IdentityLookupTokenIncomplete, contextfabric.IdentityLookupTokenGraphLag, contextfabric.IdentityLookupTokenNotRunTimeAxis, contextfabric.IdentityLookupTokenNotWired},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "CommitDecisionDigest.commit_gate",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"CommitDecisionDigest.commit_gate"},
			// schema: "", "caller_hint_short_circuit", "pre_committed_exact_hint", "exact_index", "identity_fast_path", "lone_floor", "top_of_two", "vector_margin_rescue", "evidence_census"
			goSet:      append([]string{""}, contractsv1.CommitGateVocabulary()...),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "ConversationTurn.role",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"ConversationTurn.role"},
			// schema: "user", "assistant"
			goSet:      []string{string(contractsv1.ContextFabricConversationUser), string(contractsv1.ContextFabricConversationAssistant)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "DriverJudgment.derivation",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"DriverJudgment.derivation", "RelationshipEdge.derivation", "RelationshipProjection.derivation"},
			// schema: "canonical_structured", "deterministic_projection", "graph_associated", "model_extracted", "rule_inferred"
			goSet:      []string{string(contractsv1.ContextFabricDerivationCanonicalStructured), string(contractsv1.ContextFabricDerivationDeterministicProjection), string(contractsv1.ContextFabricDerivationGraphAssociated), string(contractsv1.ContextFabricDerivationModelExtracted), string(contractsv1.ContextFabricDerivationRuleInferred)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "DriverJudgment.epistemic_status",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"DriverJudgment.epistemic_status", "RelationshipEdge.epistemic_status", "RelationshipProjection.epistemic_status"},
			// schema: "observed", "source_asserted", "inferred", "disputed", "superseded", "unknown"
			goSet:      []string{string(contractsv1.ContextFabricEpistemicObserved), string(contractsv1.ContextFabricEpistemicSourceAsserted), string(contractsv1.ContextFabricEpistemicInferred), string(contractsv1.ContextFabricEpistemicDisputed), string(contractsv1.ContextFabricEpistemicSuperseded), string(contractsv1.ContextFabricEpistemicUnknown)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "InterpretedQuestion.shape",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"InterpretedQuestion.shape"},
			// schema: "single_subject", "explicit_cohort", "discovered_cohort", "open"
			goSet:      []string{string(contractsv1.ContextFabricShapeSingleSubject), string(contractsv1.ContextFabricShapeExplicitCohort), string(contractsv1.ContextFabricShapeDiscoveredCohort), string(contractsv1.ContextFabricShapeOpen)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "InterpretedQuestion.requested_judgment_kind",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"InterpretedQuestion.requested_judgment_kind"},
			// schema: "performance", "attention"
			goSet:      []string{string(contractsv1.ContextFabricRequestedJudgmentKindPerformance), string(contractsv1.ContextFabricRequestedJudgmentKindAttention)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "PlanNarrowingStage",
			kind: vocabFrozen,
			why:  "state-machine",
			// Go: context_fabric_answer_plan.go:125 "the CLOSED vocabulary of the three moments a plan may narrow"
			sites: []string{"PlanNarrowingStage"},
			// schema: "cardinality", "synthesis_input", "assembled_result"
			goSet:      []string{string(contractsv1.ContextFabricPlanNarrowingCardinality), string(contractsv1.ContextFabricPlanNarrowingSynthesisInput), string(contractsv1.ContextFabricPlanNarrowingAssembledResult)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "QuestionFamily",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"QuestionFamily"},
			// schema: "subject_investigation", "discovered_cohort_ranking", "scoped_cohort_status", "grouped_cohort_status", "explicit_comparison", "trend", "investment_allocation", "unclassified"
			goSet:      []string{string(contractsv1.ContextFabricQuestionFamilySubjectInvestigation), string(contractsv1.ContextFabricQuestionFamilyDiscoveredCohortRanking), string(contractsv1.ContextFabricQuestionFamilyScopedCohortStatus), string(contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus), string(contractsv1.ContextFabricQuestionFamilyExplicitComparison), string(contractsv1.ContextFabricQuestionFamilyTrend), string(contractsv1.ContextFabricQuestionFamilyInvestmentAllocation), string(contractsv1.ContextFabricQuestionFamilyUnclassified)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "QuestionFamilySource",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"QuestionFamilySource"},
			// schema: "model_consensus", "model", "model_plurality_rejected", "carried", "structure_precedence", "fallback", "none"
			goSet:      []string{string(contractsv1.ContextFabricQuestionFamilySourceModelConsensus), string(contractsv1.ContextFabricQuestionFamilySourceModel), string(contractsv1.ContextFabricQuestionFamilySourcePluralityRejected), string(contractsv1.ContextFabricQuestionFamilySourceCarried), string(contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence), string(contractsv1.ContextFabricQuestionFamilySourceFallback), string(contractsv1.ContextFabricQuestionFamilySourceNone)},
			exhaustive: true,
		},
		outputVocabulary{
			name:           "RelationshipEdge.type",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ValidateStoredResult",
			sites:          []string{"RelationshipEdge.type", "RelationshipProjection.type"},
			// schema: "BELONGS_TO_REPOSITORY", "BELONGS_TO_PULL_REQUEST", "CORRELATED_WITH_INCIDENT", "RELATED_TO", "DOCUMENTED_BY", "HAS_EPISODE", "BLOCKS", "PART_OF", "RELATES_TO", "DUPLICATES", "BELONGS_TO_PROJECT", "OWNED_BY_TEAM", "LINKS_PULL_REQUEST"
			goSet:      []string{string(contractsv1.ContextFabricRelationshipBelongsToRepository), string(contractsv1.ContextFabricRelationshipBelongsToPullRequest), string(contractsv1.ContextFabricRelationshipCorrelatedWithIncident), string(contractsv1.ContextFabricRelationshipRelatedTo), string(contractsv1.ContextFabricRelationshipDocumentedBy), string(contractsv1.ContextFabricRelationshipHasEpisode), string(contractsv1.ContextFabricRelationshipBlocks), string(contractsv1.ContextFabricRelationshipPartOf), string(contractsv1.ContextFabricRelationshipRelatesTo), string(contractsv1.ContextFabricRelationshipDuplicates), string(contractsv1.ContextFabricRelationshipBelongsToProject), string(contractsv1.ContextFabricRelationshipOwnedByTeam), string(contractsv1.ContextFabricRelationshipLinksPullRequest)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "match_mechanisms.items",
			kind: vocabFrozen,
			why:  "scoring",
			// Go: context_fabric_types.go:196 "This is a CLOSED enum -- adding a member is a contract change"
			sites: []string{"SubjectCandidate.match_mechanisms.items"},
			// schema: "exact", "alias", "provider_key", "lexical", "vector", "traversal_parent"
			goSet:      []string{string(contractsv1.ContextFabricMatchExact), string(contractsv1.ContextFabricMatchAlias), string(contractsv1.ContextFabricMatchProviderKey), string(contractsv1.ContextFabricMatchLexical), string(contractsv1.ContextFabricMatchVector), string(contractsv1.ContextFabricMatchTraversalParent)},
			exhaustive: true,
		},
		outputVocabulary{
			name: "WindowConfirmationMode",
			kind: vocabFrozen,
			why:  "request-echo",
			// Go: context_fabric_window_types.go:210 a request option (InvestigationOptions.window_confirmation_mode)
			sites: []string{"WindowConfirmationMode"},
			// schema: "", "headless", "nudge"
			goSet:      []string{"", string(contractsv1.ContextFabricWindowConfirmationHeadless), string(contractsv1.ContextFabricWindowConfirmationNudge)},
			exhaustive: true,
		},
	)
}
