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
// NEVER WIRE. The raw CommitBasis VOCABULARY -- this type, its string
// values, and the type itself -- never appears in contracts/, in a JSON
// Schema, in the OpenAPI document, or in a persisted InvestigationResult.
// It is a same-request, engine-internal explanation of a decision the wire
// already carries the OUTCOME of, so the contract-first rule (AGENTS.md) is
// not engaged for the type itself.
//
// CHAOS-4087 amendment: a DERIVED BOOLEAN PROJECTION of IdentityProven()
// below -- never the raw enum, never a value that lets a reader reconstruct
// which of the four CommitBasis constants fired -- may persist onto the
// stored result (contextfabric.CommitDecisionDigest.IdentityProven, this
// package). That is the one explicitly-allowed exception to "never wire":
// the wire learns "was this a proof or a score comparison", a stable
// two-valued fact, without the internal vocabulary itself -- its exact
// values, additions, or renames -- ever becoming part of a wire contract's
// own compatibility obligations. Anything that needs the FULL trace-level
// detail (which of the four bases, at which commit gate) still reads
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

// CommitDecisionDigest (CHAOS-4087) is the WIRE-SAFE, PERSISTED companion
// to CommitBasis/CommitBasisSet above -- see CommitBasis's own "NEVER WIRE"
// doc comment for the exact boundary this type sits on the allowed side
// of. Where CommitBasisSet is CHAOS-4085's engine-internal explanation,
// read once per request by a live ResolutionTracer sink and never
// persisted, CommitDecisionDigest is the SAME underlying decision's small,
// closed-vocabulary summary, stamped onto the stored
// contractsv1.ContextFabricSubjectResolution (CommitDecisionDigests field)
// so a wrong commit discovered later from a STORED result has a durable
// link to the mechanism that produced it -- the gap CHAOS-4087's own
// ticket exists to close (ResolutionTracer is a live sink only; a tryReuse
// hit serves an old result without re-emitting any correlating trace).
//
// Recorded at the SAME point, by the SAME call sites, as CommitBasisSet.Record
// -- graphrank/resolution.go's own "write site N of 3" sequence
// (FinalizeExactResolutionWithBasis's 2 sites, plus the 7 sites inside
// ResolveFromMergedCandidatesWithGateAndBasis) -- so the two sets are
// always in lockstep for every committed subject; there is no call site
// that populates one and not the other.
type CommitDecisionDigest struct {
	// CommitGate is the closed-vocabulary name of the commit path that
	// fired for this subject: "caller_hint_short_circuit" |
	// "pre_committed_exact_hint" | "exact_index" | "identity_fast_path" |
	// "lone_floor" | "top_of_two" | "vector_margin_rescue" |
	// "evidence_census" -- the SAME vocabulary graphrank.ResolutionTraceEvent.CommitGate
	// already uses for the identical concept, live-only.
	//
	// Empty is the FAIL-CLOSED zero value, the same discipline
	// CommitBasisUnknown's own doc comment establishes: it means NOTHING
	// recorded a digest for this subject, never "recorded and clean." A
	// consumer must check CommitGate != "" before trusting the three
	// fields below at all -- a digest with CommitGate=="" and every bool
	// false is indistinguishable, by construction, from one that was never
	// populated, which is exactly the point: there is nothing safe to
	// infer from an unrecorded digest.
	CommitGate string
	// IdentityProven is CommitBasis.IdentityProven() at the moment of
	// commit -- the ONE thing this type derives from the internal,
	// never-wire CommitBasis vocabulary (see CommitBasis's own doc
	// comment for why the raw enum itself never reaches here). true means
	// this subject's commit stood on a proven identity
	// (caller_canonical_id or authoritative_identity); false means it
	// stood on a score comparison (statistical) -- or that CommitGate is
	// itself empty, in which case this field carries no meaning at all.
	IdentityProven bool
	// SearchTruncated/AliasLookupComplete mirror the SAME resolution-wide
	// signals ResolutionTraceEvent's decision-stage event already carries
	// (graphrank/resolve.go) -- persisted here so a stored result answers
	// "was the search truncated / was alias lookup complete" without a
	// live trace consumer having been attached at request time. Like
	// IdentityProven, only meaningful when CommitGate != "".
	SearchTruncated     bool
	AliasLookupComplete bool
}

// CommitDecisionDigestSet maps a subject's SubjectMapKey to the digest its
// commit stood on -- CommitBasisSet's own exact shape and safety
// discipline (nil-safe Record/For, fail-closed absent entry), kept as a
// SEPARATE type rather than widening CommitBasisSet's own value type so
// CommitBasisSet's existing consumers (chaos4085_commit_affirmation.go,
// engine.go, every GraphReader) are never touched by this ticket, and so
// CommitBasisSet's own "never wire" guarantee is never put at risk by a
// change that shares its call sites.
type CommitDecisionDigestSet map[string]CommitDecisionDigest

// For returns the recorded digest for subject, or the zero CommitDecisionDigest
// (CommitGate=="") when this set records none (including when the set
// itself is nil). Safe on a nil receiver so callers never need a guard.
func (s CommitDecisionDigestSet) For(subject SubjectRef) CommitDecisionDigest {
	if s == nil {
		return CommitDecisionDigest{}
	}
	return s[SubjectMapKey(subject)]
}

// Record stores digest for subject. A no-op on a nil map, matching
// CommitBasisSet.Record.
func (s CommitDecisionDigestSet) Record(subject SubjectRef, digest CommitDecisionDigest) {
	if s == nil {
		return
	}
	s[SubjectMapKey(subject)] = digest
}

// ResetTo makes s hold exactly other's entries, discarding whatever it held
// before -- CommitBasisSet.ResetTo's own reason applies identically here:
// the CHAOS-3896 evidence-census second pass RE-DECIDES the whole
// resolution rather than amending it, so a merge would leave a digest
// recorded for a subject the second decision no longer commits. A no-op on
// a nil receiver, matching Record.
func (s CommitDecisionDigestSet) ResetTo(other CommitDecisionDigestSet) {
	if s == nil {
		return
	}
	for key := range s {
		delete(s, key)
	}
	for key, digest := range other {
		s[key] = digest
	}
}
