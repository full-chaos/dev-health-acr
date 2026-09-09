// Package hintsource is the CLOSED ENUMERATION of the subject-hint sources
// THIS ENGINE MINTS, and the single place any decision about a hint's
// authorship is made.
//
// WHY A PACKAGE RATHER THAN A CONSTANT NEXT TO EACH USE. The producers of these
// strings and the consumers that classify them live in different packages:
// the engine mints them (the receipt redemption, the answer-reuse recheck) and
// the graph ranker decides by them. Before this package there was one string
// LITERAL at each producer and a second literal at the consumer, and the
// consumer's copy silently defined behaviour for a producer it had never been
// compiled against. One imported module removes that: a producer that does not
// use a constant from here is caught by the producer-enumeration test, and a
// consumer cannot invent a member the producers do not emit.
//
// WHAT THIS IS NOT. It is NOT a closed vocabulary of every hint source in the
// system, and it cannot be: ContextFabricSubjectHint.Source is a caller-supplied
// wire string that the v1 contract validates only for bounds (1..64 bytes,
// trimmed, non-empty). Anything not enumerated here is therefore CALLER
// AUTHORED by definition, which is the honest reading of "the engine did not
// write this". The known limit that follows -- a caller may send a string equal
// to one of these -- is recorded on the type below rather than left implied.
package hintsource

// Source is one hint source this engine mints.
type Source string

const (
	// PriorSubjectReceipt: a subject from a PRIOR result of this
	// conversation, redeemed by the receipt id the engine itself issued.
	// This engine's own earlier output read back, which is why it is
	// neither exempt from the contest nor eligible for the caller-hint
	// short circuit: crediting the caller for an identifier the engine
	// minted is the substitution the contest boundary exists to refuse.
	PriorSubjectReceipt Source = "prior_subject_receipt"
	// AnswerReuseAuthorizationRecheck: the answer-reuse path re-verifying
	// that the subjects of a candidate reusable answer are STILL authorized
	// for this principal. Engine-minted, but it is a re-verification of an
	// answer the caller is asking for by its own terms, and it commits
	// through the caller-hint short circuit -- so it stays short-circuit
	// eligible and contest-exempt. See the attribute table below.
	AnswerReuseAuthorizationRecheck Source = "answer_reuse_authorization_recheck"
)

// Attributes are the TWO INDEPENDENT FACTS about a source, kept apart on
// purpose.
//
// Before this type one boolean carried both, and the two disagreed for the
// answer-reuse recheck: it is engine-minted (authorship) and it must still
// reach the caller-hint short circuit (policy), because that exit is where its
// subjects commit and what sets their CommitBasisCallerCanonicalID. Collapsing
// the two facts into one either misnames the recheck's authorship or moves the
// reuse path off the short circuit onto hybrid search -- a live behaviour
// change measured on this branch before the split was written.
type Attributes struct {
	// EngineMinted is AUTHORSHIP: this engine wrote this string, the caller
	// did not. It says nothing about what any decision should do with it.
	EngineMinted bool
	// ContestExempt is POLICY at the contest boundary: may a candidate
	// carrying this source enter the contest set even when the question's
	// scope refuses its kind.
	ContestExempt bool
	// ShortCircuitEligible is POLICY at the caller-hint exit: may a
	// candidate carrying this source reach the exact-resolution short
	// circuit instead of falling through to hybrid search.
	ShortCircuitEligible bool
}

// registry is the enumeration. Adding a member here is the ONLY way to add an
// engine-minted source, and the producer-enumeration test fails the build when
// a production Source: literal is not one of these.
var registry = map[Source]Attributes{
	PriorSubjectReceipt: {
		EngineMinted:         true,
		ContestExempt:        false,
		ShortCircuitEligible: false,
	},
	AnswerReuseAuthorizationRecheck: {
		EngineMinted:         true,
		ContestExempt:        true,
		ShortCircuitEligible: true,
	},
}

// Lookup returns the attributes of a hint source string.
//
// A source that is not enumerated is CALLER AUTHORED: not engine-minted, exempt
// from the contest, and eligible for the short circuit. That is the correct
// reading for the population it describes -- an arbitrary wire string the
// caller chose -- and it is the behaviour every unenumerated source has today.
//
// KNOWN LIMIT, stated rather than implied: Source is a caller-supplied wire
// string, so a caller CAN send a string equal to an enumerated member and be
// classified as the engine. Distinguishing that needs provenance the request
// carries and this system cannot forge, which is a contract change, not an
// enumeration. This package narrows who can be misread; it does not close it.
func Lookup(source string) Attributes {
	// NOT trimmed here. An earlier draft called strings.TrimSpace on the way
	// in, and no mutation arm could kill its removal, because the population it
	// defended against does not exist: ContextFabricSubjectHint.Validate
	// rejects an untrimmed source at the contract, before any of this runs, and
	// that rejection is pinned. A guard nothing can observe is not defence in
	// depth, it is a second answer to a question already answered elsewhere.
	if attributes, ok := registry[Source(source)]; ok {
		return attributes
	}
	return Attributes{EngineMinted: false, ContestExempt: true, ShortCircuitEligible: true}
}

// EngineMinted reports whether this engine wrote this source string.
func EngineMinted(source string) bool { return Lookup(source).EngineMinted }

// All returns every enumerated source, for the tests that must enumerate the
// registry rather than restate it. Sorted is not required: callers that need
// order sort it themselves; what matters is that this is the SAME map the
// decisions read, so a test built from it cannot drift from them.
func All() []Source {
	out := make([]Source, 0, len(registry))
	for source := range registry {
		out = append(out, source)
	}
	return out
}
