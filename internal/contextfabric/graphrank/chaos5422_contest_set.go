package graphrank

import (
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// THE CONTEST SET: which candidates this question is allowed to resolve over,
// decided ONCE, at the boundary where a candidate is admitted at all.
//
// CHAOS-5422. A children_of_scope question declares the KIND of the members it
// wants and the TERMS of the anchor those members hang off. The subject it must
// commit is the ANCHOR, and phase-B invariant I11 states the asymmetry: the
// resolved anchor's kind is never the member kind. I11 was declared
// (frame_invariants.go) and enforced nowhere, and on the live rig that gap cost
// a substituted subject -- a team the question merely MENTIONED was offered as
// the subject, the caller redeemed the offer's receipt, and the next turn
// committed the member as the answer's subject on caller_canonical_id, the
// engine crediting the caller with an identifier the engine itself had minted.
//
// WHY THE BOUNDARY, AND NOT A GUARD PER GATE. A first attempt enforced this
// with a conjunct at each commit tier. Four adversarial review rounds then found
// the same class four times, because a per-gate rule is only as complete as the
// list of gates someone remembered:
//
//   - a candidate removed from the commit index still consumed an OFFER SLOT,
//     because removal happened after ranking, reservation and truncation -- so a
//     withheld member could take the last slot and the retrieved anchor was
//     never offered at all, while the clarification prompt told the caller
//     retrieval had matched only member-kind subjects, which was false;
//   - a candidate removed from the commit index still VETOED an anchor, because
//     its claims stayed in identityClaimants/identityMatchTerms and reached
//     identityCrossClassRivalClaimant untouched -- so the member decided the
//     outcome through a side channel while the code that was supposed to have
//     removed it read as correct.
//
// Both were measured, not argued. The lesson is the same one twice: "removed
// from the contest" is a property of the SET, so it has to be true where the set
// is built. Every consumer downstream -- ranking, the identity claimants, the
// offer pool, the census rescue, the clarification prompt, every commit tier --
// then reads the filtered set because there is nothing else to read, and a gate
// added later inherits the property instead of needing to remember it.
//
// THE SIGNAL IS THE GROUPING/SCOPE AXIS, NOT THE QUESTION'S TEXT. The decision
// reads SubjectExpression.Kind, the declared MemberKind and the confirmed kind
// -- closed-vocabulary fields a model filled in deliberately and the server
// validated -- and nothing else. No term, no label and no prose reaches it,
// which is what keeps it from becoming a keyword table deciding structure.
type contestScope struct {
	// MemberKind is the declared member kind, and the ONLY kind this scope ever
	// refuses. Empty means this question refuses nothing, which is every
	// question that is not scope-anchored -- the overwhelming common case, and
	// byte-identical to the pre-ticket behaviour.
	MemberKind contextfabric.SubjectKind
	// Source names what decided it, from the closed vocabulary below. Carried
	// rather than implied so a second source added later cannot arrive
	// unlabelled, and so an operator can tell "no scope axis" apart from "a
	// build that stopped deciding" on one line.
	Source string
}

const (
	// contestScopeNone: this question refuses no kind.
	contestScopeNone = "none"
	// contestScopeFrameMemberKind: the frame's own children_of_scope member
	// kind. The only source today.
	contestScopeFrameMemberKind = "frame_member_kind"
)

// contestSetDisposition is the per-candidate offer_pool disposition token for a
// refusal, spelled once here so the emitter and every reader name one string
// rather than two equal literals.
const contestSetDisposition = "anchor_kind_withheld"

// decideContestScope is the whole decision, TOTAL and PURE over a possibly-nil
// frame.
//
// ONLY children_of_scope refuses, and the four exclusions are load-bearing
// rather than an oversight -- each names a variant whose declared member kind IS
// a kind it may legitimately commit:
//
//   - named_subject: a named subject is not a member of anything, it IS the
//     subject, and its declared kind lives on ExpectedKind. Refusing it would
//     empty the pool for the most common question the product answers.
//   - discovered_kind: its members ARE the subjects it commits.
//   - grouped_members: both axes are cohort axes; nothing here resolves an
//     anchor, so there is no I11 asymmetry to enforce.
//   - explicit_set: its operands' subjects come from resolution itself, so a
//     kind refused here would refuse the operands the question named.
//
// A scope-anchored frame that declares NO member kind decides nothing either:
// with no declared member kind there is no kind I11 excludes, and guessing one
// would be the keyword table this seam exists to avoid.
//
// AND IT IS GATED ON THE CONFIRMED MEMBER KIND. The harm is CREATED by
// narrowing the subject pool to the confirmed member kind: that is what drops
// the anchor the terms named and leaves a pool in which every survivor is an
// I11 violation by construction. With no confirmed kind that narrowing is a
// no-op, the anchor and the members are in the pool together, and the contest
// between them is a real one -- an existing CHAOS-5393 pin holds that a member
// committing there is accepted behaviour, and refusing it would take a served
// answer away to fix a defect that turn does not have. A confirmed kind that is
// NOT the member kind decides nothing either: the narrowing then went to THAT
// kind, so its survivors are not member-kind candidates.
func decideContestScope(frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind, anchorScope anchorPoolKindScope) contestScope {
	if frame == nil {
		return contestScope{Source: contestScopeNone}
	}
	if frame.SubjectExpression.Kind != contextfabric.SubjectExpressionChildrenOfScope {
		return contestScope{Source: contestScopeNone}
	}
	kind, ok := frame.SubjectExpression.MemberKind()
	if !ok || kind == "" {
		return contestScope{Source: contestScopeNone}
	}
	if confirmedKind == nil || confirmedKind.Kind != kind {
		return contestScope{Source: contestScopeNone}
	}
	// DEFENSIVE, and stated rather than assumed. ScopeAnchorRetrievalKind
	// already refuses an anchor kind equal to the member kind, so this cannot
	// fire today -- but the anchor scope and this one would then be making
	// opposite claims about the same kind, admitting it to the pool while
	// refusing it the contest, and a future widening of either must not be able
	// to reach that state silently.
	if anchorScope.admits(kind) {
		return contestScope{Source: contestScopeNone}
	}
	return contestScope{MemberKind: kind, Source: contestScopeFrameMemberKind}
}

// refuses reports whether a candidate of kind is outside this question's
// contest set.
//
// The empty kind is REFUSED EXPLICITLY rather than by comparison. A candidate
// whose kind failed to resolve carries the zero value, and a zero-value scope
// carries it too -- so a bare `kind == s.MemberKind` would refuse that whole
// class for every question in the product through a comparison nobody wrote
// deliberately. The same "an absent value is not a match" discipline
// anchorPoolKindScope.admits already applies to its own zero.
func (s contestScope) refuses(kind contextfabric.SubjectKind) bool {
	return s.MemberKind != "" && kind == s.MemberKind
}

// observable renders the pair the decision line carries. Both halves are
// explicit tokens on every pass -- never "" -- so a question that refused
// nothing and a build that stopped deciding can never read alike.
func (s contestScope) observable() (kind string, source string) {
	if s.MemberKind == "" {
		return contestScopeNone, contestScopeNone
	}
	if s.Source == "" {
		return string(s.MemberKind), contestScopeNone
	}
	return string(s.MemberKind), s.Source
}

// contestAdmission is the ONE value the candidate-set boundary consults, and
// the one place the refusals are recorded.
//
// THE WITHHELD SET IS A MAP KEYED BY SubjectKey, DELIBERATELY. The count this
// resolution discloses is "how many DISTINCT subjects were refused", and a map
// makes that true by construction rather than by a counter someone has to keep
// correct. Two earlier attempts at that counter were both wrong in opposite
// directions -- summing per resolver pass over-counted one subject refused
// twice, and taking the last pass's value under-counted when two passes refused
// DIFFERENT subjects (the confirmed-kind re-decision resolves over a freshly
// built pool, so the populations genuinely differ). Neither arithmetic can
// express "distinct across the call"; a set can, and it cannot drift from the
// dispositions it summarises because it IS them.
//
// The zero value refuses nothing and records nothing, which is the honest
// reading for a caller that never ran interpretation.
type contestAdmission struct {
	scope contestScope
	// withheld is nil on the zero value; refuse() allocates on first use, so a
	// caller that never refuses anything allocates nothing.
	withheld map[string]contextfabric.SubjectRef
	// exempted records the subjects this scope WOULD have refused and admitted
	// anyway because the caller named them. Recorded rather than merely allowed,
	// because that is the difference between an exemption and a door around the
	// boundary: an operator reading the line can see that the engine considered
	// this candidate, knew it was the member kind, and admitted it on the
	// caller's authority. A silent pass leaves the same outcome unexplained.
	exempted map[string]contextfabric.SubjectRef
}

// newContestAdmission builds the admission for one call.
func newContestAdmission(scope contestScope) *contestAdmission {
	return &contestAdmission{scope: scope}
}

// WHERE A CANDIDATE CAME FROM, which is what the admission decides by.
//
// The boundary does not simply refuse a kind: it decides, per candidate, from
// its SOURCE. A caller who names a subject by canonical id has not been offered
// anything, so nothing about that is a substitution, and refusing it would break
// "name the subject you mean" -- decided truth, pinned by
// TestACallerExplicitHintOfTheMemberKindStillCommits. Retrieval proposing a
// member-kind candidate on a scope-anchored frame is the opposite: an I11
// violation by construction.
//
// The difference between the two is not a DOOR AROUND the boundary. It is a
// decision the boundary makes and records, which is the whole point: an
// exemption that lives inside the one admission function can be read, counted
// and reasoned about, while an insert that never reaches the function cannot.
type candidateSource string

const (
	// sourceRetrieval: any arm that searched, traversed or rescued its way to
	// this candidate. Subject to the refusal.
	sourceRetrieval candidateSource = "retrieval"
	// sourceCallerHint: the caller named this subject by canonical id in THIS
	// request. Exempt.
	sourceCallerHint candidateSource = "caller_hint"
	// sourceEngineHint: a hint this ENGINE minted and read back, which the
	// hintsource enumeration marks as NOT contest-exempt. Subject to the
	// refusal, exactly as retrieval is, because it IS retrieval -- of this
	// engine's own prior output. Admitting it would let a member kind the
	// boundary refused on turn N re-enter the contest on turn N+1 through its
	// own receipt, which is the substitution I11 forbids wearing a hint's
	// clothes.
	//
	// Engine-minted and contest-exempt are SEPARATE facts and hintsource keeps
	// them apart: the answer-reuse recheck is engine-minted AND exempt, because
	// its subjects commit through the caller-hint short circuit and moving it
	// off that exit would change the reuse path's behaviour.
	sourceEngineHint candidateSource = "engine_hint"
)

// hintCandidateSource classifies ONE SubjectHint by WHO AUTHORED IT, from the
// closed enumeration of the sources this engine mints.
//
// It is a LOOKUP in the same module the producers write through, not a string
// test written here. PR-A spelled the test as a literal comparison against
// "prior_subject_receipt" -- correct for the behaviour it preserved, but it
// was a second copy of a fact the producer owned, compiled separately from the
// producer and able to drift from it silently. hintsource.Lookup is the
// producers' own registry: a source they emit but never registered fails the
// producer-enumeration test rather than being read here as caller-authored.
//
// The unenumerated case is caller-authored, which is not a default reached for
// convenience: SubjectHint.Source is a caller-supplied wire string the v1
// contract validates only for bounds, so "not one the engine minted" IS the
// definition of caller-authored. hintsource records the limit that follows.
func hintCandidateSource(source string) candidateSource {
	if hintsource.Lookup(source).ContestExempt {
		return sourceCallerHint
	}
	return sourceEngineHint
}

// admits reports whether this candidate may enter the contest set, DECIDING BY
// SOURCE. A nil admission admits everything, so every call site with no scope to
// apply -- this package's own unit callers, and the arms that only ever run with
// no confirmed kind -- passes nil and reads as "nothing was decided" rather than
// as "everything was refused".
//
// ONLY sourceCallerHint is exempt. Every other source -- retrieval today,
// this engine's own prior receipts, and anything a later change adds -- falls
// through to the refusal, so the failure direction of a source nobody
// classified is "refused", never "silently exempt".
func (a *contestAdmission) admits(subject contextfabric.SubjectRef, source candidateSource) bool {
	if a == nil {
		return true
	}
	if source == sourceCallerHint {
		// Recorded ONLY when the scope would otherwise have refused it: an
		// ordinary hint of an unrelated kind is not an exemption, it is just a
		// hint, and counting it would make the number meaningless.
		if a.scope.refuses(subject.Kind) {
			a.exempt(subject)
		}
		return true
	}
	return !a.scope.refuses(subject.Kind)
}

// refuse records ONE refused subject. Idempotent per subject by construction.
func (a *contestAdmission) refuse(subject contextfabric.SubjectRef) {
	if a == nil {
		return
	}
	if a.withheld == nil {
		a.withheld = make(map[string]contextfabric.SubjectRef, 1)
	}
	a.withheld[SubjectKey(subject)] = subject
}

// refused reports whether THIS call already refused this exact subject. It reads
// the boundary's own record, which is what makes it different from asking
// whether the subject is absent from the candidate pool: absence has many
// causes (unauthorized, invalid, internal, never retrieved) and only one of
// them is this decision.
func (a *contestAdmission) refused(subject contextfabric.SubjectRef) bool {
	if a == nil || a.withheld == nil {
		return false
	}
	_, ok := a.withheld[SubjectKey(subject)]
	return ok
}

// exempt records ONE subject admitted by the caller-hint exemption that this
// scope would otherwise have refused. Idempotent per subject, same as refuse.
func (a *contestAdmission) exempt(subject contextfabric.SubjectRef) {
	if a == nil {
		return
	}
	if a.exempted == nil {
		a.exempted = make(map[string]contextfabric.SubjectRef, 1)
	}
	a.exempted[SubjectKey(subject)] = subject
}

// exemptedCount is the DISTINCT number of subjects admitted by the exemption.
// Explicit zero on every path, including the nil admission.
func (a *contestAdmission) exemptedCount() int {
	if a == nil {
		return 0
	}
	return len(a.exempted)
}

// withheldCount is the DISTINCT number of subjects this call refused. Explicit
// zero on every path, including the nil admission.
func (a *contestAdmission) withheldCount() int {
	if a == nil {
		return 0
	}
	return len(a.withheld)
}

// withheldSubjects returns the refused subjects in a DETERMINISTIC order, for
// the per-candidate disclosure. Map iteration is unordered and these reach a
// log an operator reads, so the order is settled here rather than left to
// whatever range produced.
func (a *contestAdmission) withheldSubjects() []contextfabric.SubjectRef {
	if a == nil || len(a.withheld) == 0 {
		return nil
	}
	keys := make([]string, 0, len(a.withheld))
	for key := range a.withheld {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]contextfabric.SubjectRef, 0, len(keys))
	for _, key := range keys {
		out = append(out, a.withheld[key])
	}
	return out
}

// observable forwards the scope's own rendering, so a caller never has to know
// which half of the admission answers this.
func (a *contestAdmission) observable() (kind string, source string) {
	if a == nil {
		return contestScopeNone, contestScopeNone
	}
	return a.scope.observable()
}
