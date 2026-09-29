package mcp

import (
	"net/http"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// Edge gate defaults: the same numbers as acr-api's per-address gate.
const (
	DefaultEdgeGateFailures    = 20
	DefaultEdgeGateWindow      = time.Minute
	DefaultEdgeGateTrackedKeys = 4096
)

// EdgeGateOptions configures the per-address failed-authentication gate at
// the MCP edge. Zero numeric fields take the acr-api defaults.
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
// address can spend at most N x FailureLimit failures per window.
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
		if o.FailureLimit <= 0 {
			o.FailureLimit = DefaultEdgeGateFailures
		}
		if o.Window <= 0 {
			o.Window = DefaultEdgeGateWindow
		}
		if o.MaxTrackedKeys <= 0 {
			o.MaxTrackedKeys = DefaultEdgeGateTrackedKeys
		}
		limiter = auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{
			Window: o.Window, FailureLimit: o.FailureLimit, MaxTrackedKeys: o.MaxTrackedKeys, MaxInFlight: o.MaxInFlight,
		})
	}
	return &edgeGate{limiter: limiter, resolver: resolver}, nil
}

func (g *edgeGate) clientIP(r *http.Request) string { return g.resolver(r) }

// refusalReason names which bound refused an address after BeginAttempt said
// no: the failure budget, or capacity (in-flight or tracked-address table).
func (g *edgeGate) refusalReason(ip string, now time.Time) string {
	if g.limiter.FailureBlocked(ip, now) {
		return HTTPGateFailureBudget
	}
	return HTTPGateCapacity
}

func (g *edgeGate) retryAfter(ip string, now time.Time) time.Duration {
	if d := g.limiter.RetryAfter(ip, now); d > 0 {
		return d
	}
	return time.Second
}
