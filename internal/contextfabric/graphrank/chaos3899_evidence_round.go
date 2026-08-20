package graphrank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ShadowOutcome is the closed vocabulary CHAOS-3899's shadow evidence round
// decides -- WOULD have decided, since Slice A suppresses every one of
// these from ever reaching a real resolution decision (design brief v5 §6
// Slice A: "decisions suppressed... production outcomes byte-identical").
type ShadowOutcome string

const (
	ShadowWouldCommit  ShadowOutcome = "would_commit"
	ShadowWouldNoMatch ShadowOutcome = "would_no_match"
	ShadowWouldClarify ShadowOutcome = "would_clarify"
)

// EvidenceDiscriminator is the composed D (brief §1.2) for ONE hypothesized
// census kind: whichever keyed discriminator classes bound. Window is
// omitted for Slice 1 (D7: historical axis skipped loudly -- current-axis
// only, brief §2/§9).
type EvidenceDiscriminator struct {
	Kind CensusKind
	// HandleValue/HandleGrammar are set together; HandleValue is
	// in-process provenance ONLY (never traced -- corpus-safety rule).
	// HandleGrammar is the registry entry's own fixed name and IS safe to
	// trace.
	HandleBound   bool
	HandleValue   string
	HandleGrammar string
	AnchorBound   bool
	Anchor        AnchorBinding
}

// HasKeyedClass reports whether D contains >=1 keyed class (grammar-bound
// handle or unique-claimant anchor) -- brief D2(a)/§3(4): window+kind
// alone never proves a decisive outcome.
func (d EvidenceDiscriminator) HasKeyedClass() bool {
	return d.HandleBound || d.AnchorBound
}

// Identity is D's corpus-safe SHA-256 (brief §5: "D-identity as SHA-256")
// over its own keyed content -- kind, handle grammar name + VALUE, anchor
// kind + canonical id. This IS derived from handle/anchor text, but a
// one-way hash is exactly the corpus-safety discipline
// resolve.go's traceTermHash already uses for the identical reason (a
// reader can correlate repeat events for the SAME D without ever seeing
// what it was).
func (d EvidenceDiscriminator) Identity() string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\n", d.Kind)
	if d.HandleBound {
		fmt.Fprintf(h, "handle=%s:%s\n", d.HandleGrammar, d.HandleValue)
	}
	if d.AnchorBound {
		fmt.Fprintf(h, "anchor=%s:%s\n", d.Anchor.Kind, d.Anchor.CanonicalID)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])
}

// CensusOutcome is the shadow round's own copy of
// devhealthsource.CensusResult's fields. A separate type, not a shared one,
// deliberately: graphrank cannot import devhealthsource (devhealthsource
// already imports graphrank -- chaos3884_identity_universe.go -- so the
// reverse would cycle), and this package boundary already has a precedent
// for "the same shape, two names" (IdentityRow / AliasLookup's own
// claimantsByTerm). CensusFunc's caller (RunShadowEvidenceRound) is the
// ONE place a devhealthsource.CensusResult is translated into this type.
type CensusOutcome struct {
	Count               int
	CensusReadAt        time.Time
	SatisfierNaturalKey string
	ClosureMismatch     bool
	StatementCount      int
	RowsRead            int
	// SatisfierCanonicalID/SatisfierCanonicalIDs/SatisfierSetClosureMismatch
	// (CHAOS-3896 Slice B) are devhealthsource.CensusResult's own
	// SatisfierNaturalKey/SatisfierNaturalKeys/SatisfierSetClosureMismatch,
	// ALREADY BRIDGED to graph canonical ids by CensusFunc's own
	// implementation (devhealthsource.NewCensusFunc) before this struct is
	// ever built -- graphrank cannot call the bridge itself (see this
	// type's own doc comment on the import-cycle boundary), so a
	// CensusFunc implementation that wants Slice B's presentation ordering
	// to see anything MUST hand back already-resolved canonical ids here,
	// never a raw natural key. Consumed ONLY by
	// chaos3896_slice_b_presentation.go's survivors-first reorder --
	// in-process only, deliberately NEVER copied into ResolutionTraceEvent
	// by this file's own emit closure (see TestEmitNeverTracesSurvivorData,
	// chaos3896_slice_b_presentation_test.go).
	SatisfierCanonicalID        string
	SatisfierCanonicalIDs       []string
	SatisfierSetClosureMismatch bool
}

// CensusFunc is the shadow round's census execution dependency -- injected
// exactly like ResolveDeps.AliasLookup/Search (nil by default, no effect on
// any caller that does not set it). A production wiring passes
// devhealthsource.RunCensus (adapted to this signature); a measurement
// harness or unit test passes a fake.
type CensusFunc func(ctx context.Context, orgID string, kind CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool) (CensusOutcome, error)

// KindAttestation is one census kind's own receipt within a round-wide
// Attestation (brief §1.3(3): "Per-kind, never aggregated across kinds").
type KindAttestation struct {
	Kind CensusKind
	// Complete is false whenever this kind's OWN census errored -- brief
	// §1.3(4)'s per-kind AND: "any kind erroring, over budget, or outside
	// the census-kind registry poisons the whole".
	Complete        bool
	Count           int
	CensusReadAt    time.Time
	ClosureMismatch bool
	Protocol        string // "aggregate_first" -- brief's pin, unconditional for every executed census
	StatementCount  int
	RowsRead        int
	HandleApplied   bool
	AnchorApplied   bool
	// SatisfierCanonicalID/SatisfierCanonicalIDs/SatisfierSetClosureMismatch
	// (CHAOS-3896 Slice B) mirror CensusOutcome's own fields of the same
	// name -- see that type's doc comment. In-process only, deliberately
	// NEVER traced (see this file's own emit closure and
	// TestEmitNeverTracesSurvivorData).
	SatisfierCanonicalID        string
	SatisfierCanonicalIDs       []string
	SatisfierSetClosureMismatch bool
}

// Attestation is the shadow round's own per-resolution artifact (brief
// §1.4/§6).
//
// STALENESS NOTE (codex xhigh review finding, CHAOS-3918, 2026-08-19):
// this paragraph used to say "SHADOW ONLY: never consumed by any
// commit-path decision" -- true through Slice A/B, but CHAOS-3896 Slice C
// (merged to main mid-flight on this ticket) now LIVE-CONSUMES this exact
// type via mergeCensusAttestedSatisfier/attestedSatisfier (resolve.go,
// chaos3896_slice_c_evidence_census.go) for its evidence_census commit
// gate -- see RunShadowEvidenceRound's own doc comment for the fuller
// account. What stays true, and is the load-bearing guarantee for
// CHAOS-3918's own widening: this struct gained ZERO new fields from that
// ticket, so nothing it added is reachable from that (or any other)
// consumer -- the widening's own result reaches ONLY ResolutionTracer's
// evidence_source_native/evidence_source_native_probe stage events,
// entirely outside this type.
type Attestation struct {
	RequestID string
	Outcome   ShadowOutcome
	// Reason is populated for a would_clarify/would_no_match-adjacent
	// refusal that names a specific closed-vocabulary DegradationReason
	// (brief §4). Empty for would_commit, and for a would_clarify decided
	// by ordinary genuine ambiguity (more than one kind or claimant
	// surviving) rather than a specific refusal.
	Reason DegradationReason
	// Protocol is always "aggregate_first" once the round actually reaches
	// at least one per-kind census (brief §1.3(2) pin) -- empty if the
	// round refused before any census ran.
	Protocol string
	// NonCensusedSurvivor is brief §3(2)'s own structural rule, kept as a
	// dedicated bool rather than overloaded onto Reason (the closed §4
	// vocabulary has no single token for it): a pooled hypothesis of a
	// kind OUTSIDE the census registry survived, so no_match can never be
	// this round's outcome -- "the proof cannot speak for kinds it did not
	// enumerate."
	NonCensusedSurvivor bool
	// UnscopedVisibility gates census EXECUTION itself (brief §1.3(5)): a
	// scoped caller gets Outcome=would_clarify, Reason=scoped_visibility,
	// with NO source reads at all -- Kinds is always empty in that case.
	UnscopedVisibility bool
	// PreconditionUnproven marks a bridge-derived comparison (pool ∩
	// satisfiers, would-commit identity against the GRAPH) as unproven
	// pending CHAOS-3898's source<->graph injectivity delivery (brief
	// §1.4/§6/§9 risk 6). True whenever Outcome==ShadowWouldCommit --
	// naming a graph satisfier is exactly what 3898 blocks, and Slice A
	// performs no graph existence read at all (that is Slice C).
	//
	// SCOPE NOTE (adversarial review finding; the brief's own §6 Slice A
	// bullet lists this as shipping here): NO keyed graph existence read
	// is performed by this round, and ReasonGraphMissingSatisfier is
	// therefore never produced -- consequently the brief's "a
	// projection-lag fixture proving graph_missing_satisfier fires
	// (source row present, node absent)" is NOT included in this PR. This
	// is a deliberate, not-yet-built piece: the fixture's whole point is
	// to prove a keyed graph read fails closed on absence, and Slice A's
	// PreconditionUnproven marking already states that no such read
	// happens at all until CHAOS-3898 lands. Building the fixture now
	// would mean building (and then discarding, once the real bridge
	// exists) throwaway graph-read plumbing this slice explicitly defers
	// to Slice C -- follow-up, not a gap papered over.
	PreconditionUnproven bool
	// DIdentity is EvidenceDiscriminator.Identity() -- empty when the
	// round refused before D was ever fully composed (e.g. multi_handle,
	// scoped_visibility).
	DIdentity            string
	HandleGrammarBound   bool
	AnchorUniqueClaimant bool
	Kinds                []KindAttestation
}

// ShadowEvidenceRoundInput is everything RunShadowEvidenceRound needs,
// gathered at the resolution decision point (resolve.go). Deliberately a
// narrow, purpose-built struct rather than the full
// contextfabric.InvestigationRequest/SubjectResolution types, so this
// package's own signature stays stable as those contracts grow.
type ShadowEvidenceRoundInput struct {
	RequestID string
	// Question is request.Question, verbatim -- consumed by BindHandles
	// AND, as of CHAOS-3918's widening measurement, BindSourceNativeHandles
	// inside this call. Never stored on the returned Attestation, never
	// traced (corpus-safety rule).
	Question string
	OrgID    string
	// PooledKinds is the resolution's own surviving-hypothesis kinds
	// (resolution.Candidates' Subject.Kind set, deduplicated) -- brief
	// §1.2's "surviving-hypothesis kinds ∪ fact-requirement-implied
	// kinds". A kind here outside the census registry blocks would_no_match
	// (NonCensusedSurvivor) without stopping the round from evaluating
	// would_commit/would_clarify for the kinds it DOES cover.
	PooledKinds []CensusKind
	// CurrentAxis is true only for contextfabric.TemporalCurrent -- brief
	// D7: historical axis is skipped loudly in Slice 1.
	CurrentAxis bool
	// UnscopedVisibility is the SAME conjunct resolve.go's
	// scopesUnrestricted already computes for the CHAOS-3829 rescue --
	// reused here unchanged, not re-derived, so this can never drift from
	// what authorization actually enforces.
	UnscopedVisibility bool
	// AliasClaimants/AliasLookupComplete are the resolution's OWN
	// already-computed AliasLookup result (resolve.go's claimantsByTerm/
	// complete) -- BindAnchor reuses them rather than re-querying.
	AliasClaimants      map[string][]IdentityMatch
	AliasLookupComplete bool
	CensusFunc          CensusFunc
	// PreNarrowingExplicitKinds (CHAOS-3972 P3, design brief §2.0) is the
	// PRE-narrowing hypothesis kind-set the caller captured BEFORE
	// applying an EXPLICIT (non-receipt) kind narrowing to PooledKinds --
	// nil/empty whenever no such narrowing was applied this round (the
	// overwhelming common case, and what keeps this an exact no-op for
	// every existing caller: RECEIPT-confirmed narrowing, this field
	// unset, behaves byte-identically to before this ticket). Non-empty
	// requires this round's own would_commit/would_no_match outcome to
	// ADDITIONALLY pass kindInsensitivityProof re-run over this set before
	// it may stand -- see the decisive switch in RunShadowEvidenceRound.
	PreNarrowingExplicitKinds []CensusKind
}

// splitCensusKinds partitions kinds into the subset the closed census
// registry covers and reports whether any kind OUTSIDE it survived (brief
// §3(2)).
func splitCensusKinds(kinds []CensusKind) (censused []CensusKind, nonCensusedSurvivor bool) {
	seen := map[CensusKind]bool{}
	for _, kind := range kinds {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		if IsCensusKindRegistered(kind) {
			censused = append(censused, kind)
		} else {
			nonCensusedSurvivor = true
		}
	}
	return censused, nonCensusedSurvivor
}

// RunShadowEvidenceRound executes the FULL shadow round (design brief v5
// §6 Slice A): typed-grammar D, unique-claimant anchor, per-kind
// base-table source census, attestation.
//
// STALENESS NOTE (codex xhigh review finding, CHAOS-3918, 2026-08-19): the
// paragraph above used to say the returned Attestation is "NEVER consumed
// by any commit-path decision" -- true for Slice A alone, but CHAOS-3896
// Slice C (merged to main as this ticket's own work was in flight) now
// LIVE-CONSUMES this SAME Attestation via mergeCensusAttestedSatisfier/
// attestedSatisfier (resolve.go) for its evidence_census commit gate. This
// function's own decisive computation (the switch statement further down,
// and every field on the Attestation it returns) is UNCHANGED by that --
// Slice C added a consumer, not a producer-side behavior change. What
// stays true, and is the load-bearing guarantee for THIS ticket's own
// CHAOS-3918 widening (traceSourceNativeBinds, below): the Attestation
// type gained ZERO new fields from CHAOS-3918, and traceSourceNativeBinds
// is called for its trace side effect alone -- it returns nothing and
// writes into no local variable this function's decisive switch statement
// or attestedSatisfier() reads. tracer may be nil (matches every other
// optional ResolveDeps-adjacent dependency's convention) -- a nil tracer
// still gets a fully-computed Attestation back (useful for a direct unit
// test), it simply never emits.
//
// Non-vacuity (brief §6/§7's own acceptance bar -- "prove the round
// actually executed"): an evidence_round stage event fires on EVERY call
// that reaches past the axis/scope gates, including a refused one, and one
// evidence_probe event fires per kind actually censused -- so "the round
// ran but found nothing" and "the round never ran" are structurally
// distinguishable from the trace alone (ShadowKindsCensused==0 with a
// specific Reason, vs. no evidence_round event at all).
func RunShadowEvidenceRound(ctx context.Context, input ShadowEvidenceRoundInput, tracer ResolutionTracer) Attestation {
	emit := func(a Attestation) Attestation {
		a.RequestID = input.RequestID
		if tracer != nil {
			tracer.Trace(ResolutionTraceEvent{
				RequestID: input.RequestID, Stage: "evidence_round",
				ShadowOutcome: string(a.Outcome), ShadowReason: string(a.Reason),
				ShadowDIdentityHash:        a.DIdentity,
				ShadowPreconditionUnproven: a.PreconditionUnproven,
				ShadowUnscopedVisibility:   a.UnscopedVisibility,
				ShadowNonCensusedSurvivor:  a.NonCensusedSurvivor,
				ShadowHandleGrammarBound:   a.HandleGrammarBound,
				ShadowAnchorUniqueClaimant: a.AnchorUniqueClaimant,
				ShadowKindsCensused:        len(a.Kinds),
			})
			for _, k := range a.Kinds {
				// readAtUnix stays 0 (never time.Time{}.Unix()'s large
				// negative sentinel) for an incomplete/errored kind, which
				// never populated k.CensusReadAt in the first place
				// (adversarial review finding) -- 0 reads as "no receipt",
				// matching CensusComplete==false, rather than as a
				// confusing pre-1970 timestamp.
				readAtUnix := int64(0)
				if k.Complete {
					readAtUnix = k.CensusReadAt.Unix()
				}
				tracer.Trace(ResolutionTraceEvent{
					RequestID: input.RequestID, Stage: "evidence_probe",
					CensusKind: k.Kind, CensusComplete: k.Complete, CensusCount: k.Count,
					CensusReadAtUnix: readAtUnix, CensusProtocol: k.Protocol,
					CensusClosureMismatch: k.ClosureMismatch, CensusStatementCount: k.StatementCount,
					CensusRowsRead: k.RowsRead, CensusHandleApplied: k.HandleApplied, CensusAnchorApplied: k.AnchorApplied,
				})
			}
		}
		return a
	}

	if !input.CurrentAxis {
		return emit(Attestation{Outcome: ShadowWouldClarify, Reason: ReasonHistoricalAxisSkip})
	}
	if !input.UnscopedVisibility {
		// brief §1.3(5): NO source reads at all for a scoped caller.
		return emit(Attestation{Outcome: ShadowWouldClarify, Reason: ReasonScopedVisibility})
	}

	// CHAOS-3899 WIDENING measurement (chris-ratified pre-registered shadow
	// measurement, 2026-08-19): fired here, deliberately BEFORE the
	// multi-handle/no-discriminators/census-kind-unregistered branches
	// below and OUTSIDE every one of their return statements, so this
	// measurement's own two trace stages (evidence_source_native/
	// evidence_source_native_probe) are structurally incapable of altering
	// -- or even being read by -- ANY of Outcome/Reason/DIdentity/Kinds/
	// PreconditionUnproven/NonCensusedSurvivor below: no local variable
	// this call produces is referenced by any of the decisive code that
	// follows. See BindSourceNativeHandles' own doc comment
	// (chaos3899_source_native_grammar.go) for the full shadow-only
	// guarantee. No new source read: claimantsFromCandidateNodes(...) was
	// already computed by resolve.go for the anchor role, before this
	// function was ever called (ShadowEvidenceRoundInput.AliasClaimants).
	traceSourceNativeBinds(tracer, input.RequestID, BindSourceNativeHandles(input.Question, input.AliasClaimants, input.AliasLookupComplete))

	bound := BindHandles(input.Question)
	if IsMultiHandle(bound) {
		return emit(Attestation{Outcome: ShadowWouldClarify, Reason: ReasonMultiHandle, UnscopedVisibility: true})
	}
	var handle *BoundHandle
	if len(bound) == 1 {
		h := bound[0]
		handle = &h
	}
	anchor, anchorOK := BindAnchor(input.AliasClaimants, input.AliasLookupComplete)

	censusKinds, nonCensusedSurvivor := splitCensusKinds(input.PooledKinds)
	if handle != nil && IsCensusKindRegistered(handle.Kind) {
		censusKinds = appendUniqueCensusKind(censusKinds, handle.Kind)
	}

	base := Attestation{
		UnscopedVisibility: true, NonCensusedSurvivor: nonCensusedSurvivor,
		HandleGrammarBound: handle != nil, AnchorUniqueClaimant: anchorOK,
	}

	if handle == nil && !anchorOK {
		// D2(a): window+kind alone never proves a decisive outcome, and
		// there is no keyed class at all here (brief §3(4)).
		base.Reason = ReasonNoDiscriminators
		base.Outcome = ShadowWouldClarify
		return emit(base)
	}
	if handle != nil && !IsCensusKindRegistered(handle.Kind) {
		base.Reason = ReasonCensusKindUnregistered
		base.Outcome = ShadowWouldClarify
		return emit(base)
	}
	if len(censusKinds) == 0 {
		// An anchor bound but no census-registered kind is in play at all
		// (e.g. every pooled hypothesis, if any, is a non-censused kind,
		// and no handle named a census kind either).
		base.Reason = ReasonNoDiscriminators
		base.Outcome = ShadowWouldClarify
		return emit(base)
	}

	// D-identity is computed over whichever ONE discriminator this round
	// actually pins (brief's per-resolution D, not per-kind) -- the
	// handle's own kind when one is bound (it is the more specific keyed
	// class), otherwise the anchor alone, scoped to the first census kind
	// evaluated for determinism.
	identityKind := censusKinds[0]
	if handle != nil {
		identityKind = handle.Kind
	}
	d := EvidenceDiscriminator{Kind: identityKind, AnchorBound: anchorOK, Anchor: anchor}
	if handle != nil {
		d.HandleBound, d.HandleValue, d.HandleGrammar = true, handle.Value, handle.Grammar
	}
	base.DIdentity = d.Identity()

	var kindAttestations []KindAttestation
	complete := true
	satisfierKinds := 0
	multiSatisfierKinds := 0
	mismatch := false
	for _, kind := range censusKinds {
		handleApplies := handle != nil && handle.Kind == kind
		anchorApplies := anchorOK && kindHasAnchorFK(kind, anchor.Kind)
		if !handleApplies && !anchorApplies {
			// This particular pooled kind has no keyed predicate reaching
			// it -- it cannot be census-eliminated, the same structural
			// gap a non-censused-registry kind leaves (brief §3(2)'s own
			// rationale, applied one level down).
			base.NonCensusedSurvivor = true
			continue
		}
		outcome, err := input.CensusFunc(ctx, input.OrgID, kind,
			valueOr(handleApplies, handle), handleApplies, anchor.Kind, anchor.CanonicalID, anchorApplies)
		ka := KindAttestation{Kind: kind, Protocol: "aggregate_first", HandleApplied: handleApplies, AnchorApplied: anchorApplies}
		if err != nil {
			ka.Complete = false
			complete = false
		} else {
			ka.Complete = true
			ka.Count = outcome.Count
			ka.CensusReadAt = outcome.CensusReadAt
			ka.ClosureMismatch = outcome.ClosureMismatch
			ka.StatementCount = outcome.StatementCount
			ka.RowsRead = outcome.RowsRead
			// CHAOS-3896 Slice B: carried through for the presentation-only
			// reorder consumer; deliberately NOT read anywhere in this
			// function's own decisive outcome computation below (mismatch/
			// satisfierKinds/multiSatisfierKinds stay keyed off
			// outcome.ClosureMismatch/outcome.Count exactly as before this
			// slice).
			ka.SatisfierCanonicalID = outcome.SatisfierCanonicalID
			ka.SatisfierCanonicalIDs = outcome.SatisfierCanonicalIDs
			ka.SatisfierSetClosureMismatch = outcome.SatisfierSetClosureMismatch
			if outcome.ClosureMismatch {
				mismatch = true
			} else if outcome.Count == 1 {
				satisfierKinds++
			} else if outcome.Count > 1 {
				multiSatisfierKinds++
			}
		}
		kindAttestations = append(kindAttestations, ka)
	}
	base.Kinds = kindAttestations
	if len(kindAttestations) == 0 {
		base.Reason = ReasonNoDiscriminators
		base.Outcome = ShadowWouldClarify
		return emit(base)
	}
	base.Protocol = "aggregate_first"

	switch {
	case mismatch:
		base.Outcome = ShadowWouldClarify
		base.Reason = ReasonCensusClosureMismatch
	case !complete:
		base.Outcome = ShadowWouldClarify
		base.Reason = ReasonCensusError
	// !base.NonCensusedSurvivor gates BOTH terminal outcomes, not just
	// would_no_match (adversarial review finding): brief §1.3(4)'s
	// censusComplete is a per-kind AND over every HYPOTHESIZED kind, and a
	// kind this round never censused at all (outside the registry, or
	// pooled but reached by no keyed predicate) structurally cannot be
	// part of that AND -- so censusComplete is exactly as false for a
	// would-be commit as it is for a would-be no_match. §1.5: a
	// non-censused hypothesis is never eliminated and survives to
	// clarification, which applies regardless of what the censused kinds
	// themselves found.
	case satisfierKinds == 1 && multiSatisfierKinds == 0 && !base.NonCensusedSurvivor:
		base.Outcome = ShadowWouldCommit
		base.PreconditionUnproven = true
	case satisfierKinds == 0 && multiSatisfierKinds == 0 && !base.NonCensusedSurvivor:
		base.Outcome = ShadowWouldNoMatch
	default:
		// satisfierKinds>1, any multiSatisfierKinds, or a would-be
		// commit/no_match whose conclusion is blocked by a
		// non-censused-kind survivor (§3(2)/§1.3(4)) -- genuinely
		// ambiguous, no single closed-vocabulary reason token names this
		// case.
		base.Outcome = ShadowWouldClarify
	}
	// CHAOS-3972 P3 (design brief §2.0's kind-insensitivity rule -- the
	// P1.D hard precondition, wired here): a would_commit/would_no_match
	// outcome reached under EXPLICIT (non-receipt) kind narrowing
	// (input.PreNarrowingExplicitKinds non-empty) is trustworthy only if
	// the SAME census, re-run over the PRE-narrowing kind set, agrees --
	// exactly the hazard the rule exists to catch: a wrong narrowing
	// collapsing N>1 satisfiers to 1, or hiding a real satisfier the
	// narrowed set excluded. A RECEIPT-confirmed kind never reaches this
	// branch (PreNarrowingExplicitKinds stays empty for it, by
	// construction -- see runShadowEvidenceRoundForResolution, resolve.go):
	// caller authority may narrow without this extra proof, exactly like a
	// confirmed window.
	if len(input.PreNarrowingExplicitKinds) > 0 && (base.Outcome == ShadowWouldCommit || base.Outcome == ShadowWouldNoMatch) {
		proof := kindInsensitivityProof(ctx, input.OrgID, input.PreNarrowingExplicitKinds,
			valueOr(handle != nil, handle), handle != nil, anchor.Kind, anchor.CanonicalID, anchorOK, input.CensusFunc)
		sound := (base.Outcome == ShadowWouldCommit && proof == kindInsensitivityCommitSound) ||
			(base.Outcome == ShadowWouldNoMatch && proof == kindInsensitivityNoMatchSound)
		if !sound {
			base.Outcome = ShadowWouldClarify
			base.Reason = ReasonKindSensitiveOutcome
			// PreconditionUnproven's own doc comment: "True whenever
			// Outcome==ShadowWouldCommit" -- this branch just left that
			// outcome, so the invariant requires resetting it.
			base.PreconditionUnproven = false
		}
	}
	return emit(base)
}

// traceSourceNativeBinds fires the CHAOS-3899 widening measurement's two
// trace stages (chaos3899_source_native_grammar.go's own doc comment):
// one aggregate "evidence_source_native" event (mirrors "evidence_round"'s
// own non-vacuity proof -- fires unconditionally once the round reaches
// past its axis/scope gates, regardless of what binds is), plus one
// "evidence_source_native_probe" event per bind (mirrors "evidence_probe"'s
// own "per-kind, never aggregated" cardinality, one level down to
// "per grammar match"). A nil tracer is a no-op, identical to every other
// emit path in this file. This function's ONLY effect is calling
// tracer.Trace -- it returns nothing and touches no Attestation field, by
// construction (see this function's own call site's doc comment for why
// that placement is the structural shadow-only guarantee).
func traceSourceNativeBinds(tracer ResolutionTracer, requestID string, binds []SourceNativeBind) {
	if tracer == nil {
		return
	}
	anyResolved := false
	for _, b := range binds {
		if b.Resolved {
			anyResolved = true
			break
		}
	}
	tracer.Trace(ResolutionTraceEvent{
		RequestID: requestID, Stage: "evidence_source_native",
		ShadowSourceNativeMatchCount: len(binds), ShadowSourceNativeAnyResolved: anyResolved,
	})
	for _, b := range binds {
		event := ResolutionTraceEvent{
			RequestID: requestID, Stage: "evidence_source_native_probe",
			ShadowSourceNativeGrammar: b.Grammar, ShadowSourceNativeResolved: b.Resolved,
		}
		if b.Resolved {
			event.ShadowSourceNativeKind = b.Kind
		}
		tracer.Trace(event)
	}
}

func appendUniqueCensusKind(kinds []CensusKind, kind CensusKind) []CensusKind {
	for _, k := range kinds {
		if k == kind {
			return kinds
		}
	}
	return append(kinds, kind)
}

// kindHasAnchorFK reports whether kind's base table carries an OWN FK
// column for anchorKind (brief §1.3(1)) -- mirrors
// devhealthsource.censusKindRegistryEntries.anchorColumns without this
// package depending on that one (see CensusOutcome's own doc comment for
// why the boundary is duplicated rather than shared; a dedicated
// cross-package test, TestKindHasAnchorFKMatchesCensusRegistry in
// devhealthsource -- which CAN import graphrank -- pins the two against
// each other so a future edit to either side that forgets its mirror
// fails loudly instead of silently drifting).
//
// work_item deliberately has NO repository anchor (adversarial review
// finding, corrected alongside chaos3899_census_registry.go's own copy of
// this same fact): a Linear-sourced work item's repo_id is the zero UUID
// at ingest (tables.go's queryWorkItems doc comment), so a
// repository-anchored work_item census would return 0 for nearly every
// real Linear item -- a near-certain false would_no_match, not an edge
// case.
func kindHasAnchorFK(kind CensusKind, anchorKind contextfabric.SubjectKind) bool {
	return KindHasAnchorFK(kind, anchorKind)
}

// KindHasAnchorFK is kindHasAnchorFK's exported mirror -- exists solely so
// devhealthsource's own registry (censusKindRegistryEntries.anchorColumns,
// which devhealthsource CAN import this package to check, unlike the
// reverse) can be cross-tested against this package's copy of the same
// fact, the identical "cross-package registries must never silently drift"
// discipline graphrank.IsAliasLookupScopedKind already uses for
// devhealthsource.IdentityUniverse's own coverage check.
func KindHasAnchorFK(kind CensusKind, anchorKind contextfabric.SubjectKind) bool {
	switch kind {
	case contextfabric.SubjectPullRequest:
		return anchorKind == contextfabric.SubjectRepository
	case contextfabric.SubjectWorkItem:
		return anchorKind == contextfabric.SubjectProject
	default:
		return anchorKind == contextfabric.SubjectRepository
	}
}

func valueOr(applies bool, handle *BoundHandle) string {
	if applies && handle != nil {
		return handle.Value
	}
	return ""
}
