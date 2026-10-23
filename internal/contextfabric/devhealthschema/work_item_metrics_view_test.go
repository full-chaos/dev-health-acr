package devhealthschema_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

func TestWorkItemMetricsViewPredicateKeepsLegacyRowsAndTheWantedView(t *testing.T) {
	if got, want := devhealthschema.WorkItemMetricsViewPredicate("org"), "row_view IN ('', 'org')"; got != want {
		t.Errorf("org predicate = %q, want %q", got, want)
	}
	if got, want := devhealthschema.WorkItemMetricsViewPredicate("scope"), "row_view IN ('', 'scope')"; got != want {
		t.Errorf("scope predicate = %q, want %q", got, want)
	}
	if got, want := devhealthschema.WorkItemMetricsViewOrder("org"), "(row_view = 'org') DESC"; got != want {
		t.Errorf("org order = %q, want %q", got, want)
	}
	if got, want := devhealthschema.WorkItemMetricsViewOrder("scope"), "(row_view = 'scope') DESC"; got != want {
		t.Errorf("scope order = %q, want %q", got, want)
	}
}

// A read that cannot name a real view must not run without one.
func TestWorkItemMetricsViewHelpersRefuseAnUnknownView(t *testing.T) {
	for name, call := range map[string]func(){
		"predicate empty":  func() { devhealthschema.WorkItemMetricsViewPredicate("") },
		"predicate quote":  func() { devhealthschema.WorkItemMetricsViewPredicate("org') OR 1=1 --") },
		"predicate team":   func() { devhealthschema.WorkItemMetricsViewPredicate("team") },
		"order empty":      func() { devhealthschema.WorkItemMetricsViewOrder("") },
		"order injection":  func() { devhealthschema.WorkItemMetricsViewOrder("org') DESC, (1") },
		"order other case": func() { devhealthschema.WorkItemMetricsViewOrder("Org") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			call()
		}()
	}
}
