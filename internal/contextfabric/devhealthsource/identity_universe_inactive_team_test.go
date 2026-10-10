package devhealthsource

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestIdentityUniverseOmitsInactiveTeams(t *testing.T) {
	original := identityUniverseKinds
	t.Cleanup(func() { identityUniverseKinds = original })
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	flag := func(v bool) map[string]contractsv1.ContextFabricScalarValue {
		return map[string]contractsv1.ContextFabricScalarValue{"is_active": {Boolean: &v}}
	}
	team := func(id string, props map[string]contractsv1.ContextFabricScalarValue) candidate {
		return candidate{observedAt: at, sortKey: id, entity: &contractsv1.ContextFabricEntityProjection{
			Subject:    contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: id, Label: "Platform"},
			Aliases:    []string{"Platform"},
			Properties: props, ObservedAt: at,
		}}
	}
	identityUniverseKinds = []entityTable{{name: "teams", query: func(_ context.Context, _ contextpacket.ClickHouseQueryClient, _ string, cursor cursorState, _ int) ([]candidate, bool, error) {
		if cursor.After != "" {
			return nil, false, nil
		}
		return []candidate{team("team:platform", flag(false)), team("team:jira:platform", flag(true)), team("team:no-flag", nil)}, false, nil
	}}}
	rows, _, _, err := IdentityUniverse(context.Background(), nil, "org-inactive-team")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].CanonicalID != "team:jira:platform" || rows[1].CanonicalID != "team:no-flag" {
		t.Fatalf("rows = %+v, want the active team and the team without a flag, not the inactive one", rows)
	}
}
