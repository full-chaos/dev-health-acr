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
	// AnchorReceiptConfirmed (CHAOS-4042, sol-max ruling) is true when the
	// round's own anchor discriminator came from ShadowEvidenceRoundInput.
	// ConfirmedAnchor (a redeemed ancr_ receipt), not from BindAnchor's
	// own question-derived unique-claimant scan. Deliberately a SEPARATE
	// field from AnchorUniqueClaimant, never both true at once for the
	// same round: AnchorUniqueClaimant asserts a specific proof (BindAnchor
	// ran and found exactly one claimant) a receipt-confirmed selection
	// never performs, so setting it here would be telemetry that lies
	// about which proof actually backed this round's outcome.
	AnchorReceiptConfirmed bool
	Kinds                  []KindAttestation
	// KindInsensitivityEvaluated/KindInsensitivityOutcome (CHAOS-4039/
	// sol-max ruling 2026-08-20): whether kindInsensitivityProof was
	// consulted this round, and its verdict when it was. See
	// ResolutionTraceEvent.ShadowKindInsensitivityEvaluated's own doc
	// comment (resolve.go) for why this is traced as a field distinct
	// from Outcome itself.
	KindInsensitivityEvaluated bool
	KindInsensitivityOutcome   kindInsensitivityOutcome
	// KindInsensitivityMode (CHAOS-4079) names WHICH explicit-kind
	// narrowing situation produced the two fields above -- empty when the
	// probe was not evaluated at all. Load-bearing for a consumer, not
	// decoration: only explicitKindNarrowingApplied ("narrowed") means the
	// verdict ATTESTS insensitivity across an actual change to the census
	// hypothesis set. An "observed_" mode means the census was never
	// narrowed, so the verdict is necessary-but-not-sufficient evidence
	// that the hint had no influence (see explicitKindNarrowingMode's own
	// doc comment, chaos3900_structure_offers.go).
	//
	// WRITE-FREEDOM (CHAOS-4079, the property this ticket exists to
	// preserve): this field and the two above are the COMPLETE set of
	// Attestation state the observation path may touch. None of the three
	// is read by any production consumer of an Attestation --
	// attestedSatisfier (chaos3896_slice_c_evidence_census.go) reads
	// Outcome/UnscopedVisibility/Kinds; SurvivorsFirstOrder and
	// ReorderingWasReachable (chaos3896_slice_b_presentation.go) read
	// Reason/Kinds -- so populating them cannot reach a commit decision.
	// TestObservedKindInsensitivityProbeIsWriteFree walks this struct with
	// reflect and fails on ANY other field differing, so a future field
	// added to the observation path fails loudly instead of silently
	// escaping the guarantee.
	KindInsensitivityMode explicitKindNarrowingMode
	// HandleInsensitivityEvaluated/HandleInsensitivityOutcome (CHAOS-4081,
	// team-lead ruling, path (a), 2026-08-25): whether/what a CONFIRMED
	// explicit subject_handle hint (ShadowEvidenceRoundInput.ConfirmedHandle)
	// would prove IN ISOLATION, over its own kind alone -- via the SAME
	// kindInsensitivityProof primitive CHAOS-3972 P3's explicit-kind-
	// narrowing branch already uses (chaos3900_structure_offers.go), scoped
	// to a single-kind set naming ONLY ConfirmedHandle.Kind.
	//
	// WRITE-FREE, mirroring KindInsensitivity*'s own guarantee immediately
	// above: computed in RunShadowEvidenceRound strictly AFTER Outcome/
	// Kinds/NonCensusedSurvivor are already final, reads none of them, and
	// is read by NOTHING production -- attestedSatisfier
	// (chaos3896_slice_c_evidence_census.go) reads Outcome/
	// UnscopedVisibility/Kinds only (this struct's own STALENESS NOTE), so
	// populating these two fields cannot reach a commit decision.
	// TestConfirmedHandleInsensitivityProbeIsWriteFree pins this the same
	// way TestObservedKindInsensitivityProbeIsWriteFree pins the
	// kind-insensitivity probe's own guarantee.
	//
	// This answers a NARROWER question than kind-insensitivity: "if this
	// EXPLICIT handle hint were trusted alone, would exactly one/zero
	// satisfier exist for its own kind" -- never "is the round's actual
	// decisive Outcome insensitive to the hint" (the round's decisive
	// `handle` discriminator is BindHandles(input.Question) alone, computed
	// independently of ConfirmedHandle -- see that field's own doc
	// comment). CHAOS-4081's own ticket text is explicit this is a genuine,
	// currently-permanent bound: a consumer may report this field, but must
	// not read it as proof the round's real Outcome was insensitive to the
	// hint.
	HandleInsensitivityEvaluated bool
	HandleInsensitivityOutcome   kindInsensitivityOutcome
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
	// ObservedExplicitKindHint (CHAOS-4079) is true when an EXPLICIT
	// (non-receipt) kind hint was present this round but applied NO
	// narrowing at all -- either disjoint from every pooled kind (the
	// trial harness's own deliberately-wrong inferred-tier kind arm) or
	// subsuming the whole pool. MUTUALLY EXCLUSIVE with a non-empty
	// PreNarrowingExplicitKinds by construction
	// (runShadowEvidenceRoundForResolution, resolve.go): a narrowing
	// either happened or it did not.
	//
	// It makes the kind-insensitivity probe OBSERVABLE for those cases,
	// which it previously was not (PreNarrowingExplicitKinds stayed empty,
	// so the decisive branch's own gate never opened and the trial's
	// kind_insensitivity partition was unreachable by construction).
	// Evaluation under this flag is WRITE-FREE and READ-FREE: it derives
	// the verdict arithmetically from census results this round ALREADY
	// collected (kindInsensitivityOutcomeFromRound, below) rather than
	// re-running kindInsensitivityProof, so it issues ZERO additional
	// CensusFunc calls and writes ONLY the three KindInsensitivity* fields.
	//
	// That construction is the whole point. The obvious alternative --
	// populating PreNarrowingExplicitKinds for these cases so the existing
	// branch runs -- was drafted and REJECTED on adversarial review (codex
	// xhigh, 2026-08-22): the existing branch performs a SECOND live census
	// read with no snapshot isolation against the first, and overwrites
	// base.Outcome to ShadowWouldClarify when the two disagree. That
	// Outcome is consumed for a REAL commit decision by CHAOS-3896 Slice C
	// (attestedSatisfier/mergeCensusAttestedSatisfier, resolve.go), so
	// census-read drift between the two reads could refuse a commit that
	// would otherwise land -- an observability change with genuine
	// commit-behavior risk. Deriving instead of re-reading makes that
	// hazard structurally impossible rather than merely unlikely.
	ObservedExplicitKindHint bool
	// ObservedExplicitKindSubsumed distinguishes the two
	// ObservedExplicitKindHint situations for the trace alone: true when
	// the hint admitted EVERY pooled kind (intersecting changed nothing),
	// false for the disjoint case. Meaningless unless
	// ObservedExplicitKindHint is set.
	ObservedExplicitKindSubsumed bool
	// ConfirmedAnchor (CHAOS-4042, sol-max ruling) is a redeemed ancr_
	// receipt's own resolved claimant -- nil for the common case (no
	// anchor receipt confirmed this round), which keeps this an exact
	// no-op for every existing caller: the round falls through to
	// BindAnchor's own question-derived scan exactly as it always has.
	// Non-nil TAKES PRIORITY over BindAnchor: the caller's own confirmed
	// selection already supplies the disambiguation term-exclusivity was
	// standing in for (the ruling's own membership-verify rationale),
	// so this round must not re-derive a possibly-different answer from
	// question text when a confirmed one exists. Only Kind/CanonicalID are
	// read; Term is left empty (redemption never has the raw term text --
	// only its hash survives past offer time, the repo's own
	// term-identity-via-hash rule) and is safe to leave empty here since
	// AnchorBinding.Term is in-process/never-traced provenance only.
	ConfirmedAnchor *AnchorBinding
	// ConfirmedHandle (CHAOS-4081, team-lead ruling, path (a), 2026-08-25)
	// is request.SubjectHandles' own explicit hint -- nil for the common
	// case (no explicit subject_handle hint this round), which keeps this
	// an exact no-op for every existing caller.
	//
	// UNLIKE ConfirmedAnchor immediately above, this does NOT take priority
	// over -- or otherwise participate in -- this round's own DECISIVE
	// `handle` discriminator (BindHandles(input.Question) alone, entirely
	// unaffected by this field): a text-derived handle was already
	// something this round trusted before CHAOS-4081, while an explicit
	// request.SubjectHandles entry NOT echoed in question text is new REACH
	// this round never had, and CHAOS-3896 Slice C now LIVE-CONSUMES this
	// round's Outcome/Kinds for a real commit decision (see Attestation's
	// own STALENESS NOTE) -- so widening what can DECIDE would be a
	// decision-behavior change, not an observability one (CHAOS-4083's own
	// audit verdict already ratified leaving this gap open rather than
	// papering over it with an unproven claim). This field feeds ONLY
	// Attestation.HandleInsensitivityEvaluated/HandleInsensitivityOutcome --
	// see those fields' own doc comment and
	// TestConfirmedHandleInsensitivityProbeIsWriteFree for the guarantee
	// that this can never reach Outcome/Kinds/NonCensusedSurvivor and
	// therefore never reach Slice C.
	ConfirmedHandle *BoundHandle
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
				ShadowDIdentityHash:              a.DIdentity,
				ShadowPreconditionUnproven:       a.PreconditionUnproven,
				ShadowUnscopedVisibility:         a.UnscopedVisibility,
				ShadowNonCensusedSurvivor:        a.NonCensusedSurvivor,
				ShadowHandleGrammarBound:         a.HandleGrammarBound,
				ShadowAnchorUniqueClaimant:       a.AnchorUniqueClaimant,
				ShadowAnchorReceiptConfirmed:     a.AnchorReceiptConfirmed,
				ShadowKindsCensused:              len(a.Kinds),
				ShadowKindInsensitivityEvaluated: a.KindInsensitivityEvaluated,
				ShadowKindInsensitivityOutcome:   string(a.KindInsensitivityOutcome),
				ShadowKindInsensitivityMode:      string(a.KindInsensitivityMode),
				// CHAOS-4081 (team-lead ruling, path (a)):
				ShadowHandleInsensitivityEvaluated: a.HandleInsensitivityEvaluated,
				ShadowHandleInsensitivityOutcome:   string(a.HandleInsensitivityOutcome),
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
	// CHAOS-4042 (sol-max ruling): a confirmed anchor selection TAKES
	// PRIORITY over BindAnchor's own question-derived scan -- see
	// ShadowEvidenceRoundInput.ConfirmedAnchor's own doc comment. anchorOK
	// is unconditionally true here (a confirmed selection was already
	// re-verified at redemption -- structure.go's canonicalizeStructure
	// reverify hook -- before it could ever reach this round), and
	// anchorReceiptConfirmed records that this round's own anchor did NOT
	// come from BindAnchor, so AnchorUniqueClaimant below stays false
	// rather than asserting a proof that never ran.
	var anchor AnchorBinding
	var anchorOK, anchorReceiptConfirmed bool
	if input.ConfirmedAnchor != nil {
		anchor = *input.ConfirmedAnchor
		anchorOK = true
		anchorReceiptConfirmed = true
	} else {
		anchor, anchorOK = BindAnchor(input.AliasClaimants, input.AliasLookupComplete)
	}

	censusKinds, nonCensusedSurvivor := splitCensusKinds(input.PooledKinds)
	if handle != nil && IsCensusKindRegistered(handle.Kind) {
		censusKinds = appendUniqueCensusKind(censusKinds, handle.Kind)
	}

	base := Attestation{
		UnscopedVisibility: true, NonCensusedSurvivor: nonCensusedSurvivor,
		HandleGrammarBound: handle != nil, AnchorUniqueClaimant: anchorOK && !anchorReceiptConfirmed,
		AnchorReceiptConfirmed: anchorReceiptConfirmed,
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
		// handleKind (codex xhigh review, CHAOS-3972 round 1, finding 1):
		// the SAME per-kind applicability gate the main census loop above
		// already applies (handleApplies := handle.Kind == kind) -- the
		// proof must never apply this handle's value to a kind it was
		// never bound to.
		var handleKind CensusKind
		// preNarrowingKinds (codex xhigh review, CHAOS-3972 round 2,
		// finding 1): input.PreNarrowingExplicitKinds is the caller's own
		// PRE-narrowing CANDIDATE-POOL kinds (resolve.go), captured before
		// this function ever ran -- it structurally cannot know about the
		// registered-handle-kind APPEND the main census loop above just
		// performed (censusKinds = appendUniqueCensusKind(...)), because a
		// bound handle can name a kind that was never in the pool at all
		// (design brief: a handle is decisive alone, independent of
		// pooling). Omitting that kind here would let the proof certify
		// soundness over an INCOMPLETE hypothesis set -- exactly the class
		// of gap this whole mechanism exists to close -- so the identical
		// append is mirrored onto the pre-narrowing set too, before it
		// reaches the proof.
		preNarrowingKinds := input.PreNarrowingExplicitKinds
		if handle != nil {
			handleKind = handle.Kind
			if IsCensusKindRegistered(handle.Kind) {
				preNarrowingKinds = appendUniqueCensusKind(preNarrowingKinds, handle.Kind)
			}
		}
		proof := kindInsensitivityProof(ctx, input.OrgID, preNarrowingKinds,
			handleKind, valueOr(handle != nil, handle), handle != nil, anchor.Kind, anchor.CanonicalID, anchorOK, input.CensusFunc)
		// Recorded regardless of soundness -- CHAOS-4039's own point is
		// distinguishing "evaluated and sound" from "evaluated and
		// unsound" from "never evaluated at all", not just the sound half.
		base.KindInsensitivityEvaluated = true
		base.KindInsensitivityOutcome = proof
		base.KindInsensitivityMode = explicitKindNarrowingApplied
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
	} else if input.ObservedExplicitKindHint && (base.Outcome == ShadowWouldCommit || base.Outcome == ShadowWouldNoMatch) {
		// CHAOS-4079: an explicit kind hint was present but applied NO
		// narrowing (disjoint from, or subsuming, the whole pool) -- see
		// ShadowEvidenceRoundInput.ObservedExplicitKindHint's own doc
		// comment. WRITE-FREE OBSERVATION: this branch assigns to the
		// three KindInsensitivity* fields and NOTHING else. It must never
		// touch Outcome/Reason/PreconditionUnproven/Kinds or call anything
		// -- the enclosing else-if keeps it structurally exclusive with the
		// decision-bearing branch above, and the trailing `else` placement
		// (rather than a second independent `if`) is what makes that
		// exclusivity a property of the code rather than of the caller.
		//
		// Same decisive gate as the branch above, deliberately: it keeps
		// KindInsensitivityEvaluated meaning exactly one thing across both
		// modes -- "the proof was consulted for a would_commit/
		// would_no_match outcome" -- so a consumer never has to ask which
		// mode changed the field's meaning.
		base.KindInsensitivityEvaluated = true
		base.KindInsensitivityOutcome = kindInsensitivityOutcomeFromRound(base.Kinds)
		base.KindInsensitivityMode = input.observedNarrowingMode()
	}
	// CHAOS-4081 (team-lead ruling, path (a)): evaluated unconditionally
	// whenever a confirmed explicit handle hint names a registered census
	// kind -- independent of base.Outcome and every other field already
	// finalized above. WRITE-FREE OBSERVATION, same discipline as the
	// KindInsensitivity* branches immediately above: this assigns to the
	// two HandleInsensitivity* fields and NOTHING else -- it must never
	// touch Outcome/Reason/PreconditionUnproven/Kinds/NonCensusedSurvivor,
	// which is exactly what keeps ConfirmedHandle's own doc comment's
	// "never reaches Slice C" guarantee true. Reuses kindInsensitivityProof
	// (chaos3900_structure_offers.go) unchanged, scoped to a single-kind
	// set naming ONLY ConfirmedHandle.Kind -- the SAME primitive the
	// explicit-kind-narrowing branch above already trusts for an identical
	// shape of proof, just over one caller-named kind instead of the whole
	// pre-narrowing pool.
	//
	// PROBE-LOCAL RECOVERY (codex R1, High, confirmed 2026-08-25): base.
	// Outcome/Kinds/NonCensusedSurvivor are ALREADY FINAL by this point --
	// this call is the one place in the whole function that runs strictly
	// after the round's real decision is committed to `base`. The caller,
	// runShadowEvidenceRoundForResolution (resolve.go), wraps the ENTIRE
	// round in its own top-level `defer recover()` that replaces the WHOLE
	// returned Attestation with its zero value on any panic anywhere in
	// this call tree -- correct for a panic that happens BEFORE the
	// decision is final (there is nothing real yet to lose), but a panic
	// from THIS purely observational probe (e.g. a caller-supplied
	// CensusFunc that panics on the confirmed-handle's own kind) would
	// otherwise wipe an already-real, already-decided commit/no_match
	// Outcome it has no business touching. confirmedHandleInsensitivityProbe
	// owns its own recover() precisely so that never happens: a panic here
	// degrades ONLY HandleInsensitivityEvaluated/Outcome (Evaluated=true,
	// Outcome=kindInsensitivityProbeError -- a distinguishable signal, NOT
	// the unevaluated zero value; see that constant's own doc comment), and
	// `base`'s already-finalized fields are untouched because this call
	// sits strictly after they were set and assigns nothing else. See
	// TestConfirmedHandleProbePanicDoesNotWipeAttestation for the red/green
	// proof.
	if input.ConfirmedHandle != nil && input.CensusFunc != nil && IsCensusKindRegistered(input.ConfirmedHandle.Kind) {
		base.HandleInsensitivityEvaluated, base.HandleInsensitivityOutcome =
			confirmedHandleInsensitivityProbe(ctx, input, anchor, anchorOK)
	}
	return emit(base)
}

// confirmedHandleInsensitivityProbe (CHAOS-4081, codex R1 High fix; degraded
// signal codex R2 Medium fix) is the ConfirmedHandle-scoped
// kindInsensitivityProof call, isolated behind its OWN deferred recover() --
// see the call site's doc comment (immediately above, in
// RunShadowEvidenceRound) for why this must never let a panic propagate
// into the caller's top-level recover(), which would zero-value an
// already-decided Attestation instead of degrading just this probe's own
// two output fields.
func confirmedHandleInsensitivityProbe(ctx context.Context, input ShadowEvidenceRoundInput, anchor AnchorBinding, anchorOK bool) (evaluated bool, outcome kindInsensitivityOutcome) {
	defer func() {
		if r := recover(); r != nil {
			// Degrade to Evaluated=true, Outcome=kindInsensitivityProbeError
			// -- NOT the "never evaluated" zero value (codex R2, Medium,
			// confirmed): a panic here means the probe DID run and its own
			// CensusFunc call is what failed, which is a materially
			// different fact than "no ConfirmedHandle was ever set" and
			// must be distinguishable in the production trace, not folded
			// into the same "not attempted" bucket. See
			// kindInsensitivityProbeError's own doc comment. Nothing else
			// is touched: `base` in the caller was already final before
			// this call, and this named-return recovery only ever writes
			// evaluated/outcome, never base.
			evaluated, outcome = true, kindInsensitivityProbeError
		}
	}()
	evaluated = true
	outcome = kindInsensitivityProof(ctx, input.OrgID,
		[]CensusKind{input.ConfirmedHandle.Kind},
		input.ConfirmedHandle.Kind, input.ConfirmedHandle.Value, true,
		anchor.Kind, anchor.CanonicalID, anchorOK, input.CensusFunc)
	return evaluated, outcome
}

// kindInsensitivityOutcomeFromRound (CHAOS-4079) is kindInsensitivityProof's
// verdict DERIVED from census results this round already collected, with
// ZERO additional CensusFunc calls.
//
// EQUIVALENCE (why this is the proof's answer, not an approximation).
// Callable only from the ObservedExplicitKindHint branch above, which holds
// two preconditions:
//
//	(1) NO narrowing was applied, so the "pre-narrowing kind set" the proof
//	    would re-census IS the set this round already censused -- resolve.go
//	    passes the UNnarrowed pooledKinds as PooledKinds in that case, and
//	    the registered-handle-kind append kindInsensitivityProof's caller
//	    mirrors onto the pre-narrowing set is the SAME append the main
//	    census loop above already performed on censusKinds.
//	(2) The outcome is DECISIVE, which the switch above reaches only when
//	    !NonCensusedSurvivor && !mismatch && complete.
//
// Under those, every early-exit inside kindInsensitivityProof is
// unreachable: no non-censused survivor and no unregistered pooled kind
// (both set NonCensusedSurvivor, barred by (2)); no kind unreached by a
// keyed predicate (the main loop sets NonCensusedSurvivor and skips such a
// kind, likewise barred); no census error (barred by `complete`); no
// ClosureMismatch (barred by `mismatch`). Each surviving per-kind census
// call would carry BYTE-IDENTICAL arguments to the call the main loop
// already made -- same kind, same valueOr(handleApplies, handle), same
// handleApplies/anchorKind/anchorCanonicalID/anchorApplies -- so its result
// is the result already recorded in KindAttestation, and re-issuing it
// would differ only by census-read DRIFT between two unsynchronized live
// reads. Deriving takes the drift-free answer and, more importantly, makes
// the second read (and therefore the commit-behavior hazard that sank the
// naive fix) structurally nonexistent.
//
// NOT vacuous: SatisfierSetClosureMismatch is checked HERE (mirroring the
// proof, codex CHAOS-3972 round 1 finding 2) but NOT by the decisive switch
// above, so a would_commit round whose census could not prove its satisfier
// SET closed derives kind_sensitive_outcome rather than commit_sound.
func kindInsensitivityOutcomeFromRound(kinds []KindAttestation) kindInsensitivityOutcome {
	if len(kinds) == 0 {
		return kindInsensitivitySensitive
	}
	total := 0
	for _, ka := range kinds {
		// Defensive, not reachable under the branch's own decisive gate
		// (see the EQUIVALENCE note): an incomplete or non-closed census
		// receipt is exactly as untrustworthy here as it is inside
		// kindInsensitivityProof, in either direction.
		if !ka.Complete || ka.ClosureMismatch || ka.SatisfierSetClosureMismatch {
			return kindInsensitivitySensitive
		}
		total += ka.Count
	}
	switch total {
	case 0:
		return kindInsensitivityNoMatchSound
	case 1:
		return kindInsensitivityCommitSound
	default:
		return kindInsensitivitySensitive
	}
}

// observedNarrowingMode reports which of the two no-narrowing situations
// ObservedExplicitKindHint stands for. The distinction is not derivable
// inside this package (PooledKinds arrives already un-narrowed either way),
// so it is carried on the input rather than re-inferred -- see
// runShadowEvidenceRoundForResolution (resolve.go), the only place that
// still holds both the hint and the pre-narrowing pool.
func (in ShadowEvidenceRoundInput) observedNarrowingMode() explicitKindNarrowingMode {
	if in.ObservedExplicitKindSubsumed {
		return explicitKindNarrowingSubsumed
	}
	return explicitKindNarrowingNoOverlap
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
