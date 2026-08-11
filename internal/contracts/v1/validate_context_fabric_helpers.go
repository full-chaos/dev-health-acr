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

func validateDrivers(values []ContextFabricDriverJudgment) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("drivers: %w", err)
		}
		if _, exists := seen[value.DriverID]; exists {
			return fmt.Errorf("driver IDs must be unique")
		}
		seen[value.DriverID] = struct{}{}
	}
	return nil
}

func validateFindings(name string, values []ContextFabricFinding) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, exists := seen[value.FindingID]; exists {
			return fmt.Errorf("%s IDs must be unique", name)
		}
		seen[value.FindingID] = struct{}{}
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
		if !stringLengthBetween(value, 8, 256) || strings.TrimSpace(value) != value {
			return false
		}
	}
	return uniqueStrings(values)
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
	case ContextFabricSubjectOrganization, ContextFabricSubjectTeam, ContextFabricSubjectProject, ContextFabricSubjectRepository, ContextFabricSubjectWorkItem, ContextFabricSubjectPullRequest, ContextFabricSubjectDeployment, ContextFabricSubjectIncident, ContextFabricSubjectDocument, ContextFabricSubjectDecision, ContextFabricSubjectEpisode, ContextFabricSubjectMetric:
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
