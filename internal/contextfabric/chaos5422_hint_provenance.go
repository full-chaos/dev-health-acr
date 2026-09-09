package contextfabric

import "slices"

// WHO NAMED THIS SUBJECT: the one place that answers it, for every consumer.
//
// CHAOS-5422, counted rounds r1 and r2. Two review rounds found the same class
// of defect -- a per-path rule that one path escaped -- and the second round
// found it in the classification itself: `resolve.go` decided "caller-explicit"
// by testing a single source string, so EVERY engine-produced hint except that
// one string was treated as if the caller had named it. The answer-reuse
// authorization recheck (answer_reuse.go) produces exactly such a hint, and it
// was therefore both exempted from withholding and reported to operators as
// caller-named.
//
// The fix is not another string test at another call site. The classification
// is a CLOSED ENUMERATION, defined once here, and both consumers -- the
// withholding exemption and the provenance observable -- read this and nothing
// else. A new internal hint producer that forgets to classify itself fails
// TestEverySubjectHintSourceIsClassified rather than silently acquiring the
// caller's exemption.
//
// The asymmetry is deliberate: caller-named is the DEFAULT for anything a wire
// request supplies, because a request's hints are the caller's own words and we
// cannot enumerate them. Engine-minted is the enumerable side, because every
// such hint is constructed by code in this repository.
const (
	// SubjectHintSourcePriorSubjectReceipt is engine.go's read-back of a
	// receipt THIS ENGINE minted in an earlier turn and the caller returned.
	SubjectHintSourcePriorSubjectReceipt = "prior_subject_receipt"
	// SubjectHintSourceAnswerReuseRecheck is answer_reuse.go's authorization
	// recheck, which re-poses a prior answer's own committed subjects. The
	// caller never named them in this request.
	SubjectHintSourceAnswerReuseRecheck = "answer_reuse_authorization_recheck"
)

// engineMintedSubjectHintSources is the closed set. Sorted, so the pin can
// compare it as a value rather than as a set-with-an-order-caveat.
var engineMintedSubjectHintSources = []string{
	SubjectHintSourceAnswerReuseRecheck,
	SubjectHintSourcePriorSubjectReceipt,
}

// EngineMintedSubjectHintSources returns a COPY of the closed set, so a
// consumer cannot mutate the enumeration it is asking about.
func EngineMintedSubjectHintSources() []string {
	return slices.Clone(engineMintedSubjectHintSources)
}

// SubjectHintIsEngineMinted reports whether this hint's identity came from this
// engine rather than from the caller. It is the ONE authority: the withholding
// exemption and the provenance token both call it, so they can never disagree
// about the same hint.
func SubjectHintIsEngineMinted(source string) bool {
	return slices.Contains(engineMintedSubjectHintSources, source)
}

// The provenance vocabulary carried on a committed subject's decision event.
// CLOSED, and total over the states a commit can be in -- there is no empty
// string, because an empty field is exactly what let a false zero look like a
// measured one (r2 finding 3).
const (
	// CommitSubjectProvenanceCallerNamed: a canonical id the caller supplied
	// in THIS request.
	CommitSubjectProvenanceCallerNamed = "caller_named"
	// CommitSubjectProvenanceEngineMinted: an id this engine produced and the
	// caller handed back, from the closed set above.
	CommitSubjectProvenanceEngineMinted = "engine_minted"
	// CommitSubjectProvenanceResolved: no hint named this subject at all --
	// retrieval found it. The common case, and it is a POSITIVE statement, not
	// a fallback.
	CommitSubjectProvenanceResolved = "resolved"
	// CommitSubjectProvenanceUnknown: this path genuinely cannot say. Distinct
	// from `resolved` on purpose -- "retrieval found it" and "I was not told"
	// are different facts, and collapsing them is how a measured zero and a
	// missing measurement became indistinguishable in the first place.
	CommitSubjectProvenanceUnknown = "unknown"
)

// ClassifySubjectHintProvenance maps a hint's source to the vocabulary above.
// Caller-named is the default because a wire request's sources are not
// enumerable; engine-minted is the closed side.
func ClassifySubjectHintProvenance(source string) string {
	if SubjectHintIsEngineMinted(source) {
		return CommitSubjectProvenanceEngineMinted
	}
	return CommitSubjectProvenanceCallerNamed
}

// MergeSubjectHintProvenance aggregates the classifications of SEVERAL hints
// that name the SAME subject, and caller-explicit WINS.
//
// CHAOS-5422, counted round r3. The v1 request validator enforces uniqueness on
// repository slugs, project ids and team ids and NOT on subject hints, and
// engine.go appends the engine's own prior-receipt hints AFTER the caller's --
// so a caller who names a subject that also has a receipt produces a duplicate
// key, in that order, on the ordinary production path.
//
// Before this, the two consumers aggregated that duplicate DIFFERENTLY: the
// exemption was sticky (set once caller-named, never cleared) and the reported
// provenance was last-write-wins. The subject stayed exempt and committed while
// the line called it engine-minted -- the exact disagreement consolidating the
// classification was supposed to make impossible. Consolidating the TEST was not
// enough while two WRITE POLICIES remained.
//
// Caller-explicit wins because the caller naming a subject by canonical id in
// THIS request is an authoritative direct ask, and it does not stop being one
// because the engine also happens to hold a receipt for the same subject. The
// asymmetry runs the same way as the exemption it feeds.
func MergeSubjectHintProvenance(existing, incoming string) string {
	if existing == "" {
		return incoming
	}
	if existing == CommitSubjectProvenanceCallerNamed || incoming == CommitSubjectProvenanceCallerNamed {
		return CommitSubjectProvenanceCallerNamed
	}
	return incoming
}
