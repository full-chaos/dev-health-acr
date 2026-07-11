package config

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/limits"
)

func TestLimitOptionsConfiguresEveryRequestClass(t *testing.T) {
	// Given
	cfg, err := load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(cfg.LimitOptions())
	if err != nil {
		t.Fatal(err)
	}
	subject := limits.Subject{OrgID: "org_1", CredentialID: "credential_1"}

	// When
	for _, class := range []limits.RequestClass{
		limits.RequestClassAuth,
		limits.RequestClassContext,
		limits.RequestClassEvidence,
		limits.RequestClassSnapshot,
		limits.RequestClassEpisode,
	} {
		claim, decision, err := manager.Claim(context.Background(), subject, class)

		// Then
		if err != nil || !decision.Allowed || claim == nil {
			t.Fatalf("class %d claim = (%#v, %#v, %v)", class, claim, decision, err)
		}
		claim.DoneClaim()
	}
}

func TestLimitOptionsUseIndependentClassPoliciesAndPacketBudgets(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{
		"ACR_CONTEXT_REQUESTS_PER_WINDOW":  "2",
		"ACR_EVIDENCE_REQUESTS_PER_WINDOW": "4",
		"ACR_LIMIT_WINDOW":                 "2m",
		"ACR_MAXIMUM_RETRY_AFTER":          "2m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(cfg.LimitOptions())
	if err != nil {
		t.Fatal(err)
	}
	subject := limits.Subject{OrgID: "org_1", CredentialID: "credential_1"}

	contextClaim, contextDecision, err := manager.Claim(context.Background(), subject, limits.RequestClassContext)
	if err != nil || !contextDecision.Allowed {
		t.Fatalf("context claim = (%#v, %#v, %v)", contextClaim, contextDecision, err)
	}
	if err := contextClaim.Complete(limits.ResourceUsage{Items: int64(cfg.MaxItems + 1)}); err == nil {
		t.Fatal("context packet item budget was not enforced")
	}
	secondContext, decision, err := manager.Claim(context.Background(), subject, limits.RequestClassContext)
	if err != nil || !decision.Allowed {
		t.Fatalf("second context claim = (%#v, %#v, %v)", secondContext, decision, err)
	}
	secondContext.DoneClaim()
	_, deniedContext, err := manager.Claim(context.Background(), subject, limits.RequestClassContext)
	if err != nil || deniedContext.Allowed || deniedContext.RetryAfter != 2*time.Minute {
		t.Fatalf("context denial = (%#v, %v)", deniedContext, err)
	}
	for range 4 {
		claim, decision, err := manager.Claim(context.Background(), subject, limits.RequestClassEvidence)
		if err != nil || !decision.Allowed {
			t.Fatalf("evidence claim = (%#v, %#v, %v)", claim, decision, err)
		}
		claim.DoneClaim()
	}
	if cfg.ContextRequestsPerMinute() != 1 {
		t.Fatalf("requests per minute = %d", cfg.ContextRequestsPerMinute())
	}
}

func TestLimitConfigRejectsFractionalCapabilityRate(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_CONTEXT_REQUESTS_PER_WINDOW": "3", "ACR_CONTEXT_LIMIT_WINDOW": "2m"}))
	if err == nil {
		t.Fatal("fractional requests-per-minute capability was accepted")
	}
}
