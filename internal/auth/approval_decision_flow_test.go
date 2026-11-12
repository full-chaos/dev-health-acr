package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-go/pep/pepfake"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13586: the decision sits in the one approval funnel, after every local
// check and before the approval is stored; a refusal leaves the authorization
// pending, and poll and redeem never ask.

func enforcedFlow(t *testing.T, decider *countingDecider) *deviceFlowFixture {
	t.Helper()
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(1))
	decisions, _ := newDecisions(t, ApprovalDecisionEnforce, decider)
	fixture.flow.approvals = decisions
	return fixture
}

func approve(fixture *deviceFlowFixture, started DeviceAuthorizationStart, principal storage.Principal) error {
	_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{Principal: principal, UserCode: started.UserCode, RepositoryScopes: []string{"*"}})
	return err
}

func TestApprovalFlow_aDenyLeavesTheAuthorizationPending(t *testing.T) {
	decider := &countingDecider{next: pdpDenying(t, deviceFlowTestOrgID)}
	fixture := enforcedFlow(t, decider)
	started := fixture.start(t)

	err := approve(fixture, started, approvalPrincipal())

	if !errors.Is(err, ErrApprovalNotPermitted) || decider.calls != 1 {
		t.Fatalf("err = %v calls = %d; want ErrApprovalNotPermitted and 1", err, decider.calls)
	}
	if _, err := fixture.flow.Poll(context.Background(), started.DeviceCode); !errors.Is(err, ErrDeviceAuthorizationPending) {
		t.Fatalf("poll after a deny = %v; want pending", err)
	}
}

func TestApprovalFlow_anAllowApprovesAndPollNeverAsks(t *testing.T) {
	decider := &countingDecider{next: pepfake.New(allowRule(nil, deviceFlowTestOrgID))}
	fixture := enforcedFlow(t, decider)
	started := fixture.start(t)

	if err := approve(fixture, started, approvalPrincipal()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationPollInterval)
	_, _ = fixture.flow.Poll(context.Background(), started.DeviceCode)

	if decider.calls != 1 {
		t.Fatalf("calls = %d; want 1 (approval only)", decider.calls)
	}
}

func TestApprovalFlow_localRefusalsComeFirstWithNoDecision(t *testing.T) {
	for name, setup := range map[string]func(*deviceFlowFixture, *storage.Principal){
		"impersonating": func(_ *deviceFlowFixture, p *storage.Principal) { p.Impersonating = true },
		"organization gone": func(f *deviceFlowFixture, _ *storage.Principal) {
			f.flow.organizations = stateOrganizations{err: ErrOrganizationGone}
		},
		"organization disabled": func(f *deviceFlowFixture, _ *storage.Principal) {
			f.flow.organizations = stateOrganizations{err: ErrOrganizationDisabled}
		},
		"repository outside the grant": func(_ *deviceFlowFixture, p *storage.Principal) {
			p.RepositoryScopes = []string{"acme/api"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			decider := &countingDecider{next: pepfake.New(allowRule(nil, deviceFlowTestOrgID))}
			fixture := enforcedFlow(t, decider)
			started := fixture.start(t)
			principal := approvalPrincipal()
			setup(fixture, &principal)

			err := approve(fixture, started, principal)

			if err == nil || decider.calls != 0 {
				t.Fatalf("err = %v calls = %d; want a refusal and 0 decisions", err, decider.calls)
			}
		})
	}
}

func TestApprovalFlow_consentMapsADenyToAccessDenied(t *testing.T) {
	var refusal *OAuthError
	if !errors.As(consentDecisionError(ErrApprovalNotPermitted), &refusal) || refusal.Code != OAuthConsentCodeDenied || refusal.Outcome != oauthvocab.OutcomeAccessDenied {
		t.Fatalf("consent deny = %#v; want access_denied", refusal)
	}
	if !errors.Is(consentDecisionError(ErrApprovalDecisionUnavailable), ErrOAuthUnavailable) {
		t.Fatal("consent outage is not unavailable")
	}
}
