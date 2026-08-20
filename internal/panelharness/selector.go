package panelharness

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Selector asks one panelist to choose, among a turn-1 result's
// StructureNeeds offers, which receipt (if any) best resolves question for
// each member named in needs.Missing -- the "select" half of this harness's
// own select-and-continue drive (design brief §2.4/§3.1; CHAOS-3860's own
// stated delta over the CHAOS-3742 trial: "driving the CLARIFICATION flow
// rather than stopping at first resolution").
//
// The returned map is keyed by the ContextFabricStructureNeedKind string
// value (e.g. "expected_kind", "subject_anchor", "subject_handle"); a
// member absent from the map, or mapped to an empty string, means the
// panelist found no offer worth confirming for that member -- run.go
// carries only the non-empty entries into turn 2's Prior*Receipts fields,
// never a synthesized or default choice.
type Selector interface {
	SelectReceipts(ctx context.Context, question string, needs contractsv1.ContextFabricStructureNeeds) (map[string]string, error)
}

// offerProjection is the bounded, uniform view of one offer this package
// sends to a panelist, regardless of which typed option list (KindOptions/
// AnchorOptions/HandleOptions) it came from -- ids/enums/label only, the
// same disclosure discipline every other structure-offer echo in this
// codebase already applies (never more than what StructureNeeds itself
// already discloses to any caller).
type offerProjection struct {
	Member    string `json:"member"`
	ReceiptID string `json:"receipt_id"`
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	Rank      int    `json:"rank"`
	Kind      string `json:"kind,omitempty"`
}

// projectOffers flattens StructureNeeds' three typed option lists into one
// bounded, member-tagged slice for a Selector's prompt -- window rides its
// own, separately designed WindowSelectionEvent path (design brief §2.4)
// and is deliberately excluded here, matching P4's own StructureSelectionEvent
// scope exactly.
func projectOffers(needs contractsv1.ContextFabricStructureNeeds) []offerProjection {
	offers := make([]offerProjection, 0, len(needs.KindOptions)+len(needs.AnchorOptions)+len(needs.HandleOptions))
	for rank, option := range needs.KindOptions {
		offers = append(offers, offerProjection{
			Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), ReceiptID: option.ReceiptID,
			OptionID: option.OptionID, Label: option.Label, Rank: rank, Kind: string(option.Kind),
		})
	}
	for rank, option := range needs.AnchorOptions {
		offers = append(offers, offerProjection{
			Member: string(contractsv1.ContextFabricStructureNeedSubjectAnchor), ReceiptID: option.ReceiptID,
			OptionID: option.OptionID, Label: option.Label, Rank: rank, Kind: string(option.Kind),
		})
	}
	for rank, option := range needs.HandleOptions {
		offers = append(offers, offerProjection{
			Member: string(contractsv1.ContextFabricStructureNeedSubjectHandle), ReceiptID: option.ReceiptID,
			OptionID: option.OptionID, Label: option.Label, Rank: rank, Kind: string(option.Kind),
		})
	}
	return offers
}

// offerIsTopRanked reports whether receiptID was the rank-0 (first-listed)
// offer among needs' options for member -- run.go's own PanelistSelection.Accepted
// derivation, matching StructureSelectionEvent.Accepted's identical
// "selected == engine's own leading proposal" semantics. false when
// receiptID is not found at all (should not happen for a receipt this
// package's own Selector chose from these same offers, but fails closed
// rather than panicking or guessing).
func offerIsTopRanked(needs contractsv1.ContextFabricStructureNeeds, member, receiptID string) bool {
	for _, offer := range projectOffers(needs) {
		if offer.Member == member && offer.ReceiptID == receiptID {
			return offer.Rank == 0
		}
	}
	return false
}
