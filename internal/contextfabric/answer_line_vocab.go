package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The closed vocabularies of the answer reuse, commit gate, synthesis status,
// prior-subject receipt, subjectless terminal and stored answerability lines,
// exported so the event specification reads them from the producer's own
// constants.

// AnswerReuseBypassReasonVocabulary is the closed set of reasons a question skipped answer reuse.
func AnswerReuseBypassReasonVocabulary() []string {
	return []string{
		string(AnswerReuseBypassConfirmedStructure), string(AnswerReuseBypassPriorSubjectReceipts), string(AnswerReuseBypassPriorResultReference),
		string(AnswerReuseBypassStatedPeriod), string(AnswerReuseBypassExplicitHandle), string(AnswerReuseBypassSuppliedInterpretation),
		string(AnswerReuseBypassClientSynthesis),
	}
}

// AnswerReuseContainmentDisclosureVocabulary is the closed set of forms a containment disclosure takes.
func AnswerReuseContainmentDisclosureVocabulary() []string {
	return []string{"structured", "reason_only"}
}

// CommitBasisLogVocabulary is the closed set of commit bases a retraction line carries; the empty basis is a graph reader that reported none.
func CommitBasisLogVocabulary() []string {
	return []string{string(CommitBasisUnknown), string(CommitBasisCallerCanonicalID), string(CommitBasisAuthoritativeIdentity), string(CommitBasisStatistical)}
}

// CommitWinningMechanismVocabulary is the closed set of match mechanisms a retraction line carries; the empty mechanism is a candidate that recorded none.
func CommitWinningMechanismVocabulary() []string {
	return []string{
		"", string(contractsv1.ContextFabricMatchExact), string(contractsv1.ContextFabricMatchAlias), string(contractsv1.ContextFabricMatchProviderKey),
		string(contractsv1.ContextFabricMatchLexical), string(contractsv1.ContextFabricMatchVector), string(contractsv1.ContextFabricMatchTraversalParent),
	}
}

// SynthesisStatusOverrideReasonVocabulary is the closed set of reasons the engine served a different status than synthesis returned.
func SynthesisStatusOverrideReasonVocabulary() []string {
	return []string{
		string(SynthesisStatusOverrideClarificationUnavailable), string(SynthesisStatusOverrideClarificationUnavailableUncommitted),
		string(SynthesisStatusOverrideNoMatchOverCohortOutcome), string(SynthesisStatusOverrideNoMatchOverReadFacts),
		string(SynthesisStatusOverrideNoMatchOverMeasuredZero), string(SynthesisStatusOverrideNoMatchKeptUnaskedFactKinds),
	}
}

// InvestigationStatusLogVocabulary is the closed set of investigation statuses a log line carries.
func InvestigationStatusLogVocabulary() []string {
	return []string{
		string(contractsv1.ContextFabricInvestigationComplete), string(contractsv1.ContextFabricInvestigationPartial),
		string(contractsv1.ContextFabricInvestigationDegraded), string(contractsv1.ContextFabricInvestigationClarificationRequired),
		string(contractsv1.ContextFabricInvestigationNoMatch),
	}
}

// PriorSubjectReceiptSkipReasonVocabulary is the closed set of reasons a prior-subject receipt was skipped.
func PriorSubjectReceiptSkipReasonVocabulary() []string {
	return []string{"failed_reauth", "unloadable", "no_match", "stale_graph_epoch"}
}

// SubjectlessTerminalReasonVocabulary is the closed set of reasons a turn reached the subjectless terminal path.
func SubjectlessTerminalReasonVocabulary() []string {
	return []string{
		"frame_gate_refused", declaredKindTerminalReason, "ambiguous", "offer_pool_emptied_by_exclusion", "graph_not_projected",
		"team_inactive", "authz_filtered_to_empty", "empty_pool", unboundScopeAnchorTerminalReason,
	}
}

// AnswerabilityRoleVocabulary is the closed set of roles an answerability line names.
func AnswerabilityRoleVocabulary() []string {
	out := make([]string, 0, len(answerabilityRoles))
	for _, role := range answerabilityRoles {
		out = append(out, string(role))
	}
	return out
}

// AnswerabilityRoleStateVocabulary is the closed set of states an answerability role is in.
func AnswerabilityRoleStateVocabulary() []string {
	out := make([]string, 0, len(answerabilityRoleStates))
	for _, state := range answerabilityRoleStates {
		out = append(out, string(state))
	}
	return out
}

// AnswerabilityChannelVocabulary is the closed set of channels an offer arrives on.
func AnswerabilityChannelVocabulary() []string {
	return []string{
		string(answerabilityChannelKindOption), string(answerabilityChannelAnchorOption), string(answerabilityChannelHandleOption),
		string(answerabilityChannelCandidateOption), string(answerabilityChannelSubjectCandidate),
	}
}

// StoredAnswerabilitySurfaceVocabulary is the closed set of read surfaces a stored answerability line names.
func StoredAnswerabilitySurfaceVocabulary() []string {
	return []string{string(StoredAnswerabilitySurfaceReuse), string(StoredAnswerabilitySurfaceResultByID)}
}

// StoredAnswerabilityDeterminationVocabulary is the closed set of determinations a stored answerability line carries.
func StoredAnswerabilityDeterminationVocabulary() []string {
	return []string{string(StoredAnswerabilityAnswerable), string(StoredAnswerabilityUnanswerable), string(StoredAnswerabilityUnavailable)}
}

// StoredAnswerabilityStepVocabulary is the closed set of steps that decide a stored answerability.
func StoredAnswerabilityStepVocabulary() []string {
	return []string{
		string(StoredAnswerabilityStepNone), string(StoredAnswerabilityStepWindowGate), string(StoredAnswerabilityStepRole),
		string(StoredAnswerabilityStepNoSubjectOffer), string(StoredAnswerabilityStepOfferLess),
	}
}

// StoredAnswerabilityReadingVocabulary is the closed set of readings of the persisted semantic state.
func StoredAnswerabilityReadingVocabulary() []string {
	return []string{
		storedReadingNotRead, storedReadingLoadFailed, storedReadingStoreUnconfigured, storedReadingUnreported,
		string(SemanticStateReadAvailable), string(SemanticStateReadAbsent), string(SemanticStateReadUnsupportedVersion), string(SemanticStateReadMalformed), string(SemanticStateReadOversized),
	}
}
