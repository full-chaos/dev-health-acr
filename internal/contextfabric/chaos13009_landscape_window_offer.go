package contextfabric

import (
	"slices"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-13009. A quadrant refused for different days says so in a limitation
// (CHAOS-12958); it also offers the window the user can pick instead of
// retyping it. The offer is the window option the answer already serves for a
// choice of window: frozen bounds and a winr_ receipt on the stored result,
// shown beside the answer by structure_needs, redeemed on the next turn as the
// turn's evidence window. A dated read on the current axis takes each team's
// latest row inside a stated window, so a window that ends after the shared day
// reads both teams on it.

// landscapeOfferFallbackLength is the length of the offered window when the turn
// states none: the default evidence window.
const landscapeOfferFallbackLength = 90 * 24 * time.Hour

// landscapeWindowOption is the option for the day shared, an ISO date: a window as
// long as the one the turn read, ending the day after the shared day (exclusive,
// as every window end is), minted under resultID. ok is false for a day that is
// not a date.
func landscapeWindowOption(shared string, effective *contractsv1.ContextFabricEffectiveEvidenceWindow, resultID string) (contractsv1.ContextFabricWindowOption, bool) {
	day, err := time.Parse(time.DateOnly, shared)
	if err != nil {
		return contractsv1.ContextFabricWindowOption{}, false
	}
	length := landscapeOfferFallbackLength
	if effective != nil && effective.Start != nil && effective.End != nil {
		if read := effective.End.Sub(*effective.Start); read > 0 && read <= maxHistoricalRangeDays*24*time.Hour {
			length = read
		}
	}
	end := day.Add(24 * time.Hour)
	start := end.Add(-length)
	option := contractsv1.ContextFabricWindowOption{Label: contractsv1.ContextFabricLandscapeWindowOptionLabel(landscapeDayWords(shared)), Start: &start, End: &end}
	content := structureOfferContent(option)
	option.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedWindow, resultID, content)
	option.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedWindow, resultID, content)
	return option, option.Validate() == nil
}

// withLandscapeWindowOffer serves the option while the quadrant is refused for
// different days and the facts name the day the teams share, and removes it
// otherwise. Finalization runs on every pass, so an earlier option is removed
// first. A window choice that is not ours stays. It reports whether it created
// the structure_needs block.
func withLandscapeWindowOffer(result InvestigationResult, shared string, refused bool) (InvestigationResult, bool) {
	result = withoutLandscapeWindowOffer(result)
	if !refused || shared == "" {
		return result, false
	}
	option, ok := landscapeWindowOption(shared, result.EffectiveEvidenceWindow, result.ResultID)
	if !ok {
		return result, false
	}
	options := append([]contractsv1.ContextFabricWindowOption(nil), optionsOf(result.WindowClarification)...)
	clarification := &contractsv1.ContextFabricWindowClarification{Options: append(options, option)}
	if clarification.Validate() != nil {
		return result, false
	}
	result.WindowClarification = clarification
	created := false
	switch needs := result.StructureNeeds; {
	case needs == nil:
		result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
			Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{option},
		}
		created = true
	case slices.Contains(needs.Missing, contractsv1.ContextFabricStructureNeedWindow):
		cloned := *needs
		cloned.WindowOptions = append(append([]contractsv1.ContextFabricWindowOption(nil), needs.WindowOptions...), option)
		result.StructureNeeds = &cloned
	}
	return result, created
}

func optionsOf(clarification *contractsv1.ContextFabricWindowClarification) []contractsv1.ContextFabricWindowOption {
	if clarification == nil {
		return nil
	}
	return clarification.Options
}

// withoutLandscapeWindowOffer removes every landscape window option from the
// clarification and from structure_needs, and the blocks themselves when that
// leaves them empty of any other window choice.
func withoutLandscapeWindowOffer(result InvestigationResult) InvestigationResult {
	isOurs := func(option contractsv1.ContextFabricWindowOption) bool {
		return contractsv1.IsContextFabricLandscapeWindowOptionLabel(option.Label)
	}
	if clarification := result.WindowClarification; clarification != nil && slices.ContainsFunc(clarification.Options, isOurs) {
		kept := make([]contractsv1.ContextFabricWindowOption, 0, len(clarification.Options))
		for _, option := range clarification.Options {
			if !isOurs(option) {
				kept = append(kept, option)
			}
		}
		if len(kept) == 0 {
			result.WindowClarification = nil
		} else {
			result.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: kept}
		}
	}
	if needs := result.StructureNeeds; needs != nil && slices.ContainsFunc(needs.WindowOptions, isOurs) {
		cloned := *needs
		cloned.WindowOptions = make([]contractsv1.ContextFabricWindowOption, 0, len(needs.WindowOptions))
		for _, option := range needs.WindowOptions {
			if !isOurs(option) {
				cloned.WindowOptions = append(cloned.WindowOptions, option)
			}
		}
		if len(cloned.WindowOptions) == 0 {
			cloned.WindowOptions = nil
			if len(cloned.Missing) == 1 && cloned.Missing[0] == contractsv1.ContextFabricStructureNeedWindow &&
				len(cloned.KindOptions) == 0 && len(cloned.AnchorOptions) == 0 && len(cloned.HandleOptions) == 0 &&
				len(cloned.CandidateOptions) == 0 && len(cloned.WindowExpandOptions) == 0 {
				result.StructureNeeds = nil
				return result
			}
		}
		result.StructureNeeds = &cloned
	}
	return result
}
