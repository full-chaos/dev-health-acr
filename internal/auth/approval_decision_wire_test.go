package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/pep"
)

// pdpWire is a real pep client against a PDP that answers one authorization_decision.v1 body.
func pdpWire(t *testing.T, allowed bool, reason, obligations string) pep.Decider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		extra := ""
		if obligations != "" {
			extra = `,"obligations":` + obligations
		}
		_, _ = fmt.Fprintf(w, `{"allowed":%t,"decision_id":"dec_EXAMPLE0000000000000000001","expires_at":%q,"organization_id":%q,"policy_revision":42,"principal_id":"prn_EXAMPLE0000000000000001","reason_code":%q,"schema_version":"authorization_decision.v1"%s}`,
			allowed, time.Now().UTC().Add(30*time.Second).Format(time.RFC3339), deviceFlowTestOrgID, reason, extra)
	}))
	t.Cleanup(server.Close)
	client, err := pep.New(pep.Config{BaseURL: server.URL, AllowPlainHTTP: true, HTTPClient: server.Client(), ClientService: "dev-health-acr-api"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestApprovalDecisions_wireObligationCells(t *testing.T) {
	tenant := `"tenant_scope":"` + deviceFlowTestOrgID + `"`
	star, named := []string{"*"}, []string{"acme/api"}
	for _, cell := range []struct {
		name         string
		allowed      bool
		reason       string
		obligations  string
		repositories []string
		outcome      string
	}{
		{"1 org-wide with repository_scope organization", true, "allowed_by_role", `{` + tenant + `,"repository_scope":"organization"}`, star, approvalOutcomeAllowed},
		{"2 org-wide with an allowlist only", true, "allowed_by_role", `{` + tenant + `,"repository_allowlist":["acme/api"]}`, star, approvalOutcomeObligation},
		{"3 org-wide with neither member", true, "allowed_by_role", `{` + tenant + `}`, star, approvalOutcomeObligation},
		{"4 named repository with repository_scope organization", true, "allowed_by_role", `{` + tenant + `,"repository_scope":"organization"}`, named, approvalOutcomeAllowed},
		{"5a named repository inside the allowlist", true, "allowed_by_role", `{` + tenant + `,"repository_allowlist":["acme/api"]}`, named, approvalOutcomeAllowed},
		{"5b named repository outside the allowlist", true, "allowed_by_role", `{` + tenant + `,"repository_allowlist":["acme/web"]}`, named, approvalOutcomeObligation},
		{"6 non-canonical key inside the allowlist", true, "allowed_by_role", `{` + tenant + `,"repository_allowlist":["acme//api"]}`, []string{"acme//api"}, approvalOutcomeUnavailable},
		{"7a platform admin allow", true, "allowed_by_platform_admin", `{` + tenant + `,"repository_scope":"organization"}`, star, approvalOutcomeAllowed},
		{"7b platform admin code on a deny", false, "allowed_by_platform_admin", "", star, approvalOutcomeUnavailable},
		{"8 allowlist too large deny", false, "repository_allowlist_too_large", "", star, approvalOutcomeDenied},
		{"9 repository unresolved deny", false, "repository_unresolved", "", named, approvalOutcomeDenied},
	} {
		t.Run(cell.name, func(t *testing.T) {
			decisions, _ := newDecisions(t, ApprovalDecisionEnforce, pdpWire(t, cell.allowed, cell.reason, cell.obligations))
			outcome, reason := decisions.decide(t.Context(), approvalPrincipal(), cell.repositories)
			t.Logf("CELL %s => outcome=%s reason=%s", cell.name, outcome, reason)
			if outcome != cell.outcome {
				t.Fatalf("outcome = %s (%s); want %s", outcome, reason, cell.outcome)
			}
		})
	}
}

// A well-formed PDP deny with the reason repository_unresolved is the PDP's own deny: the decision reads it as a deny with that
// reason, and enforce mode refuses the approval as not permitted, not as an unavailable decision.
func TestApprovalDecisions_repositoryUnresolvedDenyIsADeny(t *testing.T) {
	decisions, _ := newDecisions(t, ApprovalDecisionEnforce, pdpWire(t, false, "repository_unresolved", ""))
	outcome, reason := decisions.decide(t.Context(), approvalPrincipal(), []string{"acme/api"})
	if outcome != approvalOutcomeDenied || reason != "repository_unresolved" {
		t.Fatalf("decide = %s (%s); want %s (repository_unresolved)", outcome, reason, approvalOutcomeDenied)
	}
	err := decisions.Check(t.Context(), approvalPrincipal(), []string{"acme/api"})
	if !errors.Is(err, ErrApprovalNotPermitted) || errors.Is(err, ErrApprovalDecisionUnavailable) {
		t.Fatalf("Check = %v; want ErrApprovalNotPermitted", err)
	}
}
