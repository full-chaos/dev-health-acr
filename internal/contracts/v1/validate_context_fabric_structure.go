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
func hasCandidateReceiptPrefix(id string) bool {
	return hasPrefixLen(id, ContextFabricCandidateOptionReceiptPrefix)
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
	ContextFabricKindOptionReceiptPrefix:      hasKindReceiptPrefix,
	ContextFabricAnchorOptionReceiptPrefix:    hasAnchorReceiptPrefix,
	ContextFabricHandleOptionReceiptPrefix:    hasHandleReceiptPrefix,
	ContextFabricWindowOptionReceiptPrefix:    hasWindowReceiptPrefix,
	ContextFabricCandidateOptionReceiptPrefix: hasCandidateReceiptPrefix,
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
	if !optionalStringBetween(o.PriorVersionID, 1, 256) || !optionalStringBetween(o.PriorEntryID, 1, 256) {
		return fmt.Errorf("kind option prior_version_id or prior_entry_id violates v1 bounds")
	}
	if !optionalStringBetween(o.Phrasing, 1, ContextFabricStructureOfferPhrasingMaxLength) {
		return fmt.Errorf("kind option phrasing violates v1 bounds")
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
	if !stringLengthBetween(o.CanonicalID, 1, 256) {
		return fmt.Errorf("anchor option canonical_id violates v1 bounds")
	}
	if !matchedTermHashPattern.MatchString(o.MatchedTermHash) {
		return fmt.Errorf("anchor option matched_term_hash must be a 24-character lowercase hex digest")
	}
	if !ValidContextFabricStructureOfferSource(o.OfferSource) {
		return fmt.Errorf("anchor option offer_source is invalid")
	}
	if !optionalStringBetween(o.PriorVersionID, 1, 256) || !optionalStringBetween(o.PriorEntryID, 1, 256) {
		return fmt.Errorf("anchor option prior_version_id or prior_entry_id violates v1 bounds")
	}
	if !optionalStringBetween(o.Phrasing, 1, ContextFabricStructureOfferPhrasingMaxLength) {
		return fmt.Errorf("anchor option phrasing violates v1 bounds")
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
	if !optionalStringBetween(o.PriorVersionID, 1, 256) || !optionalStringBetween(o.PriorEntryID, 1, 256) {
		return fmt.Errorf("handle option prior_version_id or prior_entry_id violates v1 bounds")
	}
	if !optionalStringBetween(o.Phrasing, 1, ContextFabricStructureOfferPhrasingMaxLength) {
		return fmt.Errorf("handle option phrasing violates v1 bounds")
	}
	return nil
}

func (o ContextFabricCandidateOption) Validate() error {
	if !stringLengthBetween(o.ReceiptID, 8, 256) || !hasCandidateReceiptPrefix(o.ReceiptID) {
		return fmt.Errorf("candidate option receipt_id must carry the %q namespace prefix and satisfy v1 bounds", ContextFabricCandidateOptionReceiptPrefix)
	}
	if !stringLengthBetween(o.OptionID, 1, 256) || !stringLengthBetween(o.Label, 1, 200) {
		return fmt.Errorf("candidate option option_id or label violates v1 bounds")
	}
	if !validContextFabricSubjectKind(o.Kind) {
		return fmt.Errorf("candidate option kind is invalid")
	}
	if !stringLengthBetween(o.CanonicalID, 1, 256) {
		return fmt.Errorf("candidate option canonical_id violates v1 bounds")
	}
	if !ValidContextFabricStructureOfferSource(o.OfferSource) {
		return fmt.Errorf("candidate option offer_source is invalid")
	}
	if !optionalStringBetween(o.PriorVersionID, 1, 256) || !optionalStringBetween(o.PriorEntryID, 1, 256) {
		return fmt.Errorf("candidate option prior_version_id or prior_entry_id violates v1 bounds")
	}
	if !optionalStringBetween(o.Phrasing, 1, ContextFabricStructureOfferPhrasingMaxLength) {
		return fmt.Errorf("candidate option phrasing violates v1 bounds")
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

// ContextFabricStructureOfferPhrasingMaxLength (CHAOS-4171 PR2) is the v1
// wire bound every offer option's own optional Phrasing field carries.
// Exported so genkitruntime's phrasing prompt and
// internal/contextfabric's closed-vocabulary guard state the IDENTICAL
// number this package enforces, rather than an independently-maintained
// copy that could silently drift from it -- the exact defect class
// contextFabricFactKindList/interpretationSystemPrompt's own interpolation
// discipline exists to prevent (genkitruntime/prompts.go).
const ContextFabricStructureOfferPhrasingMaxLength = 200

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
		len(n.AcceptedGrammars) > contextFabricStructureNeedsMaxOptions ||
		len(n.CandidateOptions) > contextFabricStructureNeedsMaxOptions {
		return fmt.Errorf("structure needs offer lists violate v1 bounds")
	}
	seenReceipt := make(map[string]struct{}, len(n.KindOptions)+len(n.AnchorOptions)+len(n.HandleOptions)+len(n.WindowOptions)+len(n.CandidateOptions))
	seenOption := make(map[string]struct{}, len(n.KindOptions)+len(n.AnchorOptions)+len(n.HandleOptions)+len(n.WindowOptions)+len(n.CandidateOptions))
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
	for _, opt := range n.CandidateOptions {
		if err := opt.Validate(); err != nil {
			return fmt.Errorf("candidate_options: %w", err)
		}
		if err := addUnique(opt.ReceiptID, opt.OptionID); err != nil {
			return fmt.Errorf("candidate_options: %w", err)
		}
	}
	for _, grammar := range n.AcceptedGrammars {
		if err := grammar.Validate(); err != nil {
			return fmt.Errorf("accepted_grammars: %w", err)
		}
	}
	// window_expand_options (CHAOS-4314) is DELIBERATELY excluded from the
	// addUnique sweep above: ContextFabricWindowExpandOption.ReceiptID/
	// OptionID copy an existing window_options entry VERBATIM by design
	// (that type's own doc comment) -- a cross-list uniqueness violation
	// here would be the intended shape, not a minting bug. windowOptionMatches
	// enforces the referential integrity that discipline needs instead: a
	// window_expand receipt/option pair must name a real window_options
	// entry, never a fabricated one -- and (codex xhigh review, confirmed
	// Medium finding) must match it on EVERY field the type claims to copy
	// verbatim (Label, RelativeID), not merely the id pair. An id match
	// with a diverging label/relative_id would let a result persist a
	// recommendation whose displayed text disagrees with what redemption
	// actually applies -- the exact "misleading disclosure" class the
	// verbatim-copy design exists to prevent.
	if len(n.WindowExpandOptions) > 1 {
		return fmt.Errorf("window_expand_options violates v1 bounds")
	}
	for _, opt := range n.WindowExpandOptions {
		if err := opt.Validate(); err != nil {
			return fmt.Errorf("window_expand_options: %w", err)
		}
		if !windowOptionMatches(n.WindowOptions, opt) {
			return fmt.Errorf("window_expand_options: receipt_id/option_id/label/relative_id must match an existing window_options entry verbatim")
		}
	}
	return nil
}

// windowOptionMatches reports whether options carries an entry whose
// ReceiptID, OptionID, Label, and RelativeID all equal expand's own --
// the referential integrity check ContextFabricWindowExpandOption's
// deliberate duplication needs (see its own doc comment and
// ContextFabricStructureNeeds.Validate's own comment on why it is
// separate from the addUnique sweep). Checks every field the type's own
// doc comment claims is copied verbatim, not merely the id pair (codex
// xhigh review, confirmed Medium finding).
func windowOptionMatches(options []ContextFabricWindowOption, expand ContextFabricWindowExpandOption) bool {
	for _, opt := range options {
		if opt.ReceiptID == expand.ReceiptID && opt.OptionID == expand.OptionID &&
			opt.Label == expand.Label && opt.RelativeID == expand.RelativeID {
			return true
		}
	}
	return false
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
	// A receipt-sourced entry must name the receipt it redeemed; a
	// carried entry (CHAOS-4360) names the ORIGIN result it inherited from
	// but redeemed no receipt of its own on this request, so it carries
	// prior_result_id WITHOUT a receipt_id; every other source carries
	// neither (design brief §2.1: the echo is per CARRIED member, and only
	// receipt-sourced/carried members have a prior result to name).
	switch e.Source {
	case ContextFabricStructureSourceReceipt:
		if !stringLengthBetween(e.PriorResultID, 8, 256) || !stringLengthBetween(e.ReceiptID, 8, 256) {
			return fmt.Errorf("confirmed structure entry with source=receipt must carry prior_result_id and receipt_id")
		}
		if e.OfferSource != "" && !ValidContextFabricStructureOfferSource(e.OfferSource) {
			return fmt.Errorf("confirmed structure entry offer_source is invalid")
		}
	case ContextFabricStructureSourceCarried:
		if !stringLengthBetween(e.PriorResultID, 8, 256) {
			return fmt.Errorf("confirmed structure entry with source=carried must carry prior_result_id")
		}
		if e.ReceiptID != "" {
			return fmt.Errorf("confirmed structure entry with source=carried must not carry a receipt_id")
		}
	default:
		if e.PriorResultID != "" || e.ReceiptID != "" {
			return fmt.Errorf("confirmed structure entry with source=%q must not carry a receipt identity", e.Source)
		}
	}
	if !optionalStringBetween(e.PriorVersionID, 1, 256) || !optionalStringBetween(e.PriorEntryID, 1, 256) {
		return fmt.Errorf("confirmed structure entry prior_version_id or prior_entry_id violates v1 bounds")
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
	if !optionalStringBetween(e.PriorVersionID, 1, 256) || !optionalStringBetween(e.PriorEntryID, 1, 256) {
		return fmt.Errorf("structure offer snapshot entry prior_version_id or prior_entry_id violates v1 bounds")
	}
	return nil
}
