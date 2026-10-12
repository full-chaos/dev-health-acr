package api

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type ruleBasisClient struct {
	latestSeverity string
	latestRisk     float64
}

type ruleBasisScanner struct {
	rows [][]any
	row  int
}

func (c ruleBasisClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case strings.Contains(statement, "max(window_end) OVER (PARTITION BY team_id)"):
		return &ruleBasisScanner{rows: [][]any{{"CHAOS", "2026-10-03", uint32(1), uint8(1), uint8(1)}}}, nil
	case strings.Contains(statement, "WHERE rn = 1 AND fired = 1"):
		return &ruleBasisScanner{rows: [][]any{{
			"CHAOS", "compounding-risk", "v1", "warning", "Code risk is compounding where change pressure is highest.",
			"Compounding Risk score is 0.509 (severity: elevated). Churn, complexity trend, ownership concentration, and review latency are compounding above their tuned thresholds.",
			"criterion", "2026-09-26", "2026-10-03", "2026-10-03 06:00:00",
		}}}, nil
	case strings.Contains(statement, "toString(day)\nFROM ("):
		return &ruleBasisScanner{rows: [][]any{{"CHAOS", c.latestSeverity, uint8(1), c.latestRisk, "2026-10-04 00:30:00", "2026-10-03"}}}, nil
	case strings.Contains(statement, "ORDER BY (severity != 'unknown') DESC"):
		return &ruleBasisScanner{rows: [][]any{{
			"CHAOS", c.latestSeverity, uint8(1), c.latestRisk, "2026-10-04 00:30:00", "2026-10-03", uint8(1), uint8(1),
			uint8(1), 0.3, 0.4, uint8(1), 0.2, 0.3, uint8(1), 0.5, 0.2, uint8(1), 0.1, 0.1,
		}}}, nil
	}
	return &ruleBasisScanner{}, nil
}

func (s *ruleBasisScanner) Next() bool { return s.row < len(s.rows) }
func (s *ruleBasisScanner) Err() error { return nil }
func (s *ruleBasisScanner) Close() error {
	return nil
}
func (s *ruleBasisScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *uint8:
			*value = row[index].(uint8)
		case *uint32:
			*value = row[index].(uint32)
		case *float64:
			*value = row[index].(float64)
		}
	}
	s.row++
	return nil
}

// The synthesis input is what the answer-writing model reads. A team whose
// rule ran on 0.509 (elevated) and whose latest health computation reads 0.72
// (high) must reach the model with the rule's value named as its basis, the
// latest value named as current, and the move stated.
func TestSynthesisInputNamesTheRuleBasisAndTheLatestRiskOfATeam(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	var client contextpacket.ClickHouseQueryClient = ruleBasisClient{latestSeverity: "high", latestRisk: 0.72}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{Now: rig.now})
	if err != nil {
		t.Fatal(err)
	}
	rig.delegate = func(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
		request.Subjects = []contextfabric.SubjectRef{team}
		request.Requirements = []contextfabric.FactRequirement{
			{Kind: contextfabric.FactOperationalDeficiencies, Subjects: []contextfabric.SubjectRef{team}},
			{Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{team}},
		}
		return registry.ReadFacts(ctx, principal, request)
	}
	input := string(rig.firstCall(t).SynthesisInput.Input)
	for _, want := range []string{"rule_basis_compounding_risk", "0.509", "latest_compounding_risk", "0.72", "score_moved_since_rule", "rule_computed_at", "latest_computed_at"} {
		if !strings.Contains(input, want) {
			t.Errorf("the synthesis input lacks %q: %s", want, input)
		}
	}
}
