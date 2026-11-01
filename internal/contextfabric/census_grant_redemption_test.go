package contextfabric_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// redemptionClient is the ClickHouse the census reads: it answers one count and
// records every statement, so "read nothing" is measured, not assumed.
type redemptionClient struct {
	count      uint64
	statements []string
}

func (c *redemptionClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	const key = "org_reuse:repo-1:5063"
	if strings.Contains(statement, "count(), now64()") {
		return &redemptionRows{rows: [][]any{{c.count, time.Now().UTC(), key}}}, nil
	}
	return &redemptionRows{rows: [][]any{{key}}}, nil
}

type redemptionRows struct {
	rows [][]any
	row  int
}

func (r *redemptionRows) Next() bool { return r.row < len(r.rows) }
func (r *redemptionRows) Scan(dest ...any) error {
	row := r.rows[r.row]
	r.row++
	for i, d := range dest {
		switch target := d.(type) {
		case *uint64:
			*target = row[i].(uint64)
		case *time.Time:
			*target = row[i].(time.Time)
		case *string:
			*target = row[i].(string)
		}
	}
	return nil
}
func (r *redemptionRows) Err() error   { return nil }
func (r *redemptionRows) Close() error { return nil }

// productionVerifier is hosted/open.go's HandleVerifier over client, and it
// counts the calls: a veto that never reached the census proves nothing.
func productionVerifier(client *redemptionClient, calls *int) contextfabric.HandleVerifier {
	census := devhealthsource.NewCensusFunc(client)
	return func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, contextfabric.HandleVerificationReason) {
		*calls++
		ok, reason := graphrank.VerifyHandle(ctx, orgID, kind, patternID, value, census)
		return ok, contextfabric.HandleVerificationReason(reason)
	}
}

func redemptionOption(kind contractsv1.ContextFabricSubjectKind, patternID, value string) contextfabric.HandleOption {
	return contextfabric.HandleOption{Kind: kind, PatternID: patternID, Value: value}
}

type refusedRedemption struct {
	name      string
	principal storage.Principal
	kind      contractsv1.ContextFabricSubjectKind
	patternID string
	value     string
}

func refusedRedemptions(t *testing.T) []refusedRedemption {
	projectOnly := userprincipal.ProjectOnly(t, "org_reuse", []string{"project.v2:github:P-1"})
	return []refusedRedemption{
		{"project-only caller, work item", projectOnly, contextfabric.SubjectWorkItem, "work_item_ticket_key", "CHAOS-11516"},
		{"project-only caller, pull request", projectOnly, contextfabric.SubjectPullRequest, "pull_request_number", "5063"},
		{"project-only caller, ci run", projectOnly, contractsv1.ContextFabricSubjectCIRun, "ci_run_id", "554433"},
	}
}

const unresolvedSentence = "a structure confirmation receipt could not be resolved"

// A handr_ redemption the grant cannot scope is vetoed with no statement. The
// veto is disclosed, it is the text of every unresolved veto, and nothing in
// the result says the handle does not exist.
func TestARedemptionTheGrantCannotScopeIsAVetoThatReadsNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range refusedRedemptions(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &redemptionClient{count: 1}
			calls := 0
			result, err := contextfabric.RedeemHandleOfferForTest(t, tc.principal, redemptionOption(tc.kind, tc.patternID, tc.value), productionVerifier(client, &calls))
			if calls != 1 {
				t.Fatalf("verifier calls = %d, want 1: the redemption did not reach the census", calls)
			}
			if len(client.statements) != 0 {
				t.Fatalf("statements = %q, want none: an organization-wide read", client.statements)
			}
			if err != nil {
				t.Fatalf("Investigate() error = %v, want the veto result", err)
			}
			if result.Status != contextfabric.InvestigationNoMatch || result.DeterministicAnswer != unresolvedSentence || !slices.Equal(result.Limitations, []string{unresolvedSentence}) {
				t.Fatalf("result = status %q answer %q limitations %q, want the unresolved veto", result.Status, result.DeterministicAnswer, result.Limitations)
			}
			if len(result.ConfirmedStructure) != 1 {
				t.Fatalf("confirmed structure = %+v, want the vetoed member echoed", result.ConfirmedStructure)
			}
			echo := result.ConfirmedStructure[0]
			if echo.Member != contractsv1.ContextFabricStructureNeedSubjectHandle || echo.Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved || echo.AppliedValue != tc.value {
				t.Fatalf("echo = %+v, want the handle vetoed_unresolved with its value", echo)
			}
			rendered, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.ToLower(string(rendered))
			for _, claim := range []string{"does not exist", "doesn't exist", "not exist", "not found", "no such", "no record", "never existed"} {
				if strings.Contains(text, claim) {
					t.Fatalf("the result says %q: %s", claim, text)
				}
			}
		})
	}
}

// A real absence and a refused census read the same to the caller, so the
// refusal is no oracle and neither is told as "does not exist".
func TestARefusedRedemptionReadsAsAnAbsentOne(t *testing.T) {
	t.Parallel()
	restricted := userprincipal.Narrowed(t, "org_reuse", []string{"acme/*"}, []string{"acme/api"})
	absentClient := &redemptionClient{count: 0}
	absentCalls := 0
	absent, err := contextfabric.RedeemHandleOfferForTest(t, restricted, redemptionOption(contextfabric.SubjectPullRequest, "pull_request_number", "5063"), productionVerifier(absentClient, &absentCalls))
	if err != nil {
		t.Fatal(err)
	}
	if len(absentClient.statements) != 1 || !strings.Contains(absentClient.statements[0], "{census_grant_0:String}") {
		t.Fatalf("statements = %q, want one count inside the grant", absentClient.statements)
	}
	for _, tc := range refusedRedemptions(t) {
		client := &redemptionClient{count: 1}
		calls := 0
		refused, err := contextfabric.RedeemHandleOfferForTest(t, tc.principal, redemptionOption(tc.kind, tc.patternID, tc.value), productionVerifier(client, &calls))
		if err != nil {
			t.Fatal(err)
		}
		if refused.Status != absent.Status || refused.DeterministicAnswer != absent.DeterministicAnswer || !slices.Equal(refused.Limitations, absent.Limitations) || refused.ConfirmedStructure[0].Disposition != absent.ConfirmedStructure[0].Disposition {
			t.Fatalf("%s reads apart from an absent row: %+v / %+v", tc.name, refused, absent)
		}
	}
}

// The control: a redemption inside the grant is read inside the grant, and
// passes.
func TestARedemptionInsideTheGrantPasses(t *testing.T) {
	t.Parallel()
	restricted := userprincipal.Narrowed(t, "org_reuse", []string{"acme/*"}, []string{"acme/api"})
	client := &redemptionClient{count: 1}
	calls := 0
	_, err := contextfabric.RedeemHandleOfferForTest(t, restricted, redemptionOption(contextfabric.SubjectPullRequest, "pull_request_number", "5063"), productionVerifier(client, &calls))
	if len(client.statements) == 0 || !strings.Contains(client.statements[0], "{census_grant_0:String}") {
		t.Fatalf("statements = %q, want the grant in them", client.statements)
	}
	if !errors.Is(err, contextfabric.ErrHandleRedemptionPassedForTest) {
		t.Fatalf("Investigate() error = %v, want the turn past the redemption", err)
	}
}

// CHAOS-12456: a restricted caller's work-item redemption is read inside the
// grant through the native-link arm. The dead end PR1 left is closed for a linked
// item, and an unlinked one is a veto that reads as any absent row does.
func TestAWorkItemRedemptionIsDecidedByTheLinkArm(t *testing.T) {
	t.Parallel()
	restricted := userprincipal.Narrowed(t, "org_reuse", []string{"acme/*"}, []string{"acme/api"})
	option := redemptionOption(contextfabric.SubjectWorkItem, "work_item_ticket_key", "CHAOS-11516")
	t.Run("linked", func(t *testing.T) {
		t.Parallel()
		client := &redemptionClient{count: 1}
		calls := 0
		_, err := contextfabric.RedeemHandleOfferForTest(t, restricted, option, productionVerifier(client, &calls))
		if !errors.Is(err, contextfabric.ErrHandleRedemptionPassedForTest) {
			t.Fatalf("Investigate() error = %v, want the turn past the redemption", err)
		}
		if len(client.statements) == 0 || !strings.Contains(client.statements[0], "wia_link_auth.wia_link_repositories") {
			t.Fatalf("statements = %q, want the link arm in them", client.statements)
		}
	})
	t.Run("not linked", func(t *testing.T) {
		t.Parallel()
		client := &redemptionClient{count: 0}
		calls := 0
		result, err := contextfabric.RedeemHandleOfferForTest(t, restricted, option, productionVerifier(client, &calls))
		if err != nil {
			t.Fatal(err)
		}
		if result.DeterministicAnswer != unresolvedSentence || len(result.ConfirmedStructure) != 1 || result.ConfirmedStructure[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
			t.Fatalf("result = %+v, want the unresolved veto", result)
		}
	})
	t.Run("remembered and linked", func(t *testing.T) {
		t.Parallel()
		client := &redemptionClient{count: 1}
		calls := 0
		if reason, ok := contextfabric.ReverifyRememberedHandleForTest(restricted, contextfabric.SubjectWorkItem, "work_item_ticket_key", "CHAOS-11516", productionVerifier(client, &calls)); !ok || reason != "" {
			t.Fatalf("reverify = %q %v, want it kept", reason, ok)
		}
	})
}

// A remembered handle replays the same read: it is dropped alone, with no
// statement, and reported as not confirmed.
func TestARememberedHandleTheGrantCannotScopeIsDroppedAndReadsNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range refusedRedemptions(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &redemptionClient{count: 1}
			calls := 0
			reason, ok := contextfabric.ReverifyRememberedHandleForTest(tc.principal, tc.kind, tc.patternID, tc.value, productionVerifier(client, &calls))
			if calls != 1 || len(client.statements) != 0 {
				t.Fatalf("verifier calls = %d statements = %q, want one call and none", calls, client.statements)
			}
			if ok || reason != contextfabric.ConfirmedNeedMemberDropReverifyNotConfirmed {
				t.Fatalf("reverify = %q %v, want it dropped as not confirmed", reason, ok)
			}
		})
	}
}
