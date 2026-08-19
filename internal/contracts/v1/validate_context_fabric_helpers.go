package v1

import (
	"fmt"
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

func validContextFabricSubjectKind(value ContextFabricSubjectKind) bool {
	switch value {
	case ContextFabricSubjectOrganization, ContextFabricSubjectTeam, ContextFabricSubjectProject, ContextFabricSubjectRepository, ContextFabricSubjectWorkItem, ContextFabricSubjectPullRequest, ContextFabricSubjectDeployment, ContextFabricSubjectIncident, ContextFabricSubjectDocument, ContextFabricSubjectDecision, ContextFabricSubjectEpisode, ContextFabricSubjectMetric, ContextFabricSubjectPullRequestReview, ContextFabricSubjectCIRun, ContextFabricSubjectWorkItemRef:
		return true
	default:
		return false
	}
}

func validResolutionState(value ContextFabricResolutionState) bool {
	switch value {
	case ContextFabricResolutionCommitted, ContextFabricResolutionProposed, ContextFabricResolutionAmbiguous, ContextFabricResolutionUnresolved:
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
