package contextpacket

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func vocabularyEvidence(provenance string) contractsv1.EvidenceRef {
	return contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: "evidence-0001",
		Source:     contractsv1.EvidenceSource{System: "git", EntityType: "commit", EntityID: "abc123", DisplayLabel: "a commit"},
		Provenance: provenance, Confidence: 1, Citation: "a citation", ObservedAt: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
		Availability: contractsv1.EvidenceAvailable,
	}
}

func TestEvidenceProvenanceOutsideTheSetIsQuarantinedOnTheServedPath(t *testing.T) {
	for _, provenance := range contractsv1.EvidenceProvenanceVocabulary() {
		if got := validateEvidenceBundle([]contractsv1.EvidenceRef{vocabularyEvidence(provenance)}, nil); len(got.valid) != 1 || len(got.quarantined) != 0 {
			t.Errorf("%s: valid %d quarantined %d", provenance, len(got.valid), len(got.quarantined))
		}
	}
	for _, off := range []string{fmt.Sprintf("%s_%d", "native", 2), ""} {
		got := validateEvidenceBundle([]contractsv1.EvidenceRef{vocabularyEvidence(off)}, nil)
		if len(got.valid) != 0 || len(got.quarantined) != 1 || got.quarantined[0].Rule != "invalid evidence provenance" {
			t.Errorf("%q: valid %d quarantined %+v", off, len(got.valid), got.quarantined)
		}
	}
}

func TestWatermarkStatusOutsideTheSetFailsPacketValidation(t *testing.T) {
	freshness := func(status string) contractsv1.Freshness {
		return contractsv1.Freshness{Watermarks: []contractsv1.SourceWatermark{{Source: "git", Status: status}}}
	}
	for _, status := range contractsv1.SourceWatermarkStatusVocabulary() {
		if !validFreshness(freshness(status)) {
			t.Errorf("%s refused", status)
		}
	}
	if validFreshness(freshness(fmt.Sprintf("%s_%d", "fresh", 2))) {
		t.Error("an off-set watermark status was served")
	}
}

var (
	sqlInList        = regexp.MustCompile(`(?i)\bIN\s*\([^)]*\)`)
	sqlOutputLiteral = regexp.MustCompile(`(?:^|[,(]\s*)'([a-z_]*)'\s*(?:[,)]|$)`)
	sqlAliasLiteral  = regexp.MustCompile(`'([a-z_]*)'\s+(?:AS\s+)?provenance\b`)
)

// An empty string is the deliberate "unknown, fail downstream" output of a
// reviewed mapping; every other emitted literal must be a served provenance.
func TestProvenanceSQLEmitsOnlyServedValues(t *testing.T) {
	allowed := append(contractsv1.EvidenceProvenanceVocabulary(), "")
	check := func(name, sql string) int {
		found := 0
		for _, m := range sqlOutputLiteral.FindAllStringSubmatch(sqlInList.ReplaceAllString(sql, "IN ()"), -1) {
			found++
			if !slices.Contains(allowed, m[1]) {
				t.Errorf("%s emits %q, outside the provenance vocabulary", name, m[1])
			}
		}
		return found
	}
	mappings := map[string]string{
		"aiWorkflowArtifactProvenanceMapping": aiWorkflowArtifactProvenanceMapping, "aiReviewOutcomeProvenanceMapping": aiReviewOutcomeProvenanceMapping,
		"deploymentIncidentProvenanceMapping": deploymentIncidentProvenanceMapping, "incidentRelationshipProvenanceMapping": incidentRelationshipProvenanceMapping,
	}
	total := 0
	for name, sql := range mappings {
		// A mapping's condition literals (source values) are not outputs: keep
		// only the segments after the comma that follows a condition.
		total += check(name, strings.ReplaceAll(sql, " = '", " = "))
	}
	aliases := 0
	statements := map[string]string{"read_adapter": clickHouseEvidenceQueryV1}
	for _, q := range SourceQueryCatalogV1 {
		statements["catalog/"+q.ID] = q.Statement
	}
	for _, q := range SourceRowOnlyQueriesV2 {
		statements["rowonly/"+q.ID] = q.Statement
	}
	for _, q := range OrganizationRowQueriesV1 {
		statements["organization/"+q.ID] = q.Statement
	}
	for name, sql := range statements {
		for _, m := range sqlAliasLiteral.FindAllStringSubmatch(sql, -1) {
			aliases++
			if !slices.Contains(contractsv1.EvidenceProvenanceVocabulary(), m[1]) {
				t.Errorf("%s emits %q as provenance", name, m[1])
			}
		}
	}
	if total < 8 || aliases < 15 {
		t.Fatalf("found %d mapping outputs and %d alias literals; the measurement did not happen", total, aliases)
	}
}
