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
	// CohortGroupAuthorization: the grouped-cohort path asking whether this
	// principal may see each group identity the grouping CONSTRUCTED, before
	// any group-rooted fact read is issued. Engine-minted, and its subjects
	// are named by canonical id the engine derived from the source rows, so
	// it takes the same attributes as the reuse recheck: it is a filter over
	// identities this turn already holds, never a way to widen a pool.
	CohortGroupAuthorization Source = "cohort_group_authorization"
)

// Attributes are the INDEPENDENT FACTS about a source, kept apart on purpose:
// authorship, and three policies read at three different decision points.
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
	ContestExempt ContestPolicy
	// ShortCircuitEligible is POLICY at the caller-hint exit: may a
	// candidate carrying this source reach the exact-resolution short
	// circuit instead of falling through to hybrid search.
	ShortCircuitEligible ShortCircuitPolicy
	// SearchFallback is POLICY for a hint set that resolved NOTHING: may the
	// resolution widen into hybrid search, or does it end with the empty
	// exact answer.
	//
	// A third fact, not a reading of the second. The short circuit decides
	// what happens when a hint RESOLVED; this decides what happens when none
	// did. For an identity question -- "may this principal see these ids" --
	// an id the keyed lookup could not authorize cannot be admitted by a
	// search either, so the search can only cost time, and it costs a great
	// deal: on the trial venue a batch of 50 group ids that resolved nothing
	// spent 18-20 s in hybrid search (embeddings included) and admitted
	// nothing, where one resolvable id in the same call short-circuits in
	// ~40 ms.
	SearchFallback SearchFallbackPolicy
}

// THE POLICIES ARE DIFFERENT TYPES WITH DIFFERENT ACCESSORS (the third,
// SearchFallbackPolicy, joined on the same rule), and both
// halves of that are load-bearing.
//
// They answer different questions at different call sites, and today every
// enumerated member happens to carry the SAME value for both. A read site that
// consulted the wrong one would therefore behave identically, and adversarial
// review demonstrated exactly that: a mutation swapping them at the resolver's
// read survived every test, because no fixture could tell them apart.
//
// A test cannot close that. Discriminating needs a member whose two values
// differ, which is a decision about what the registry CONTAINS, not about how
// it is tested. So the type system closes it instead.
//
// NAMED BOOLEANS WERE NOT ENOUGH, and this is measured rather than assumed: a
// `type ContestPolicy bool` is still boolean-kinded, so `if attrs.ContestExempt`
// compiles wherever `if attrs.ShortCircuitEligible` did and the swap survives.
// That was the first attempt here and it was verified NOT to work before this
// one was written. Each policy is therefore a STRUCT with its own single,
// differently-named accessor: a struct is not usable in a boolean context, and
// `attrs.ContestExempt.Eligible()` names a method ContestPolicy does not have.
// Swapping the two reads is a compile error, not a surviving mutant.
type (
	// ContestPolicy answers: may this source's candidate enter the contest
	// set even when the question's scope refuses its kind.
	ContestPolicy struct{ exempt bool }
	// ShortCircuitPolicy answers: may this source's candidate reach the
	// exact-resolution caller-hint exit rather than falling through to
	// hybrid search.
	ShortCircuitPolicy struct{ eligible bool }
	// SearchFallbackPolicy answers: may a hint set carrying this source,
	// having resolved nothing, fall through to hybrid search.
	SearchFallbackPolicy struct{ permitted bool }
)

// Exempt reports whether the contest boundary admits this source's candidate
// despite a kind refusal.
func (p ContestPolicy) Exempt() bool { return p.exempt }

// Eligible reports whether this source's candidate may reach the caller-hint
// short circuit.
func (p ShortCircuitPolicy) Eligible() bool { return p.eligible }

// Permitted reports whether a hint set of this source that resolved nothing
// may widen into hybrid search.
func (p SearchFallbackPolicy) Permitted() bool { return p.permitted }

// Contest and ShortCircuit build the policies, and the registry below uses them
// rather than composite literals. Two reasons, both about keeping the types
// meaningful: the field stays unexported, so the accessor is the only way to
// read a policy outside this package; and a constructor with a named parameter
// makes `Contest(false)` say which fact is false, where a bare literal beside
// another bare literal invites the transposition these types exist to stop.
func Contest(exempt bool) ContestPolicy             { return ContestPolicy{exempt: exempt} }
func ShortCircuit(eligible bool) ShortCircuitPolicy { return ShortCircuitPolicy{eligible: eligible} }
func SearchFallback(permitted bool) SearchFallbackPolicy {
	return SearchFallbackPolicy{permitted: permitted}
}

// registry is the enumeration. Adding a member here is the ONLY way to add an
// engine-minted source, and the producer-enumeration test fails the build when
// a production Source: literal is not one of these.
var registry = map[Source]Attributes{
	// A receipt is a conversational reference, not an identity question: a
	// follow-up naming a different subject than the receipt bound must still
	// be found by search, so it keeps the fallback.
	PriorSubjectReceipt: {
		EngineMinted:         true,
		ContestExempt:        Contest(false),
		ShortCircuitEligible: ShortCircuit(false),
		SearchFallback:       SearchFallback(true),
	},
	// The two AUTHORIZATION questions never widen. Each asks whether a set of
	// ids it already holds is visible to this principal; a search cannot
	// answer that for an id the keyed lookup refused, so it can only spend
	// the turn's time before reporting the same empty answer.
	AnswerReuseAuthorizationRecheck: {
		EngineMinted:         true,
		ContestExempt:        Contest(true),
		ShortCircuitEligible: ShortCircuit(true),
		SearchFallback:       SearchFallback(false),
	},
	CohortGroupAuthorization: {
		EngineMinted:         true,
		ContestExempt:        Contest(true),
		ShortCircuitEligible: ShortCircuit(true),
		SearchFallback:       SearchFallback(false),
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
	return Attributes{EngineMinted: false, ContestExempt: Contest(true), ShortCircuitEligible: ShortCircuit(true), SearchFallback: SearchFallback(true)}
}

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
