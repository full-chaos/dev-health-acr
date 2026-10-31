package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-go/authverify"
)

// CHAOS-12094 (F1, chris decision 8: MCP bearers for MCP calls only). acr-api
// admits a resource-bound credential when the request carries the matching
// X-ACR-Resource, and that header is caller-set: an MCP-bound bearer worked
// directly on acr-api with the MCP resource value. With the hop required,
// acr-mcp proves each call it forwards with its own projected Kubernetes
// ServiceAccount token in MCPHopHeader. acr-api verifies it through
// TokenReview (the same validator the workload token exchange uses) and
// accepts only the configured acr-mcp ServiceAccount. A credential bound to an
// MCP resource (any bound resource other than acr-api's own, the OAuth
// issuer) is then admitted only on a call that carries a valid hop. The
// browser class (bound to the issuer) and unbound credentials are unchanged.

// MCPHopHeader carries acr-mcp's projected ServiceAccount token on every call
// it forwards to acr-api. It is never the caller's: acr-mcp reads it from its
// own token file.
const MCPHopHeader = "X-ACR-MCP-Hop"

// mcpHopCacheLimit bounds the verified-token cache. acr-mcp presents one token
// per pod at a time (kubelet rotates it well before it expires), so a handful
// of entries are live; the bound only keeps a flood of distinct valid tokens
// from growing the map.
const mcpHopCacheLimit = 256

var (
	// errMCPHopMissing: the call carries no hop (or more than one).
	errMCPHopMissing = errors.New("mcp hop missing")
	// errMCPHopRefused: the hop is not a live token of the acr-mcp
	// ServiceAccount for the hop audience.
	errMCPHopRefused = errors.New("mcp hop refused")
)

// MCPHopValidator validates a projected ServiceAccount token; it is
// authverify's TokenReview validator in production.
type MCPHopValidator interface {
	Validate(ctx context.Context, subjectToken string) (authverify.SubjectIdentity, error)
}

// MCPHopPolicyOptions configure the hop check.
type MCPHopPolicyOptions struct {
	// Validator checks a hop token (TokenReview with the hop audience).
	Validator MCPHopValidator
	// Namespace and ServiceAccount name the acr-mcp ServiceAccount.
	Namespace, ServiceAccount string
	// Issuer is acr-api's own resource (ACR_OAUTH_ISSUER). A credential bound
	// to any other resource is MCP-bound and needs the hop.
	Issuer string
	Now    func() time.Time
}

// MCPHopPolicy decides and verifies the hop. A nil policy requires nothing.
type MCPHopPolicy struct {
	validator                 MCPHopValidator
	namespace, serviceAccount string
	issuer                    string
	now                       func() time.Time

	mu    sync.Mutex
	cache map[string]time.Time // sha256(token) -> token expiry
}

// NewMCPHopPolicy builds the hop policy; every option is required.
func NewMCPHopPolicy(options MCPHopPolicyOptions) (*MCPHopPolicy, error) {
	if options.Validator == nil {
		return nil, errors.New("mcp hop: a token validator is required")
	}
	if strings.TrimSpace(options.Namespace) == "" || strings.TrimSpace(options.ServiceAccount) == "" {
		return nil, errors.New("mcp hop: the acr-mcp service account namespace and name are required")
	}
	if strings.TrimSpace(options.Issuer) == "" {
		return nil, errors.New("mcp hop: the acr-api resource (the OAuth issuer) is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &MCPHopPolicy{
		validator: options.Validator, namespace: options.Namespace, serviceAccount: options.ServiceAccount,
		issuer: options.Issuer, now: options.Now, cache: map[string]time.Time{},
	}, nil
}

// requires reports whether a credential bound to resource needs the hop.
func (p *MCPHopPolicy) requires(resource string) bool {
	return p != nil && resource != "" && !SameResource(resource, p.issuer)
}

// verify checks the hop values a request carries. It returns
// errMCPHopMissing or errMCPHopRefused for a hop that fails, and another
// error when the check itself could not run (TokenReview unreachable): that
// one is not the caller's fault and is answered as unavailable.
func (p *MCPHopPolicy) verify(ctx context.Context, values []string) error {
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return errMCPHopMissing
	}
	token := strings.TrimSpace(values[0])
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := p.now().UTC()
	p.mu.Lock()
	expiry, cached := p.cache[key]
	p.mu.Unlock()
	if cached && now.Before(expiry) {
		return nil
	}
	identity, err := p.validator.Validate(ctx, token)
	if errors.Is(err, authverify.ErrSubjectTokenInvalid) {
		return errMCPHopRefused
	}
	if err != nil {
		return err
	}
	if identity.Namespace != p.namespace || identity.ServiceAccountName != p.serviceAccount || !now.Before(identity.ExpiresAt) {
		return errMCPHopRefused
	}
	p.remember(key, identity.ExpiresAt, now)
	return nil
}

// remember caches a verified token until its own expiry, never longer.
func (p *MCPHopPolicy) remember(key string, expiry, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cache) >= mcpHopCacheLimit {
		for k, e := range p.cache {
			if !now.Before(e) {
				delete(p.cache, k)
			}
		}
	}
	if len(p.cache) >= mcpHopCacheLimit {
		p.cache = map[string]time.Time{}
	}
	p.cache[key] = expiry
}
