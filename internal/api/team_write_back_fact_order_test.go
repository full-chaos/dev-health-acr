package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type teamTable struct {
	match string
	rows  func(call int) [][]any
}

// teamStoreClient answers the team readiness and investment statements from
// static rows, served in a different order and with a different float
// summation order on every call.
type teamStoreClient struct {
	mu     sync.Mutex
	call   int
	tables []teamTable
}

func (c *teamStoreClient) nextCall() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call++
	return c.call
}

func (c *teamStoreClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for _, table := range c.tables {
		if strings.Contains(statement, table.match) {
			return &teamStoreScanner{rows: table.rows(c.currentCall())}, nil
		}
	}
	return &teamStoreScanner{}, nil
}

func (c *teamStoreClient) currentCall() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call
}

type teamStoreScanner struct {
	rows [][]any
	row  int
}

func (s *teamStoreScanner) Next() bool { return s.row < len(s.rows) }

func (s *teamStoreScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *int64:
			*value = row[index].(int64)
		case *uint64:
			*value = row[index].(uint64)
		case *uint8:
			*value = row[index].(uint8)
		case *float64:
			*value = row[index].(float64)
		case *map[string]float64:
			*value = row[index].(map[string]float64)
		default:
			return errors.New("teamStoreScanner: unsupported destination")
		}
	}
	s.row++
	return nil
}

func (s *teamStoreScanner) Err() error   { return nil }
func (s *teamStoreScanner) Close() error { return nil }

func reorderedFor[T any](rows []T, call int) []T {
	out := make([]T, 0, len(rows))
	shift := call % len(rows)
	for i := range rows {
		out = append(out, rows[(i+shift)%len(rows)])
	}
	if call%2 == 0 {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

func newTeamStoreClient() *teamStoreClient {
	day := "2026-10-03"
	readiness := [][]any{
		{"CHAOS", "scope-a", "linear", day, int64(0), int64(0), int64(0), uint8(0), float64(0)},
		{"CHAOS", "scope-b", "linear", day, int64(0), int64(1), int64(1), uint8(1), float64(0)},
		{"CHAOS", "scope-c", "linear", day, int64(0), int64(5), int64(5), uint8(1), float64(0)},
		{"CHAOS", "scope-d", "github", "2026-10-04", int64(0), int64(5), int64(5), uint8(1), float64(0)},
		{"CHAOS", "scope-e", "github", "2026-08-16", int64(0), int64(0), int64(0), uint8(0), float64(0)},
	}
	repos := []string{"repo-a", "repo-b", "repo-c", "repo-d"}
	efforts := []float64{0.1, 0.2, 0.3, 0.7}
	owned := make([][]any, 0, len(repos))
	mix := make([][]any, 0, len(repos))
	for index, repo := range repos {
		owned = append(owned, []any{"CHAOS", repo})
		scale := efforts[index]
		mix = append(mix, []any{uint8(0), repo, map[string]float64{
			"feature_delivery": 0.48898883728007437 * scale, "operational": 0.047865254810142 * scale,
			"maintenance": 0.2304889002988043 * scale, "quality": 0.1388265562681117 * scale, "risk": 0.0936 * scale,
		}, 0.0864588925764 * scale, uint64(3)})
	}
	return &teamStoreClient{tables: []teamTable{
		{match: "PARTITION BY team_id, work_scope_id, provider ORDER BY day DESC", rows: func(call int) [][]any { return reorderedFor(readiness, call) }},
		{match: "provider, work_scope_id, day ORDER BY computed_at DESC", rows: func(int) [][]any {
			return [][]any{{"CHAOS", "2026-10-03", int64(0), int64(6), int64(6)}, {"CHAOS", "2026-08-07", int64(0), int64(8), int64(8)}}
		}},
		{match: "FROM team_repo_ownership", rows: func(call int) [][]any { return reorderedFor(owned, call) }},
		{match: "FROM work_unit_investments", rows: func(call int) [][]any { return reorderedFor(mix, call) }},
	}}
}

func teamWriteBackDraft(project contextfabric.SubjectRef) contextfabric.SynthesisDraft {
	return contextfabric.SynthesisDraft{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "The team appears to lean toward feature delivery.",
		CurrentState:       "Investment appears to lean toward feature delivery.",
		StrongestPressures: []string{"Investment appears to lean toward feature delivery."},
		Drivers: []contextfabric.DriverJudgment{{
			DriverID: "driver_teamorder01", Standing: contextfabric.DriverPrincipal, Category: "investment",
			Title: "Investment appears to lean toward feature delivery", Summary: "The persisted mix appears to lean toward feature delivery.",
			AffectedSubjects: []contextfabric.SubjectRef{project}, EvidenceRefIDs: []string{"acr:v1:team:CHAOS"},
			ClaimedFactIDs: []string{"claim_basis_teamorder"}, Derivation: contextfabric.DerivationCanonicalStructured,
			EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		}},
		RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{},
		Limitations: []string{"Only canonical facts were read."}, EvidenceRefIDs: []string{"acr:v1:team:CHAOS"},
		ClaimedFacts: []contextfabric.ClaimedFact{{
			ClaimID: "claim_basis_teamorder", Kind: contextfabric.FactInvestment, Subject: project, Field: "owned_repository_count",
			Value: contextfabric.ScalarValue{Integer: int64Pointer(4)},
		}}, DeterministicAnswer: "The team appears to lean toward feature delivery.", Warnings: []string{},
	}
}

// TestTeamQuestionWriteBackIsServedOverReorderedStoreRows is the prod failure:
// the facts of a team question come through the real registry and providers,
// the store returns the same rows in a different order (and the effort sums in
// a different float order) on every read, and the second call of the write-back
// must still carry the input the first call gave.
func TestTeamQuestionWriteBackIsServedOverReorderedStoreRows(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	client := newTeamStoreClient()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{Now: rig.now})
	if err != nil {
		t.Fatal(err)
	}
	rig.delegate = func(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
		client.nextCall()
		request.Subjects = []contextfabric.SubjectRef{team}
		request.Requirements = []contextfabric.FactRequirement{
			{Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{team}},
			{Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{team}},
		}
		return registry.ReadFacts(ctx, principal, request)
	}

	first := rig.firstCall(t)
	rig.advance(7 * time.Minute)
	second := rig.firstCall(t)
	if len(first.SynthesisInput.Input) == 0 || !strings.Contains(string(first.SynthesisInput.Input), "theme_feature_delivery") || !strings.Contains(string(first.SynthesisInput.Input), "work_scope_id") {
		t.Fatalf("the input carries no team readiness or investment fact: %s", first.SynthesisInput.Input)
	}
	if first.SynthesisInput.InputSHA256 != second.SynthesisInput.InputSHA256 {
		t.Fatalf("input_sha256 differs across two identical team calls: %s != %s\nfirst:  %s\nsecond: %s",
			first.SynthesisInput.InputSHA256, second.SynthesisInput.InputSHA256, first.SynthesisInput.Input, second.SynthesisInput.Input)
	}

	recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, teamWriteBackDraft(team)), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("write-back status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	served := decodeEnvelope(t, recorder)
	if served.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient {
		t.Fatalf("served source %q, want client", served.Versions.SynthesisSource)
	}
}

func int64Pointer(value int64) *int64 { return &value }
