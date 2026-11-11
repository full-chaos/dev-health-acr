package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/pep"
	"github.com/full-chaos/dev-health-go/pep/pepfake"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13586: one decision per approval, from the policy decision point,
// asked with the approver reference only.

const approvalTestReference = "eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJ1c2VyIn0.c2lnbmF0dXJlLXNpZ25hdHVyZQ"

type countingDecider struct {
	next  pep.Decider
	calls int
	last  pep.Request
}

func (c *countingDecider) Decide(ctx context.Context, req pep.Request) pep.Decision {
	c.calls++
	c.last = req
	return c.next.Decide(ctx, req)
}

func (c *countingDecider) DecideBatch(ctx context.Context, reqs []pep.Request) []pep.Decision {
	c.calls += len(reqs)
	return c.next.DecideBatch(ctx, reqs)
}

func approvalPrincipal() storage.Principal {
	principal := deviceApprovalPrincipal("*")
	principal.ApproverReference = approvalTestReference
	return principal
}

func allowRule(allowlist []string, tenant string) pepfake.Rule {
	return pepfake.Rule{PrincipalReference: approvalTestReference, Action: "acr.credential.issue", ResourceType: "organization",
		ResourceKey: deviceFlowTestOrgID, OrganizationID: deviceFlowTestOrgID, TenantScope: tenant, RepositoryAllowlist: allowlist}
}

func newDecisions(t *testing.T, mode ApprovalDecisionMode, decider pep.Decider) (*ApprovalDecisions, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	decisions, err := NewApprovalDecisions(mode, decider, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return decisions, logs
}

// pdpAnswering is a real pep client against a PDP that answers status.
func pdpAnswering(t *testing.T, status int) pep.Decider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(server.Close)
	client, err := pep.New(pep.Config{BaseURL: server.URL, AllowPlainHTTP: true, HTTPClient: server.Client(), ClientService: "dev-health-acr-api"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// pdpDenying is a real pep client against a PDP whose answer is a wire deny
// (authorization_decision.v1, allowed false) for org.
func pdpDenying(t *testing.T, org string) pep.Decider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"allowed":false,"decision_id":"dec_EXAMPLE0000000000000000001","expires_at":%q,"organization_id":%q,"policy_revision":42,"principal_id":"prn_EXAMPLE0000000000000001","reason_code":"no_membership","schema_version":"authorization_decision.v1"}`,
			time.Now().UTC().Add(30*time.Second).Format(time.RFC3339), org)
	}))
	t.Cleanup(server.Close)
	client, err := pep.New(pep.Config{BaseURL: server.URL, AllowPlainHTTP: true, HTTPClient: server.Client(), ClientService: "dev-health-acr-api"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestApprovalDecisions_offAsksNothing(t *testing.T) {
	decider := &countingDecider{next: pepfake.New()}
	decisions, _ := newDecisions(t, ApprovalDecisionOff, decider)
	var none *ApprovalDecisions

	if err := decisions.Check(context.Background(), approvalPrincipal(), []string{"*"}); err != nil || decider.calls != 0 {
		t.Fatalf("off: err = %v calls = %d; want nil and 0", err, decider.calls)
	}
	if err := none.Check(context.Background(), approvalPrincipal(), []string{"*"}); err != nil {
		t.Fatalf("nil: err = %v; want nil", err)
	}
}

func TestApprovalDecisions_enforceAsksOnceWithTheReferenceAndTheOrganizationKey(t *testing.T) {
	decider := &countingDecider{next: pepfake.New(allowRule(nil, deviceFlowTestOrgID))}
	decisions, _ := newDecisions(t, ApprovalDecisionEnforce, decider)

	err := decisions.Check(context.Background(), approvalPrincipal(), []string{"*"})

	if err != nil || decider.calls != 1 {
		t.Fatalf("err = %v calls = %d; want nil and 1", err, decider.calls)
	}
	want := pep.Request{PrincipalReference: approvalTestReference, Action: "acr.credential.issue", ResourceType: "organization",
		ResourceKey: deviceFlowTestOrgID, OrganizationID: deviceFlowTestOrgID}
	if decider.last != want {
		t.Fatalf("request = %#v; want %#v", decider.last, want)
	}
}

func TestApprovalDecisions_enforceRefusals(t *testing.T) {
	for _, test := range []struct {
		name         string
		decider      func(t *testing.T) pep.Decider
		principal    func() storage.Principal
		repositories []string
		want         error
	}{
		{name: "deny", decider: func(t *testing.T) pep.Decider { return pdpDenying(t, deviceFlowTestOrgID) }, want: ErrApprovalNotPermitted},
		{name: "no decision", decider: func(*testing.T) pep.Decider { return pepfake.New() }, want: ErrApprovalDecisionUnavailable},
		{name: "pdp refuses the reference (401)", decider: func(t *testing.T) pep.Decider { return pdpAnswering(t, http.StatusUnauthorized) }, want: ErrApprovalNotPermitted},
		{name: "pdp outage (503)", decider: func(t *testing.T) pep.Decider { return pdpAnswering(t, http.StatusServiceUnavailable) }, want: ErrApprovalDecisionUnavailable},
		{name: "pdp rate limit (429)", decider: func(t *testing.T) pep.Decider { return pdpAnswering(t, http.StatusTooManyRequests) }, want: ErrApprovalDecisionUnavailable},
		{name: "no client", decider: func(*testing.T) pep.Decider { return nil }, want: ErrApprovalDecisionUnavailable},
		{name: "no reference", decider: func(*testing.T) pep.Decider { return pepfake.New(allowRule(nil, deviceFlowTestOrgID)) }, principal: func() storage.Principal {
			p := approvalPrincipal()
			p.ApproverReference = ""
			return p
		}, want: ErrApprovalNotPermitted},
		{name: "a reference that failed the pair check", decider: func(*testing.T) pep.Decider { return pepfake.New(allowRule(nil, deviceFlowTestOrgID)) }, principal: func() storage.Principal {
			p := approvalPrincipal()
			p.ApproverReference, p.ApproverReferenceFault = "", ApproverReferencePair
			return p
		}, want: ErrApprovalNotPermitted},
		{name: "tenant scope of another organization", decider: func(*testing.T) pep.Decider {
			return pepfake.New(allowRule(nil, "22222222-2222-2222-2222-222222222222"))
		}, want: ErrApprovalNotPermitted},
		{name: "organization-wide approval under a repository allowlist", decider: func(*testing.T) pep.Decider {
			return pepfake.New(allowRule([]string{"acme/api"}, deviceFlowTestOrgID))
		}, want: ErrApprovalNotPermitted},
		{name: "a repository outside the allowlist", decider: func(*testing.T) pep.Decider {
			return pepfake.New(allowRule([]string{"acme/api"}, deviceFlowTestOrgID))
		}, repositories: []string{"acme/api", "acme/web"}, want: ErrApprovalNotPermitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			decisions, _ := newDecisions(t, ApprovalDecisionEnforce, test.decider(t))
			principal := approvalPrincipal()
			if test.principal != nil {
				principal = test.principal()
			}
			repositories := test.repositories
			if repositories == nil {
				repositories = []string{"*"}
			}

			err := decisions.Check(context.Background(), principal, repositories)

			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v; want %v", err, test.want)
			}
			if errors.Is(err, ErrInvalidDeviceFlow) {
				t.Fatal("a decision refusal is an ErrInvalidDeviceFlow (400)")
			}
		})
	}
}

func TestApprovalDecisions_exactRepositoriesWithinTheAllowlistPass(t *testing.T) {
	decisions, _ := newDecisions(t, ApprovalDecisionEnforce, pepfake.New(allowRule([]string{"acme/api", "acme/web"}, deviceFlowTestOrgID)))

	if err := decisions.Check(context.Background(), approvalPrincipal(), []string{"acme/api"}); err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
}

func TestApprovalDecisions_shadowNeverRefusesAndRecordsTheOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		decider pep.Decider
		outcome string
	}{
		{"deny", pdpDenying(t, deviceFlowTestOrgID), `"outcome":"denied"`},
		{"outage", pdpAnswering(t, http.StatusServiceUnavailable), `"outcome":"unavailable"`},
		{"no client", nil, `"outcome":"not_configured"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			decisions, logs := newDecisions(t, ApprovalDecisionShadow, test.decider)

			err := decisions.Check(context.Background(), approvalPrincipal(), []string{"*"})

			if err != nil {
				t.Fatalf("shadow refused: %v", err)
			}
			if !strings.Contains(logs.String(), `"msg":"device approval decision","mode":"shadow",`+test.outcome) {
				t.Fatalf("no decision line with %s:\n%s", test.outcome, logs.String())
			}
		})
	}
}

func TestApprovalDecisions_onWithoutAClientLogsOneError(t *testing.T) {
	_, logs := newDecisions(t, ApprovalDecisionEnforce, nil)

	if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
		t.Fatalf("want one ERROR line:\n%s", logs.String())
	}
}

func TestParseApprovalDecisionMode(t *testing.T) {
	for value, want := range map[string]ApprovalDecisionMode{"": ApprovalDecisionOff, "off": ApprovalDecisionOff, "shadow": ApprovalDecisionShadow, "enforce": ApprovalDecisionEnforce} {
		if got, err := ParseApprovalDecisionMode(value); err != nil || got != want {
			t.Fatalf("%q = %q, %v; want %q", value, got, err, want)
		}
	}
	if got, err := ParseApprovalDecisionMode("on"); err == nil || got != ApprovalDecisionOff {
		t.Fatalf("unknown mode = %q, %v; want off and an error to log", got, err)
	}
}
