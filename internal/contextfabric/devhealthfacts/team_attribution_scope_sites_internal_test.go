package devhealthfacts

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type emptyScanner struct{}

func (emptyScanner) Next() bool        { return false }
func (emptyScanner) Scan(...any) error { return nil }
func (emptyScanner) Err() error        { return nil }
func (emptyScanner) Close() error      { return nil }

type recordingClient struct{ statements []string }

func (c *recordingClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	return emptyScanner{}, nil
}

func attributionStatements(statements []string) []string {
	var out []string
	for _, statement := range statements {
		if strings.Contains(statement, "work_item_team_attributions") {
			out = append(out, statement)
		}
	}
	return out
}

// The team-scoped cohort reads read the co-owner rows too.
func TestTeamScopedCohortStatementsReadCoOwnerRows(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		policy contextfabric.FactScopePolicy
		target contextfabric.SubjectKind
	}{
		"team repositories": {contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, contextfabric.SubjectRepository},
		"team work items":   {contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemWork, contextfabric.SubjectWorkItem},
	} {
		client := &recordingClient{}
		_, err := NewScopeExpander(client).ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
			Principal:       storage.Principal{OrgID: "org", RepositoryScopes: []string{"*"}},
			RequirementKind: contextfabric.FactMetrics,
			Origins:         []contextfabric.SubjectRef{{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}},
			Policy:          tc.policy,
			TargetKind:      tc.target,
			Limit:           5,
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		statements := attributionStatements(client.statements)
		if len(statements) != 1 {
			t.Fatalf("%s: %d attribution statements, want 1", name, len(statements))
		}
		if !strings.Contains(statements[0], "a.is_primary IN (1, 2)") || strings.Contains(statements[0], "is_primary = 1") {
			t.Errorf("%s: statement does not read primary and co-owner rows:\n%s", name, statements[0])
		}
	}
}

// The one-team-per-work-unit votes stay on the primary row, so a work unit
// is counted once however many teams co-own its items.
func TestInvestmentVoteStatementsReadOnlyPrimaryRows(t *testing.T) {
	t.Parallel()
	for name, statement := range map[string]string{
		"roll-up mix":  projectRollupMixStatement(factTimeBound{}),
		"evidence arm": projectRollupEvidenceArmStatement(),
	} {
		if !strings.Contains(statement, "AND is_primary = 1") || strings.Contains(statement, "IN (1, 2)") {
			t.Errorf("%s: the vote must read is_primary = 1 only", name)
		}
	}
}
