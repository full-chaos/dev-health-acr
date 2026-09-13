package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-5660: an offer a caller cannot answer THIS question with is not an
// answer to this question.
//
// WHAT WAS MEASURED, on the 2026-09-12 yardstick of record
// (~/.cache/acr-kiac-askdev/proofs/2026-09-12-main-b6579178, acr b6579178 /
// ask-dev 3cc908b7, 36 rows x 3 replicates). Two rows named a subject by a
// token the graph carries for no project -- "the acr project" -- and each
// one spent all five of its turns in clarification_required, in every
// replicate, serving no text. Every turn offered something: five candidate
// options, twenty handle options, four kind options on the turns that
// raised the kind need. Not one option, on any of the thirty served
// documents, carried kind `project`. The kind options were
// ci_pipeline_run / pull_request / pull_request_review / repository; the
// candidate and handle options were ci_pipeline_run and pull_request; the
// anchor list was empty throughout.
//
// So CHAOS-5637's invariant held and the conversation still could not
// converge. That predicate asks whether the document carries AN option. The
// question these turns asked was whether it carries an option that can
// satisfy the need THIS FRAME DECLARED, and those are different questions
// the moment a frame declares a kind: a caller who answers `repository` to
// a question the frame says is about a `project` has not answered it, and a
// caller who refuses to answer by position -- which is the only correct
// thing to do with an option list that omits the kind one asked for -- sends
// back nothing, loses the chain with it, and re-asks the bare question. The
// engine then re-raises the window need it had already satisfied, and the
// exchange oscillates on a two-turn period until the budget runs out.
//
// WHAT THIS FILE DECIDES. Whether any offer channel this turn carries can
// satisfy the frame's own declared kind, and nothing else. It does not
// decide which candidates may be offered (graphrank's exclusion is
// untouched), whether the kind NEED is raised (chaos3900_structure_offers.go
// already withholds a declared kind nothing in the pool can serve, and that
// suppression is correct and unchanged), or what any other terminal says.
//
// THE CLASS, SWEPT. Two sites in this package compose
// clarification_required in production. resolveTerminalStatus (unresolved.go)
// is this one, on all three of its return sites. The other is the
// window-confirmation gate (window.go), and it is NOT a sibling: it fires
// BEFORE subject resolution, so no candidate and no structure offer exists
// yet, the need it raises is the WINDOW, and the options it offers are
// window options -- the offer satisfies the need it was raised for, which is
// the property this file checks. decideDeclaredKind is correct there by
// construction rather than by luck: with no offers at all it reports
// satisfiable, and that arm is pinned rather than left to be inferred.
//
// WHAT IT DOES NOT FIX, stated plainly. The rows this changes move from five
// turns of clarification_required to one no_match. They do not become
// answered, and nothing here makes them answerable: the token `acr` resolves
// to zero project identities in the projected graph -- the project's node
// carries the label "Dev Health Agent Context Runtime (Context Fabric)", an
// empty alias list, and no `acr` in its search text -- so no retrieval fix
// inside this seam can produce the subject. Giving that project a retrieval
// handle is a data decision tracked separately. What this buys is a truthful
// terminal on turn one instead of five turns of an ask that had no answer.

// declaredKindTerminalReason is the subjectlessTerminalReason member this
// outcome reports: the candidate pool or the offer channels were non-empty
// and NOTHING in them carried the kind the frame declared, so no option the
// caller was handed could satisfy the need they were asked about.
//
// IT IS NOT `ambiguous`, which is the value this state reported before this
// file existed and which is true of the POOL rather than of the decision. A
// pool of seven ci_pipeline_runs offered against a question about a project
// is not an ambiguity the caller can resolve -- there is nothing among them
// to pick. Reporting the new terminal under the old token would leave the
// change invisible in the one line an operator reads to count it, which is
// the same collapse `offer_pool_emptied_by_exclusion` was added to end.
//
// IT IS NOT `offer_pool_emptied_by_exclusion` either: that member requires
// an EMPTY candidate list paired with the exclusion prompt, and it describes
// a pool whose members were withheld for being vector-only. Here the members
// are disclosed and simply of the wrong kind.
const declaredKindTerminalReason = "no_candidate_of_declared_kind"

// declaredKindTerminalLimitation is the sentence this terminal carries: the
// basis vocabulary's OWN fixed sentence for declared_kind_unmatched.
//
// ONE DECISION, ONE BASIS, ONE SENTENCE. A sentence already owned by another
// decision cannot distinguish the two states for anyone reading the served
// answer -- the reader sees one wording and cannot tell which decision
// produced it -- and that is the same collapse the basis vocabulary itself
// exists to end. So the sentence a terminal carries is the one its basis
// owns, and nothing else. The service-authored registry and the domain cells
// beside it hold the pairing.
const declaredKindTerminalLimitation = contractsv1.ContextFabricDeclaredKindUnmatchedLimitation

// declaredKindTerminalBasis is the wire refusal basis this terminal
// discloses, so the class is countable by a CONSUMER and not only by an
// operator reading the log line.
//
// It stays a named constant with a single reference even now that it is set,
// for the reason it was one while it was empty: the whole cost of this
// disclosure is readable from one declaration. It is deliberately NOT a
// frame refusal -- the frame validated and its gate passed -- so the member
// sits outside ValidContextFabricFrameRefusalBasis and carries its own
// sentence rather than the member-kind one, which would claim the service
// cannot serve a kind it serves.
const declaredKindTerminalBasis = contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched

// DeclaredKinds returns the kinds THIS FRAME ITSELF declared -- what the
// question says its subject IS (SubjectExpression.MemberKind, which reads
// named_subject's ExpectedKind too) and, for a grouped question, the axis it
// is grouped BY (GroupKind) -- in closed-vocabulary order, or nil when the
// frame declared none.
//
// ONE DERIVATION, TWO READERS. graphrank's frameKindHints was this
// derivation's only home and is now a delegation to it (cohort_kind.go):
// phase 4 reserves candidate slots for exactly these kinds, and the
// answerability decision below asks whether any offer carries one of exactly
// these kinds. A second copy would be a second authority on what the frame
// declared, and the two would disagree the first time the union grew a
// variant -- which is the defect the frame itself exists to remove.
//
// A NIL FRAME DECLARES NOTHING, and so does a frame whose variant carries no
// kind. Both return nil, and every caller reads that as "this question
// constrained no kind", never as "this question declared a kind that is
// missing" -- absent is a weaker claim than declared, and the decision below
// turns on the difference.
func (f *QuestionFrame) DeclaredKinds() []SubjectKind {
	if f == nil {
		return nil
	}
	declared := make(map[SubjectKind]bool, 2)
	if kind, ok := f.SubjectExpression.MemberKind(); ok {
		declared[kind] = true
	}
	if kind, ok := f.SubjectExpression.GroupKind(); ok {
		declared[kind] = true
	}
	if len(declared) == 0 {
		return nil
	}
	var kinds []SubjectKind
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		if declared[SubjectKind(kind)] {
			kinds = append(kinds, SubjectKind(kind))
		}
	}
	return kinds
}

// declaredKindDecision is what one turn concluded about its own declared
// kind, carried to the status decision and to the log line as ONE value so
// the two cannot disagree about a turn.
type declaredKindDecision struct {
	// DeclaredKinds is the frame's own declared set, empty when the frame
	// declared none. Empty means this decision does not apply at all --
	// Unsatisfiable is then always false.
	DeclaredKinds []SubjectKind
	// OfferedKinds is every distinct kind this turn actually put in front
	// of the caller, across all four structure channels AND the subject
	// candidate list, in first-seen order. It is the evidence for the
	// decision, and it is what makes the log line readable without the
	// served document beside it: "declared project, offered
	// ci_pipeline_run/pull_request" is the whole finding on one line.
	OfferedKinds []SubjectKind
	// Unsatisfiable is true when the frame declared at least one kind and
	// NONE of the declared kinds appears among OfferedKinds.
	//
	// It is deliberately false when OfferedKinds is empty as well: a turn
	// with no offers at all is CHAOS-5637's condition, decided by
	// CHAOS-5637's predicate, and reporting it here too would give one
	// state two names in two log lines.
	Unsatisfiable bool
}

// decideDeclaredKind answers, for one turn, whether any offer it carries can
// satisfy the kind its frame declared.
//
// EVERY CHANNEL A CALLER CAN REDEEM, not a sample. All four structure option
// lists carry a Kind on the wire, and so does every subject candidate; a
// caller may answer through any of them, so an option of the declared kind
// on ANY of them makes the turn satisfiable. Reading fewer channels here
// would terminate a turn the caller could in fact have answered.
//
// WINDOW OPTIONS ARE NOT A CHANNEL FOR THIS QUESTION, and their absence from
// the list is the decision this function turns on. A window option answers
// WHEN, never WHICH SUBJECT; redeeming one on these turns was measured to
// advance the exchange by exactly one turn and then return it to the same
// state. Counting a window offer as satisfying a declared KIND need is what
// would keep the loop alive through this predicate.
func decideDeclaredKind(frame *QuestionFrame, resolution SubjectResolution, material StructureOfferMaterial) declaredKindDecision {
	decision := declaredKindDecision{DeclaredKinds: frame.DeclaredKinds()}
	seen := make(map[SubjectKind]bool, 4)
	add := func(kind contractsv1.ContextFabricSubjectKind) {
		subjectKind := SubjectKind(kind)
		if subjectKind == "" || seen[subjectKind] {
			return
		}
		seen[subjectKind] = true
		decision.OfferedKinds = append(decision.OfferedKinds, subjectKind)
	}
	for _, option := range material.KindOptions {
		add(option.Kind)
	}
	for _, option := range material.AnchorOptions {
		add(option.Kind)
	}
	for _, option := range material.HandleOptions {
		add(option.Kind)
	}
	for _, option := range material.CandidateOptions {
		add(option.Kind)
	}
	for _, candidate := range resolution.Candidates {
		add(candidate.Subject.Kind)
	}
	if len(decision.DeclaredKinds) == 0 || len(decision.OfferedKinds) == 0 {
		return decision
	}
	for _, kind := range decision.DeclaredKinds {
		if seen[kind] {
			return decision
		}
	}
	decision.Unsatisfiable = true
	return decision
}

// ObservableDeclaredKinds renders the declared set for the log line, with an
// explicit token when the frame declared none.
//
// A WORD, NEVER AN EMPTY VALUE, for the reason FrameGate.ObservableRefusalBasis
// already gives for its own "none": `declared_kinds=""` on a rig line is
// indistinguishable from a key nobody wrote, and the whole point of this line
// is to tell "the frame declared nothing" apart from "the frame declared
// something that was missing".
func (d declaredKindDecision) ObservableDeclaredKinds() string {
	return observableKindList(d.DeclaredKinds)
}

// ObservableOfferedKinds renders the offered set for the log line, with the
// same explicit "none" token and for the same reason.
func (d declaredKindDecision) ObservableOfferedKinds() string {
	return observableKindList(d.OfferedKinds)
}

// observableKindList joins a kind list for a log value, rendering the empty
// list as the explicit token rather than as an empty string.
func observableKindList(kinds []SubjectKind) string {
	if len(kinds) == 0 {
		return "none"
	}
	rendered := ""
	for index, kind := range kinds {
		if index > 0 {
			rendered += ","
		}
		rendered += string(kind)
	}
	return rendered
}

// observableRefusalBasis renders the EFFECTIVE refusal basis for the log
// line, with the explicit "none" token when the turn was not refused.
//
// It exists because FrameGate.ObservableRefusalBasis answers the same
// question for the GATE only, and a caller that has an effective basis from
// any producer needs one rendering rule, not two that agree by coincidence.
// The token is the same word the gate's own renderer uses, for the same
// missing-versus-measured-zero reason.
func observableRefusalBasis(basis contractsv1.ContextFabricRefusalBasis) string {
	if basis == "" {
		return "none"
	}
	return string(basis)
}
