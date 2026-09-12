package v1

import "strings"

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

// contextFabricGroupingRefusalLimitationPrefix, -Middle and -Suffix are the
// three FIXED segments of the grouping-refusal disclosure. It is the one
// disclosure in this file that is INTERPOLATED, and that is chris's own
// ruling rather than an oversight: a reader told only "the answer is
// presented ungrouped" cannot tell whether the axis they asked for was
// missing or merely different, so the sentence names both kinds. Both are
// closed-vocabulary subject kinds, so it still carries no model text and no
// corpus content -- which is the property the "fixed and non-interpolated"
// discipline elsewhere in this file exists to guarantee, reached by a
// different route.
//
// THE PREFIX IS SHARED with the unplaceable disclosure below, deliberately:
// both are answers to "you asked for a breakdown and did not get one", and a
// reader meeting either should recognise the same opening. What must NOT be
// shared is recognition -- see contextFabricGroupingUnplaceableLimitationSuffix
// for why the two families are kept disjoint by their tails rather than their
// heads, and grouping_limitation_families_test.go for the assertion that they
// never accept each other's sentence.
const (
	contextFabricGroupingRefusalLimitationPrefix = "This question asked for a breakdown by "
	contextFabricGroupingRefusalLimitationMiddle = ", but the available facts group by "
	contextFabricGroupingRefusalLimitationSuffix = ", so the answer is presented ungrouped."
)

// contextFabricGroupingUnplaceableLimitationSuffix is the whole tail of the
// SECOND grouping disclosure: the plan declared a group axis and not one
// member of the answer could be placed on it, because no member's own facts
// named a group at all.
//
// A DIFFERENT DEFECT FROM THE MISMATCH ABOVE, and the reader is owed the
// difference. A mismatch means the source disagreed -- it grouped by
// something, just not the asked-for thing, and naming the two kinds tells the
// reader where to look instead. This one means the source was SILENT: there
// is no other axis to point at, so a sentence naming a second kind would have
// to invent one. Hence ONE interpolated value, not two.
//
// ONE INTERPOLATION, NOT TWO, AND NO COUNT. The number of unplaced members
// rides the telemetry line and CohortGroupingOutcome instead. Interpolating it
// here would force the recogniser below to accept an arbitrary digit run in
// the middle of a service-authored sentence, which widens what a model-authored
// caveat can impersonate -- and undisplaceability is exactly what the registry
// grants. A closed-vocabulary subject kind is a set the model never writes
// into; an integer is not.
//
// WHY THE TAIL AND NOT THE HEAD DISCRIMINATES. This family shares its opening
// with the mismatch family, and both end in the same six words, so neither
// segment alone separates them. The whole tail from ", but" onward is one
// constant precisely so that CutSuffix is a decision: the mismatch parse cuts
// its own shorter suffix and then fails to find its Middle, and this parse
// fails CutSuffix outright on a mismatch sentence. Both directions are pinned.
const contextFabricGroupingUnplaceableLimitationSuffix = ", but none of the items in this answer could be placed under one, so the answer is presented ungrouped."

// ContextFabricGroupingRefusalLimitation composes CHAOS-4636's disclosure
// that a grouped question was answered ungrouped because the planned group
// kind and the fact source's own kind disagree.
//
// THE SOLE COMPOSER, and that matters more here than for a constant. Because
// this string is interpolated it cannot be recognised by the equality check
// every other disclosure uses, so recognition is a PARSE
// (IsContextFabricGroupingRefusalLimitation) over exactly the segments this
// function writes. A second, hand-rolled Sprintf at a call site would produce
// a string the parser might not accept -- and an unrecognised service
// disclosure is silently displaceable, which is the round-3 finding.
func ContextFabricGroupingRefusalLimitation(plannedKind, sourceKind ContextFabricSubjectKind) string {
	return contextFabricGroupingRefusalLimitationPrefix + string(plannedKind) +
		contextFabricGroupingRefusalLimitationMiddle + string(sourceKind) +
		contextFabricGroupingRefusalLimitationSuffix
}

// IsContextFabricGroupingRefusalLimitation reports whether a limitation is
// one ContextFabricGroupingRefusalLimitation could have composed.
//
// WHY A PARSE AND NOT A PREFIX MATCH. Everything that consults the
// service-authored registry is deciding whether a string may be DISPLACED to
// make room for another disclosure. A loose match would let a model-authored
// caveat that merely opens with this wording become undisplaceable and take a
// real caveat's place; a prefix match would do exactly that. So both
// interpolated segments must be members of the CLOSED subject-kind registry,
// which is a set the model never writes into. The empty kind is not a member,
// so a half-composed sentence is not recognised either.
func IsContextFabricGroupingRefusalLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupingRefusalLimitationPrefix)
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, contextFabricGroupingRefusalLimitationSuffix)
	if !ok {
		return false
	}
	plannedKind, sourceKind, ok := strings.Cut(body, contextFabricGroupingRefusalLimitationMiddle)
	if !ok {
		return false
	}
	// Exactly one separator. A second occurrence would mean the kinds are
	// not what Cut returned, and guessing which split was intended is
	// precisely the ambiguity a closed vocabulary lets us refuse instead.
	if strings.Contains(sourceKind, contextFabricGroupingRefusalLimitationMiddle) {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(plannedKind)) &&
		ValidContextFabricSubjectKind(ContextFabricSubjectKind(sourceKind))
}

// ContextFabricGroupingUnplaceableLimitation composes the disclosure that a
// grouped question was answered ungrouped because NOT ONE member could be
// placed on the planned axis.
//
// THE SOLE COMPOSER, for the reason its sibling above states at length: an
// interpolated disclosure is recognised by a PARSE, so a second hand-rolled
// Sprintf at a call site produces a string the parser may reject, and an
// unrecognised service disclosure is silently displaceable by the next
// composer. That is not a hypothetical here -- it is the shipped defect this
// file's own registry comment records.
func ContextFabricGroupingUnplaceableLimitation(plannedKind ContextFabricSubjectKind) string {
	return contextFabricGroupingRefusalLimitationPrefix + string(plannedKind) +
		contextFabricGroupingUnplaceableLimitationSuffix
}

// IsContextFabricGroupingUnplaceableLimitation reports whether a limitation
// is one ContextFabricGroupingUnplaceableLimitation could have composed.
//
// A PARSE, not a prefix match, and for the sharper of the two reasons its
// sibling gives. The loose-match hazard is the same -- a model caveat opening
// with this wording would become undisplaceable and take a real caveat's place
// -- but there is a second hazard here that the mismatch family did not have:
// the two grouping families SHARE their opening words and their closing six.
// A prefix match would therefore not merely admit model text, it would make
// the two service families indistinguishable from each other, so a mismatch
// sentence would read as an unplaceable one and vice versa. Requiring the
// whole tail is what keeps them disjoint.
//
// The empty kind is not a vocabulary member, so a half-composed sentence is
// not recognised either.
func IsContextFabricGroupingUnplaceableLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupingRefusalLimitationPrefix)
	if !ok {
		return false
	}
	plannedKind, ok := strings.CutSuffix(body, contextFabricGroupingUnplaceableLimitationSuffix)
	if !ok {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(plannedKind))
}

// contextFabricGroupReadUnreadLimitationPrefix/-Suffix and
// contextFabricGroupListOverBoundLimitationPrefix/-Suffix are the fixed
// segments of the two group-read disclosures: a grouped answer some of whose
// groups could not be read, and a grouped question whose group list was larger
// than an answer can carry.
//
// KIND-ONLY AND COUNT-FREE, like the grouping disclosures above: the one
// interpolated value is a member of the closed subject-kind vocabulary the
// model never writes into, so recognition is a parse that cannot admit model
// text. Neither sentence shares an opening with the grouping families, so
// no service family can be read as another.
const (
	contextFabricGroupReadUnreadLimitationPrefix    = "Not every "
	contextFabricGroupReadUnreadLimitationSuffix    = " group in this answer could be read, so what it says about those groups rests on their members' evidence alone."
	contextFabricGroupListOverBoundLimitationPrefix = "This question asked for more "
	contextFabricGroupListOverBoundLimitationSuffix = " groups than an answer can carry, so the answer is presented ungrouped."
)

// ContextFabricGroupReadUnreadLimitation composes the disclosure that at least
// one group of a grouped answer was not read: denied by authorization, read
// with nothing returned for it, or not read at all because the group read
// failed, could not be authorized, or could not be composed with the member
// read. THE SOLE COMPOSER: recognition is a parse over exactly these segments.
func ContextFabricGroupReadUnreadLimitation(groupKind ContextFabricSubjectKind) string {
	return contextFabricGroupReadUnreadLimitationPrefix + string(groupKind) + contextFabricGroupReadUnreadLimitationSuffix
}

// IsContextFabricGroupReadUnreadLimitation reports whether a limitation is one
// ContextFabricGroupReadUnreadLimitation could have composed. A parse, not a
// prefix match, and the kind must be a vocabulary member, so neither a model
// caveat opening with these words nor a half-composed sentence is recognised.
func IsContextFabricGroupReadUnreadLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupReadUnreadLimitationPrefix)
	if !ok {
		return false
	}
	kind, ok := strings.CutSuffix(body, contextFabricGroupReadUnreadLimitationSuffix)
	if !ok {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(kind))
}

// ContextFabricGroupListOverBoundLimitation composes the disclosure that a
// grouped question proposed more groups than the contract's group bound, so
// its group axis was refused before any group was read and the answer is
// presented ungrouped. THE SOLE COMPOSER, for the same reason.
func ContextFabricGroupListOverBoundLimitation(groupKind ContextFabricSubjectKind) string {
	return contextFabricGroupListOverBoundLimitationPrefix + string(groupKind) + contextFabricGroupListOverBoundLimitationSuffix
}

// IsContextFabricGroupListOverBoundLimitation reports whether a limitation is
// one ContextFabricGroupListOverBoundLimitation could have composed.
func IsContextFabricGroupListOverBoundLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupListOverBoundLimitationPrefix)
	if !ok {
		return false
	}
	kind, ok := strings.CutSuffix(body, contextFabricGroupListOverBoundLimitationSuffix)
	if !ok {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(kind))
}

// contextFabricRefusalBasisLimitationPrefix/-Middle/-Suffix are the three
// FIXED segments of CHAOS-5442's frame-refusal disclosure, and
// contextFabricFrameInvariantRefusalLimitation is the whole sentence for the
// refusal that has no kind to name.
//
// INTERPOLATED, under the same ruling the grouping-refusal disclosure above
// records: both interpolated values are members of CLOSED vocabularies the
// model never writes into (a subject kind and a refusal basis), so the
// sentence still carries no model text and no corpus content -- the property
// the "fixed and non-interpolated" discipline exists to guarantee, reached by
// the same different route.
//
// WHY IT NAMES THE BASIS TOKEN and not only the kind. The reader of a
// refusal has to be able to join the sentence to the machine field beside it
// (Completeness.RefusalBasis) and to the Info line the server logged, and the
// only thing all three can share is the token. A sentence that described the
// refusal in prose alone would leave the operator correlating an English
// phrase against a snake_case field, which is exactly the translation step
// that makes a disclosure go unread.
//
// WHY THE KIND IS IN THE SENTENCE AND NOT IN THE FIELD. The declared kind is
// what makes the refusal ACTIONABLE -- it is the one thing the asker can
// change about their question. Putting it in the closed field would have
// meant either a cross-product vocabulary (one member per basis-and-kind
// pair) or a second field, and the sentence is the honest home for a value
// that varies per question rather than per decision.
const (
	contextFabricRefusalBasisLimitationPrefix = "This question asked about a population of "
	contextFabricRefusalBasisLimitationMiddle = ", which this service has no way to enumerate, so it was not searched and no canonical facts were read. The server refused this question's frame on the basis "
	contextFabricRefusalBasisLimitationSuffix = "."
)

// ContextFabricFrameInvariantRefusalLimitation is the fixed disclosure for a
// frame refused because it VIOLATED AN INVARIANT rather than because it named
// an unservable population.
//
// FIXED, not interpolated, because there is nothing safe to interpolate: the
// only value that would distinguish one instance from another is the failed
// invariant's name, and that vocabulary is a server-internal validation
// detail -- see ContextFabricRefusalBasisFrameInvariantViolated for why it is
// deliberately not promoted to the wire. The operator reads which invariant
// failed on the frame-validation log line.
const ContextFabricFrameInvariantRefusalLimitation = "The server could not act on this question as stated, because the way it combines its subject and its grouping is not answerable as asked, so no canonical facts were read. Rephrasing the question so it asks for one thing at a time may answer it."

// ContextFabricRefusalBasisLimitation composes the disclosure that a
// question was refused because its declared member kind has no discovery arm.
//
// THE SOLE COMPOSER, for the reason its grouping-refusal sibling states at
// length: an interpolated string cannot be recognised by the equality check
// the fixed disclosures use, so recognition is a PARSE over exactly the
// segments this function writes, and a second hand-rolled Sprintf at a call
// site would produce a string the parser might not accept. An unrecognised
// service disclosure is silently displaceable.
func ContextFabricRefusalBasisLimitation(declaredKind ContextFabricSubjectKind, basis ContextFabricRefusalBasis) string {
	return contextFabricRefusalBasisLimitationPrefix + string(declaredKind) +
		contextFabricRefusalBasisLimitationMiddle + string(basis) +
		contextFabricRefusalBasisLimitationSuffix
}

// IsContextFabricRefusalBasisLimitation reports whether a limitation is one
// ContextFabricRefusalBasisLimitation could have composed.
//
// A PARSE, AND BOTH INTERPOLATED SEGMENTS ARE CHECKED FOR MEMBERSHIP, for the
// reason the grouping recogniser states: everything that consults the
// service-authored registry is deciding whether a string may be DISPLACED,
// and a loose or prefix match would let a model-authored caveat that merely
// opens with this wording become undisplaceable and take a real caveat's
// place. The empty kind and the empty basis are not members, so a
// half-composed sentence is not recognised either.
func IsContextFabricRefusalBasisLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricRefusalBasisLimitationPrefix)
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, contextFabricRefusalBasisLimitationSuffix)
	if !ok {
		return false
	}
	declaredKind, basis, ok := strings.Cut(body, contextFabricRefusalBasisLimitationMiddle)
	if !ok {
		return false
	}
	// Exactly one separator, the same refusal the grouping parse makes:
	// a second occurrence means the two values are not what Cut returned,
	// and guessing which split was intended is precisely the ambiguity a
	// closed vocabulary lets us decline instead.
	if strings.Contains(basis, contextFabricRefusalBasisLimitationMiddle) {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(declaredKind)) &&
		ValidContextFabricFrameRefusalBasis(ContextFabricRefusalBasis(basis))
}

// ContextFabricContinuationContextUnverifiableLimitation is the fixed
// disclosure for a window-only continuation refused because the prior turn's
// semantic context could not be verified
// (ContextFabricRefusalBasisContinuationContextUnverifiable).
//
// FIXED, not interpolated. The only values that would tell one instance from
// another are the prior result's id and the internal reason the carrier failed
// admission, and neither belongs in prose: the id is the caller's own
// reference, and the reason vocabulary is server-internal. Both are on the
// Info decision line an operator reads (referenced_result_id and
// decision_reason, with carrier_read separating an unreadable carrier from one
// read and proved invalid).
//
// IT NAMES THE BASIS TOKEN for the reason the member-kind sentence does: a
// reader joins the sentence to the machine field and to the log line through
// the one value all three share.
const ContextFabricContinuationContextUnverifiableLimitation = "This request continued an earlier answer by confirming only its evidence window, and the server could not verify the earlier answer's reading of the question, so it did not answer under a different reading and no canonical facts were read. Ask the question again without the earlier answer's window offer to start a fresh investigation. The server refused this continuation on the basis continuation_context_unverifiable."

// ContextFabricDeclaredKindUnmatchedLimitation is the sentence a turn carries
// when its frame declared a subject kind and nothing retrieval could offer
// carried that kind.
//
// It names the KIND and it names no subject, because there is no subject to
// name: every candidate retrieval found was of some other kind, and naming one
// of those would hand back the wrong-kind guess the whole decision exists to
// withhold. It tells the asker the one thing they can act on -- the kind the
// question was read as being about -- so they can rename the subject or say
// they meant a different kind.
const ContextFabricDeclaredKindUnmatchedLimitation = "This question was read as being about a subject of a particular kind, and nothing that matched the terms it named was of that kind, so no subject was confirmed and no canonical facts were read. Naming the subject differently, or saying which kind of thing it is, may answer it. The server refused this question on the basis declared_kind_unmatched."

// ContextFabricServiceAuthoredLimitations returns every disclosure this
// service composes for itself, in no significant order.
//
// A NEW FIXED DISCLOSURE BELONGS IN THIS LIST. Everything that reasons
// about "did the service write this, or did the model?" asks
// IsContextFabricServiceAuthoredLimitation -- the engine's displacement
// rule (which never displaces a service disclosure) and the validator's
// coherence rule (which accepts a positive displaced count only when one is
// present) -- and that predicate is this list PLUS the interpolated
// grouping-refusal family, which no list of constants can hold.
//
// A NEW INTERPOLATED DISCLOSURE therefore needs its own composer, its own
// parse-based recogniser, and a line in that predicate. It is more work than
// adding a constant on purpose: the round-3 finding was an interpolated
// disclosure that nobody could recognise, so it was displaced by the next
// composer and the served answer carried no statement that the question had
// been answered on a different axis than it asked for.
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
		ContextFabricFrameInvariantRefusalLimitation,
		ContextFabricContinuationContextUnverifiableLimitation,
		ContextFabricDeclaredKindUnmatchedLimitation,
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
	// The two grouping disclosures are interpolated, so they can only be
	// recognised by parsing them -- see
	// IsContextFabricGroupingRefusalLimitation and
	// IsContextFabricGroupingUnplaceableLimitation. This predicate, NOT the
	// fixed list above, is the authority every consumer asks; the list
	// remains what it says it is (the fixed disclosures) and
	// ContextFabricServiceAuthoredLimitations' own callers use it only to
	// enumerate those.
	//
	// EVERY MEMBER OF THE GROUPING VOCABULARY THAT DISCLOSES NEEDS A LINE
	// HERE. Omitting one does not fail loudly: the sentence is composed, is
	// classified as a model caveat, and is displaced by the composer that runs
	// after it, so the served answer states nothing about having been answered
	// on a different axis than the one asked for. That is the shipped round-3
	// defect, and it is invisible to any test that drives the composer.
	return IsContextFabricGroupingRefusalLimitation(limitation) ||
		IsContextFabricGroupingUnplaceableLimitation(limitation) ||
		IsContextFabricRefusalBasisLimitation(limitation) ||
		IsContextFabricGroupReadUnreadLimitation(limitation) ||
		IsContextFabricGroupListOverBoundLimitation(limitation)
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
