package devhealthsource

import (
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Producer-side normalization: keep the row instead of quarantining it.
//
// Per-item quarantine (item_quarantine.go) ended the wedge class -- one
// unprojectable row used to reject its whole page forever -- but it still
// costs the row. Three bounds cost rows for reasons that are not lost
// information at all, only an unnormalized rendering of information the
// source did supply:
//
//   - a label with leading or trailing whitespace, or one longer than the
//     contract's label bound;
//   - a free-text property longer than the contract's scalar bound;
//   - a single-row validity window whose end precedes its start.
//
// Each is repaired here, before validation, so the item projects. Nothing is
// repaired silently: every normalization is counted under its own closed
// token, distinct from the quarantine vocabulary, so an operator can tell
// "this producer is normalizing N rows a tick" from "this producer is
// dropping N rows a tick" -- two very different signals that a shared token
// would merge.
//
// This runs as ONE pass over every built candidate rather than as an edit at
// each row-building site, and it is the ONLY place any of it happens.
//
// The site list is long and grows: labels alone are minted at more than a
// dozen places across tables.go, teams_projects.go, teams_projects_edges.go
// and clickhouse.go, and free-text properties at twenty-eight -- fourteen
// through setStringProperty with a zero limit, and fourteen more written
// straight through stringScalar with no cap at all. A fix applied site by site
// is exactly the enumeration that misses the site added next quarter; a pass
// at the one point every candidate passes through cannot miss one.
//
// Nor does any site repair its own value first. An earlier revision also
// trimmed the title at the three work_items / git_pull_requests /
// operational_incidents sites, for readability. It repaired those rows just as
// well and made them INVISIBLE: the pass had nothing left to do, so the three
// families an operator most wants on the label_trimmed counter never reached
// it. A repair applied upstream of the counter is a silent rewrite, which is
// the one thing this change promised not to be.
const (
	// normalizationLabelTrimmed marks a subject label that carried leading
	// or trailing whitespace and was trimmed.
	normalizationLabelTrimmed = "label_trimmed"
	// normalizationLabelCapped marks a subject label longer than the
	// contract's label bound, cut to its head.
	normalizationLabelCapped = "label_capped"
	// normalizationScalarCapped marks at least one free-text string property
	// longer than the contract's scalar bound, cut to its head.
	normalizationScalarCapped = "scalar_capped"
	// normalizationWindowCollapsed marks a validity window whose end preceded
	// its start, collapsed to the degenerate half-open window edgeValidity
	// already produces for the same shape on the edge sites.
	normalizationWindowCollapsed = "window_collapsed"
)

// normalizationObservation is one normalization applied to one item.
// Counts and closed tokens only, on the same terms as quarantineObservation:
// no row data, no free text, never a validator message.
type normalizationObservation struct {
	// Reason is one of the closed tokens above.
	Reason string
	// Kind names which item shape was normalized: entity or relationship.
	Kind string
}

// normalizeCandidates repairs the three producer-side bounds on every item in
// all, reporting each normalization to observe.
//
// Counted PER ITEM, at most once per token per item -- never once per field.
// An entity whose three free-text properties are all oversize reports
// scalar_capped once, because the operational question is "how many items did
// this producer have to normalize", not "how many strings were long".
//
// Per item rather than per source ROW deliberately, so these counters share a
// denominator with the quarantine counters they replace: one work_items row
// with an inverted window mints both an entity and its BELONGS_TO_REPOSITORY
// edge, and before this change quarantine counted inverted_window twice for
// it. window_collapsed now reads 2 for that row, so the ledger balances
// item-for-item and an operator comparing a before/after tick is comparing
// like with like.
//
// Mutates the items in place through the pointers the candidates hold, so the
// caller's slice -- which still derives the batch's NextCursor -- sees the
// normalized items and no copy can drift from the original.
//
// Runs BEFORE partitionProjectableCandidates, never after: the whole point is
// that an item repaired here is never offered to quarantine at all.
func normalizeCandidates(all []candidate, observe func(normalizationObservation)) {
	for _, c := range all {
		var n itemNormalizer
		switch {
		case c.entity != nil:
			n = itemNormalizer{kind: "entity"}
			n.label(&c.entity.Subject)
			n.properties(c.entity.Properties)
			n.window(&c.entity.ValidFrom, &c.entity.ValidTo)
		case c.relationship != nil:
			n = itemNormalizer{kind: "relationship"}
			// Both endpoints: an edge labels its endpoints itself, and a
			// relationship whose To label is oversize is rejected exactly as
			// hard as one whose From label is.
			n.label(&c.relationship.From)
			n.label(&c.relationship.To)
			n.properties(c.relationship.Properties)
			n.window(&c.relationship.ValidFrom, &c.relationship.ValidTo)
		}
		if n.fired() {
			n.report(c, observe)
		}
		// Episodes and tombstones are deliberately untouched. A tombstone
		// carries no label, no properties and no window -- only an
		// EffectiveAt, whose bound (representable instant) is not something
		// this producer may invent a value for. Episodes never reach this
		// pass at all: EpisodesProjectionSource calls buildBatch directly
		// (episodes.go) and has its own assembly path, and widening this pass
		// to a source whose drops are still uncounted would be the same trade
		// item_quarantine.go refused to make there.
	}
}

// itemNormalizer collects which tokens one item earned, so each is reported at
// most once no matter how many fields triggered it.
type itemNormalizer struct {
	kind     string
	trimmed  bool
	capped   bool
	scalar   bool
	collapse bool
}

// label trims and head-caps a subject label in place.
//
// ORDER IS LOAD-BEARING: trim, then cap, then trim the tail the cap exposed.
// Capping first can leave whitespace at the new end -- " x<510 runes> y  z"
// cut at the bound -- and the contract rejects an untrimmed label just as
// hard as an oversize one, so a cap-then-stop would trade one violation for
// another. Trimming first also guarantees the cap cannot empty the label:
// rune 0 of a trimmed non-empty string is not whitespace, so the head of it
// is not all whitespace, so the closing trim always leaves at least one rune
// and the contract's 1-rune minimum still holds.
//
// CanonicalID is deliberately NOT normalized, though the same validator
// rejects an untrimmed or overlong one. A canonical ID is IDENTITY: changing
// it re-points the node, and an item whose id changed is a different item to
// every consumer that already stored the old one. That is a rebuild decision,
// not a producer repair. A row with an untrimmed canonical ID therefore still
// quarantines under untrimmed_label, which is the correct outcome and is
// pinned by a test.
func (n *itemNormalizer) label(s *contractsv1.ContextFabricSubjectRef) {
	trimmed := strings.TrimSpace(s.Label)
	if trimmed != s.Label {
		n.trimmed = true
	}
	if utf8.RuneCountInString(trimmed) > contractsv1.ContextFabricSubjectRefLabelMaxLength {
		trimmed = strings.TrimSpace(headRunes(trimmed, contractsv1.ContextFabricSubjectRefLabelMaxLength))
		n.capped = true
	}
	s.Label = trimmed
}

// properties head-caps every oversize free-text string property in place.
//
// Only the String variant carries a length bound; Integer, Number, Boolean
// and Null have none, and a non-finite Number is a value this producer must
// not invent a replacement for, so it is left to quarantine.
//
// Not trimmed: the contract bounds a scalar's LENGTH and says nothing about
// its whitespace, and setStringProperty already trims on the way in. Trimming
// here would change stored values that project perfectly well today, which is
// exactly the class this change must not touch.
func (n *itemNormalizer) properties(properties map[string]contractsv1.ContextFabricScalarValue) {
	for name, value := range properties {
		if value.String == nil {
			continue
		}
		if utf8.RuneCountInString(*value.String) <= contractsv1.ContextFabricClaimedFactValueMaxLength {
			continue
		}
		capped := headRunes(*value.String, contractsv1.ContextFabricClaimedFactValueMaxLength)
		value.String = &capped
		properties[name] = value
		n.scalar = true
	}
}

// window collapses an inverted validity window to [validFrom, validFrom).
//
// This is edgeValidity's rule (validity.go), applied to the single-row sites
// that never had it. The argument there carries over unchanged: the pair says
// the thing was never valid, a zero-width half-open interval states exactly
// that, the contract accepts it (only a STRICTLY earlier end is rejected),
// and no time-filtered read admits it, while a structural read that ignores
// the temporal axis still sees the item. It never widens a window into an
// interval the source did not assert, and it never drops the item.
//
// A COPY of validFrom, not the pointer: callers hold these pointers as an
// endpoint's own window and pass the same pair to belongsToRepository, so
// aliasing the two bounds would let a later adjustment to one silently move
// the other -- the same aliasing hazard edgeValidity's own comment names.
//
// Strict Before, matching edgeValidity: a window that is already zero-width
// because the bounds merely touch is untouched and reports nothing, because
// it is already the representation this produces.
func (n *itemNormalizer) window(validFrom, validTo **time.Time) {
	from, to := *validFrom, *validTo
	if from == nil || to == nil || !to.Before(*from) {
		return
	}
	collapsed := *from
	*validTo = &collapsed
	n.collapse = true
}

// fired reports whether this item earned any token at all, so the validation
// probe below costs nothing on the overwhelming majority of items, which need
// no repair.
func (n *itemNormalizer) fired() bool {
	return n.trimmed || n.capped || n.scalar || n.collapse
}

// report announces the repairs -- but ONLY if the item is now projectable.
//
// A repair that does not keep the row must not be counted as one. A
// work_item_ref stub labels itself with the raw target id AND derives its
// canonical id from that same string, so trimming the label leaves the
// identity untrimmed and the contract still refuses it: the row is
// quarantined, and a label_trimmed line beside that drop would claim a save
// that did not happen. The log line says "the item is kept", and it has to be
// true.
//
// The item is deliberately left NORMALIZED rather than reverted. It is dropped
// either way, and the repaired copy is what quarantineReason then inspects, so
// the quarantine token names the bound that ACTUALLY still blocks the row
// rather than the one this pass already handled -- strictly better diagnosis
// for the operator reading both counters.
//
// Judged by the contract's own validator, never by a second opinion about what
// "projectable" means. An item can still be dropped afterwards by a
// BATCH-level pass (duplicate identity, orphaned dependent, quarantined
// endpoint); those are not this bound's business and the repair genuinely
// happened, so they do not suppress the count.
func (n *itemNormalizer) report(c candidate, observe func(normalizationObservation)) {
	if observe == nil {
		return
	}
	if _, err := validateCandidateItem(c); err != nil {
		return
	}
	for _, entry := range []struct {
		fired  bool
		reason string
	}{
		{n.trimmed, normalizationLabelTrimmed},
		{n.capped, normalizationLabelCapped},
		{n.scalar, normalizationScalarCapped},
		{n.collapse, normalizationWindowCollapsed},
	} {
		if entry.fired {
			observe(normalizationObservation{Reason: entry.reason, Kind: n.kind})
		}
	}
}

// normalizationLogger builds the observer both sources hand to the shared
// assembly engine, on the same one-implementation terms as quarantineLogger:
// the second copy of a telemetry line is how the gap where one source
// reported and the other did not comes back.
//
// Emitted at INFO, not WARN, and that difference is the point. A quarantine
// is a row LOST and an operator must see it; a normalization is a row KEPT,
// the outcome this producer wants, and paging someone for the success case
// trains them to ignore the warning that matters. A sustained rate is still
// worth watching -- it says the source's shape and the contract's bounds
// disagree -- so it is counted rather than silent.
//
// Content-safe by construction: a closed reason token and a fixed item-kind
// label, nothing derived from row data. Deliberately no Detail field, unlike
// quarantineObservation: the offending value here is free text (a title, a
// pipeline name) rather than a bounded enum, so there is nothing this package
// could safely name.
func normalizationLogger(logger *slog.Logger, sourceName string) func(normalizationObservation) {
	if logger == nil {
		return nil
	}
	return func(observation normalizationObservation) {
		logger.Info("context_fabric: projection item normalized to a contract bound; the item is kept",
			"source", sourceName,
			"normalization_reason", observation.Reason,
			"item_kind", observation.Kind,
		)
	}
}
