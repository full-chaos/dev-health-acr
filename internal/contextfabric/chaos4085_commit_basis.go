package contextfabric

// CHAOS-4085: the commit BASIS vocabulary -- an INTERNAL (never-wire) record
// of WHAT KIND OF PROOF stood behind each subject a resolution committed.
//
// Why this exists at all. Before CHAOS-4085 the engine received a
// SubjectResolution whose Committed list carried no account of itself: a
// subject committed because the caller named it by canonical id and a
// subject committed because an embedding-similarity margin broke a
// three-way relevance tie were the same two fields (Kind, CanonicalID) on
// the wire. The v9 trial's case-61 wrong commit (a never-commit control
// that committed a work_item on a tied, truncated, statistically-rescued
// match) is exactly the second shape, and the ONLY way to refuse it
// without also refusing the first shape is to know which one happened.
//
// Why the mechanism set and Confidence are NOT that account (sol@xhigh
// review, 2026-08-22, change 2 -- this is the finding that produced this
// file): Confidence==1 plus a MatchExact/MatchAlias mechanism looks like an
// identity proof and is not one.
//
//   - MatchExact CONFLATES two different claims. resolve.go's caller-hint
//     branch stamps MatchExact on a subject the caller named by CANONICAL
//     ID, re-read from the graph and re-authorized. resolution.go's
//     exactIndex tier stamps the same mechanism for LABEL equality, which
//     its own doc comment admits is unsound under truncation ("a duplicate
//     label hidden entirely behind the truncation boundary remains the
//     residual risk"). One is proof; the other is a heuristic that happens
//     to score 1.0.
//   - MatchAlias/MatchProviderKey are only safe under conjuncts that are
//     NOT visible on the candidate: identity_fast_path requires
//     aliasIdentityComplete (the identity universe was enumerated
//     COMPLETELY, not merely searched), uniqueness within the class
//     (identityCollision) and across classes (identityCrossClassRivalClaimant).
//     The identical candidate, with the identical mechanism set and the
//     identical 1.0, can also reach Committed through lone_floor with
//     aliasIdentityComplete FALSE -- an unproven uniqueness claim.
//
// So the basis is recorded AT THE POINT OF COMMIT, where those conjuncts
// are local variables, and carried out to the engine. It is a closed
// vocabulary, and its ZERO VALUE IS THE UNSAFE ONE: a commit whose basis
// nothing recorded reads as CommitBasisUnknown, which IdentityProven
// reports as false, which routes it into CHAOS-4085's post-synthesis
// affirmation gate. A GraphReader that never populates a basis therefore
// gets the STRICTER treatment, never the laxer one -- the fail-closed
// direction, and the reason this can be introduced without auditing every
// implementation for completeness first.
//
// NEVER WIRE. This never appears in contracts/, in a JSON Schema, in the
// OpenAPI document, or in a persisted InvestigationResult. It is a
// same-request, engine-internal explanation of a decision the wire already
// carries the OUTCOME of, so the contract-first rule (AGENTS.md) is not
// engaged. Anything that needs to observe it after the fact reads
// telemetry, not the result.
type CommitBasis string

const (
	// CommitBasisUnknown is the zero value and the FAIL-CLOSED reading: no
	// commit path recorded a basis for this subject, so nothing may be
	// assumed about the proof behind it. Treated exactly like
	// CommitBasisStatistical by every consumer -- never like a proven
	// identity. A resolution produced by a GraphReader that predates
	// CHAOS-4085, or by a test double that returns a bare SubjectResolution,
	// lands here for every committed subject by construction.
	CommitBasisUnknown CommitBasis = ""

	// CommitBasisCallerCanonicalID is a subject the CALLER named by
	// canonical id -- an explicit RequestedScope.SubjectHint, or a prior
	// subject receipt the engine already redeemed into one -- which was
	// then re-read from THIS organization's graph by keyed lookup
	// (ResolveDeps.ExactHint) and re-authorized against this principal and
	// this request's scope (AuthorizedAttributes + NodeCandidate) before it
	// was allowed to commit.
	//
	// This is proof, not a heuristic: there is no ranking, no relevance
	// score, and no truncation boundary anywhere in it. The caller asserted
	// an identity, the graph confirmed that identity exists, and
	// authorization confirmed this caller may see it. A synthesis that then
	// says "I could not establish anything about this subject" is a
	// legitimate NON-ANSWER ABOUT THE RIGHT SUBJECT, which is a different
	// event from a wrong commit and must not be punished as one.
	CommitBasisCallerCanonicalID CommitBasis = "caller_canonical_id"

	// CommitBasisAuthoritativeIdentity is a subject committed through the
	// identity fast path on an AUTHORITATIVE keyed identity (an approved
	// alias or an upstream provider key) under the full conjunct set that
	// path requires, all of which must hold together:
	//
	//   - complete enumeration: the identity universe for the term was read
	//     COMPLETELY (aliasIdentityComplete), so "unique" is a proof rather
	//     than an artifact of a truncated read;
	//   - unique claimant WITHIN the class (no identityCollision);
	//   - unique claimant ACROSS classes (no identityCrossClassRivalClaimant
	//     -- CHAOS-3917's label-vs-alias rival check);
	//   - graph existence and authorization, proven by construction: the
	//     candidate exists because a keyed read of THIS org's graph
	//     returned it, and survived NodeCandidate's authorization filter.
	//
	// Same standing as CommitBasisCallerCanonicalID and for the same
	// reason: the caller's literal term IS this subject's recorded
	// identity, proven unique over a completely-read universe.
	CommitBasisAuthoritativeIdentity CommitBasis = "authoritative_identity"

	// CommitBasisStatistical is every other commit: the lone-candidate
	// confidence floor, the top-of-two gap, the CHAOS-3829 vector-margin
	// rescue, the CHAOS-3896 evidence-census rescue, and the exact-LABEL
	// tier (which is a label heuristic, not an identity proof -- see this
	// type's own doc comment). What these share is that the subject was
	// selected by comparing SCORES among candidates, so "this is the right
	// subject" rests on the retrieved population being representative --
	// precisely the premise a truncated search does not establish.
	//
	// A statistical commit is admissible; it is simply not SELF-proving,
	// which is why CHAOS-4085 requires the synthesized answer to
	// independently support it before it survives into the result.
	CommitBasisStatistical CommitBasis = "statistical"
)

// IdentityProven reports whether b is one of the two bases that stand on a
// proven identity rather than on a score comparison. Only these two are
// exempt from CHAOS-4085's post-synthesis affirmation gate.
//
// Written as an explicit allow-list of the two proven values rather than as
// "not statistical" on purpose: a basis added later, or a basis some future
// commit path forgets to record, must default to NOT proven. A negated
// test would default new vocabulary the other way, which is the direction
// that loses subjects to wrong commits.
func (b CommitBasis) IdentityProven() bool {
	switch b {
	case CommitBasisCallerCanonicalID, CommitBasisAuthoritativeIdentity:
		return true
	default:
		return false
	}
}

// CommitBasisSet maps a subject's SubjectMapKey to the basis its commit
// stood on. A nil or absent entry is CommitBasisUnknown, which
// IdentityProven reports as false -- see the type doc above for why the
// zero value must be the strict one.
type CommitBasisSet map[string]CommitBasis

// For returns the recorded basis for subject, or CommitBasisUnknown when
// this set records none (including when the set itself is nil). Safe on a
// nil receiver so callers never need a guard.
func (s CommitBasisSet) For(subject SubjectRef) CommitBasis {
	if s == nil {
		return CommitBasisUnknown
	}
	return s[SubjectMapKey(subject)]
}

// Record stores basis for subject. A no-op on a nil map (a caller that
// never allocated a set is recording into nothing, which reads back as
// CommitBasisUnknown -- the fail-closed value, not a panic mid-resolution).
func (s CommitBasisSet) Record(subject SubjectRef, basis CommitBasis) {
	if s == nil {
		return
	}
	s[SubjectMapKey(subject)] = basis
}

// ResetTo makes s hold exactly other's entries, discarding whatever it held
// before. Used where a resolution is RE-DECIDED rather than amended (the
// CHAOS-3896 evidence-census second pass re-runs the whole commit decision
// and returns a fresh resolution): merging there would leave a basis
// recorded for a subject the second decision no longer commits, and a stale
// proven basis is precisely the failure this vocabulary exists to prevent.
// A no-op on a nil receiver, matching Record.
func (s CommitBasisSet) ResetTo(other CommitBasisSet) {
	if s == nil {
		return
	}
	for key := range s {
		delete(s, key)
	}
	for key, basis := range other {
		s[key] = basis
	}
}

// SubjectMapKey is the stable map key for a SubjectRef: kind and canonical
// id joined by a byte that cannot occur in either, so two subjects of
// different kinds sharing a canonical id never collide.
//
// This is the ONE definition -- graphrank.SubjectKey delegates to it rather
// than restating the formula. A CommitBasisSet is written by graphrank and
// read by the engine, so the two packages MUST key identically or a basis
// silently reads back as CommitBasisUnknown for every subject; a single
// shared function is what makes that impossible rather than merely
// currently-true.
func SubjectMapKey(subject SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}
