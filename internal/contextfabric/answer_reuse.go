package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CanonicalizeQuestion reduces a caller's question text to the form
// answer-reuse hashing binds to (CHAOS-3782, TRD §19.7.2, AC-3782-5). The
// transform is deliberately narrow and fully deterministic:
//
//  1. Leading/trailing whitespace is trimmed.
//  2. Every internal run of whitespace (spaces, tabs, newlines) collapses to
//     one ASCII space.
//  3. The text is lowercased with strings.ToLower -- a simple byte/rune
//     case fold, not a locale-aware collation. This is a known, accepted
//     limitation: two questions that differ only by a locale-specific
//     case rule (e.g. Turkish dotless i) are not guaranteed to canonicalize
//     identically. Documented here rather than silently assumed.
//  4. Trailing punctuation is stripped, together with any whitespace that
//     stripping exposes, repeated to a fixed point -- so "done?", "done ?",
//     and "done?!" all canonicalize to "done".
//
// Nothing else changes: no stemming, no synonym folding, no internal
// punctuation removal, no stop-word dropping. AC-3782-5's second half --
// "two questions that differ in any word do not [hash the same]" -- depends
// on this staying narrow. Widening it (e.g. folding "backend" and "back
// end" together) would be a reuse-correctness change, not a cosmetic one,
// and needs its own acceptance criterion.
func CanonicalizeQuestion(question string) string {
	collapsed := collapseInternalWhitespace(strings.TrimSpace(question))
	lowered := strings.ToLower(collapsed)
	return stripTrailingPunctuation(lowered)
}

// QuestionHash returns the reuse-key hash of question: the SHA-256 digest,
// hex-encoded, of CanonicalizeQuestion(question). Two questions produce the
// same hash if and only if they canonicalize identically.
func QuestionHash(question string) string {
	sum := sha256.Sum256([]byte(CanonicalizeQuestion(question)))
	return hex.EncodeToString(sum[:])
}

func collapseInternalWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastWasSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				b.WriteByte(' ')
			}
			lastWasSpace = true
			continue
		}
		lastWasSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// isReuseTerminalPunctuation is the CLOSED set stripTrailingPunctuation
// removes: the ASCII sentence/clause terminators a caller's own keyboard
// naturally ends a question with. Codex round-1 F7: the prior
// implementation used unicode.IsPunct, which also matches '#', '@', '&',
// '%', '*', '_', and '-' -- so "is this C#?" and "is this C" canonicalized
// identically, silently merging materially different questions. Only
// widen this set with a corresponding AC-3782-5 test case proving the
// new character still only affects a genuinely-equivalent trailing
// position, never a word boundary.
func isReuseTerminalPunctuation(r rune) bool {
	switch r {
	case '.', '?', '!', ',', ';', ':':
		return true
	default:
		return false
	}
}

// stripTrailingPunctuation removes trailing terminal punctuation (see
// isReuseTerminalPunctuation) and any whitespace it exposes, to a fixed
// point, so multiple trailing marks (optionally interspersed with
// whitespace, e.g. "done ? !") reduce the same as one ("done?!") or none
// ("done"). Punctuation outside the closed set (e.g. a trailing '#' or
// '@mention') is never touched.
func stripTrailingPunctuation(s string) string {
	for {
		next := strings.TrimRightFunc(s, isReuseTerminalPunctuation)
		next = strings.TrimRightFunc(next, unicode.IsSpace)
		if next == s {
			return next
		}
		s = next
	}
}

// reuseCurrentAxisKey is the TimeAxisKey value for a current-axis
// question. It is a FIXED LITERAL, and that is load-bearing.
//
// The tempting alternative -- deriving the key from the current wall clock
// so a "now" question is keyed to its own instant -- would make every
// current-axis key unique, so no two requests could ever share one. Answer
// reuse would silently drop to a zero hit rate while every CHAOS-3782 test
// kept passing, because each of those reuses within a single key it
// computed once. The failure would surface only as an unexplained cost and
// latency regression in production.
//
// A current-axis question means "as of whenever you answer this", and the
// staleness window (condition 4) plus the watermark check (condition 3)
// already bound how old that answer may be. The time axis contributes no
// additional identity here, so it must contribute a constant.
const reuseCurrentAxisKey = "current"

// TimeAxisKeyFor canonicalizes a time context into the ReuseKey dimension
// that keeps two different as-of questions apart (CHAOS-3781). See
// ReuseKey.TimeAxisKey for why this exists and why it is not folded into
// QuestionHash.
//
// Instants are rendered as epoch NANOSECONDS, never as a formatted
// timestamp: the same instant must produce the same key byte-for-byte, and
// a formatted string does not guarantee that (time.Format trims trailing
// zeros, so one instant has several textual renderings). It also matches
// the `_ns` convention AC-3781-7 requires for every other temporal
// comparison in this system.
//
// The caller passes the CLAMPED EFFECTIVE context, never the raw wire one
// (CHAOS-3781 round-3 F1). The distinction only bites for a request whose
// as_of sits in the future inside the skew tolerance, where the clamp
// pulls it back to `now`:
//
//   - Wire keying made the same as_of key identically at every arrival, so
//     a request arriving at or after that instant -- which is answered for
//     the instant itself -- hit a row whose answer had meant the earlier
//     clamped time. A stale answer served under a key that claimed
//     otherwise.
//   - Effective keying makes the key mean what the answer means.
//
// DECISION, not an oversight: this makes a future-dated-within-tolerance
// request key PER ARRIVAL, since `now` moves. That class therefore never
// reuses, and each request writes its own row. Accepted because the class
// is close to empty in practice -- a "now" question uses axis=current,
// whose key is the fixed literal below, and a real historical question
// carries a past as_of that never clamps -- while the alternative serves
// an answer whose effective time differs from the question asked, the
// exact defect class this issue exists to remove.
//
// The extra rows need no special handling: they are ordinary saved
// answers, subject to the same staleness window, watermark check and
// rebuild invalidation as every other row, so the growth is bounded by
// normal retention rather than accumulating.
//
// A historical context missing the bounds its own axis requires yields the
// empty string, which callers treat as "never reusable" -- fail closed,
// exactly like a punctuation-only question.
func TimeAxisKeyFor(timeContext TimeContext) string {
	switch timeContext.Axis {
	case TemporalCurrent:
		return reuseCurrentAxisKey
	case TemporalValidTime, TemporalObservedTime:
		if timeContext.AsOf == nil {
			return ""
		}
		return string(timeContext.Axis) + ":" + strconv.FormatInt(timeContext.AsOf.UTC().UnixNano(), 10)
	case TemporalRange:
		if timeContext.Start == nil || timeContext.End == nil {
			return ""
		}
		return string(timeContext.Axis) + ":" +
			strconv.FormatInt(timeContext.Start.UTC().UnixNano(), 10) + ":" +
			strconv.FormatInt(timeContext.End.UTC().UnixNano(), 10)
	default:
		return ""
	}
}

// maxReuseSubjectRecheckCount bounds how many distinct subjects tryReuse
// will re-authorize for one candidate. It matches the existing
// RequestedScope.SubjectHints wire bound (see Investigate's
// maxSubjectHints) -- a candidate naming more distinct subjects than a
// single request could ever legally hint at is treated as impossible to
// fully recheck, and answer reuse fails closed rather than checking only
// a subset.
const maxReuseSubjectRecheckCount = 50

// reuseRecheckOptions is the InvestigationOptions the condition-6 recheck
// uses for ResolveSubjects/DiscoverContext -- the contract's own MAXIMUM
// bound for every Max* field (ContextFabricInvestigationOptions.Validate,
// internal/contracts/v1/validate_context_fabric_request.go), never the
// live caller's own (possibly smaller) Options.
//
// Why this matters (flagged in review): graphrank truncates its final
// admitted set to Options.MaxRelationshipPaths/MaxEvidenceRefs/
// MaxCohortMembers/MaxSubjectCandidates (see falkorgraph/reader.go and
// graphrank/discover.go, resolve.go). If the recheck used the CURRENT
// request's Options and those happened to be smaller than whatever
// Options generated the stored candidate, the recheck's discovered set
// could legitimately shrink relative to the original -- with the
// watermark and authorization both completely unchanged -- and the
// containment check below would then reject a candidate that is
// genuinely still fresh and visible. That is a spurious cache miss, not
// a wrong answer (the safe direction), but if it fires often it craters
// the reuse rate silently. Using the contract's ceiling here means the
// recheck's set can only be as large as or larger than what ANY legal
// original investigation could have produced, so it stops truncation
// itself from ever being the reason a recheck fails.
var reuseRecheckOptions = InvestigationOptions{
	MaxSubjectCandidates: 50, MaxCohortMembers: 250, MaxRelationshipPaths: 250,
	MaxDrivers: 50, MaxEvidenceRefs: 500, MaxSerializedBytes: 1 << 20,
	AllowClarification: true,
}

// AnswerReuseOutcome classifies why one Investigate call did or did not
// reuse a stored result (CHAOS-3782, AC-3782-8). A closed set of fixed
// labels -- content-safe by construction, never free text -- so a
// dashboard can tell "reuse rate looks low because of authorization
// churn" apart from "reuse rate looks low because containment keeps
// failing" (the latter usually means the recheck's own bounds, not real
// staleness, are the problem -- see reuseRecheckOptions).
type AnswerReuseOutcome string

const (
	// AnswerReuseHit: a stored result was served; zero model calls made.
	AnswerReuseHit AnswerReuseOutcome = "hit"
	// AnswerReuseMissNoCandidate: no gate candidate matched, or the
	// candidate failed one of AnswerReuseGate's own conditions
	// (1/2/3/4/5/7) -- ReuseGate is nil, the question hash/org/contract/
	// projection/model-identity didn't match anything, a source
	// watermark advanced, or the candidate fell outside the staleness/
	// invalidation window.
	AnswerReuseMissNoCandidate AnswerReuseOutcome = "miss_no_candidate"
	// AnswerReuseMissAuthorization: a candidate was found, but a subject
	// it names no longer resolves under current authorization (or the
	// recheck itself could not be completed).
	AnswerReuseMissAuthorization AnswerReuseOutcome = "miss_authorization"
	// AnswerReuseMissEvidenceContainment: the subject recheck passed, but
	// an evidence reference the candidate cites was not present in a
	// freshly discovered evidence set (or the recheck itself could not
	// be completed).
	AnswerReuseMissEvidenceContainment AnswerReuseOutcome = "miss_evidence_containment"
	// AnswerReuseMissStaleGraphEpoch (CHAOS-3898 v4.1 F5): FindReusable
	// found a row matching every OTHER reuse dimension, but it was
	// generated under a different graph_epoch than this investigation's
	// own ResolvedGraphBinding -- a build/flip, not staleness, invalidated
	// it. See ReuseMissReason's doc comment.
	AnswerReuseMissStaleGraphEpoch AnswerReuseOutcome = "stale_graph_epoch"
	// AnswerReuseMissGraphNotProjected (CHAOS-4077): the stored candidate
	// is a never-projected-org no_match (SubjectResolution.GraphNotProjected).
	// Always a miss, checked BEFORE reuseAuthorizationStillHolds' own
	// zero-subjects/zero-evidence "nothing to recheck, trivially still
	// valid" shortcut can ever fire for it: that shortcut is correct for
	// an ORDINARY confirmed-empty result (nothing about reality changed),
	// but wrong here -- the org's graph may have been projected for the
	// first time since this candidate was stored, and the identical empty
	// resolution it carries is no longer evidence the graph still doesn't
	// exist. Forcing a miss makes every reuse attempt re-resolve fresh,
	// which is what discovers real candidates the moment a projector
	// finally runs, rather than serving the same cached "no match"
	// indefinitely.
	AnswerReuseMissGraphNotProjected AnswerReuseOutcome = "graph_not_projected"
	// AnswerReuseHitDegraded (CHAOS-4831): a stored result was served,
	// but the authorization recheck could not prove every reference the
	// stored payload carried is still visible, so the unprovable ones
	// were REMOVED from the served copy and the narrowing was disclosed
	// on coverage. Still a hit -- zero model calls -- and deliberately
	// NOT reported as one: a degraded serve routinely removes more
	// references than the answer itself cites, and a dashboard that
	// counted it as a clean hit would hide that entirely.
	AnswerReuseHitDegraded AnswerReuseOutcome = "hit_degraded"
	// AnswerReuseMissRecheckUnavailable (CHAOS-4831, CHAOS-4822 class):
	// the containment recheck could not be COMPLETED -- the fresh
	// discovery errored -- so nothing was proven either way. Split out of
	// miss_evidence_containment, which used to carry both this and a
	// genuine refusal; they mean opposite things (one is "we looked and
	// the evidence is gone", the other is "we could not look"), and
	// conflating them is exactly the undifferentiated-miss defect the
	// sibling telemetry ticket exists for.
	AnswerReuseMissRecheckUnavailable AnswerReuseOutcome = "miss_recheck_unavailable"
	// AnswerReuseMissDegradeInvalid (CHAOS-4831): a partial miss was
	// degradable in principle, but the stripped payload no longer
	// satisfies the stored-result contract, so reuse refused rather than
	// serve a malformed answer. A narrowed answer is useful; an invalid
	// one is a 500 for the caller and a silent contract break for every
	// consumer.
	AnswerReuseMissDegradeInvalid AnswerReuseOutcome = "miss_degrade_invalid"
)

// tryReuse implements CHAOS-3782 (TRD §19.7). It is called from
// Investigate BEFORE QuestionInterpreter.Interpret -- see the call site's
// comment for why that ordering is what makes AC-3782-1's zero-model-call
// guarantee hold. ok=false covers every way a reuse attempt can fail to
// pan out (no ReuseGate configured, no candidate found, a candidate that
// failed the gate's own conditions 1-5/7, or a candidate that failed the
// condition-6 authorization recheck here) -- Investigate always falls
// through to a fresh investigation in that case; ok=false is never an
// error.
// binding is the CHAOS-3898 §2.1 ResolvedGraphBinding Investigate resolved
// ONCE, immediately before this call -- see ResolvedGraphBinding's own doc
// comment. It feeds ReuseKey.GraphEpoch below (§2.3) and is threaded
// unchanged into reuseAuthorizationStillHolds' own recheck graph calls, so
// a reuse hit and the fresh investigation it would otherwise have run are
// always evaluated against the SAME graph.
// windowKey is CHAOS-3900 W1's own request-side window canonicalization
// key fragment (requestWindowCanonicalization.KeyComponent, window.go) --
// "" when no question_stated/clarification_confirmed window is in play for
// this request. Composed onto the axis key via composeTimeAxisKey, never
// folded into TimeAxisKeyFor itself, so every OTHER caller of
// TimeAxisKeyFor (tests, other packages) is unaffected.
// windowKeyEncoding is the SAME encoding requestWindowCanonicalization.KeyEncoding
// carries for windowKey -- threaded through so the recheck below can
// re-derive a candidate's OWN key using this request's own TRUSTED,
// in-process encoding choice, never the candidate's stored (untrusted)
// Provenance. Meaningless when windowKey == "".
func (e *Engine) tryReuse(ctx context.Context, principal storage.Principal, request InvestigationRequest, effectiveTimeContext TimeContext, windowKey string, windowKeyEnc windowKeyEncoding, binding ResolvedGraphBinding) (InvestigationResult, bool) {
	if e.reuseGate == nil {
		return InvestigationResult{}, false
	}
	if CanonicalizeQuestion(request.Question) == "" {
		// Codex round-2 finding #4: a punctuation-only (or otherwise
		// entirely-stripped) question canonicalizes to the empty string,
		// so every such question would share ONE hash -- "?", "!!", and
		// "?!?" are unrelated questions that must never be treated as the
		// same one. Fail closed: never even attempt a lookup. See
		// reuseColumnsFor's matching guard on the save side.
		e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
		return InvestigationResult{}, false
	}
	modelIdentities := e.reuseModelIdentities
	if e.reuseModelIdentityResolver != nil {
		// Codex round-2 finding #3 (chain widened by CHAOS-3786): resolve
		// the org-EFFECTIVE chain now, not the single static chain fixed
		// at engine-construction time -- see ReuseModelIdentityResolver's
		// doc comment for the per-organization staleness bug a static
		// chain causes, and for why this is a chain (primary + fallback)
		// rather than one identity. A resolve failure (e.g. a BYO
		// configuration that exists but no longer decrypts) is treated
		// exactly like "no candidate found": fail closed, fall through to
		// a fresh investigation, never guess.
		resolved, err := e.reuseModelIdentityResolver.ResolveReuseModelIdentity(ctx, principal.OrgID)
		if err != nil {
			e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
			return InvestigationResult{}, false
		}
		modelIdentities = resolved
	}
	// CHAOS-3781 round-3 F1: the axis key is the CLAMPED EFFECTIVE
	// context, passed in explicitly, and Save keys on the identical
	// value. Round-2 F2 established that both sides must agree; round 3
	// moved both to the effective value, because the wire value does not
	// describe what the answer means once clamping has moved it. See
	// TimeAxisKeyFor for the per-arrival cost this accepts and why.
	//
	// tryReuse runs BEFORE Interpret -- the whole mechanism behind
	// AC-3782-1's zero-model-call guarantee -- so no interpreted axis
	// exists here yet, which is also why Save keys on the clamped REQUEST
	// context rather than the clamped interpreted one.
	//
	// Save keys the same way, from the same clamped effective context,
	// rather than from the interpreted result. The two sides MUST agree.
	// When Save keyed from the interpretation, an
	// interpreter reading a current-axis request as historical saved
	// under a historical key that this lookup -- keyed "current" -- could
	// never find, so that entire class of question reused nothing, and
	// nothing surfaced the miss.
	//
	// The key's job is REQUEST identity, not interpretation identity.
	// Interpretation is still proved to match before anything is served:
	// condition 6 re-resolves every subject against the candidate's own
	// stored Interpretation.
	timeAxisKey := composeTimeAxisKey(TimeAxisKeyFor(effectiveTimeContext), windowKey)
	if timeAxisKey == "" {
		// A historical context missing its own required bounds. Fail
		// closed rather than key it as anything -- see TimeAxisKeyFor.
		e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
		return InvestigationResult{}, false
	}
	key := ReuseKey{
		QuestionHash:      QuestionHash(request.Question),
		ContractVersion:   InvestigationResultSchemaV1,
		ProjectionVersion: e.reuseProjectionVersion,
		ModelIdentities:   modelIdentities,
		TimeAxisKey:       timeAxisKey,
		// CHAOS-3833: the SAME deployment-current values Save persists,
		// from the same EngineOptions field, so lookup and save cannot
		// drift within one process. Conjunctive equality on the gate side
		// means a pre-0014 row (NULL columns) or a row saved under
		// different embed-text/retrieval-policy semantics never matches.
		EmbedRetrievalIdentity: e.reuseRetrievalIdentity.EmbedRetrievalIdentity,
		RetrievalPolicyVersion: e.reuseRetrievalIdentity.RetrievalPolicyVersion,
		// CHAOS-3862: the SAME deployment-current pair Save persists, from
		// the same EngineOptions field, so lookup and save cannot drift
		// within one process -- mirroring the EmbedRetrievalIdentity/
		// RetrievalPolicyVersion comment immediately above. Conjunctive
		// equality means a pre-0015 row (NULL columns) or a row produced
		// under a different prompt version never matches.
		InterpretationPromptVersion: e.reusePromptVersions.InterpretationPromptVersion,
		SynthesisPromptVersion:      e.reusePromptVersions.SynthesisPromptVersion,
		// CHAOS-3862 round 2: same mirrored discipline, three MORE
		// deployment-current authorities.
		// CHAOS-3884: same mirrored discipline, one more version authority
		// (identity-term normalization -- see ReuseKey's own field doc
		// comment).
		IdentityNormalizationVersion: e.reuseVersionAuthorities.IdentityNormalizationVersion,
		// CHAOS-3900 W1: a FOURTEENTH conjunctive dimension, same mirrored
		// discipline -- see ReuseKey.WindowInferenceVersion's own field doc
		// comment.
		WindowInferenceVersion: e.reuseVersionAuthorities.WindowInferenceVersion,
		// CHAOS-4085: a FIFTEENTH conjunctive dimension, same mirrored
		// discipline -- and the one that makes the commit gate
		// cache-proof. This lookup runs before Interpret and before
		// synthesis, so without it a row saved under the old gate would be
		// served with its old Committed list intact. See
		// ReuseKey.CommitGateVersion's own field doc comment.
		CommitGateVersion: e.reuseVersionAuthorities.CommitGateVersion,
		// CHAOS-4398 PR3 (R4 ruling): a SIXTEENTH conjunctive dimension,
		// same mirrored discipline -- see
		// ReuseKey.RankingFormulaVersion's own field doc comment for why
		// RankCohort running after this lookup makes fencing on it
		// mandatory, not optional.
		RankingFormulaVersion: e.reuseVersionAuthorities.RankingFormulaVersion,
		// CHAOS-4634 (S4): a SEVENTEENTH conjunctive dimension, same
		// mirrored discipline -- see ReuseKey.QuestionFamilyVersion's own
		// field doc comment for why the family gate running before this
		// lookup exists makes fencing on it mandatory.
		QuestionFamilyVersion:    e.reuseVersionAuthorities.QuestionFamilyVersion,
		QueryVersion:             e.reuseVersionAuthorities.QueryVersion,
		CanonicalServiceVersion:  e.reuseVersionAuthorities.CanonicalServiceVersion,
		ModelOutputSchemaVersion: e.reuseVersionAuthorities.ModelOutputSchemaVersion,
		// CHAOS-3898 §2.3: the SAME binding Investigate resolved before
		// this call. A stored candidate matches only if it was generated
		// under this exact active graph epoch.
		GraphEpoch: binding.Epoch,
	}
	candidate, ok, missReason, err := e.reuseGate.FindReusable(ctx, principal, key)
	if err != nil || !ok {
		outcome := AnswerReuseMissNoCandidate
		if missReason == ReuseMissStaleGraphEpoch {
			outcome = AnswerReuseMissStaleGraphEpoch
		}
		e.recordReuseOutcome(ctx, principal, outcome)
		return InvestigationResult{}, false
	}
	if err := ctx.Err(); err != nil {
		return InvestigationResult{}, false
	}
	// CHAOS-3900 W1 (codex review, rounds 2-5, consolidated round 5): a
	// window-keyed lookup (windowKey != "") must never serve a candidate
	// whose OWN stored content disagrees with what the window fragment in
	// the key claims. Re-deriving the candidate's OWN key with THIS
	// request's OWN TRUSTED, in-process windowKeyEnc (never inferred from
	// the candidate's stored -- and therefore untrusted -- Provenance
	// field; round 5's own finding on why that inference was unsound) and
	// comparing it against windowKey byte-for-byte is what closes every
	// prior round's gap: two DIFFERENT windows (e.g. trailing_30d vs
	// trailing_90d), two DIFFERENT frozen intervals sharing one
	// RelativeID, and a candidate whose Provenance disagrees with its own
	// actual content can never collide here. Every FRESH save this
	// package's own write path produces already guarantees axis and
	// window agree with the key it was saved under (canonicalizeEvidenceWindow
	// only ever resolves a request-side Effective window against a
	// current-axis request and always derives KeyComponent from that SAME
	// Effective value and encoding; windowVetoResult's own window_axis_conflict
	// veto keys its Save on the INTERPRETED context -- never the window-
	// fragment key -- whenever Interpret disagrees) -- but that guarantee
	// is a property of window.go's write path, not of this Store. Any row
	// this Store might ever hold for a reason this package's own write
	// path did not itself produce (e.g. a differently-ruled binary, a
	// direct write, an earlier deploy of code a fix has since corrected)
	// is caught HERE instead of trusted blind, mirroring
	// reuseAuthorizationStillHolds' own "prove it, don't assume it"
	// discipline for subjects/evidence. Fails exactly like an ordinary
	// no-candidate miss -- never an error, always falls through to a
	// fresh investigation.
	if windowKey != "" {
		if candidate.Interpretation.TimeContext.Axis != TemporalCurrent || candidate.EffectiveEvidenceWindow == nil {
			e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
			return InvestigationResult{}, false
		}
		if windowKeyComponent(*candidate.EffectiveEvidenceWindow, windowKeyEnc) != windowKey {
			e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
			return InvestigationResult{}, false
		}
	}
	// CHAOS-4040 (sol-max ruling 2026-08-21): a DECISIVE candidate carrying
	// an inferred (unconfirmed) window predates this ticket's gate -- no
	// FRESH Save can ever produce that combination any more
	// (windowConfirmationRequiredResult intercepts every inferred window
	// before a decisive terminal is ever reached, window.go). Rejected
	// UNCONDITIONALLY, not only inside the windowKey!="" branch above:
	// windowKey=="" is the ORDINARY no-explicit-window request (the exact
	// shape most likely to have matched one of these old rows on every
	// OTHER reuse-key dimension), and that is precisely the case the
	// windowKey!="" guard above never even inspects the candidate's own
	// window for. Checked on candidate.Status directly (not re-derived
	// from any local classification helper this package does not already
	// have) -- Complete/Partial/Degraded are the three decisive
	// (answer-bearing) statuses; ClarificationRequired/NoMatch are not,
	// and a non-decisive candidate could never have been influenced by an
	// unconfirmed window's own fact-retrieval bound in the first place.
	if candidate.EffectiveEvidenceWindow != nil && candidate.EffectiveEvidenceWindow.Provenance == WindowInferredDefault {
		switch candidate.Status {
		case InvestigationComplete, InvestigationPartial, InvestigationDegraded:
			e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
			return InvestigationResult{}, false
		}
	}
	verdict := e.reuseAuthorizationStillHolds(ctx, principal, request, candidate, binding)
	if verdict.Refused {
		e.recordReuseOutcome(ctx, principal, verdict.Outcome)
		e.recordReuseContainment(ctx, principal, verdict, reuseStripCounts{}, "")
		return InvestigationResult{}, false
	}
	// CHAOS-4831 (chris's ruling R1): the recheck covers everything a
	// reused response will SERVE, and a PARTIAL miss degrades instead of
	// refusing. Refusal is reserved for a missing TOP-LEVEL citation,
	// which verdict.Refused already carries. Everything still unproven
	// here is auxiliary, and it is REMOVED before the answer is served --
	// never served unchecked. That is the invariant, enforced by
	// construction rather than by remembering to strip: see
	// answer_reuse_degrade.go's file comment.
	reuseOutcome := AnswerReuseHit
	stripCounts := reuseStripCounts{}
	disclosure := reuseDegradeDisclosure("")
	if len(verdict.Partition.Missing) > 0 {
		degraded, counts, degradeDisclosure, ok := degradeReusedResult(candidate, verdict.Partition.Missing)
		if !ok {
			e.recordReuseOutcome(ctx, principal, AnswerReuseMissDegradeInvalid)
			e.recordReuseContainment(ctx, principal, verdict, counts, degradeDisclosure)
			return InvestigationResult{}, false
		}
		candidate = degraded
		stripCounts = counts
		disclosure = degradeDisclosure
		reuseOutcome = AnswerReuseHitDegraded
	}
	e.recordReuseContainment(ctx, principal, verdict, stripCounts, disclosure)
	// The candidate is served EXACTLY as stored -- same ResultID,
	// RequestID, and GeneratedAt (AC-3782-2: those name the reused
	// result's own identifier and generation time, not this call's).
	// Reused is per-serving metadata, set only on this in-memory copy;
	// nothing about the stored row is touched.
	candidate.Reused = true
	e.recordReuseOutcome(ctx, principal, reuseOutcome)
	// CHAOS-3888: telemetry-only -- candidate.RequestID (served to the
	// caller, unchanged, per AC-3782-2 above) is deliberately NOT touched
	// here; this only reports whether it differs from request.RequestID,
	// THIS call's own id, so an operator can tell "the response's
	// RequestID names an old investigation" apart from a bug without
	// reading the response body at all.
	if e.telemetry != nil {
		e.telemetry.RecordAnswerReuseServedRequestID(ctx, principal, candidate.RequestID, candidate.RequestID != request.RequestID)
	}
	return candidate, true
}

func (e *Engine) recordReuseOutcome(ctx context.Context, principal storage.Principal, outcome AnswerReuseOutcome) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordAnswerReuse(ctx, principal, outcome)
}

// reuseAuthorizationStillHolds implements TRD §19.7.3 condition 6 --
// current authorization for every subject and evidence reference in the
// stored result -- using only GraphReader's two existing methods
// (ResolveSubjects, DiscoverContext). No new port or graph query surface
// is needed: both calls are graph reads, never model calls, so this stays
// inside AC-3782-1's zero-model-call bound. On failure it returns
// (false, reason) so tryReuse's telemetry can tell an authorization
// rejection apart from a containment rejection (AC-3782-8) -- see
// AnswerReuseOutcome's doc comment for why that split matters
// diagnostically. Both legs use reuseRecheckOptions, the contract's
// ceiling bounds, not the live caller's own Options -- see that var's doc
// comment for why using the caller's (possibly smaller) Options here
// would risk a truncation-induced false miss.
//
// Subject leg: every subject the candidate ever names is re-resolved
// through ResolveSubjects with an exact SubjectHint, the same mechanism
// resolvePriorSubjectHints already relies on to re-authorize a prior
// turn's committed subject -- a subject that no longer resolves (deleted,
// or the caller's authorization narrowed) fails the recheck.
//
// Evidence-ref leg: DiscoverContext is re-run for the now-reauthorized
// subjects, using the candidate's OWN stored Interpretation (already
// computed -- no model call). If every condition-3 watermark is truly
// unchanged, the freshly discovered evidence set must contain everything
// the candidate cites; if authorization narrowed since the candidate was
// generated, DiscoverContext silently omits what is no longer visible,
// and the containment check below correctly fails closed.
// binding is the CHAOS-3898 §2.1 ResolvedGraphBinding tryReuse resolved
// once, before FindReusable. Both graph calls below MUST run against this
// SAME binding, never a freshly re-resolved one -- the serving decision
// (would this candidate be served) and the graph these calls read from must
// agree, or a recheck could pass against an epoch the candidate itself was
// never generated under.
func (e *Engine) reuseAuthorizationStillHolds(ctx context.Context, principal storage.Principal, request InvestigationRequest, candidate InvestigationResult, binding ResolvedGraphBinding) reuseRecheckVerdict {
	// CHAOS-4077: checked FIRST, before subjects/evidenceRefs are even
	// collected -- see AnswerReuseMissGraphNotProjected's own doc comment
	// for why the zero-subjects/zero-evidence shortcut below is unsafe
	// specifically for this candidate shape.
	if candidate.SubjectResolution.GraphNotProjected {
		return reuseRecheckVerdict{Refused: true, Outcome: AnswerReuseMissGraphNotProjected}
	}
	subjects := reuseSubjectsToRecheck(candidate)
	if len(subjects) > maxReuseSubjectRecheckCount {
		return reuseRecheckVerdict{Refused: true, Outcome: AnswerReuseMissAuthorization}
	}
	recheckRequest := request
	recheckRequest.Options = reuseRecheckOptions
	if len(subjects) > 0 {
		hints := make([]SubjectHint, 0, len(subjects))
		for _, subject := range subjects {
			hints = append(hints, SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "answer_reuse_authorization_recheck"})
		}
		recheckRequest.RequestedScope.SubjectHints = hints
	}

	// CHAOS-3900 P1.C: this recheck only re-verifies SUBJECT authorization
	// (the committed set below) -- it discards the structure-offer
	// material ResolveSubjects also returns, deliberately: a reuse
	// recheck never produces a NEW served result of its own (it either
	// confirms the candidate or misses), so there is nothing to attach
	// fresh offers to here.
	//
	// CHAOS-3900 P1.D: nil, not derived from any structureCanon -- and
	// provably always nil here, not merely defaulted: this recheck runs
	// only from inside tryReuse, and canonicalizeStructure's own
	// reuse-bypass (engine.go) means tryReuse is NEVER called at all
	// once a request has confirmed structure. A non-nil
	// ConfirmedExpectedKind reaching this call site would mean the
	// bypass itself had broken. CHAOS-4042: ConfirmedAnchorSelection is
	// nil here for the identical reason.
	// CHAOS-4085: the commit basis is discarded here on purpose. This
	// recheck asks only "is every subject the stored answer speaks for
	// still resolvable and still authorized for THIS principal" -- it never
	// produces a commit of its own. The stored answer's own commits were
	// already gated (by affirmation, or by a proven identity) when it was
	// synthesized and saved, and the reuse VERSION fence
	// (chaos4085CommitGateReuseVersion, chaos4085_commit_affirmation.go) is
	// what keeps a row saved BEFORE that gate existed from being served
	// through this path at all.
	// CHAOS-4087: the digest set is discarded here for the identical
	// reason CommitBasisSet is discarded immediately above -- this recheck
	// never produces a commit of its own, and the STORED result's own
	// digest (from the original synthesis) is exactly what a reuse hit is
	// for: it survives on the served row without this recheck needing to
	// reproduce it.
	// NO FRAME on the reuse recheck, deliberately. This call asks only
	// "are the stored answer's subjects still resolvable and still
	// authorized for THIS principal" -- it re-resolves a PRIOR turn's
	// subjects, and that turn's frame is not this turn's. Supplying the
	// current frame would hint the recheck's pool toward kinds the stored
	// answer was never about; supplying a reconstructed one would be the
	// re-derivation bar 5 forbids. A nil frame yields no kind hints, which
	// is the pre-seam-7 behaviour of this call unchanged.
	resolution, _, _, _, err := e.graph.ResolveSubjects(ctx, principal, recheckRequest, candidate.Interpretation, binding, nil, nil, nil, "")
	if err != nil {
		return reuseRecheckVerdict{Refused: true, Outcome: AnswerReuseMissAuthorization}
	}
	committed := make(map[string]struct{}, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		committed[subjectKeyForModel(subject)] = struct{}{}
	}
	for _, subject := range subjects {
		if _, ok := committed[subjectKeyForModel(subject)]; !ok {
			return reuseRecheckVerdict{Refused: true, Outcome: AnswerReuseMissAuthorization}
		}
	}

	evidenceRefs := reuseEvidenceRefsToRecheck(candidate)
	if len(evidenceRefs) == 0 {
		return reuseRecheckVerdict{Outcome: AnswerReuseHit}
	}
	graphContext, err := e.graph.DiscoverContext(ctx, principal, GraphDiscoveryRequest{
		Request: recheckRequest, Interpretation: candidate.Interpretation, Resolution: resolution, Binding: binding,
	})
	if err != nil {
		// Nothing was proven either way -- see
		// AnswerReuseMissRecheckUnavailable's own doc comment for why
		// this is no longer folded into the containment refusal.
		return reuseRecheckVerdict{Refused: true, Outcome: AnswerReuseMissRecheckUnavailable}
	}
	// CHAOS-4831: the visible set is collected from the fresh discovery
	// with the SAME traversal that collected the demanded set. It used to
	// be GraphContext.EvidenceRefIDs alone, which is the EDGE-attribute
	// closure -- so a subject candidate's NODE-attribute refs could never
	// be contained in it and containment failed with certainty for any
	// stored answer carrying candidates. See graphContextEvidenceSurface.
	partition := partitionMissingRefs(candidate, graphContext)
	verdict := reuseRecheckVerdict{ContainmentRan: true, Partition: partition, Outcome: AnswerReuseHit}
	if partition.MissingCitation {
		// A missing TOP-LEVEL citation is not a narrowing to disclose --
		// the answer's own cited evidence is gone, which makes it a
		// different answer. Refuse, exactly as before this ticket.
		verdict.Refused = true
		verdict.Outcome = AnswerReuseMissEvidenceContainment
	}
	return verdict
}

// reuseRecheckVerdict is what the condition-6 recheck reports. It states
// FACTS (what was proven visible, what was not, whether a citation was
// among the unproven) and leaves the serve/degrade/refuse decision to
// tryReuse, so the policy lives at one call site instead of being spread
// through the recheck's early returns.
type reuseRecheckVerdict struct {
	// Refused is set when reuse must not happen at all.
	Refused bool
	// Outcome is the telemetry value for this verdict.
	Outcome AnswerReuseOutcome
	// ContainmentRan reports whether the evidence leg executed. False
	// means Partition is meaningless (the subject leg refused first, or
	// there was nothing to recheck) -- not that containment passed.
	ContainmentRan bool
	Partition      reuseContainmentPartition
}

// recordReuseContainment emits the containment measurement: how many refs
// the stored payload demanded, how many the fresh discovery proved, how
// many could not be proven, and what the degrade removed. This is the
// instrument that lets a low reuse rate be ATTRIBUTED rather than guessed
// at -- the defect this ticket fixes was invisible for exactly as long as
// the only signal was one undifferentiated miss value.
func (e *Engine) recordReuseContainment(ctx context.Context, principal storage.Principal, verdict reuseRecheckVerdict, counts reuseStripCounts, disclosure reuseDegradeDisclosure) {
	if e.telemetry == nil || !verdict.ContainmentRan {
		return
	}
	e.telemetry.RecordAnswerReuseContainment(ctx, principal, AnswerReuseContainmentEvent{
		DemandedCount:     len(verdict.Partition.Demanded),
		VisibleCount:      verdict.Partition.VisibleCount,
		MissingCount:      len(verdict.Partition.Missing),
		MissingCitation:   verdict.Partition.MissingCitation,
		StrippedRefs:      counts.Refs(),
		StrippedLabels:    counts.StrippedLabels,
		DroppedCandidates: counts.DroppedCandidates,
		DroppedMembers:    counts.DroppedMembers,
		DroppedDrivers:    counts.DroppedDrivers,
		DroppedFindings:   counts.DroppedFindings,
		DroppedPaths:      counts.DroppedPaths,
		Disclosure:        string(disclosure),
	})
}

// reuseSubjectsToRecheck collects every distinct subject named anywhere in
// candidate -- committed and candidate subject resolutions, cohort
// members AND exclusions (an exclusion still discloses a subject's
// identity and the reason it was excluded, so it needs the same
// recheck), claimed facts, driver/finding affected subjects, and
// relationship path nodes.
func reuseSubjectsToRecheck(candidate InvestigationResult) []SubjectRef {
	seen := make(map[string]struct{})
	subjects := make([]SubjectRef, 0, len(candidate.SubjectResolution.Committed))
	add := func(subject SubjectRef) {
		key := subjectKeyForModel(subject)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		subjects = append(subjects, subject)
	}
	for _, subject := range candidate.SubjectResolution.Committed {
		add(subject)
	}
	for _, sc := range candidate.SubjectResolution.Candidates {
		add(sc.Subject)
	}
	if candidate.Cohort != nil {
		for _, member := range candidate.Cohort.Members {
			add(member.Subject)
		}
		for _, exclusion := range candidate.Cohort.Exclusions {
			add(exclusion.Subject)
		}
	}
	for _, claim := range candidate.ClaimedFacts {
		add(claim.Subject)
	}
	for _, driver := range candidate.Drivers {
		for _, subject := range driver.AffectedSubjects {
			add(subject)
		}
	}
	for _, findings := range [][]Finding{candidate.RemainingWork, candidate.ReadinessGaps, candidate.Conflicts} {
		for _, finding := range findings {
			for _, subject := range finding.Subjects {
				add(subject)
			}
		}
	}
	for _, path := range candidate.Paths {
		for _, subject := range path.Nodes {
			add(subject)
		}
	}
	return subjects
}

// reuseEvidenceRefsToRecheck collects every distinct evidence reference ID
// named anywhere in candidate -- the top-level citation set plus every
// nested occurrence (subject candidates, cohort members, drivers,
// findings, paths and their edges). This is the DEMANDED set: everything a
// reused payload would serve, and therefore everything the recheck is
// obliged to prove still visible.
//
// CHAOS-4831: the walk itself now lives in collectEvidenceRefs, shared
// with the VISIBLE side of the same check. It was previously duplicated,
// and the two copies covered different surfaces -- which is what made the
// containment check unsatisfiable on real data. A shared traversal makes
// that class of divergence unrepresentable rather than merely fixed.
func reuseEvidenceRefsToRecheck(candidate InvestigationResult) []string {
	return collectEvidenceRefs(resultEvidenceSurface(candidate))
}
