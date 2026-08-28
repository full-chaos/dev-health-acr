package limits

import (
	"context"
	"sync"
	"time"
)

const (
	defaultMaxTrackedOrganizations = 1024
	defaultMaxCredentialsPerOrg    = 128
	defaultStateRetention          = time.Hour
	defaultConcurrencyRetry        = 5 * time.Second
	defaultMaxRetry                = time.Minute
)

type Options struct {
	Now                           func() time.Time
	Policies                      PolicySet
	PerOrgConcurrency             int
	MaxTrackedOrganizations       int
	MaxCredentialsPerOrganization int
	StateRetention                time.Duration
	ConcurrencyRetryAfter         time.Duration
	MaxRetryAfter                 time.Duration
}

type DenialReason string

const (
	DenialNone             DenialReason = ""
	DenialOrgQuota         DenialReason = "org_quota"
	DenialCredentialQuota  DenialReason = "credential_quota"
	DenialConcurrency      DenialReason = "org_concurrency"
	DenialTrackingCapacity DenialReason = "tracking_capacity"
)

type Decision struct {
	Allowed    bool
	Reason     DenialReason
	RetryAfter time.Duration
}

type UsageCounters struct {
	Admitted  int64
	Denied    int64
	Completed int64
	Items     int64
	Tokens    int64
	Bytes     int64
}

type UsageTotals struct {
	WindowStarted time.Time
	Org           UsageCounters
	Credential    UsageCounters
}

type ResourceUsage struct{ Items, Tokens, Bytes int64 }

type Manager struct {
	mu                                    sync.Mutex
	now                                   func() time.Time
	policies                              PolicySet
	concurrency                           int
	maxOrganizations, maxCredentials      int
	retention, concurrencyRetry, maxRetry time.Duration
	windows                               map[RequestClass]map[string]*usageWindow
	inflight                              map[string]int
}

type usageWindow struct {
	started, quotaStarted time.Time
	lastTouched           time.Time
	quotaOrg              int64
	quotaCredential       map[string]int64
	org                   UsageCounters
	credentials           map[string]UsageCounters
}

type Claim struct {
	manager *Manager
	class   RequestClass
	subject Subject
	window  *usageWindow
	budget  ResourceBudget
	once    sync.Once
	result  error
}

func NewManager(options Options) (*Manager, error) {
	if err := options.Policies.validate(); err != nil || options.PerOrgConcurrency < 0 || options.MaxTrackedOrganizations < 0 || options.MaxCredentialsPerOrganization < 0 || options.StateRetention < 0 || options.ConcurrencyRetryAfter < 0 || options.MaxRetryAfter < 0 {
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidPolicy
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxTrackedOrganizations == 0 {
		options.MaxTrackedOrganizations = defaultMaxTrackedOrganizations
	}
	if options.MaxCredentialsPerOrganization == 0 {
		options.MaxCredentialsPerOrganization = defaultMaxCredentialsPerOrg
	}
	if options.StateRetention == 0 {
		options.StateRetention = defaultStateRetention
	}
	if options.MaxRetryAfter == 0 {
		options.MaxRetryAfter = defaultMaxRetry
	}
	if options.ConcurrencyRetryAfter == 0 {
		options.ConcurrencyRetryAfter = defaultConcurrencyRetry
	}
	if options.ConcurrencyRetryAfter > options.MaxRetryAfter {
		options.ConcurrencyRetryAfter = options.MaxRetryAfter
	}
	return &Manager{
		now: options.Now, policies: options.Policies, concurrency: options.PerOrgConcurrency,
		maxOrganizations: options.MaxTrackedOrganizations, maxCredentials: options.MaxCredentialsPerOrganization,
		retention: options.StateRetention, concurrencyRetry: options.ConcurrencyRetryAfter, maxRetry: options.MaxRetryAfter,
		windows: make(map[RequestClass]map[string]*usageWindow), inflight: make(map[string]int),
	}, nil
}

func (m *Manager) Claim(ctx context.Context, subject Subject, class RequestClass) (*Claim, Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, Decision{}, err
	}
	if err := subject.validate(); err != nil {
		return nil, Decision{}, err
	}
	policy, err := m.policies.policy(class)
	if err != nil {
		return nil, Decision{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, Decision{}, err
	}
	now := m.now().UTC()
	m.sweep(now)
	window, decision := m.windowFor(class, subject, policy, now)
	if !decision.Allowed {
		return nil, decision, nil
	}
	if m.concurrency > 0 && m.inflight[subject.OrgID] >= m.concurrency {
		window.denied(subject.CredentialID, now)
		return nil, Decision{Reason: DenialConcurrency, RetryAfter: m.concurrencyRetry}, nil
	}
	if policy.PerOrgLimit > 0 && window.quotaOrg >= int64(policy.PerOrgLimit) {
		window.denied(subject.CredentialID, now)
		return nil, m.denied(DenialOrgQuota, window.quotaStarted, policy.Window, now), nil
	}
	credential := window.credentials[subject.CredentialID]
	if policy.PerCredentialLimit > 0 && window.quotaCredential[subject.CredentialID] >= int64(policy.PerCredentialLimit) {
		window.denied(subject.CredentialID, now)
		return nil, m.denied(DenialCredentialQuota, window.quotaStarted, policy.Window, now), nil
	}
	window.quotaOrg++
	window.quotaCredential[subject.CredentialID]++
	window.org.Admitted++
	credential.Admitted++
	window.credentials[subject.CredentialID] = credential
	window.lastTouched = now
	m.inflight[subject.OrgID]++
	return &Claim{manager: m, class: class, subject: subject, window: window, budget: policy.Resources}, Decision{Allowed: true}, nil
}

func (m *Manager) Usage(subject Subject, class RequestClass) (UsageTotals, error) {
	if err := subject.validate(); err != nil {
		return UsageTotals{}, err
	}
	if _, err := m.policies.policy(class); err != nil {
		return UsageTotals{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	m.sweep(now)
	window := m.windows[class][subject.OrgID]
	if window == nil {
		return UsageTotals{}, nil
	}
	return UsageTotals{WindowStarted: window.started, Org: window.org, Credential: window.credentials[subject.CredentialID]}, nil
}

func (c *Claim) Complete(usage ResourceUsage) error {
	if c == nil || c.manager == nil {
		return nil
	}
	if usage.Items < 0 || usage.Tokens < 0 || usage.Bytes < 0 {
		return ErrInvalidUsage
	}
	c.once.Do(func() {
		if !c.budget.allows(usage) {
			c.result = ErrResourceBudgetExceeded
			c.manager.complete(c, usage, false)
			return
		}
		c.manager.complete(c, usage, true)
	})
	return c.result
}

// CompleteWithBudget completes the claim like Complete, but evaluates
// usage against override instead of the RequestClass's own configured
// resource budget. The class's admission/rate-limit accounting
// (PerOrgLimit/PerCredentialLimit/concurrency, all decided at Claim() time)
// is entirely unaffected -- override only changes which ceiling the
// resource-usage check applies, and usage is still what gets recorded
// into the org/credential window totals Usage() reports, exactly as
// Complete records it. This exists for a caller that legitimately needs a
// per-request resource ceiling looser (or tighter) than its RequestClass's
// shared default along one dimension, without a false accounting record
// on that dimension and without a new RequestClass fragmenting the shared
// rate-limit window (CHAOS-4355: a Context Fabric investigation response
// can legitimately carry far more estimated tokens than
// RequestClassContext's shared MaxTokens permits, while its actual byte
// size stays governed by that same class's MaxBytes).
func (c *Claim) CompleteWithBudget(usage ResourceUsage, override ResourceBudget) error {
	if c == nil || c.manager == nil {
		return nil
	}
	if usage.Items < 0 || usage.Tokens < 0 || usage.Bytes < 0 {
		return ErrInvalidUsage
	}
	if !override.valid() {
		return ErrInvalidPolicy
	}
	c.once.Do(func() {
		if !override.allows(usage) {
			c.result = ErrResourceBudgetExceeded
			c.manager.complete(c, usage, false)
			return
		}
		c.manager.complete(c, usage, true)
	})
	return c.result
}

func (c *Claim) DoneClaim() { _ = c.Complete(ResourceUsage{}) }

func (m *Manager) complete(claim *Claim, usage ResourceUsage, accepted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	claim.window.org.Completed++
	credential := claim.window.credentials[claim.subject.CredentialID]
	credential.Completed++
	if accepted {
		claim.window.org.Items += usage.Items
		claim.window.org.Tokens += usage.Tokens
		claim.window.org.Bytes += usage.Bytes
		credential.Items += usage.Items
		credential.Tokens += usage.Tokens
		credential.Bytes += usage.Bytes
	} else {
		claim.window.org.Denied++
		credential.Denied++
	}
	claim.window.credentials[claim.subject.CredentialID] = credential
	claim.window.lastTouched = now
	if m.inflight[claim.subject.OrgID] <= 1 {
		delete(m.inflight, claim.subject.OrgID)
		return
	}
	m.inflight[claim.subject.OrgID]--
}
