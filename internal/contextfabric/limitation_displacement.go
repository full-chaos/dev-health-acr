package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// withRetrievalDegradation returns limitations carrying the
// retrieval-degradation disclosure, DISPLACING the last model-authored
// entry when the list is already at the contract's cap (CHAOS-3746).
//
// A plain append was the original defect. When the model returned a full
// list, the append produced one entry too many and
// ContextFabricInvestigationResult.Validate rejected the whole result --
// ErrInvalidResult, no answer at all, because a disclosure could not fit.
// That is a real path rather than a curiosity: a degraded retrieval is
// exactly the run most likely to produce a long limitation list, since the
// same missing mechanism gives the model more gaps to note.
//
// Displacement, not refusal, and not silence. The three candidate
// behaviours at the cap are: drop the disclosure (a degraded answer reads
// as a clean one -- the worst outcome, and invisible), fail the
// investigation (the current defect), or displace one model caveat. Only
// the third keeps the answer AND the statement of what it is worth.
//
// The disclosure is service-authored and says how much the whole answer can
// be trusted; a model caveat is one of many, and the last one is the least
// prominent. So the LAST model-authored entry gives way, earlier ones keep
// their order, and the disclosure lands at the end where a reader meets it
// after the caveats it qualifies.
//
// The cap is read from the contract rather than restated, so this cannot
// drift from the bound it exists to respect -- the same relation
// fact_registry.go's coverage clamps now hold.
// appendBoundedLimitations is the ONE path by which anything is added to a
// composed result's limitations (CHAOS-3746 round-17 finding 1).
//
// It exists because the cap was handled at one append site and not the
// other. The retrieval-degradation disclosure displaced a caveat to fit;
// CHAOS-3781's standing historical disclosures were then appended straight
// on top with no cap awareness, so a degraded answer on a historical axis
// with a full limitation list overflowed and died at
// ContextFabricInvestigationResult.Validate -- the very defect the
// displacement was written to prevent, re-entering through the site the
// fix did not cover. Fixing that site alone would have left the next one.
//
// Semantics are exactly the displacement rule, generalised: an addition
// already stated is skipped; otherwise it is appended, and at the cap the
// last MODEL-AUTHORED caveat gives way. Service-authored disclosures are
// never displaced -- displacing one disclosure to make room for another
// would be a net loss of exactly the statements that cannot be recovered
// from anywhere else in the document.
//
// Returns the composed list and how many model caveats it dropped, because
// nothing downstream can recover that: a displaced list and a list that
// had room are the same length and carry the same disclosures.
func appendBoundedLimitations(limitations []string, additions []string) (composed []string, displaced int) {
	// Normalize BEFORE adding anything. An input already over the cap can
	// reach here: SynthesisDraft.ValidateAgainst does not check the
	// top-level collection counts (the documented CHAOS-3790 gap), so a
	// model that returned too many limitations is caught only by the
	// composed result's own Validate -- which is the too-late rejection
	// this whole mechanism exists to avoid.
	//
	// The single-purpose appender this replaced normalized for free, by
	// taking limitations[:cap-1]. Generalising it into a splice preserved
	// the overflow instead, so the round-17 rewrite lost that property
	// (round-18 fix A). It is restored deliberately here rather than as a
	// side effect, and every trimmed entry is counted in the SAME
	// accounting as everything else this function drops -- a trim is a
	// loss like any other, and silent loss is the defect, not the size.
	// DEDUP, then normalize, then displace -- one pipeline, in that order,
	// and the order is not arbitrary. Normalization trims from the TAIL,
	// so a duplicate near the front survives every trim and the composed
	// result is rejected for uniqueness after the appender was supposed
	// to have made it valid. Deduping first also means the copies it
	// removes shorten the list before the cap is measured, so no caveat
	// is displaced to make room that a duplicate was already wasting.
	//
	// The engine cannot assume its synthesizer deduped: Synthesizer is a
	// port, and only one implementation happens to call trimmedUnique on
	// decode. Whatever this is handed has to come out valid.
	composed = withoutDuplicates(limitations)
	composed, displaced = normalizeToCapacity(composed)
	for _, addition := range additions {
		if alreadyStates(composed, addition) {
			continue
		}
		if len(composed) < contractsv1.ContextFabricLimitationsMaxCount {
			composed = append(composed, addition)
			continue
		}
		index := lastModelAuthoredLimitation(composed)
		if index < 0 {
			// Unreachable with today's vocabulary: there are three
			// service-authored disclosures in total against a cap of 100,
			// so a full list always holds a model caveat. Kept as a
			// branch rather than an assumption because the alternative is
			// silently returning an over-cap list, and skipping the
			// addition at least leaves a valid answer.
			// TestServiceDisclosuresCannotFillTheCap pins the premise.
			continue
		}
		shortened := append([]string(nil), composed[:index]...)
		shortened = append(shortened, composed[index+1:]...)
		composed = append(shortened, addition)
		displaced++
	}
	return composed, displaced
}

// alreadyStates reports whether limitations already says what addition
// says. The retrieval-degradation disclosure matches on EITHER spelling:
// a stored answer carrying the legacy wording must not gain a second,
// differently worded copy of the same statement.
func alreadyStates(limitations []string, addition string) bool {
	if contractsv1.IsContextFabricRetrievalDegradedLimitation(addition) {
		return hasRetrievalDegradedLimitation(limitations)
	}
	for _, limitation := range limitations {
		if limitation == addition {
			return true
		}
	}
	return false
}

// lastModelAuthoredLimitation returns the index of the last entry that is
// not a service-authored disclosure, or -1 when every entry is one.
func lastModelAuthoredLimitation(limitations []string) int {
	for i := len(limitations) - 1; i >= 0; i-- {
		if !isServiceAuthoredLimitation(limitations[i]) {
			return i
		}
	}
	return -1
}

// isServiceAuthoredLimitation reports whether a limitation is one this
// service composes rather than one the model wrote. These are the
// statements a reader cannot get from anywhere else in the document, so
// they are never the ones displaced.
func isServiceAuthoredLimitation(limitation string) bool {
	if contractsv1.IsContextFabricRetrievalDegradedLimitation(limitation) {
		return true
	}
	for _, disclosure := range serviceAuthoredLimitations() {
		if limitation == disclosure {
			return true
		}
	}
	return false
}

// serviceAuthoredLimitations enumerates every disclosure this service
// composes, derived from the same declarations the composers use so a new
// disclosure cannot become displaceable by being forgotten here.
func serviceAuthoredLimitations() []string {
	disclosures := append([]string(nil), temporalLimitations...)
	return append(disclosures, observedTimeLimitation)
}

func withRetrievalDegradation(limitations []string) (composed []string, displaced int) {
	return appendBoundedLimitations(limitations, []string{retrievalDegradedLimitation})
}

// normalizeToCapacity brings an over-cap list down to the contract's limit
// by dropping MODEL-authored caveats from the end, and reports how many it
// dropped.
//
// Only model caveats, under the same discipline as displacement: bringing
// a list to size by discarding service-authored disclosures would remove
// exactly the statements a reader cannot reconstruct from anywhere else in
// the document.
func normalizeToCapacity(limitations []string) (composed []string, trimmed int) {
	if len(limitations) <= contractsv1.ContextFabricLimitationsMaxCount {
		return limitations, 0
	}
	composed = append([]string(nil), limitations...)
	for len(composed) > contractsv1.ContextFabricLimitationsMaxCount {
		index := lastModelAuthoredLimitation(composed)
		if index < 0 {
			// Unreachable for the same reason the displacement branch is:
			// there are far fewer service-authored disclosures than the
			// cap (TestServiceDisclosuresCannotFillTheCap). Returning the
			// list as-is keeps the loss visible as a validation failure
			// rather than silently discarding a disclosure to hide it.
			return composed, trimmed
		}
		composed = append(composed[:index], composed[index+1:]...)
		trimmed++
	}
	return composed, trimmed
}

// withoutDuplicates removes repeated entries, keeping the first occurrence
// of each so the caller's order survives.
//
// Removals here are DELIBERATELY NOT COUNTED as displacements. A duplicate
// carries no information its surviving copy does not, so dropping one
// loses nothing -- and limitations_omitted is read as "content you are not
// seeing". Counting a dropped copy there would report a loss that did not
// happen, which erodes the signal exactly as much as hiding a real one.
//
// Equality is exact, matching the validator's own uniqueness key
// (uniqueTrimmedStrings compares whole values and separately rejects
// untrimmed ones on the write path). Trimming here instead would silently
// repair a padded value the write path means to reject.
func withoutDuplicates(limitations []string) []string {
	seen := make(map[string]struct{}, len(limitations))
	deduped := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		if _, exists := seen[limitation]; exists {
			continue
		}
		seen[limitation] = struct{}{}
		deduped = append(deduped, limitation)
	}
	return deduped
}
