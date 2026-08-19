package v1

import "fmt"

// hasKindReceiptPrefix, hasAnchorReceiptPrefix, hasHandleReceiptPrefix
// mirror hasWindowReceiptPrefix exactly -- one predicate per closed
// structure-receipt namespace.
func hasKindReceiptPrefix(id string) bool {
	return hasPrefixLen(id, ContextFabricKindOptionReceiptPrefix)
}
func hasAnchorReceiptPrefix(id string) bool {
	return hasPrefixLen(id, ContextFabricAnchorOptionReceiptPrefix)
}
func hasHandleReceiptPrefix(id string) bool {
	return hasPrefixLen(id, ContextFabricHandleOptionReceiptPrefix)
}

func hasPrefixLen(id, prefix string) bool {
	return len(id) >= len(prefix) && id[:len(prefix)] == prefix
}

// contextFabricStructureReceiptPrefixCheckers is the CLOSED SET of
// structure-receipt namespace predicates (design brief §2.1/§2.5:
// "this enumeration is the CLOSED SET of structure-receipt namespaces and
// is DEFINITIONALLY EXHAUSTIVE: a future namespace MUST be added to this
// pre-reuse resolution set in the same change that mints it"). Used to
// reject any structure-namespaced id appearing in the WRONG receipt field
// -- e.g. a kindr_ id inside prior_anchor_receipts, or any of the four
// inside prior_subject_receipts.
var contextFabricStructureReceiptPrefixCheckers = map[string]func(string) bool{
	ContextFabricKindOptionReceiptPrefix:   hasKindReceiptPrefix,
	ContextFabricAnchorOptionReceiptPrefix: hasAnchorReceiptPrefix,
	ContextFabricHandleOptionReceiptPrefix: hasHandleReceiptPrefix,
	ContextFabricWindowOptionReceiptPrefix: hasWindowReceiptPrefix,
}

// validateStructureReceiptField validates one prior_*_receipts field
// against ITS OWN required prefix, rejecting cross-namespace ids from the
// closed set above, and enforcing the shared bound + uniqueness shape
// PriorWindowReceipts already established. fieldName is used only for
// error text.
func validateStructureReceiptField(fieldName string, receipts []ContextFabricBoundSubjectReceipt, ownPrefix string) error {
	if len(receipts) > 20 {
		return fmt.Errorf("%s exceeds v1 bounds", fieldName)
	}
	seen := make(map[string]struct{}, len(receipts))
	for _, receipt := range receipts {
		if err := receipt.Validate(); err != nil {
			return fmt.Errorf("%s: %w", fieldName, err)
		}
		if !contextFabricStructureReceiptPrefixCheckers[ownPrefix](receipt.ReceiptID) {
			return fmt.Errorf("%s: receipt_id must carry the %q namespace prefix", fieldName, ownPrefix)
		}
		key := receipt.ResultID + "\x00" + receipt.ReceiptID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s must be unique", fieldName)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateNoStructureReceiptPrefix rejects any of the CLOSED set of
// structure-receipt namespace prefixes appearing where they do not belong
// (design brief §2.5 row 1, generalizing the winr_-in-prior_subject_receipts
// check W1 shipped to all four namespaces). Used both for
// prior_subject_receipts (none of the four belong there) and for each
// prior_*_receipts field against the OTHER three namespaces.
func validateNoStructureReceiptPrefix(fieldName, receiptID string, excludePrefix string) error {
	for prefix, check := range contextFabricStructureReceiptPrefixCheckers {
		if prefix == excludePrefix {
			continue
		}
		if check(receiptID) {
			return fmt.Errorf("%s: receipt_id must not carry the %q namespace prefix", fieldName, prefix)
		}
	}
	return nil
}

func (o ContextFabricKindOption) Validate() error {
	if !stringLengthBetween(o.ReceiptID, 8, 256) || !hasKindReceiptPrefix(o.ReceiptID) {
		return fmt.Errorf("kind option receipt_id must carry the %q namespace prefix and satisfy v1 bounds", ContextFabricKindOptionReceiptPrefix)
	}
	if !stringLengthBetween(o.OptionID, 1, 256) || !stringLengthBetween(o.Label, 1, 200) {
		return fmt.Errorf("kind option option_id or label violates v1 bounds")
	}
	if !validContextFabricSubjectKind(o.Kind) {
		return fmt.Errorf("kind option kind is invalid")
	}
	if !ValidContextFabricStructureOfferSource(o.OfferSource) {
		return fmt.Errorf("kind option offer_source is invalid")
	}
	return nil
}

func (o ContextFabricAnchorOption) Validate() error {
	if !stringLengthBetween(o.ReceiptID, 8, 256) || !hasAnchorReceiptPrefix(o.ReceiptID) {
		return fmt.Errorf("anchor option receipt_id must carry the %q namespace prefix and satisfy v1 bounds", ContextFabricAnchorOptionReceiptPrefix)
	}
	if !stringLengthBetween(o.OptionID, 1, 256) || !stringLengthBetween(o.Label, 1, 200) {
		return fmt.Errorf("anchor option option_id or label violates v1 bounds")
	}
	if !validContextFabricSubjectKind(o.Kind) {
		return fmt.Errorf("anchor option kind is invalid")
	}
	if !stringLengthBetween(o.CanonicalID, 1, 256) || !stringLengthBetween(o.ClaimantKey, 1, 256) {
		return fmt.Errorf("anchor option canonical_id or claimant_key violates v1 bounds")
	}
	if !ValidContextFabricStructureOfferSource(o.OfferSource) {
		return fmt.Errorf("anchor option offer_source is invalid")
	}
	return nil
}

func (o ContextFabricHandleOption) Validate() error {
	if !stringLengthBetween(o.ReceiptID, 8, 256) || !hasHandleReceiptPrefix(o.ReceiptID) {
		return fmt.Errorf("handle option receipt_id must carry the %q namespace prefix and satisfy v1 bounds", ContextFabricHandleOptionReceiptPrefix)
	}
	if !stringLengthBetween(o.OptionID, 1, 256) || !stringLengthBetween(o.Label, 1, 200) {
		return fmt.Errorf("handle option option_id or label violates v1 bounds")
	}
	if !validContextFabricSubjectKind(o.Kind) {
		return fmt.Errorf("handle option kind is invalid")
	}
	if !stringLengthBetween(o.PatternID, 1, 128) || !stringLengthBetween(o.Value, 1, 256) || !stringLengthBetween(o.SourceColumn, 1, 128) {
		return fmt.Errorf("handle option pattern_id, value, or source_column violates v1 bounds")
	}
	if !ValidContextFabricStructureOfferSource(o.OfferSource) {
		return fmt.Errorf("handle option offer_source is invalid")
	}
	return nil
}

func (g ContextFabricAcceptedGrammar) Validate() error {
	if !ValidContextFabricStructureNeedKind(g.Member) {
		return fmt.Errorf("accepted grammar member is invalid")
	}
	if g.Kind != "" && !validContextFabricSubjectKind(g.Kind) {
		return fmt.Errorf("accepted grammar kind is invalid")
	}
	if !stringLengthBetween(g.PatternID, 1, 128) {
		return fmt.Errorf("accepted grammar pattern_id violates v1 bounds")
	}
	return nil
}

// contextFabricStructureNeedsMaxOptions bounds each offer list, mirroring
// contextFabricWindowClarificationMaxOptions's own generous, non-tight
// ceiling reasoning.
const contextFabricStructureNeedsMaxOptions = 20

func (n ContextFabricStructureNeeds) Validate() error {
	if len(n.Missing) == 0 || len(n.Missing) > ContextFabricStructureNeedKindCount {
		return fmt.Errorf("structure needs missing violates v1 bounds")
	}
	seenMissing := make(map[ContextFabricStructureNeedKind]struct{}, len(n.Missing))
	for _, need := range n.Missing {
		if !ValidContextFabricStructureNeedKind(need) {
			return fmt.Errorf("structure needs missing entry is invalid")
		}
		if _, exists := seenMissing[need]; exists {
			return fmt.Errorf("structure needs missing entries must be unique")
		}
		seenMissing[need] = struct{}{}
	}
	if len(n.KindOptions) > contextFabricStructureNeedsMaxOptions ||
		len(n.AnchorOptions) > contextFabricStructureNeedsMaxOptions ||
		len(n.HandleOptions) > contextFabricStructureNeedsMaxOptions ||
		len(n.WindowOptions) > contextFabricStructureNeedsMaxOptions ||
		len(n.AcceptedGrammars) > contextFabricStructureNeedsMaxOptions {
		return fmt.Errorf("structure needs offer lists violate v1 bounds")
	}
	seenReceipt := make(map[string]struct{}, len(n.KindOptions)+len(n.AnchorOptions)+len(n.HandleOptions)+len(n.WindowOptions))
	seenOption := make(map[string]struct{}, len(n.KindOptions)+len(n.AnchorOptions)+len(n.HandleOptions)+len(n.WindowOptions))
	addUnique := func(receiptID, optionID string) error {
		if _, exists := seenReceipt[receiptID]; exists {
			return fmt.Errorf("structure needs option receipt_id must be unique across every offer list")
		}
		seenReceipt[receiptID] = struct{}{}
		if _, exists := seenOption[optionID]; exists {
			return fmt.Errorf("structure needs option_id must be unique across every offer list")
		}
		seenOption[optionID] = struct{}{}
		return nil
	}
	for _, opt := range n.KindOptions {
		if err := opt.Validate(); err != nil {
			return fmt.Errorf("kind_options: %w", err)
		}
		if err := addUnique(opt.ReceiptID, opt.OptionID); err != nil {
			return fmt.Errorf("kind_options: %w", err)
		}
	}
	for _, opt := range n.AnchorOptions {
		if err := opt.Validate(); err != nil {
			return fmt.Errorf("anchor_options: %w", err)
		}
		if err := addUnique(opt.ReceiptID, opt.OptionID); err != nil {
			return fmt.Errorf("anchor_options: %w", err)
		}
	}
	for _, opt := range n.HandleOptions {
		if err := opt.Validate(); err != nil {
			return fmt.Errorf("handle_options: %w", err)
		}
		if err := addUnique(opt.ReceiptID, opt.OptionID); err != nil {
			return fmt.Errorf("handle_options: %w", err)
		}
	}
	for _, opt := range n.WindowOptions {
		if err := opt.Validate(); err != nil {
			return fmt.Errorf("window_options: %w", err)
		}
		if err := addUnique(opt.ReceiptID, opt.OptionID); err != nil {
			return fmt.Errorf("window_options: %w", err)
		}
	}
	for _, grammar := range n.AcceptedGrammars {
		if err := grammar.Validate(); err != nil {
			return fmt.Errorf("accepted_grammars: %w", err)
		}
	}
	return nil
}

func (e ContextFabricConfirmedStructureEntry) Validate() error {
	if !ValidContextFabricStructureNeedKind(e.Member) {
		return fmt.Errorf("confirmed structure entry member is invalid")
	}
	if !stringLengthBetween(e.AppliedValue, 1, 256) {
		return fmt.Errorf("confirmed structure entry applied_value violates v1 bounds")
	}
	if !ValidContextFabricStructureSource(e.Source) {
		return fmt.Errorf("confirmed structure entry source is invalid")
	}
	if !ValidContextFabricStructureProvenance(e.Provenance) {
		return fmt.Errorf("confirmed structure entry provenance is invalid")
	}
	if !ValidContextFabricStructureDisposition(e.Disposition) {
		return fmt.Errorf("confirmed structure entry disposition is invalid")
	}
	// A receipt-sourced entry must name the receipt it redeemed; every
	// other source carries no receipt at all (design brief §2.1: the echo
	// is per CARRIED member, and only receipt-sourced members redeem
	// something with a globally-scoped identity).
	if e.Source == ContextFabricStructureSourceReceipt {
		if !stringLengthBetween(e.PriorResultID, 8, 256) || !stringLengthBetween(e.ReceiptID, 8, 256) {
			return fmt.Errorf("confirmed structure entry with source=receipt must carry prior_result_id and receipt_id")
		}
		if e.OfferSource != "" && !ValidContextFabricStructureOfferSource(e.OfferSource) {
			return fmt.Errorf("confirmed structure entry offer_source is invalid")
		}
	} else if e.PriorResultID != "" || e.ReceiptID != "" {
		return fmt.Errorf("confirmed structure entry with source=%q must not carry a receipt identity", e.Source)
	}
	return nil
}

func (e ContextFabricStructureOfferSnapshotEntry) Validate() error {
	if !ValidContextFabricStructureNeedKind(e.Member) {
		return fmt.Errorf("structure offer snapshot entry member is invalid")
	}
	if !stringLengthBetween(e.OfferID, 1, 256) {
		return fmt.Errorf("structure offer snapshot entry offer_id violates v1 bounds")
	}
	if e.Rank < 0 {
		return fmt.Errorf("structure offer snapshot entry rank must be non-negative")
	}
	if !ValidContextFabricStructureOfferSource(e.OfferSource) {
		return fmt.Errorf("structure offer snapshot entry offer_source is invalid")
	}
	return nil
}
