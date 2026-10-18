package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestCIRunLabelCarriesThePipelineNameWhenStored(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	tables = append(tables, fakeTable{match: "FROM ci_pipeline_runs AS c", rows: [][]any{
		{"run-1", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, "Release build"},
		{"run-2", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, ""},
		{"run-3", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, strings.Repeat("w", 300)},
		{"run-4", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, "CI"},
		{"run-5", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, "ci build"},
		{"run-6", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, "CIX"},
	}})
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatal(err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil || !available {
		t.Fatalf("batch available=%t err=%v", available, err)
	}
	if err := batch.Validate(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ci_pipeline_run.v2:repo-1:run-1": "CI Release build #run-1",
		"ci_pipeline_run.v2:repo-1:run-2": "CI run-2",
		"ci_pipeline_run.v2:repo-1:run-3": "CI " + strings.Repeat("w", 80) + " #run-3",
		"ci_pipeline_run.v2:repo-1:run-4": "CI #run-4",
		"ci_pipeline_run.v2:repo-1:run-5": "ci build #run-5",
		"ci_pipeline_run.v2:repo-1:run-6": "CI CIX #run-6",
	}
	seen := 0
	for _, entity := range batch.Entities {
		if label, ok := want[entity.Subject.CanonicalID]; ok {
			seen++
			if entity.Subject.Label != label {
				t.Errorf("%s label = %q, want %q", entity.Subject.CanonicalID, entity.Subject.Label, label)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d of %d CI run entities", seen, len(want))
	}
}
