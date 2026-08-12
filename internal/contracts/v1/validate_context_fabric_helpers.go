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

func validateDrivers(values []ContextFabricDriverJudgment, claimed map[string]ContextFabricClaimedFact) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
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

func validateFindings(name string, values []ContextFabricFinding, claimed map[string]ContextFabricClaimedFact) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
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
	if values == nil || len(values) > 250 {
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

func validatePaths(values []ContextFabricRelationshipPath) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
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
		if value != nil && value.IsZero() {
			return fmt.Errorf("temporal timestamp is invalid")
		}
	}
	if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		return fmt.Errorf("valid_to precedes valid_from")
	}
	return nil
}

func boundedEvidenceRefs(values []string, maximum int, allowEmpty bool) bool {
	if values == nil || len(values) > maximum || (!allowEmpty && len(values) == 0) {
		return false
	}
	for _, value := range values {
		// '|' is the delimited-string separator backends such as the
		// zepgraph adapter use to encode a list of evidence ref IDs; an
		// evidence ref ID containing it would corrupt that encoding and
		// silently narrow the stored evidence closure.
		if !stringLengthBetween(value, 8, 256) || strings.TrimSpace(value) != value || strings.Contains(value, "|") {
			return false
		}
	}
	return uniqueStrings(values)
}

// containsSeparatorCharacter reports whether any value contains '|', the
// delimited-string separator character used by backends that persist a list
// of strings as a single "|a|b|"-encoded field (see zepgraph's encodeScope).
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
	case ContextFabricSubjectOrganization, ContextFabricSubjectTeam, ContextFabricSubjectProject, ContextFabricSubjectRepository, ContextFabricSubjectWorkItem, ContextFabricSubjectPullRequest, ContextFabricSubjectDeployment, ContextFabricSubjectIncident, ContextFabricSubjectDocument, ContextFabricSubjectDecision, ContextFabricSubjectEpisode, ContextFabricSubjectMetric, ContextFabricSubjectPullRequestReview, ContextFabricSubjectCIRun:
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
	case ContextFabricSourceAvailable, ContextFabricSourceStale, ContextFabricSourceUnavailable, ContextFabricSourceUnconfigured, ContextFabricSourceUnauthorized, ContextFabricSourceNoData, ContextFabricSourceTruncated, ContextFabricSourceConflicted, ContextFabricSourceNotApplicable:
		return true
	default:
		return false
	}
}

func validFactKind(value ContextFabricFactKind) bool {
	switch value {
	case ContextFabricFactIdentity, ContextFabricFactMembership, ContextFabricFactStatus, ContextFabricFactActualCompletion, ContextFabricFactWork, ContextFabricFactBlockers, ContextFabricFactRequiredChildren, ContextFabricFactPullRequests, ContextFabricFactReviews, ContextFabricFactContinuousIntegration, ContextFabricFactDeployments, ContextFabricFactIncidents, ContextFabricFactMetrics, ContextFabricFactHealth, ContextFabricFactWorkload, ContextFabricFactInvestment, ContextFabricFactReadiness, ContextFabricFactOperationalDeficiencies, ContextFabricFactSourceHealth, ContextFabricFactEvidence:
		return true
	default:
		return false
	}
}
