package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-go/pep"
	"github.com/full-chaos/dev-health-go/pep/pepfake"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
)

// switchDecider answers with next, which a cell may change between calls.
type switchDecider struct {
	next  pep.Decider
	calls int
}

func (s *switchDecider) Decide(ctx context.Context, req pep.Request) pep.Decision {
	s.calls++
	return s.next.Decide(ctx, req)
}

func (s *switchDecider) DecideBatch(ctx context.Context, reqs []pep.Request) []pep.Decision {
	s.calls += len(reqs)
	return s.next.DecideBatch(ctx, reqs)
}

// A-3 (a): the consent approve through the REAL OAuthService with the check
// enforced: a deny is OAuth access_denied and the request stays pending (a
// later allowed approve succeeds); an outage is unavailable; an allow approves.
func TestApprovalDecisions_consentThroughTheRealService(t *testing.T) {
	h := newOAuthHarness(t)
	decider := &switchDecider{next: pdpDenying(t, testOrg)}
	decisions, _ := newDecisions(t, ApprovalDecisionEnforce, decider)
	h.devices.approvals = decisions
	clientID := h.register(t)
	_, challenge := pkce(t)
	authorization, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge,
		CodeChallengeMethod: "S256", Resource: testResource, State: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := webPrincipal([]string{"org/repo"})
	principal.SessionID = h.sessionID
	principal.ApproverReference = approvalTestReference

	_, denyErr := h.oauth.ApproveConsent(context.Background(), authorization.Handle, principal, []string{"org/repo"})
	var refusal *OAuthError
	if !errors.As(denyErr, &refusal) || refusal.Code != OAuthConsentCodeDenied || refusal.Outcome != oauthvocab.OutcomeAccessDenied {
		t.Fatalf("deny = %v; want OAuth access_denied", denyErr)
	}

	decider.next = pdpAnswering(t, http.StatusServiceUnavailable)
	if _, err := h.oauth.ApproveConsent(context.Background(), authorization.Handle, principal, []string{"org/repo"}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("outage = %v; want unavailable", err)
	}

	decider.next = pepfake.New(pepfake.Rule{PrincipalReference: approvalTestReference, Action: "acr.credential.issue", ResourceType: "organization",
		ResourceKey: testOrg, OrganizationID: testOrg, TenantScope: testOrg})
	decision, err := h.oauth.ApproveConsent(context.Background(), authorization.Handle, principal, []string{"org/repo"})
	if err != nil || codeFrom(t, decision.RedirectURL) == "" {
		t.Fatalf("allow after a deny and an outage = %v %q; want an approval (the request stayed pending)", err, decision.RedirectURL)
	}
	if decider.calls != 3 {
		t.Fatalf("decisions asked = %d; want 3 (one per approve)", decider.calls)
	}
}
