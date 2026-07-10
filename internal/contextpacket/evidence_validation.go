package contextpacket

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func validateEvidence(ref contractsv1.EvidenceRef) error {
	if ref.SchemaVersion != contractsv1.EvidenceRefSchema || !evidenceString(ref.EvidenceRefID, 8, 256) || !evidenceString(ref.Source.System, 1, 100) || !evidenceString(ref.Source.EntityType, 1, 100) || !evidenceString(ref.Source.EntityID, 1, 1024) || !evidenceString(ref.Source.DisplayLabel, 1, 1000) || !evidenceString(ref.Citation, 1, 2000) || !optionalEvidenceURI(ref.Source.SafeURI) || !optionalEvidenceString(ref.SourceVersion, 512) || !optionalEvidenceString(ref.SnapshotHash, 256) || !optionalEvidenceString(ref.ContentDigest, 256) || ref.ObservedAt.IsZero() {
		return fmt.Errorf("invalid evidence_ref")
	}
	if math.IsNaN(ref.Confidence) || math.IsInf(ref.Confidence, 0) || ref.Confidence < 0 || ref.Confidence > 1 {
		return fmt.Errorf("invalid evidence confidence")
	}
	switch ref.Provenance {
	case "native", "explicit_text", "heuristic", "derived":
	default:
		return fmt.Errorf("invalid evidence provenance")
	}
	switch ref.Availability {
	case contractsv1.EvidenceAvailable, contractsv1.EvidenceStale, contractsv1.EvidenceRedacted, contractsv1.EvidenceDeleted, contractsv1.EvidenceUnauthorized:
	default:
		return fmt.Errorf("invalid evidence availability")
	}
	return nil
}

func evidenceString(value string, minimum, maximum int) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) >= minimum && utf8.RuneCountInString(value) <= maximum
}

func optionalEvidenceString(value string, maximum int) bool {
	return utf8.RuneCountInString(value) <= maximum
}

func optionalEvidenceURI(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && utf8.RuneCountInString(value) <= 2048
}

func validateExpandedEvidence(value contractsv1.ExpandedEvidence) error {
	if value.SchemaVersion != contractsv1.ExpandedEvidenceSchema {
		return fmt.Errorf("invalid expanded evidence")
	}
	return validateEvidence(value.Evidence)
}
