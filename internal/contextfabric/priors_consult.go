package contextfabric

import (
	"context"
	"strconv"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3977 P5 (pivot-intent design brief, DESIGN-FINAL, §2.4/§3.4,
// DP4(a)): the TWO ruled consultation sites, and nowhere else. DP4(a)
// (chris, ratified): "(a) -- both sites, disclosed, non-decisive, measured;
// 3859 phase-2 lexical arm stays PARKED pending its own ratification
// round."
//
// ONE READ PER INVESTIGATE CALL: Engine.fetchPriorEntries is the SOLE
// caller of PriorConsultant.Consult -- Investigate calls it once (engine.go,
// right before ResolveSubjects) and threads the resulting entries into
// BOTH consumption points below (consultPriorStructureOffers, the offer
// builder; resolveWindowPriorProposal, the inferred-default slot) and into
// terminalResult's own subjectless-terminal path (unresolved.go) -- neither
// consumer performs its own I/O. This matches PriorConsultant's own doc
// comment ("called at MOST once per Investigate call") and avoids a
// redundant Postgres round trip for the SAME (org, question) key within
// one investigation.
//
// SCOPE TRIM (v1, disclosed -- this repository's PR description carries the
// same note for chris's review): the offer-builder site consults
// expected_kind and subject_handle; subject_anchor prior entries are
// curated, stored, and revocable (the substrate is complete -- see
// structurepriorcuration) but NOT yet consulted at runtime. §2.4 requires a
// prior anchor proposal to be "verified against the identity universe
// before offering," and the only production primitive that does that
// (graphrank.VerifyAnchorClaimantUnique) needs a MatchedTermHash tied to a
// SPECIFIC alias-term match THIS investigation's own resolution produced --
// a value a historical, aggregated prior cannot supply without inventing a
// term-free existence check outside this ticket's scope. Redemption-time
// re-verification (structure.go's own reverify hooks) is unconditional
// regardless of OfferSource already, so this trim costs no safety property
// (a wrong anchor offer would fail closed at redemption exactly like a
// stale engine-derived one does today) -- it only means subject_anchor's
// own cf_prior_consulted count is honestly zero in v1, tabulated as such,
// never silently hidden.

// fetchPriorEntries is the ONE I/O call site for prior consultation per
// Investigate call (see this file's own header comment). Returns nil when
// e.priorConsultant is nil (feature off) or when the consult degraded
// (already recorded via RecordPriorDegradation before returning) -- either
// way, an empty result degrades every downstream consumer to "no prior
// proposals," identical to §3.7 cold start.
func (e *Engine) fetchPriorEntries(ctx context.Context, principal storage.Principal, questionHash string) []StructurePriorEntry {
	if e.priorConsultant == nil {
		return nil
	}
	entries, state := e.priorConsultant.Consult(ctx, principal.OrgID, questionHash)
	if state != PriorDegradationNone {
		e.recordPriorDegradation(ctx, principal, state)
		return nil
	}
	return entries
}

// consultPriorStructureOffers is DP4(a) site one: the StructureNeeds offer
// builder. Called from Investigate immediately after ResolveSubjects
// returns (engine.go), BEFORE composeStructureNeeds mints any receipt/
// option id -- so a prior-sourced entry gets its ids minted through the
// EXACT SAME path an engine-derived one does, never a second minting
// scheme.
//
// ADD, never RE-RANK (design brief §2.4 names both verbs; v1 implements
// only the first): prior-sourced entries are APPENDED after every
// engine-derived entry for the same member, in the entry's own curated
// Rank order. Re-ranking (promoting a prior-favored value ahead of an
// engine-derived one) is deliberately deferred -- it would let priors
// change the FIRST-RANKED (Rank==0) offer's identity, which
// StructureSelectionEvent.Accepted (structure_capture.go) reads as "did the
// caller confirm the SYSTEM'S own leading proposal" -- conflating an
// engine's and a prior's own leading pick would corrupt the Bridge's own
// acceptance-rate measurement before the first shadow run ever collects
// one. ADD-only cannot corrupt it: an appended entry can only ever be
// Rank>0 by construction.
//
// class-conditional gating (§1.3): only members present in
// material.Missing are ever populated -- a member outside the question's
// own class frame is unconstructible, not filtered (mirrors §2.4's own
// pin, ResolveSubjects' existing Missing computation).
func (e *Engine) consultPriorStructureOffers(ctx context.Context, principal storage.Principal, entries []StructurePriorEntry, material StructureOfferMaterial) StructureOfferMaterial {
	if len(entries) == 0 || len(material.Missing) == 0 {
		return material
	}
	missing := map[contractsv1.ContextFabricStructureNeedKind]bool{}
	for _, m := range material.Missing {
		missing[m] = true
	}
	material = e.mergePriorKindOffers(ctx, principal, entries, missing, material)
	material = e.mergePriorHandleOffers(ctx, principal, entries, missing, material)
	return material
}

func (e *Engine) mergePriorKindOffers(ctx context.Context, principal storage.Principal, entries []StructurePriorEntry, missing map[contractsv1.ContextFabricStructureNeedKind]bool, material StructureOfferMaterial) StructureOfferMaterial {
	if !missing[contractsv1.ContextFabricStructureNeedExpectedKind] {
		return material
	}
	var sawCandidate, offered, revoked bool
	for _, entry := range entries {
		if entry.Member != contractsv1.ContextFabricStructureNeedExpectedKind {
			continue
		}
		sawCandidate = true
		if entry.Revoked {
			revoked = true
			continue
		}
		kind := contractsv1.ContextFabricSubjectKind(entry.Value)
		label, ok := structurePriorKindLabel(kind)
		if !ok {
			continue
		}
		material.KindOptions = append(material.KindOptions, contractsv1.ContextFabricKindOption{
			Label: label, Kind: kind,
			OfferSource:    contractsv1.ContextFabricStructureOfferPrior,
			PriorVersionID: strconv.FormatInt(entry.Version, 10), PriorEntryID: entry.EntryID,
		})
		offered = true
	}
	e.recordPriorConsultOutcome(ctx, principal, contractsv1.ContextFabricStructureNeedExpectedKind, sawCandidate, offered, revoked)
	return material
}

func (e *Engine) mergePriorHandleOffers(ctx context.Context, principal storage.Principal, entries []StructurePriorEntry, missing map[contractsv1.ContextFabricStructureNeedKind]bool, material StructureOfferMaterial) StructureOfferMaterial {
	if !missing[contractsv1.ContextFabricStructureNeedSubjectHandle] {
		return material
	}
	var sawCandidate, offered, revoked bool
	for _, entry := range entries {
		if entry.Member != contractsv1.ContextFabricStructureNeedSubjectHandle {
			continue
		}
		sawCandidate = true
		if entry.Revoked {
			revoked = true
			continue
		}
		if e.priorHandleGrammarChecker == nil {
			continue
		}
		sourceColumn, ok := e.priorHandleGrammarChecker(entry.Kind, entry.PatternID, entry.Value)
		if !ok {
			continue
		}
		label, ok := structurePriorHandleLabel(entry.Kind, entry.Value)
		if !ok {
			continue
		}
		material.HandleOptions = append(material.HandleOptions, contractsv1.ContextFabricHandleOption{
			Label: label, Kind: entry.Kind, PatternID: entry.PatternID, Value: entry.Value, SourceColumn: sourceColumn,
			OfferSource:    contractsv1.ContextFabricStructureOfferPrior,
			PriorVersionID: strconv.FormatInt(entry.Version, 10), PriorEntryID: entry.EntryID,
		})
		offered = true
	}
	e.recordPriorConsultOutcome(ctx, principal, contractsv1.ContextFabricStructureNeedSubjectHandle, sawCandidate, offered, revoked)
	return material
}

// resolveWindowPriorProposal is DP4(a) site two: the inferred-default
// proposal slot, and the shared gate both of Investigate's own window-
// composing call sites use (engine.go's decisive path, unresolved.go's
// subjectless-terminal path) -- kept as one function so the "only when
// precedence steps 1-2 both declined to decide" gate can never drift
// between the two copies.
//
// Consults ONLY when windowCanon carries neither a confirmed/stated window
// (precedence step 1) nor a binder-routed span (step 2) -- i.e. only at the
// exact point "the engine's own fallback would otherwise guess" (design
// brief §3.4's own phrase). entries is the SAME slice fetchPriorEntries
// already retrieved for this Investigate call (no second I/O call here). A
// prior proposal here still enters at WindowInferredDefault provenance,
// exactly like the class-table default it substitutes for -- it never
// grants question_stated, and composeEffectiveWindow's own interpreted-axis
// gate and "refuse to guess" discipline apply identically regardless of
// source.
func (e *Engine) resolveWindowPriorProposal(ctx context.Context, principal storage.Principal, entries []StructurePriorEntry, windowCanon requestWindowCanonicalization) windowPriorProposal {
	if windowCanon.Effective != nil || windowCanon.BinderProposal.Reason == WindowBindRoutedInferred {
		return windowPriorProposal{}
	}
	var sawCandidate, revoked bool
	for _, entry := range entries {
		if entry.Member != contractsv1.ContextFabricStructureNeedWindow {
			continue
		}
		sawCandidate = true
		if entry.Revoked {
			revoked = true
			continue
		}
		candidate := RelativeWindowID(entry.Value)
		if !ValidRelativeWindowID(candidate) {
			continue
		}
		e.recordPriorConsultOutcome(ctx, principal, contractsv1.ContextFabricStructureNeedWindow, sawCandidate, true, false)
		return windowPriorProposal{
			RelativeID: candidate, PriorVersionID: strconv.FormatInt(entry.Version, 10), PriorEntryID: entry.EntryID, OK: true,
		}
	}
	e.recordPriorConsultOutcome(ctx, principal, contractsv1.ContextFabricStructureNeedWindow, sawCandidate, false, revoked)
	return windowPriorProposal{}
}

func (e *Engine) recordPriorDegradation(ctx context.Context, principal storage.Principal, state PriorDegradationState) {
	if e.telemetry == nil || state == PriorDegradationNone {
		return
	}
	e.telemetry.RecordPriorDegradation(ctx, principal, state)
}

// recordPriorConsultOutcome reports cf_prior_consulted{member,outcome} for
// ONE member of ONE Investigate call's prior consult -- called at most once
// per member per call, mirroring recordStructureNeedsTelemetry's own "once
// per nonzero signal" discipline. sawCandidate=false (no entry for this
// member at all) reports nothing, the same "a member with nothing to say
// contributes no call" convention RecordStructureOfferCount already uses.
func (e *Engine) recordPriorConsultOutcome(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, sawCandidate, offered, revoked bool) {
	if e.telemetry == nil || !sawCandidate {
		return
	}
	outcome := PriorConsultedSuppressedVerification
	switch {
	case offered:
		outcome = PriorConsultedOffered
	case revoked:
		outcome = PriorConsultedSuppressedRevoked
	}
	e.telemetry.RecordPriorConsulted(ctx, principal, member, outcome)
}

// structurePriorKindLabel mirrors graphrank.kindOfferLabel's own closed
// switch verbatim (contextfabric cannot import graphrank -- the same
// package-graph constraint AnchorVerificationReason/HandleVerificationReason's
// own doc comments already name -- so this is the package-local copy every
// other cross-boundary vocabulary in this file already establishes as this
// codebase's convention). ok=false for any kind outside the closed
// structure-offer set: a prior proposing a kind this package does not know
// how to label is dropped, never surfaced with a raw enum string standing
// in for display text.
func structurePriorKindLabel(kind contractsv1.ContextFabricSubjectKind) (string, bool) {
	switch kind {
	case contractsv1.ContextFabricSubjectPullRequest:
		return "a pull request", true
	case contractsv1.ContextFabricSubjectPullRequestReview:
		return "a pull request review", true
	case contractsv1.ContextFabricSubjectCIRun:
		return "a CI pipeline run", true
	case contractsv1.ContextFabricSubjectWorkItem:
		return "a work item", true
	case contractsv1.ContextFabricSubjectRepository:
		return "a repository", true
	case contractsv1.ContextFabricSubjectProject:
		return "a project", true
	case contractsv1.ContextFabricSubjectTeam:
		return "a team", true
	default:
		return "", false
	}
}

// structurePriorHandleLabel mirrors graphrank.handleOfferLabel's own closed
// switch verbatim, same reasoning as structurePriorKindLabel above.
func structurePriorHandleLabel(kind contractsv1.ContextFabricSubjectKind, value string) (string, bool) {
	switch kind {
	case contractsv1.ContextFabricSubjectPullRequest:
		return "pull request #" + value, true
	case contractsv1.ContextFabricSubjectWorkItem:
		return "work item " + value, true
	case contractsv1.ContextFabricSubjectCIRun:
		return "CI run #" + value, true
	default:
		return "", false
	}
}
