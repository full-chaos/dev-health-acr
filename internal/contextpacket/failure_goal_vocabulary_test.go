package contextpacket

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every stored failure status has a goal word that names it, so the two
// vocabularies cannot drift apart.
func TestEveryStoredFailureStatusHasAGoalWord(t *testing.T) {
	for status := range failureTokens {
		named := false
		for _, part := range strings.Split(status, "_") {
			named = named || failureGoalWords[part]
		}
		if !named {
			t.Errorf("failure status %q has no word in failureGoalWords", status)
		}
	}
}

func TestGoalNamesFailureOnlyForAFailureWord(t *testing.T) {
	for goal, want := range map[string]bool{
		"Reduce failed deployments and CI failures": true,
		"no failures this week":                     true,
		"why was the rollback needed":               true,
		"Add saved payment methods at checkout":     false,
		"release notes":                             false,
	} {
		if got := goalNamesFailure(goal); got != want {
			t.Errorf("goalNamesFailure(%q) = %v, want %v", goal, got, want)
		}
	}
}

// Every code-health entity type is emitted by the hotspot or complexity
// catalog source, and each of those sources emits one of them.
func TestCodeHealthEntityTypesAreTheHotspotAndComplexitySources(t *testing.T) {
	for entityType := range codeHealthEntityTypes {
		found := false
		for _, query := range SourceQueryCatalogV1 {
			if (query.ID == "file_hotspots.v1" || query.ID == "file_complexity.v1") && strings.Contains(query.Statement, "'"+entityType+"' entity_type") {
				found = true
			}
		}
		if !found {
			t.Errorf("code-health entity type %q is not emitted by file_hotspots.v1 or file_complexity.v1", entityType)
		}
	}
	for _, id := range []string{"file_hotspots.v1", "file_complexity.v1"} {
		emitted := false
		for _, query := range SourceQueryCatalogV1 {
			for entityType := range codeHealthEntityTypes {
				emitted = emitted || (query.ID == id && strings.Contains(query.Statement, "'"+entityType+"' entity_type"))
			}
		}
		if !emitted {
			t.Errorf("%s emits no code-health entity type", id)
		}
	}
}

func TestGoalNamesCodeHealthOnlyForACodeHealthWord(t *testing.T) {
	for goal, want := range map[string]bool{
		"reduce churn in the checkout service": true,
		"which files are hotspots":             true,
		"refactor the payment module":          true,
		"Reduce failed deployments":            false,
		"release notes":                        false,
	} {
		if got := goalNamesCodeHealth(goal); got != want {
			t.Errorf("goalNamesCodeHealth(%q) = %v, want %v", goal, got, want)
		}
	}
}

func internalRow(id, entityType, citation string, metadata map[string]any) contractsv1.EvidenceRef {
	return contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: id,
		Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: entityType, EntityID: id, DisplayLabel: id},
		Provenance: contractsv1.EvidenceProvenanceNative, Confidence: 1, Citation: citation,
		ObservedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Availability: contractsv1.EvidenceAvailable, Metadata: metadata,
	}
}

// Failure pressure is the one definition of a failure row: every row it
// accepts is served as pressure, and blocked work (also pressure) is not one.
func TestFailurePressureIsTheFailureSubsetOfPressure(t *testing.T) {
	failures := []contractsv1.EvidenceRef{
		internalRow("ci", "ci_pipeline_run", "failed", nil),
		internalRow("check", "check", "failed", nil),
		internalRow("deploy", "deployment", "status=failed", map[string]any{metadataDeploymentStatus: "failed"}),
		internalRow("incident", "incident", "status=open", nil),
	}
	for _, ref := range failures {
		if !failurePressure(ref) || evidenceCategoryRule(ref).category != contractsv1.CategoryPressure {
			t.Errorf("%s: failurePressure=%v category=%s, want a failure served as pressure", ref.EvidenceRefID, failurePressure(ref), evidenceCategoryRule(ref).category)
		}
	}
	for _, ref := range []contractsv1.EvidenceRef{
		internalRow("ci-ok", "ci_pipeline_run", "success", nil),
		internalRow("deploy-ok", "deployment", "status=success", map[string]any{metadataDeploymentStatus: "success"}),
		internalRow("incident-closed", "incident", "status=resolved", nil),
	} {
		if failurePressure(ref) || evidenceCategoryRule(ref).category == contractsv1.CategoryPressure {
			t.Errorf("%s is not a failure but is served as pressure or failure pressure", ref.EvidenceRefID)
		}
	}
	blocker := internalRow("blocker", "work_item_dependency", "BLOCKS", map[string]any{metadataRelationKey: "blocks:fwd", metadataBlockerID: "PAY-1", metadataBlockerStatus: "in_progress"})
	if got := newRoleResolver([]contractsv1.EvidenceRef{blocker}).rule(blocker).category; got != contractsv1.CategoryPressure {
		t.Fatalf("active blocker category = %s, want pressure (role and quota unchanged)", got)
	}
	if failurePressure(blocker) {
		t.Fatal("an active blocker is blocked work, not a failure")
	}
}
