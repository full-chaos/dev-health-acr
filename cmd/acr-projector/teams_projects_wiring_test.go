package main

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// unreachableClient stands in for ClickHouse in wiring tests: any query it
// receives is a test failure, which is what makes the "disabled" assertion
// below meaningful. A disabled source must not merely return an empty batch
// -- it must not touch the database at all.
type unreachableClient struct{ t *testing.T }

func (c unreachableClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.t.Fatal("a disabled teams/projects source must not query ClickHouse")
	return nil, nil
}

// TestTeamsProjectsSourceIsRegisteredRegardlessOfItsFeatureFlag is the
// composition-root reachability check CHAOS-3802 D2 requires: prove the flag
// is dead in NEITHER direction.
//
// Registration must be unconditional. A source dropped from the coordinator's
// list on a false flag would strand its (org_id, source) projection
// checkpoint rather than idling it, so flipping the flag back on would resume
// from a stale watermark instead of the full snapshot a never-registered
// source gets -- silently skipping every team and project whose updated_at
// predates the stranded cursor.
func TestTeamsProjectsSourceIsRegisteredRegardlessOfItsFeatureFlag(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		source, err := devhealthsource.NewTeamsProjectsSource(unreachableClient{t: t}, enabled)
		if err != nil {
			t.Fatalf("NewTeamsProjectsSource(enabled=%v): %v", enabled, err)
		}
		var found bool
		for _, pair := range projectionSources(nil, nil, source) {
			if pair.Name == devhealthsource.TeamsProjectsSourceName {
				found = true
				if pair.Source == nil {
					t.Fatalf("enabled=%v: teams/projects registered with a nil source", enabled)
				}
			}
		}
		if !found {
			t.Fatalf("enabled=%v: teams/projects source must be registered unconditionally so its checkpoint is never stranded", enabled)
		}
	}
}

// TestTeamsProjectsFlagActuallyGatesTheSource is the other direction: the
// flag must not be inert. A wiring that passed a hardcoded value (or ignored
// the parameter) would still satisfy the registration test above.
func TestTeamsProjectsFlagActuallyGatesTheSource(t *testing.T) {
	t.Parallel()
	disabled, err := devhealthsource.NewTeamsProjectsSource(unreachableClient{t: t}, false)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	_, available, err := disabled.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil || available {
		t.Fatalf("a disabled source must idle silently; got available=%v err=%v", available, err)
	}
}
