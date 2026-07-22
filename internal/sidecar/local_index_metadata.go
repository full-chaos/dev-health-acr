package sidecar

import (
	"math"
	"regexp"
	"unicode"
	"unicode/utf8"
)

const (
	maxLocalIndexedRefBytes       = 512
	maxLocalWarnings              = 100
	maxLocalWarningBytes          = 128
	maxLocalEvidenceQueryBytes    = 64
	maxLocalEvidenceRelationBytes = 64
)

var lowercaseCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func localBundleMetadataBytes(bundle LocalEvidenceBundle) (int, error) {
	bytes := 1 + len(bundle.IndexedRef) + len(bundle.IndexedCommit)
	if bundle.IndexedAt != nil {
		encoded, err := bundle.IndexedAt.MarshalText()
		if err != nil {
			return 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "indexed at")
		}
		bytes += len(encoded)
	}
	if !validLocalMetadataText(bundle.IndexedRef, maxLocalIndexedRefBytes) || (bundle.IndexedCommit != "" && !lowercaseCommitPattern.MatchString(bundle.IndexedCommit)) || len(bundle.Warnings) > maxLocalWarnings {
		return 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "metadata")
	}
	seen := make(map[string]struct{}, len(bundle.Warnings))
	for _, warning := range bundle.Warnings {
		if !validLocalMetadataText(warning, maxLocalWarningBytes) {
			return 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "warnings")
		}
		if _, duplicate := seen[warning]; duplicate {
			return 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "warnings")
		}
		seen[warning] = struct{}{}
		bytes += len(warning)
	}
	return bytes, nil
}

func localEvidenceMetadataBytes(evidence LocalExpandedEvidence) (int, error) {
	if !validLocalMetadataText(evidence.QueryID, maxLocalEvidenceQueryBytes) || !validLocalMetadataText(evidence.Relation, maxLocalEvidenceRelationBytes) || !validRepositoryRelativePath(evidence.RepositoryPath) && evidence.RepositoryPath != "" || evidence.StartLine < 0 || evidence.StartLine > math.MaxInt32 || evidence.StartLine != 0 && evidence.RepositoryPath == "" {
		return 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence metadata")
	}
	return len(evidence.QueryID) + len(evidence.Relation) + len(evidence.RepositoryPath) + decimalDigits(evidence.StartLine), nil
}

func validLocalMetadataText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func decimalDigits(value int) int {
	if value == 0 {
		return 1
	}
	digits := 0
	for value > 0 {
		digits++
		value /= 10
	}
	return digits
}
