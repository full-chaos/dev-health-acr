package sidecar

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestRankingRowRendersFromSubjectKeysOnce(t *testing.T) {
	t.Parallel()
	id, label, kind := "project_1", "P One", "project"
	str := func(v *string) contractsv1.ContextFabricScalarValue {
		return contractsv1.ContextFabricScalarValue{String: v}
	}
	row := contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
		"subject_canonical_id": str(&id), "subject_label": str(&label), "subject_kind": str(&kind),
		"team_canonical_id": str(&id), "team_label": str(&label),
	}}
	line := rankingTableRowLine(row)
	want := "- project canonical id: project_1; project label: P One"
	if !strings.Contains(line, want) {
		t.Fatalf("line lacks %q: %s", want, line)
	}
	if strings.Count(line, "canonical id") != 1 || strings.Count(line, "label") != 1 {
		t.Fatalf("deprecated keys rendered twice: %s", line)
	}
}
