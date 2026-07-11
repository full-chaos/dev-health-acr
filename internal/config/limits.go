package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/limits"
)

const (
	defaultMaxTrackedOrganizations = 1024
	defaultMaxCredentialsPerOrg    = 128
	defaultAuthTrackedKeys         = 4096
)

type ClassLimitConfig struct {
	Window   time.Duration
	Requests int
}

type RequestControlsConfig struct {
	Auth                    ClassLimitConfig
	Context                 ClassLimitConfig
	Evidence                ClassLimitConfig
	Snapshot                ClassLimitConfig
	Episode                 ClassLimitConfig
	AuthFailures            int
	AuthTrackedKeys         int
	PerOrgConcurrency       int
	MaxTrackedOrganizations int
	MaxCredentialsPerOrg    int
	StateRetention          time.Duration
	ConcurrencyRetryAfter   time.Duration
	MaximumRetryAfter       time.Duration
}

func requestControlsValue(lookup lookupEnv, fallback int) (RequestControlsConfig, error) {
	sharedWindow, err := durationValue(lookup, "ACR_LIMIT_WINDOW", time.Minute)
	if err != nil {
		return RequestControlsConfig{}, err
	}
	readClass := func(prefix string) (ClassLimitConfig, error) {
		window, err := durationValue(lookup, "ACR_"+prefix+"_LIMIT_WINDOW", sharedWindow)
		if err != nil {
			return ClassLimitConfig{}, err
		}
		requests, err := intValue(lookup, "ACR_"+prefix+"_REQUESTS_PER_WINDOW", fallback)
		return ClassLimitConfig{Window: window, Requests: requests}, err
	}
	classes := make([]ClassLimitConfig, 5)
	for index, prefix := range []string{"AUTH", "CONTEXT", "EVIDENCE", "SNAPSHOT", "EPISODE"} {
		classes[index], err = readClass(prefix)
		if err != nil {
			return RequestControlsConfig{}, err
		}
	}
	values := make([]int, 5)
	for index, value := range []struct {
		key      string
		fallback int
	}{
		{"ACR_AUTH_FAILURES_PER_WINDOW", min(20, fallback)},
		{"ACR_AUTH_MAX_TRACKED_KEYS", defaultAuthTrackedKeys},
		{"ACR_PER_ORG_CONCURRENCY", min(8, fallback)},
		{"ACR_MAX_TRACKED_ORGANIZATIONS", defaultMaxTrackedOrganizations},
		{"ACR_MAX_CREDENTIALS_PER_ORG", defaultMaxCredentialsPerOrg},
	} {
		values[index], err = intValue(lookup, value.key, value.fallback)
		if err != nil {
			return RequestControlsConfig{}, err
		}
	}
	stateRetention, err := durationValue(lookup, "ACR_LIMIT_STATE_RETENTION", time.Hour)
	if err != nil {
		return RequestControlsConfig{}, err
	}
	concurrencyRetry, err := durationValue(lookup, "ACR_CONCURRENCY_RETRY_AFTER", 5*time.Second)
	if err != nil {
		return RequestControlsConfig{}, err
	}
	maximumRetryFallback := sharedWindow
	for _, class := range classes {
		if class.Window > maximumRetryFallback {
			maximumRetryFallback = class.Window
		}
	}
	if maximumRetryFallback < time.Minute {
		maximumRetryFallback = time.Minute
	}
	maximumRetry, err := durationValue(lookup, "ACR_MAXIMUM_RETRY_AFTER", maximumRetryFallback)
	if err != nil {
		return RequestControlsConfig{}, err
	}
	return RequestControlsConfig{
		Auth: classes[0], Context: classes[1], Evidence: classes[2], Snapshot: classes[3], Episode: classes[4],
		AuthFailures: values[0], AuthTrackedKeys: values[1], PerOrgConcurrency: values[2],
		MaxTrackedOrganizations: values[3], MaxCredentialsPerOrg: values[4], StateRetention: stateRetention,
		ConcurrencyRetryAfter: concurrencyRetry, MaximumRetryAfter: maximumRetry,
	}, nil
}

func (c RequestControlsConfig) validate() error {
	for _, entry := range []struct {
		name  string
		class ClassLimitConfig
	}{
		{"AUTH", c.Auth}, {"CONTEXT", c.Context}, {"EVIDENCE", c.Evidence}, {"SNAPSHOT", c.Snapshot}, {"EPISODE", c.Episode},
	} {
		if entry.class.Window <= 0 || entry.class.Requests < 1 {
			return fmt.Errorf("ACR_%s limit window and requests must be positive", entry.name)
		}
	}
	if c.AuthFailures < 1 || c.AuthTrackedKeys < 1 || c.PerOrgConcurrency < 1 || c.MaxTrackedOrganizations < 1 || c.MaxCredentialsPerOrg < 1 {
		return errors.New("ACR request-control counts must be positive")
	}
	if c.StateRetention <= 0 || c.ConcurrencyRetryAfter <= 0 || c.MaximumRetryAfter <= 0 || c.ConcurrencyRetryAfter > c.MaximumRetryAfter {
		return errors.New("ACR request-control durations must be positive and retry bounds ordered")
	}
	if perMinute(c.Context) < 1 {
		return errors.New("ACR_CONTEXT_REQUESTS_PER_WINDOW and ACR_CONTEXT_LIMIT_WINDOW must represent at least one request per minute")
	}
	if (int64(c.Context.Requests)*int64(time.Minute))%int64(c.Context.Window) != 0 {
		return errors.New("ACR context request limit must convert to a whole requests-per-minute capability")
	}
	return nil
}

func (c Config) LimitOptions() limits.Options {
	resources := limits.ResourceBudget{MaxItems: int64(c.MaxItems), MaxTokens: int64(c.MaxOutputTokens), MaxBytes: int64(c.MaxSerializedBytes)}
	contextPolicy := limitPolicy(c.RequestControls.Context)
	contextPolicy.Resources = resources
	return limits.Options{
		Policies: limits.PolicySet{
			Auth:     limits.AuthPolicy(limitPolicy(c.RequestControls.Auth)),
			Context:  contextPolicy,
			Evidence: limits.EvidencePolicy(limitPolicy(c.RequestControls.Evidence)),
			Snapshot: limits.SnapshotPolicy(limitPolicy(c.RequestControls.Snapshot)),
			Episode:  limits.EpisodePolicy(limitPolicy(c.RequestControls.Episode)),
		},
		PerOrgConcurrency:             c.RequestControls.PerOrgConcurrency,
		MaxTrackedOrganizations:       c.RequestControls.MaxTrackedOrganizations,
		MaxCredentialsPerOrganization: c.RequestControls.MaxCredentialsPerOrg,
		StateRetention:                c.RequestControls.StateRetention,
		ConcurrencyRetryAfter:         c.RequestControls.ConcurrencyRetryAfter,
		MaxRetryAfter:                 c.RequestControls.MaximumRetryAfter,
	}
}

func limitPolicy(class ClassLimitConfig) limits.ContextPolicy {
	return limits.ContextPolicy{Window: class.Window, PerOrgLimit: class.Requests, PerCredentialLimit: class.Requests}
}

func (c Config) ContextRequestsPerMinute() int {
	return perMinute(c.RequestControls.Context)
}

func perMinute(class ClassLimitConfig) int {
	if class.Window <= 0 || class.Requests <= 0 {
		return 0
	}
	return int((int64(class.Requests) * int64(time.Minute)) / int64(class.Window))
}
