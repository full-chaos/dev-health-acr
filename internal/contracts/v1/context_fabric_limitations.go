package v1

// The retrieval-degradation limitation lives here, in the contract, rather
// than in the engine that writes it (CHAOS-3746, option (a)).
//
// It is written by internal/contextfabric and read by
// internal/contextfabric/answerprojection, and the projection may not
// import the engine: answerprojection is import-pure by constraint --
// standard library and this package, nothing else -- so that both the
// hosted API and the MCP sidecar can call it. TestPackageImportsStayPure
// enforces that, which leaves exactly two options for a string both sides
// must recognise: restate it on the read side, or move it to the contract
// they already share. Restating it is the anchor-drift class this codebase
// keeps closing; the string is part of what an answer MEANS, not an
// implementation detail of how one gets composed.

// ContextFabricRetrievalDegradedLimitation is the fixed, non-interpolated
// limitation an investigation carries when a retrieval mechanism was
// unavailable while the answer was produced.
//
// It names no mechanism, no provider, no model, and no error text. A
// limitation is answer-facing prose, and every cause -- an embed timeout,
// an unreachable embedder, a server that served the wrong model, a
// fenced-off stale index -- has the same consequence for a reader:
// retrieval saw less than it should have. Operator-facing detail belongs in
// telemetry, which already receives it.
//
// The phrasing describes the ANSWER'S PROVENANCE ("when this answer was
// produced"), not the current request, and that is load-bearing rather than
// stylistic: a REUSED answer carries this limitation forward verbatim from
// the run that produced it, and the earlier wording pointed ambiguously at
// the current request in exactly that case.
const ContextFabricRetrievalDegradedLimitation = "One retrieval mechanism was unavailable when this answer was produced, so fewer candidate subjects may have been considered than usual."

// ContextFabricRetrievalDegradedLimitationLegacy is the wording used before
// the phrasing above replaced it.
//
// BOTH STRINGS EXIST IN THE WILD, permanently. A
// ContextFabricInvestigationResult is immutable and CHAOS-3782's answer
// reuse keys on its stored bytes, so results written before the change keep
// this spelling verbatim -- nothing rewrites a stored row, and nothing may
// treat one as malformed.
const ContextFabricRetrievalDegradedLimitationLegacy = "One retrieval mechanism was unavailable for this investigation, so fewer candidate subjects may have been considered than usual."

// IsContextFabricRetrievalDegradedLimitation reports whether a limitation
// string is either spelling.
//
// It exists so no caller compares against ONE constant and silently stops
// recognising answers written by the other.
func IsContextFabricRetrievalDegradedLimitation(limitation string) bool {
	return limitation == ContextFabricRetrievalDegradedLimitation ||
		limitation == ContextFabricRetrievalDegradedLimitationLegacy
}

// HasContextFabricRetrievalDegradedLimitation reports whether any entry is either
// spelling. Declared here beside the strings it scans for, so the contract
// can enforce LimitationsDisplaced's coherence rule without depending on
// the engine that writes it.
func HasContextFabricRetrievalDegradedLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if IsContextFabricRetrievalDegradedLimitation(limitation) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The rest of the service-authored disclosure vocabulary (CHAOS-4098)
// ---------------------------------------------------------------------------
//
// These three moved here for the SAME reason the retrieval-degradation
// string above did, applied to a second reader: LimitationsDisplaced's
// coherence rule (validate_context_fabric_result.go) has to know which
// entries in a limitation list are service-authored, and the contract may
// not import the engine that writes them.
//
// WHY THIS MATTERS RATHER THAN BEING TIDINESS. That rule was written when
// retrieval degradation was the ONLY thing that could displace a model
// caveat, so it asks for that one disclosure by name. Three more displacers
// shipped afterwards -- CHAOS-3781's standing historical disclosures,
// CHAOS-4085's commit retraction, and CHAOS-4098's clarification override --
// and none of them updated the rule. Every one of them therefore produces a
// result the validator REJECTS whenever the model returned a full
// limitation list: the displacement is recorded, the disclosure that caused
// it is not the one the rule looks for, and the whole investigation fails
// with ErrInvalidResult. That is the exact defect class CHAOS-3746's
// displacement mechanism was built to prevent, reintroduced by a rule that
// enumerates instead of deriving.
//
// So the rule now asks whether ANY service-authored disclosure is present,
// and this list is the single place that answers it. A fifth disclosure is
// covered by being declared here, not by someone remembering to revisit a
// validator.

// ContextFabricTemporalProjectionLimitation is CHAOS-3781's standing
// historical disclosure: the graph holds only the current projection, so a
// subject deleted at source since the requested time is simply gone.
const ContextFabricTemporalProjectionLimitation = "Subjects deleted at source since the requested time are not recoverable from the projected graph."

// ContextFabricObservedTimeLimitation is CHAOS-3781's observed-time
// substitution disclosure: the caller asked what was KNOWN at a past
// instant and the graph answered from what was TRUE then.
const ContextFabricObservedTimeLimitation = "Observed-time questions cannot be answered on their own terms: no canonical source retains observation history, so this answer reflects what was TRUE at the requested time, not what was KNOWN then."

// ContextFabricCommitRetractionLimitation is CHAOS-4085's disclosure that
// the commit gate identified a candidate subject and declined to stand
// behind it.
const ContextFabricCommitRetractionLimitation = "A candidate subject was identified but not committed: the evidence assembled for it does not support naming it as the answer to this question."

// ContextFabricSynthesisClarificationUnavailableLimitation is CHAOS-4098's
// disclosure that the synthesis step declined to conclude on a path with no
// clarification to offer.
const ContextFabricSynthesisClarificationUnavailableLimitation = "This question could not be answered from the evidence assembled, and no clarification could be offered to narrow it further."

// ContextFabricFactScopeUnexpandedLimitation is CHAOS-4099's disclosure that
// a requested fact family could not be reached from the subject this answer
// is about.
//
// WHY THIS STRING EXISTS AT ALL. Before it, that situation was reported to
// the reader as nothing whatsoever: the fact planner pruned the capability,
// SourcePruned is documented as a PROOF that nothing is missing, and the
// answer went out saying it had found no match -- with no indication that
// the evidence had never been looked for. A project-scoped question about
// pull requests, reviews or metrics is the live case (see
// internal/contextfabric/fact_scope.go's header), and the reader could not
// tell it apart from a project that genuinely has none.
//
// FIXED AND NON-INTERPOLATED, the same discipline every disclosure above
// holds. It names no fact family, no subject kind, no policy and no
// traversal: those are operator-facing detail, and telemetry
// (RecordFactScopeExpansion) receives all of them. What a READER needs is
// the one thing that changes how they should read the answer -- that its
// silence on some evidence is a gap in reach, not a finding of absence.
const ContextFabricFactScopeUnexpandedLimitation = "Some requested evidence could not be reached from the subject of this question, so this answer's silence on it is a limit of what was retrievable rather than a finding that none exists."

// ContextFabricFactScopeActivityProxyLimitation is CHAOS-4099's disclosure
// that some evidence in this answer was gathered by ACTIVITY association
// rather than by ownership.
//
// WHY A SECOND STRING RATHER THAN REUSING THE ONE ABOVE. They say opposite
// things. The unexpanded disclosure says "we could not reach some evidence";
// this one says "we DID reach evidence, by a route that is weaker than it
// looks". A reader who is told only the first would take everything present
// in the answer at face value, which is exactly the misreading this exists
// to prevent.
//
// THE MISREADING IT PREVENTS, CONCRETELY. A "project" here is a
// work-tracking project (Linear-shaped). There is no project-to-repository
// ownership edge in the projected graph. What the traversal establishes is
// that a repository has at least one work item linked to the project -- good
// enough to scope "how is this project's code doing", and NOT a statement
// that the project owns the repository or that its repositories are only
// these. Without this sentence a reader takes a proxy for a roster.
//
// FIXED and non-interpolated like every disclosure here: it names no policy,
// no subject kind and no traversal. Operators get all of that on the
// RecordFactScopeExpansion telemetry stream.
const ContextFabricFactScopeActivityProxyLimitation = "Some evidence in this answer was gathered by association rather than by ownership, so it reflects where related activity was found and may not be the complete or authoritative set."

// ContextFabricFactScopeAttributedPrimaryTeamLimitation is CHAOS-4101's
// disclosure that some evidence in this answer was reached via a team's
// PRIMARY WORK-ITEM ATTRIBUTION -- a computed assignment, not a fact any
// provider asserted.
//
// A THIRD, INDEPENDENT DISCLOSURE, alongside (never instead of) the
// unexpanded and activity-proxy limitations above. All three can be true of
// one answer at once: a team-scoped question can reach some evidence
// directly, some through the activity-proxy chain on solid team footing, and
// some through this weaker footing, while a DIFFERENT requirement on the
// same subject hits a policy that has never been activated at all.
//
// WHY A SEPARATE STRING FROM THE ACTIVITY-PROXY ONE. That disclosure says
// "we reached this by association, not ownership" -- true of every
// team-origin target, since the chain is a work_item BELONGS_TO_REPOSITORY
// hop exactly like the project chain's. This one says something further:
// which TEAM the evidence is associated with is itself Ops' own computed
// guess for some of that evidence, not a claim any provider made. A reader
// told only the first would still read "this team's repositories" as a
// settled fact about team ownership of the association; this sentence is
// what corrects that for the subset of evidence where it applies.
//
// FIXED and non-interpolated, the same discipline every disclosure in this
// file holds: it names no source enum value, no confidence level, no team
// and no policy. RecordFactScopeExpansion's AttributionSourceCounts carries
// the closed-vocabulary source breakdown for an operator; this sentence
// carries only the one thing a reader needs -- that some of what is here was
// attributed by inference, not asserted.
const ContextFabricFactScopeAttributedPrimaryTeamLimitation = "Some evidence in this answer was associated with its team by a computed attribution rather than one directly asserted by a data source, so that association may be imprecise."

// ContextFabricServiceAuthoredLimitations returns every disclosure this
// service composes for itself, in no significant order.
//
// A NEW DISCLOSURE BELONGS IN THIS LIST. Everything that reasons about
// "did the service write this, or did the model?" derives from here --
// the engine's displacement rule (which never displaces a service
// disclosure) and the validator's coherence rule (which accepts a positive
// displaced count only when one is present).
func ContextFabricServiceAuthoredLimitations() []string {
	return []string{
		ContextFabricRetrievalDegradedLimitation,
		ContextFabricRetrievalDegradedLimitationLegacy,
		ContextFabricTemporalProjectionLimitation,
		ContextFabricObservedTimeLimitation,
		ContextFabricCommitRetractionLimitation,
		ContextFabricSynthesisClarificationUnavailableLimitation,
		ContextFabricFactScopeUnexpandedLimitation,
		ContextFabricFactScopeActivityProxyLimitation,
		ContextFabricFactScopeAttributedPrimaryTeamLimitation,
	}
}

// IsContextFabricServiceAuthoredLimitation reports whether one limitation
// is a disclosure this service composes rather than a model caveat.
func IsContextFabricServiceAuthoredLimitation(limitation string) bool {
	for _, disclosure := range ContextFabricServiceAuthoredLimitations() {
		if limitation == disclosure {
			return true
		}
	}
	return false
}

// HasContextFabricServiceAuthoredLimitation reports whether any entry is
// one. This is LimitationsDisplaced's coherence oracle: a displacement only
// ever happens to force a service disclosure into a list that was already
// full, so a positive count requires one to be present.
func HasContextFabricServiceAuthoredLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if IsContextFabricServiceAuthoredLimitation(limitation) {
			return true
		}
	}
	return false
}
