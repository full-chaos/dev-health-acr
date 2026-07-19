package sidecar

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxLocalWarningCodeBytes  = 128
	maxLocalEvidenceStartLine = 1<<31 - 1
)

func ValidateLocalIndexCapabilities(capabilities LocalIndexCapabilities) error {
	if !capabilities.Available {
		if capabilities.ProviderID != "" || capabilities.ProviderVersion != "" || capabilities.MaxItems != 0 || capabilities.MaxOutputTokens != 0 {
			return invalidLocalIndexValue(ErrInvalidLocalIndexCapabilities, "unavailable capabilities")
		}
		return nil
	}
	if !boundedNonEmpty(capabilities.ProviderID, maxLocalIndexProviderIDBytes) || !boundedNonEmpty(capabilities.ProviderVersion, maxLocalIndexProviderVersionBytes) || !boundedPositive(capabilities.MaxItems, maxLocalEvidenceItems) || !boundedPositive(capabilities.MaxOutputTokens, maxLocalEvidenceTokens) {
		return invalidLocalIndexValue(ErrInvalidLocalIndexCapabilities, "capabilities")
	}
	return nil
}

func ValidateLocalContextRequest(request LocalContextRequest) error {
	if !boundedNonEmpty(request.TaskID, maxLocalTaskIDBytes) || !boundedNonEmpty(request.Goal, maxLocalTaskBytes) || (request.TaskRef != "" && !validCodeGraphText(request.TaskRef, maxLocalTaskIDBytes)) || !validRequestedCategories(request.RequestedCategories) || !boundedPositive(request.MaxItems, maxLocalEvidenceItems) || !boundedPositive(request.MaxOutputTokens, maxLocalEvidenceTokens) {
		return invalidLocalIndexValue(ErrInvalidLocalContextRequest, "request")
	}
	return nil
}

func ValidateLocalEvidenceBundle(bundle LocalEvidenceBundle) error {
	_, _, _, err := localEvidenceBundleUsage(bundle)
	return err
}

func ValidateLocalEvidenceBundleForRequest(request LocalContextRequest, capabilities LocalIndexCapabilities, bundle LocalEvidenceBundle) error {
	if err := ValidateLocalContextRequest(request); err != nil {
		return err
	}
	if err := ValidateLocalIndexCapabilities(capabilities); err != nil {
		return err
	}
	if !capabilities.Available {
		return ErrLocalIndexUnavailable
	}
	if bundle.ProviderID != capabilities.ProviderID || bundle.ProviderVersion != capabilities.ProviderVersion || len(bundle.Evidence) > request.MaxItems || len(bundle.Evidence) > capabilities.MaxItems {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "request metadata")
	}
	_, tokens, _, err := localEvidenceBundleUsage(bundle)
	if err != nil {
		return err
	}
	if tokens > request.MaxOutputTokens || tokens > capabilities.MaxOutputTokens {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence tokens")
	}
	return nil
}

func ValidateLocalExpandedEvidence(evidence LocalExpandedEvidence) error {
	if !boundedNonEmpty(evidence.ID, maxLocalEvidenceIDBytes) || !boundedLocalLocator(evidence.Locator) || !boundedNonEmpty(evidence.Title, maxLocalEvidenceTitleBytes) || len(evidence.Excerpt) > maxLocalEvidenceExcerptBytes || evidence.EstimatedTokens < 0 || evidence.EstimatedTokens > maxLocalEvidenceTokens || !safeOptionalText(evidence.QueryID, maxLocalIndexQueryIDBytes) || !safeOptionalText(evidence.Relation, maxLocalIndexQueryVersionBytes) || (evidence.RepositoryPath != "" && !validRepositoryRelativePath(evidence.RepositoryPath)) || evidence.StartLine < 0 || evidence.StartLine > maxLocalEvidenceStartLine || (evidence.StartLine != 0 && evidence.RepositoryPath == "") {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence")
	}
	return nil
}

func validateLocalEvidenceMetadata(bundle LocalEvidenceBundle) error {
	if bundle.IndexedAt != nil {
		if _, err := bundle.IndexedAt.MarshalText(); err != nil {
			return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "indexed at")
		}
	}
	if !safeOptionalText(bundle.IndexedRef, maxLocalEvidenceLocatorBytes) || (bundle.IndexedCommit != "" && !validCommitSHA(bundle.IndexedCommit)) || len(bundle.Warnings) > 100 {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "metadata")
	}
	seen := make(map[string]struct{}, len(bundle.Warnings))
	for _, warning := range bundle.Warnings {
		if !boundedNonEmpty(warning, maxLocalWarningCodeBytes) || !safeOptionalText(warning, maxLocalWarningCodeBytes) {
			return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "warning")
		}
		if _, found := seen[warning]; found {
			return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "duplicate warning")
		}
		seen[warning] = struct{}{}
	}
	return nil
}

func boundedNonEmpty(value string, maximum int) bool {
	return len(value) <= maximum && strings.TrimSpace(value) != ""
}
func boundedPositive(value, maximum int) bool { return value > 0 && value <= maximum }

func boundedLocalLocator(value string) bool {
	return boundedNonEmpty(value, maxLocalEvidenceLocatorBytes) && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "\\") && !hasWindowsAbsolutePathPrefix(value) && safeOptionalText(value, maxLocalEvidenceLocatorBytes)
}

func safeOptionalText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func hasWindowsAbsolutePathPrefix(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}
