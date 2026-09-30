package mcp

import (
	"net/http"
	"os"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
)

// EdgeGateOptions configures the per-address failed-authentication gate at
// the MCP edge. A zero numeric field takes the value internal/config resolves
// from the process environment: the same loader and fallback chain acr-api
// uses, so the two gates share one set of defaults.
type EdgeGateOptions struct {
	FailureLimit   int
	Window         time.Duration
	MaxTrackedKeys int
	MaxInFlight    int
	// TrustedProxyCIDRs lists the proxies whose X-Forwarded-For is believed.
	// Empty: the gate keys on the TCP peer address and ignores the header.
	TrustedProxyCIDRs []string
	// Limiter replaces the in-memory limiter (tests).
	Limiter auth.AttemptLimiter
}

// edgeGate counts failed authentications per client address BEFORE any hosted
// API call, so missing and malformed bearers (which never reach acr-api) are
// counted too. State is in memory per process: with N acr-mcp replicas an
// address can record about N x (FailureLimit + MaxInFlight) failures per
// window (attempts admitted before the limit was reached can still fail).
type edgeGate struct {
	limiter  auth.AttemptLimiter
	resolver auth.ClientIPResolver
}

func newEdgeGate(o EdgeGateOptions) (*edgeGate, error) {
	resolver, err := auth.NewTrustedProxyClientIPResolver(o.TrustedProxyCIDRs)
	if err != nil {
		return nil, err
	}
	limiter := o.Limiter
	if limiter == nil {
		if o.FailureLimit <= 0 || o.Window <= 0 || o.MaxTrackedKeys <= 0 || o.MaxInFlight <= 0 {
			shared, err := config.AuthGateLimitsFromEnvironment(os.LookupEnv)
			if err != nil {
				return nil, err
			}
			if o.FailureLimit <= 0 {
				o.FailureLimit = shared.FailureLimit
			}
			if o.Window <= 0 {
				o.Window = shared.Window
			}
			if o.MaxTrackedKeys <= 0 {
				o.MaxTrackedKeys = shared.MaxTrackedKeys
			}
			if o.MaxInFlight <= 0 {
				o.MaxInFlight = shared.MaxInFlight
			}
		}
		limiter = auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{
			Window: o.Window, FailureLimit: o.FailureLimit, MaxTrackedKeys: o.MaxTrackedKeys, MaxInFlight: o.MaxInFlight,
		})
	}
	return &edgeGate{limiter: limiter, resolver: resolver}, nil
}

func (g *edgeGate) clientIP(r *http.Request) string { return g.resolver(r) }

// begin admits one attempt and, when refused, names the bound that refused it.
func (g *edgeGate) begin(ip string, now time.Time) (func(), string) {
	release, decision := auth.BeginAttemptDecision(g.limiter, ip, now)
	switch decision.Refusal {
	case auth.RefusalNone:
		return release, HTTPGateAdmitted
	case auth.RefusalFailureBudget:
		return nil, HTTPGateFailureBudget
	case auth.RefusalInFlight:
		return nil, HTTPGateInFlight
	case auth.RefusalTrackedKeys:
		return nil, HTTPGateTrackedKeys
	}
	return nil, HTTPGateUnspecified
}

func (g *edgeGate) retryAfter(ip string, now time.Time) time.Duration {
	if d := g.limiter.RetryAfter(ip, now); d > 0 {
		return d
	}
	return time.Second
}
