package devhealthfacts_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// CHAOS-7120 codex r1 P1: the project completion counts are computed over
// the work items the CALLER is authorized for. Every count and ratio of the
// project rollup must be declared CallerScoped, so read_facts serves them
// with population_scope and a client never reads a restricted caller's
// subset as the project-wide ratio.
func TestCHAOS7120ProjectCompletionCountsAreCallerScoped(t *testing.T) {
	var capability contextfabric.FactCapability
	for _, provider := range devhealthfacts.NewProviders(nil) {
		if provider.Capability().Kind == contextfabric.FactActualCompletion {
			capability = provider.Capability()
		}
	}
	if capability.Kind == "" {
		t.Fatal("actual_completion provider not registered")
	}
	scoped := 0
	for _, field := range capability.Fields {
		if !field.AppliesTo(contextfabric.SubjectProject) || field.AppliesTo(contextfabric.SubjectWorkItem) {
			continue
		}
		numeric := field.Type == contextfabric.FactFieldInteger || field.Type == contextfabric.FactFieldNumber
		if numeric && !field.CallerScoped {
			t.Errorf("project completion field %s is a count or ratio over the caller's authorized items but is not CallerScoped", field.Name)
		}
		if field.CallerScoped {
			scoped++
		}
		if field.Aggregate {
			t.Errorf("project completion field %s must not be Aggregate (it is not computed over every owned repository)", field.Name)
		}
	}
	if scoped == 0 {
		t.Fatal("no caller-scoped project completion field: the check measured nothing")
	}
}
