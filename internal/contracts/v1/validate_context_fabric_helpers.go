package v1

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

func validateScalarMap(values map[string]ContextFabricScalarValue) error {
	for key, value := range values {
		if !stringLengthBetween(key, 1, 128) || strings.TrimSpace(key) != key {
			return fmt.Errorf("scalar property key violates v1 bounds")
		}
		if err := value.Validate(); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
	}
	return nil
}

// validateClaimedFactRows checks a claimed/projected fact's OPTIONAL
// renderable table (CHAOS-4347), shared by ContextFabricClaimedFact.Validate
// and ContextFabricAnswerProjection's key-fact validation so the two never
// drift on what "rows" is allowed to contain. Bounds mirror the published
// schema's ClaimedFactRow/ProjectedFactRow $defs exactly (maxItems 64 on
// the schema's "rows" array, maxProperties 32 on each row's "fields").
func validateClaimedFactRows(rows []ContextFabricClaimedFactRow) error {
	if len(rows) > ContextFabricClaimedFactMaxRows {
		return fmt.Errorf("claimed fact rows exceed v1 bounds")
	}
	for _, row := range rows {
		if len(row.Fields) == 0 || len(row.Fields) > ContextFabricClaimedFactRowMaxFields {
			return fmt.Errorf("claimed fact row field count violates v1 bounds")
		}
		if err := validateScalarMap(row.Fields); err != nil {
			return fmt.Errorf("claimed fact row: %w", err)
		}
	}
	return nil
}

// ClaimedFactRowContentBytes is one claimed/projected fact row's ACTUAL
// json.Marshal size -- the same measure MaxSerializedBytes itself is
// checked against (context_fabric_routes.go's MeasureContextFabricResponse
// marshals the whole response and compares the byte count; this file's own
// mcp_federation_validate.go packetContentSerializedBytes follows the
// identical discipline), and the same basis CHAOS-4785's Phase 1 real-data
// measurement used (Postgres octet_length(payload::text) on the stored,
// already-serialized JSON).
//
// EXPORTED (chris's ruling via team-lead, codex terra xhigh round 2's
// finding): this must be the ONLY implementation of this measurement in
// the repository. An earlier version had two -- this one and a raw-
// string-length copy in internal/contextfabric/devhealthfacts/shared.go's
// factValueRowContentBytes -- and round 1 fixed only this one (see below),
// leaving the sibling to fail the identical way in round 2. "Two copies
// is the defect class, not the bug": devhealthfacts now converts its own
// domain row type to ContextFabricClaimedFactRow and calls this function
// directly rather than maintaining a second implementation that can drift
// again. Measure with the serializer that enforces the ceiling, never a
// proxy for it.
//
// An earlier version of this function summed raw string lengths instead
// (codex terra xhigh round 1, P2, EXECUTED): Go's encoding/json escapes
// '<', '>', '&' to "<" etc. by default (html-safety), a 1-byte-to-
// 6-byte inflation nothing in the raw-length sum accounted for, so a row
// full of those characters could pass this check while its actual
// marshaled size exceeded the bound it was supposed to enforce -- 251,904
// counted bytes accepted a fact whose real combined size was 1,516,708
// bytes, defeating the guard entirely. json.Marshal is the only measure
// that cannot be defeated this way, because it is not an approximation of
// the wire size -- it computes it.
func ClaimedFactRowContentBytes(row ContextFabricClaimedFactRow) int {
	encoded, err := json.Marshal(row)
	if err != nil {
		// ContextFabricClaimedFactRow's Fields are all
		// ContextFabricScalarValue -- string/int64/float64/bool/null, every
		// one of which encoding/json always marshals successfully -- so
		// this is unreachable in practice. If it is ever reached anyway,
		// a computation this validator cannot trust must never UNDER-count
		// (the exact failure class this fix exists to close), so it counts
		// as maximally oversized rather than as zero.
		return math.MaxInt32
	}
	return len(encoded)
}

// validateClaimedFactRowsCombined bounds Rows and TimeSeriesRows TOGETHER
// (CHAOS-4785). validateClaimedFactRows above already bounds each
// collection on its own, but nothing bounded their SUM, so a fact could
// legally carry two independently-legal tables (a legacy breakdown/ranking
// alongside a CHAOS-4682 time series) that together serialize far past the
// service's MaxSerializedBytes ceiling. See
// ContextFabricClaimedFactCombinedCellsMax /
// ContextFabricClaimedFactCombinedContentBytesMax's own doc comment
// (context_fabric_model_bounds.go) for the arithmetic and the real-data
// measurement behind the chosen values.
//
// Applies ONLY when BOTH collections are non-empty: this ticket's own gap
// is specifically the unbounded PRODUCT/COMBINATION of two tables on one
// fact, not either table alone -- a single-table fact (Rows-only, or
// TimeSeriesRows-only, CHAOS-4347's pre-existing shape) stays governed
// exclusively by validateClaimedFactRows' own per-table bound, unchanged.
// Without this gate, ContextFabricClaimedFactCombinedContentBytesMax
// (262,144 bytes, sized off REAL dual-table data) would be far smaller
// than one table's own legal maximum (~8.45M content bytes) and would
// newly reject single-table facts that were legal before this ticket --
// a regression caught by TestCHAOS4785RegressionSingleTableAloneAtMax
// MustNotBeRejected before this gate was added.
func validateClaimedFactRowsCombined(rows, timeSeriesRows []ContextFabricClaimedFactRow) error {
	if len(rows) == 0 || len(timeSeriesRows) == 0 {
		return nil
	}
	cells, contentBytes := 0, 0
	for _, row := range rows {
		cells += len(row.Fields)
		contentBytes += ClaimedFactRowContentBytes(row)
	}
	for _, row := range timeSeriesRows {
		cells += len(row.Fields)
		contentBytes += ClaimedFactRowContentBytes(row)
	}
	if cells > ContextFabricClaimedFactCombinedCellsMax {
		return fmt.Errorf("claimed fact rows+time_series_rows combined cell count violates v1 bounds")
	}
	if contentBytes > ContextFabricClaimedFactCombinedContentBytesMax {
		return fmt.Errorf("claimed fact rows+time_series_rows combined content size violates v1 bounds")
	}
	return nil
}

func validateDrivers(values []ContextFabricDriverJudgment, claimed map[string]ContextFabricClaimedFact, bounds contextFabricBounds) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.validate(bounds); err != nil {
			return fmt.Errorf("drivers: %w", err)
		}
		if _, exists := seen[value.DriverID]; exists {
			return fmt.Errorf("driver IDs must be unique")
		}
		seen[value.DriverID] = struct{}{}
		if err := validateClaimedFactReferences("driver", value.ClaimedFactIDs, value.Category, claimed); err != nil {
			return err
		}
	}
	return nil
}

func validateFindings(name string, values []ContextFabricFinding, claimed map[string]ContextFabricClaimedFact, bounds contextFabricBounds) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.validate(bounds); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, exists := seen[value.FindingID]; exists {
			return fmt.Errorf("%s IDs must be unique", name)
		}
		seen[value.FindingID] = struct{}{}
		if err := validateClaimedFactReferences(name, value.ClaimedFactIDs, value.Kind, claimed); err != nil {
			return err
		}
	}
	return nil
}

// validateClaimedFacts checks ContextFabricInvestigationResult.ClaimedFacts
// bounds and ClaimID uniqueness, returning a ClaimID-indexed lookup map for
// validateClaimedFactReferences to cross-check driver/finding references
// against.
func validateClaimedFacts(values []ContextFabricClaimedFact) (map[string]ContextFabricClaimedFact, error) {
	if values == nil || len(values) > ContextFabricClaimedFactsMaxCount {
		return nil, fmt.Errorf("claimed facts violate v1 bounds")
	}
	seen := make(map[string]ContextFabricClaimedFact, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("claimed_facts: %w", err)
		}
		if _, exists := seen[value.ClaimID]; exists {
			return nil, fmt.Errorf("claimed fact IDs must be unique")
		}
		seen[value.ClaimID] = value
	}
	return seen, nil
}

// validateClaimedFactReferences checks that every ID in claimIDs resolves
// inside claimed, and -- when category names a canonical-fact-shaped
// judgment per ContextFabricDriverCategoryRequiresClaimedFact -- that at
// least one referenced claim's Kind matches. This is the result-level half
// of value-level evidence closure: it proves a driver/finding's claim
// actually exists and is of the right shape. It does NOT compare claim
// values against a canonical fact bundle -- that bundle isn't part of the
// persisted result, so that comparison is SynthesisDraft.ValidateAgainst's
// job in internal/contextfabric, which runs before a result is ever built.
func validateClaimedFactReferences(name string, claimIDs []string, category string, claimed map[string]ContextFabricClaimedFact) error {
	requiredKind, required := ContextFabricDriverCategoryRequiresClaimedFact(ContextFabricDriverCategory(category))
	matchedKind := false
	for _, id := range claimIDs {
		fact, ok := claimed[id]
		if !ok {
			return fmt.Errorf("%s references unknown claimed fact %q", name, id)
		}
		if required && fact.Kind == requiredKind {
			matchedKind = true
		}
	}
	if required && !matchedKind {
		return fmt.Errorf("%s category %q requires a claimed fact of kind %q", name, category, requiredKind)
	}
	return nil
}

func validatePaths(values []ContextFabricRelationshipPath, bounds contextFabricBounds) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.validate(bounds); err != nil {
			return fmt.Errorf("paths: %w", err)
		}
		if _, exists := seen[value.PathID]; exists {
			return fmt.Errorf("path IDs must be unique")
		}
		seen[value.PathID] = struct{}{}
	}
	return nil
}

// validateEntityProjections rejects a batch that projects the same subject
// (by kind + canonical ID) more than once. A backend that upserts by
// subject key would silently apply only the last entry -- e.g. its
// authorization scope or aliases -- while a caller-visible receipt still
// reports every entity as applied, understating what was actually dropped.
func validateEntityProjections(values []ContextFabricEntityProjection) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("entities: %w", err)
		}
		key := subjectKey(value.Subject)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("entities: subject must appear at most once per batch")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateRelationshipProjections rejects a batch that reuses the same
// RelationshipID for more than one relationship -- a backend that upserts
// edges by relationship ID would silently overwrite the earlier edge's
// target/authorization/evidence with the later one's.
func validateRelationshipProjections(values []ContextFabricRelationshipProjection) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("relationships: %w", err)
		}
		if _, exists := seen[value.RelationshipID]; exists {
			return fmt.Errorf("relationships: relationship IDs must be unique within a batch")
		}
		seen[value.RelationshipID] = struct{}{}
	}
	return nil
}

// validateContentProjections rejects a batch that reuses the same
// ContentID for more than one content item.
func validateContentProjections(values []ContextFabricContentProjection) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("contents: %w", err)
		}
		if _, exists := seen[value.ContentID]; exists {
			return fmt.Errorf("contents: content IDs must be unique within a batch")
		}
		seen[value.ContentID] = struct{}{}
	}
	return nil
}

// validateEpisodeProjections rejects a batch that reuses the same
// EpisodeID for more than one episode.
func validateEpisodeProjections(values []ContextFabricEpisodeProjection) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("episodes: %w", err)
		}
		if _, exists := seen[value.EpisodeID]; exists {
			return fmt.Errorf("episodes: episode IDs must be unique within a batch")
		}
		seen[value.EpisodeID] = struct{}{}
	}
	return nil
}

// validateProjectionTombstones rejects a batch that tombstones the same
// subject (by kind + canonical ID) more than once.
// relationshipDeletingTombstoneKinds are the tombstone kinds falkorgraph's
// applyTombstone routes to a relationship DELETE keyed on relationship_id.
// Every other kind deletes a NODE, in a different key space, where an equal
// string means nothing.
//
// Compared case-insensitively, because applyTombstone lower-cases before it
// switches -- a guard that disagreed with the code it guards about which
// kinds matter would be worse than none.
var relationshipDeletingTombstoneKinds = map[string]struct{}{"relationship": {}, "edge": {}}

// validateProjectionRelationshipTombstoneCollision rejects a batch that
// ASSERTS a relationship and TOMBSTONES that same relationship id.
//
// CHAOS-4565. validateProjectionRelationships and validateProjectionTombstones
// each enforce uniqueness within their OWN slice and neither looks at the
// other, so a cross-kind collision passed cleanly. falkorgraph applies
// relationships BEFORE tombstones, so such a batch wrote the edge and
// immediately deleted it -- a valid, still-asserted ownership silently
// removed from the graph, with every count, log and receipt reporting
// success.
//
// A producer is not supposed to be able to build one: devhealthsource's
// ownership producer decides per GROUP, and a group either asserts or
// retracts. That argument is only as good as the id, and OWNED_BY_TEAM ids
// are a colon concatenation over id spaces that themselves contain colons, so
// two different groups can land on one id (CHAOS-4635 carries the root fix).
// This is the seam where "should not happen" becomes "cannot happen
// silently".
//
// It rejects rather than dropping the tombstone, and that is deliberate. A
// rejected batch holds the checkpoint, which is loud and recoverable;
// silently preferring one of the two would be this defect again with the
// pipeline agreeing that everything is fine. A wedge is a bad outcome -- it
// is simply not the worst one available here.
func validateProjectionRelationshipTombstoneCollision(relationships []ContextFabricRelationshipProjection, tombstones []ContextFabricProjectionTombstone) error {
	if len(relationships) == 0 || len(tombstones) == 0 {
		return nil
	}
	asserted := make(map[string]struct{}, len(relationships))
	for _, relationship := range relationships {
		asserted[relationship.RelationshipID] = struct{}{}
	}
	for _, tombstone := range tombstones {
		if _, deletesEdge := relationshipDeletingTombstoneKinds[strings.ToLower(strings.TrimSpace(tombstone.Kind))]; !deletesEdge {
			continue
		}
		if _, collides := asserted[tombstone.CanonicalID]; collides {
			return fmt.Errorf("tombstones: relationship tombstone %q retracts a relationship the same batch asserts; tombstones are applied after relationships, so this would write the edge and immediately delete it", tombstone.CanonicalID)
		}
	}
	return nil
}

func validateProjectionTombstones(values []ContextFabricProjectionTombstone) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("tombstones: %w", err)
		}
		key := value.Kind + "\x00" + value.CanonicalID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("tombstones: kind and canonical ID must be unique within a batch")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateTimeRange(observed, validFrom, validTo *time.Time) error {
	for _, value := range []*time.Time{observed, validFrom, validTo} {
		if value == nil {
			continue
		}
		if value.IsZero() {
			return fmt.Errorf("temporal timestamp is invalid")
		}
		// CHAOS-3781 round-5 R5-3: the same representation-derived bound
		// R4-4 applies to REQUEST timestamps, applied to PROJECTION
		// ingest.
		//
		// Every temporal comparison downstream converts through
		// UnixNano, which is undefined outside the epoch-nanosecond
		// range: a year-9999 valid_to does not saturate, it WRAPS to a
		// plausible instant. Ingested, that silently corrupts historical
		// admission -- an element would be excluded or admitted at
		// entirely the wrong times -- and the same wrap reorders
		// tombstones against the rows they are meant to retire.
		//
		// An out-of-range producer timestamp is data corruption, not a
		// caller mistake, so the batch is REJECTED rather than clamped.
		// Clamping would write a value the source never asserted and
		// leave no trace that anything was wrong.
		if !representableInstant(*value) {
			return fmt.Errorf("temporal timestamp is outside the representable range (%s..%s)",
				minRepresentableInstant.Format("2006-01-02"), maxRepresentableInstant.Format("2006-01-02"))
		}
	}
	if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		return fmt.Errorf("valid_to precedes valid_from")
	}
	return nil
}

// optionalEvidenceRefs validates an evidence reference list on a field the
// JSON Schema does NOT mark required and that carries `omitempty` in Go.
// For those fields nil and empty mean the same thing: "none".
//
// This exists because boundedEvidenceRefs rejects nil outright, which is
// correct for a required field and wrong for an optional one. An optional
// empty slice serializes to an OMITTED field and decodes back as nil, so a
// validator demanding non-nil would reject the service's own valid output
// the moment anything re-read it -- and InvestigationResultStore.Get
// re-validates on every read, so a stored result carrying a candidate with
// no evidence refs would fail to load. That is the same defect already
// recorded for Coverage.DegradedReasons (CHAOS-3755 finding M2), reached
// through a different field.
//
// Kept separate from boundedEvidenceRefs deliberately: the REQUIRED
// evidence fields (DriverJudgment, Finding, RelationshipPath, and the edge
// shapes) must keep rejecting nil, because for them a missing list really
// is invalid.
func optionalEvidenceRefs(values []string, maximum int) bool {
	if values == nil {
		return true
	}
	return boundedEvidenceRefs(values, maximum, true)
}

func boundedEvidenceRefs(values []string, maximum int, allowEmpty bool) bool {
	if values == nil || len(values) > maximum || (!allowEmpty && len(values) == 0) {
		return false
	}
	for _, value := range values {
		// '|' is the delimited-string separator a graph backend adapter
		// storing a list of strings as a single field would use to encode
		// evidence ref IDs (zepgraph did, before its CHAOS-3771 deletion);
		// an evidence ref ID containing it would corrupt that encoding and
		// silently narrow the stored evidence closure. Kept as a contract
		// invariant regardless of which backend is current.
		if !stringLengthBetween(value, 8, 256) || strings.TrimSpace(value) != value || strings.Contains(value, "|") {
			return false
		}
	}
	return uniqueStrings(values)
}

// containsSeparatorCharacter reports whether any value contains '|', the
// delimited-string separator character a backend that persists a list of
// strings as a single "|a|b|"-encoded field would use (zepgraph's
// encodeScope did, before its CHAOS-3771 deletion).
func containsSeparatorCharacter(values []string) bool {
	for _, value := range values {
		if strings.Contains(value, "|") {
			return true
		}
	}
	return false
}

func uniqueTrimmedStrings(values []string, maximum int) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != value || !stringLengthBetween(value, 1, maximum) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueSubjects(values []ContextFabricSubjectRef) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return false
		}
		key := subjectKey(value)
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func subjectKey(subject ContextFabricSubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}

func validVersion(value string) bool {
	return stringLengthBetween(value, 1, 256) && strings.TrimSpace(value) == value
}

// ContextFabricModelIdentityMaxLength bounds Versions.ModelIdentity
// (CHAOS-3782), which is NOT a general version string like its
// VersionSet siblings -- it is "<provider>/<model>", and both halves are
// independently bounded at 256 bytes each: modelprovider.Config's own
// Provider/Model fields (maximumProviderOrModelLength,
// internal/contextfabric/modelprovider/config.go) and this package's own
// CHAOS-3775 per-organization ContextFabricOrgModelConfig.Provider/Model
// (contextFabricOrgModelConfigMaxFieldLength, context_fabric_model_config.go,
// which already documents mirroring modelprovider's constant) both use
// exactly 256. 256 + 1 ("/") + 256 = 513 is therefore the true worst case
// a fully valid, already-billed model call can produce -- validVersion's
// shared 256-byte bound (correct for every OTHER VersionSet field, which
// are all short deployment/prompt version tokens ACR itself controls)
// would reject it. Codex round-2 finding #8: a valid, in-bounds org model
// configuration was failing InvestigationResult.Validate() AFTER a
// successful, billable model call, purely because this field was folded
// into the same 256-byte check as everything else.
//
// Derived from contextFabricOrgModelConfigMaxFieldLength (same package,
// already the single source of truth for this 256 value) rather than a
// second literal, so the two cannot drift apart independently of each
// other.
const ContextFabricModelIdentityMaxLength = 2*contextFabricOrgModelConfigMaxFieldLength + 1

func validModelIdentity(value string) bool {
	return stringLengthBetween(value, 1, ContextFabricModelIdentityMaxLength) && strings.TrimSpace(value) == value
}

// validDriverCategory reports whether value is one of the closed
// ContextFabricDriverCategory vocabulary members (CHAOS-3755 adversarial
// review finding H4). Before this, ContextFabricDriverJudgment.Category
// was an unbounded free string, so a model could pick a novel spelling
// that ContextFabricDriverCategoryRequiresClaimedFact's exact-match lookup
// would never recognize as fact-shaped -- silently bypassing value-level
// closure for a judgment that was, in substance, exactly the kind of
// canonical-fact claim closure exists to check. Closing the vocabulary
// makes that bypass structurally impossible: every category is either a
// known fact-shaped one (requires a claim) or a known narrative one
// (relationship/narrative -- doesn't), never an unrecognized third thing.
// validDriverCategory derives from contextFabricDriverCategories rather than
// restating the vocabulary in a second switch, so the accepted set and the
// declared set cannot drift apart.
func validDriverCategory(value ContextFabricDriverCategory) bool {
	for _, category := range contextFabricDriverCategories {
		if category == value {
			return true
		}
	}
	return false
}

func allStringsInSet[T ~string](values []T, valid func(T) bool) bool {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validInvestigationStatus(value ContextFabricInvestigationStatus) bool {
	switch value {
	case ContextFabricInvestigationComplete, ContextFabricInvestigationPartial, ContextFabricInvestigationDegraded, ContextFabricInvestigationClarificationRequired, ContextFabricInvestigationNoMatch:
		return true
	default:
		return false
	}
}

func validInvestigationShape(value ContextFabricInvestigationShape) bool {
	switch value {
	case ContextFabricShapeSingleSubject, ContextFabricShapeExplicitCohort, ContextFabricShapeDiscoveredCohort, ContextFabricShapeOpen:
		return true
	default:
		return false
	}
}

// validContextFabricSubjectKind delegates to the exported
// ValidContextFabricSubjectKind (context_fabric_types.go), which reads the
// single contextFabricSubjectKinds vocabulary. It previously carried its
// own switch listing all 15 members -- a second copy of the same list,
// which is exactly the drift the vocabulary array exists to prevent.
// Kept as an unexported alias so every existing call site in this package
// is untouched.
func validContextFabricSubjectKind(value ContextFabricSubjectKind) bool {
	return ValidContextFabricSubjectKind(value)
}

// validContextFabricCohortDataCompleteness accepts the empty string as a
// distinct, valid value: it means "RankCohort has not run for this member",
// not an out-of-vocabulary write. See ContextFabricCohortMember's doc
// comment.
func validContextFabricCohortDataCompleteness(value ContextFabricCohortDataCompleteness) bool {
	switch value {
	case "", ContextFabricCohortDataComplete, ContextFabricCohortDataPartial, ContextFabricCohortDataDegraded:
		return true
	default:
		return false
	}
}

// validContextFabricCohortMemberOutcome is CHAOS-4398 PR3's closed
// vocabulary check for ContextFabricCohortMember.Outcome (design doc §8).
// Unlike validContextFabricCohortDataCompleteness above, "" is NOT a valid
// value here on the required path -- Outcome is a brand-new field with no
// legacy shape to stay lenient for (see cohortMemberOutcomeRequired's own
// doc comment); callers gate the required-vs-absent decision themselves.
func validContextFabricCohortMemberOutcome(value ContextFabricCohortMemberOutcome) bool {
	switch value {
	case ContextFabricCohortOutcomeQualified, ContextFabricCohortOutcomeProvisional,
		ContextFabricCohortOutcomeInsufficientEvidence, ContextFabricCohortOutcomeNotApplicable:
		return true
	default:
		return false
	}
}

// contextFabricCohortRankingBasisLabels is the CLOSED vocabulary
// ContextFabricCohortMember.RankingBasis entries must be drawn from --
// mirrored here from internal/contextfabric/cohort_ranking.go's own
// RankingSignal*/Driver* constants (this package cannot import that one,
// which imports this one) rather than left to a bare length/count bound
// that would let a stray value or model-authored prose through
// undetected. A new signal family or driver label needs BOTH sides
// updated in the same change, the same discipline every other closed
// vocabulary in this file already applies.
var contextFabricCohortRankingBasisLabels = map[string]struct{}{
	"investment_mix":                              {},
	"health.compounding_risk":                     {},
	"operational_deficiencies.severity":           {},
	"readiness.coverage_gap":                      {},
	"workload.forecast_pressure":                  {},
	"investment_mix.reactive_share_high":          {},
	"investment_mix.deliberate_share_low":         {},
	"investment_mix.mix_concentrated":             {},
	"investment_mix.mix_shift_toward_operational": {},
	"investment_mix.mix_shift_toward_feature":     {},
	"investment_mix.mix_shift_other":              {},
}

func validContextFabricCohortRankingBasisLabel(value string) bool {
	_, ok := contextFabricCohortRankingBasisLabels[value]
	return ok
}

// contextFabricCohortMemberDriverWeights is CHAOS-4398 PR2's CLOSED
// signal-family -> weight map, mirrored from
// internal/contextfabric/cohort_ranking.go's weight* constants (same
// cross-package mirroring discipline as
// contextFabricCohortRankingBasisLabels above -- this package cannot
// import that one). A ContextFabricCohortMemberDriver's Weight must equal
// its Signal's own entry here exactly: a formula weight change needs a new
// RankingFormulaVersion and both sides of this mirror updated together.
var contextFabricCohortMemberDriverWeights = map[string]float64{
	"investment_mix":                    30,
	"health.compounding_risk":           25,
	"operational_deficiencies.severity": 20,
	"readiness.coverage_gap":            15,
	"workload.forecast_pressure":        10,
}

// contextFabricCohortMemberDriverRequiredFactKind (CHAOS-4398 PR3b, R4-style
// ruling) is the CLOSED signal-family -> FactKind map every driver's
// SourceClaimedFactIDs must resolve at least one entry against -- the
// SAME table ContextFabricDriverCategoryRequiresClaimedFact uses for
// synthesis-authored ContextFabricDriverJudgment.ClaimedFactIDs (its own
// "investment"/"health"/"deficiency"/"readiness"/"workload" categories),
// keyed here by the RankingCohort signal-family STRING (this package
// cannot import internal/contextfabric's RankingSignal* constants, same
// cross-package mirroring discipline as contextFabricCohortMemberDriverWeights
// above) rather than by ContextFabricDriverCategory, since a cohort member
// driver has no Category field of its own. RankCohort mints the citation
// itself at ranking time (see ContextFabricCohortMemberDriver.
// SourceClaimedFactIDs' own doc comment) -- this map is what
// validateCohortDriverClaimedFacts checks the mint against.
var contextFabricCohortMemberDriverRequiredFactKind = map[string]ContextFabricFactKind{
	"investment_mix":                    ContextFabricFactInvestment,
	"health.compounding_risk":           ContextFabricFactHealth,
	"operational_deficiencies.severity": ContextFabricFactOperationalDeficiencies,
	"readiness.coverage_gap":            ContextFabricFactReadiness,
	"workload.forecast_pressure":        ContextFabricFactWorkload,
}

// validateCohortDriverClaimedFacts is the cohort-member-driver twin of
// validateClaimedFactReferences: every ContextFabricCohortMemberDriver's
// SourceClaimedFactIDs must resolve (in claimed, built from
// r.ClaimedFacts) and at least one resolved entry must carry the FactKind
// contextFabricCohortMemberDriverRequiredFactKind names for that driver's
// Signal. Called once per result, after Drivers is shape-validated
// (ContextFabricCohortMemberDriver.validate checks bounds/uniqueness only
// -- it has no access to r.ClaimedFacts). There is deliberately no blanket
// "required" bound gating this the way per-driver shape checks are gated
// (team-lead ruling, CHAOS-4398 PR3b: "required-if-cited, not blanket-
// required" -- a driver RankCohort ranked but narrateCohortDriverJudgments
// never cited legitimately keeps SourceClaimedFactIDs empty forever, not
// just on legacy rows): an empty SourceClaimedFactIDs is always a no-op
// here, and the check only ever engages once a driver actually carries an
// ID to resolve.
func validateCohortDriverClaimedFacts(cohort *ContextFabricCohort, claimed map[string]ContextFabricClaimedFact) error {
	for _, member := range cohort.Members {
		memberKey := subjectKey(member.Subject)
		for _, driver := range member.Drivers {
			if len(driver.SourceClaimedFactIDs) == 0 {
				continue
			}
			requiredKind, hasRequirement := contextFabricCohortMemberDriverRequiredFactKind[driver.Signal]
			matchedKind := false
			for _, id := range driver.SourceClaimedFactIDs {
				fact, ok := claimed[id]
				if !ok {
					return fmt.Errorf("cohort member driver references unknown claimed fact %q", id)
				}
				// Codex R1 (CHAOS-4398 PR3b): this is the ONLY grounding
				// check a cohort-member-driver claim ever passes through
				// (narrateCohortDriverJudgments mints AFTER
				// SynthesisDraft.ValidateAgainst has already run, so that
				// per-subject grounding check never sees these claims --
				// see narrateCohortDriverJudgments' own doc comment).
				// Without this subject check, a driver could cite a
				// claim minted for a DIFFERENT cohort member and still
				// pass as long as some OTHER referenced ID happened to
				// carry the required Kind -- defeating the field's own
				// "this member's own evidence" guarantee. Every
				// referenced claim must belong to THIS member, not just
				// one of them.
				if subjectKey(fact.Subject) != memberKey {
					return fmt.Errorf("cohort member driver references claimed fact %q belonging to a different subject", id)
				}
				if hasRequirement && fact.Kind == requiredKind {
					matchedKind = true
				}
			}
			if hasRequirement && !matchedKind {
				return fmt.Errorf("cohort member driver signal %q requires a claimed fact of kind %q", driver.Signal, requiredKind)
			}
		}
	}
	return nil
}

// contextFabricInvestmentMixSubWeights is CHAOS-4398 PR2 R4's CLOSED
// threshold-label -> sub-weight map, mirrored from
// internal/contextfabric/cohort_ranking.go's subWeight* constants (same
// cross-package mirroring discipline as contextFabricCohortMemberDriverWeights
// above). investmentMixSignal's own Value is BY CONSTRUCTION the sum of
// exactly these sub-weights for whichever labels fired -- codex R3 finding
// 3: nothing checked that a driver's claimed ThresholdLabels actually
// reconstruct its own Value, so a driver could claim
// "reactive_share_high" fired while reporting Value: 0. The three
// mix_shift_* labels share ONE sub-weight (0.20, mutually exclusive --
// investmentMixSignal fires at most one).
var contextFabricInvestmentMixSubWeights = map[string]float64{
	"investment_mix.reactive_share_high":          0.35,
	"investment_mix.deliberate_share_low":         0.30,
	"investment_mix.mix_concentrated":             0.15,
	"investment_mix.mix_shift_toward_operational": 0.20,
	"investment_mix.mix_shift_toward_feature":     0.20,
	"investment_mix.mix_shift_other":              0.20,
}

// contextFabricMixShiftLabels is the mutually-exclusive subset of
// contextFabricInvestmentMixSubWeights' keys -- investmentMixSignal fires
// at most ONE of these three per member (codex R3 finding 1).
var contextFabricMixShiftLabels = map[string]struct{}{
	"investment_mix.mix_shift_toward_operational": {},
	"investment_mix.mix_shift_toward_feature":     {},
	"investment_mix.mix_shift_other":              {},
}

func validContextFabricCohortMemberDriverWindow(value ContextFabricCohortMemberDriverWindow) bool {
	switch value {
	case ContextFabricCohortMemberDriverWindowCurrent, ContextFabricCohortMemberDriverWindowCurrentVsPrior:
		return true
	default:
		return false
	}
}

// contextFabricCohortMemberDriverConcentrationMethods is CHAOS-4398 PR3's
// CLOSED vocabulary for ContextFabricCohortMemberDriver.ConcentrationMethod,
// mirrored from internal/contextfabric/cohort_ranking.go's
// ConcentrationMethod* constants (same cross-package mirroring discipline
// as contextFabricCohortMemberDriverWeights above). "max_share" is the only
// method today; CHAOS-4414 adds "hhi" -- a real Herfindahl-Hirschman Index
// -- as a genuinely NEW closed-vocab value, never a rename of this field.
var contextFabricCohortMemberDriverConcentrationMethods = map[string]struct{}{
	"max_share": {},
}

func validContextFabricCohortMemberDriverConcentrationMethod(value string) bool {
	_, ok := contextFabricCohortMemberDriverConcentrationMethods[value]
	return ok
}

func validResolutionState(value ContextFabricResolutionState) bool {
	switch value {
	case ContextFabricResolutionCommitted, ContextFabricResolutionProposed, ContextFabricResolutionAmbiguous, ContextFabricResolutionUnresolved:
		return true
	default:
		return false
	}
}

// validCommitGate (CHAOS-4087) is a plain-string closed-vocabulary check,
// not a dedicated Go enum type, matching graphrank.ResolutionTraceEvent.CommitGate's
// own bare-string precedent for the identical concept (live-only there).
// "" IS a valid member here -- the fail-closed "nothing recorded" reading
// ContextFabricCommitDecisionDigest.CommitGate's own doc comment
// establishes, not an omission to reject.
func validCommitGate(value string) bool {
	switch value {
	case "", "caller_hint_short_circuit", "pre_committed_exact_hint", "exact_index",
		"identity_fast_path", "lone_floor", "top_of_two", "vector_margin_rescue", "evidence_census":
		return true
	default:
		return false
	}
}

// validCommitDecisionDigestIdentityProven (CHAOS-4087, codex R1) rejects an
// IdentityProven value that contradicts what its CommitGate can ever
// produce, mirroring every graphrank.go/resolution.go call site's own fixed
// pairing (see chaos4085_commit_basis.go's CommitBasis.IdentityProven()):
//
//   - "" (unrecorded): every field must sit at its honest zero value -- a
//     digest claiming IdentityProven=true with no gate recorded is exactly
//     the "recorded and clean" false reading the fail-closed contract
//     exists to rule out.
//   - "pre_committed_exact_hint" / "identity_fast_path": always
//     CommitBasisCallerCanonicalID / CommitBasisAuthoritativeIdentity ->
//     IdentityProven must be true.
//   - "exact_index" / "lone_floor" / "top_of_two" / "vector_margin_rescue" /
//     "evidence_census": always CommitBasisStatistical -> IdentityProven
//     must be false.
//   - "caller_hint_short_circuit" is the ONE gate that legitimately varies
//     per subject at the SAME call site (resolution.go's
//     FinalizeExactResolutionWithBasis: a caller-explicit hint is proven,
//     a receipt-derived rider that merely rode along is not) -- both
//     values are valid here, see
//     TestChaos4085_ExactHintShortCircuitRecordsBasisPerClass.
func validCommitDecisionDigestIdentityProven(gate string, identityProven bool) bool {
	switch gate {
	case "":
		return !identityProven
	case "pre_committed_exact_hint", "identity_fast_path":
		return identityProven
	case "exact_index", "lone_floor", "top_of_two", "vector_margin_rescue", "evidence_census":
		return !identityProven
	case "caller_hint_short_circuit":
		return true
	default:
		return false
	}
}

// ValidContextFabricSubjectMatchMechanism reports whether value is one of the
// six closed enum members (CHAOS-3778 / AC-3778-6). Exported because
// graphrank's corroboration band counts DISTINCT mechanisms and must reject an
// unrecognized one at the boundary rather than counting it toward a commit.
func ValidContextFabricSubjectMatchMechanism(value ContextFabricSubjectMatchMechanism) bool {
	switch value {
	case ContextFabricMatchExact, ContextFabricMatchAlias, ContextFabricMatchProviderKey,
		ContextFabricMatchLexical, ContextFabricMatchVector, ContextFabricMatchTraversalParent:
		return true
	default:
		return false
	}
}

// validMatchMechanisms bounds the recorded mechanism set: at most one entry per
// enum member (six), every entry a recognized member, and no duplicates. An
// EMPTY set is valid -- see ContextFabricSubjectCandidate.MatchMechanisms for
// why absence must stay legal in v1.
func validMatchMechanisms(values []ContextFabricSubjectMatchMechanism) bool {
	if len(values) > 6 {
		return false
	}
	seen := make(map[ContextFabricSubjectMatchMechanism]struct{}, len(values))
	for _, value := range values {
		if !ValidContextFabricSubjectMatchMechanism(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validDriverStanding(value ContextFabricDriverStanding) bool {
	switch value {
	case ContextFabricDriverPrincipal, ContextFabricDriverContributing, ContextFabricDriverSymptom, ContextFabricDriverContext, ContextFabricDriverWithheld:
		return true
	default:
		return false
	}
}

func validDerivationMethod(value ContextFabricDerivationMethod) bool {
	switch value {
	case ContextFabricDerivationCanonicalStructured, ContextFabricDerivationDeterministicProjection, ContextFabricDerivationGraphAssociated, ContextFabricDerivationModelExtracted, ContextFabricDerivationRuleInferred:
		return true
	default:
		return false
	}
}

// ValidContextFabricRelationshipType reports whether value is one of the
// closed ContextFabricRelationshipType vocabulary members. Exported so a
// producer package (e.g. devhealthsource, falkorgraph) can self-verify the
// types it claims to produce without duplicating this switch -- see the
// AC-3779-9 cross-wiring test in cmd/acr-projector, which is the only
// caller today.
func ValidContextFabricRelationshipType(value ContextFabricRelationshipType) bool {
	return validContextFabricRelationshipType(value)
}

// validContextFabricRelationshipType reports whether value is one of the
// closed ContextFabricRelationshipType vocabulary members. See that type's
// doc comment (CHAOS-3779, closing drift item D9 / the H4 lesson).
func validContextFabricRelationshipType(value ContextFabricRelationshipType) bool {
	switch value {
	case ContextFabricRelationshipBelongsToRepository, ContextFabricRelationshipBelongsToPullRequest,
		ContextFabricRelationshipCorrelatedWithIncident, ContextFabricRelationshipRelatedTo,
		ContextFabricRelationshipDocumentedBy, ContextFabricRelationshipHasEpisode,
		ContextFabricRelationshipBlocks, ContextFabricRelationshipPartOf,
		ContextFabricRelationshipRelatesTo, ContextFabricRelationshipDuplicates,
		ContextFabricRelationshipBelongsToProject, ContextFabricRelationshipOwnedByTeam:
		return true
	default:
		return false
	}
}

func validEpistemicStatus(value ContextFabricEpistemicStatus) bool {
	switch value {
	case ContextFabricEpistemicObserved, ContextFabricEpistemicSourceAsserted, ContextFabricEpistemicInferred, ContextFabricEpistemicDisputed, ContextFabricEpistemicSuperseded, ContextFabricEpistemicUnknown:
		return true
	default:
		return false
	}
}

func validSourceState(value ContextFabricSourceState) bool {
	switch value {
	case ContextFabricSourceAvailable, ContextFabricSourceStale, ContextFabricSourceUnavailable, ContextFabricSourceUnconfigured, ContextFabricSourceUnauthorized, ContextFabricSourceNoData, ContextFabricSourceTruncated, ContextFabricSourceConflicted, ContextFabricSourceNotApplicable, ContextFabricSourcePruned:
		return true
	default:
		return false
	}
}

// validFactKind derives from contextFabricFactKinds rather than restating
// the vocabulary in a second switch, so the accepted set and the declared
// set cannot drift apart.
func validFactKind(value ContextFabricFactKind) bool {
	for _, kind := range contextFabricFactKinds {
		if kind == value {
			return true
		}
	}
	return false
}

// validEvidenceEntityType derives from contextFabricEvidenceEntityTypes
// rather than restating the vocabulary in a second switch, so the accepted
// set and the declared set cannot drift apart -- mirrors validFactKind.
func validEvidenceEntityType(value ContextFabricEvidenceEntityType) bool {
	for _, entityType := range contextFabricEvidenceEntityTypes {
		if entityType == value {
			return true
		}
	}
	return false
}
