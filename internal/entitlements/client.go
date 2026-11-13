// Package entitlements provides the fail-closed Dev Health service client.
package entitlements

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
)

const entitlementSchemaVersion = "acr_entitlement.v1"

// unknownOrganizationCode is the stable code Dev Health sends on the 404 for a
// well-formed organization id that has no organization row.
const unknownOrganizationCode = "acr_organization_unknown"

// disabledOrganizationCode is the stable code Dev Health sends on the 404 for
// an organization row that exists but is disabled. It is never "unknown": a
// disabled organization is refused, never purged.
const disabledOrganizationCode = "acr_organization_disabled"

var (
	errUnavailable = errors.New("Dev Health entitlement service unavailable")

	// ErrOrganizationUnknown means Dev Health positively answered that the
	// organization does not exist. It is never a grant and never cached.
	ErrOrganizationUnknown = errors.New("Dev Health does not know the organization")

	// ErrOrganizationDisabled means Dev Health positively answered that the
	// organization exists and is disabled. It is never a grant and never cached.
	ErrOrganizationDisabled = errors.New("Dev Health answers the organization as disabled")
)

// OrganizationState is Dev Health's positive answer about an organization.
type OrganizationState int

const (
	OrganizationLive OrganizationState = iota
	OrganizationDisabled
	OrganizationUnknown
)

// Client implements api.EntitlementProvider without exposing credentials or
// upstream response bodies through errors.
type Client struct {
	baseURL  *url.URL
	workload TokenSource
	observe  func(ctx context.Context, credential, outcome string)
	timeout  time.Duration
	maxBody  int64
	http     *http.Client
	positive time.Duration
	negative time.Duration
	cache    cache
	now      func() time.Time
	// llmConfigs caches OrgLLMConfig answers apart from entitlements.
	llmConfigs *llmConfigCache
}

// New parses all untrusted configuration before accepting a client. It never
// permits environment proxy settings.
func New(config Config) (*Client, error) {
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}
	config.BaseURL = cloneURL(config.BaseURL)
	config.ProxyURL = cloneURL(config.ProxyURL)
	transport, err := newTransport(config)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:    config.BaseURL,
		workload:   config.Workload,
		observe:    config.Observe,
		timeout:    config.Timeout,
		maxBody:    config.MaxResponseBytes,
		http:       &http.Client{Transport: transport, CheckRedirect: rejectRedirect},
		positive:   config.PositiveCacheTTL,
		negative:   config.NegativeCacheTTL,
		cache:      newCache(config.CacheCapacity),
		now:        config.Now,
		llmConfigs: &llmConfigCache{},
	}, nil
}

const (
	agentContextRuntimeEntitlement   = "agent_context_runtime"
	serviceModelEntitlement          = "acr_service_model"
	platformModelFallbackEntitlement = "platform_model_fallback"
)

// HasEntitlement answers agent_context_runtime, acr_service_model or
// platform_model_fallback from one cached response per organization. The
// cache lifetime follows agent_context_runtime, so a changed acr_service_model
// or platform_model_fallback grant is seen within the positive or negative
// cache TTL.
func (c *Client) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	if strings.TrimSpace(orgID) == "" || (entitlement != agentContextRuntimeEntitlement && entitlement != serviceModelEntitlement && entitlement != platformModelFallbackEntitlement) {
		return false, errors.New("unsupported entitlement request")
	}
	value, err := c.lookup(ctx, orgID)
	if err != nil {
		return false, err
	}
	switch entitlement {
	case serviceModelEntitlement:
		return value.serviceModel, nil
	case platformModelFallbackEntitlement:
		return value.platformModelFallback, nil
	}
	return value.runtime, nil
}

// OrganizationKnown is ErrOrganizationUnknown only for the coded 404; any other failure is the unavailable error.
func (c *Client) OrganizationKnown(ctx context.Context, orgID string) error {
	if strings.TrimSpace(orgID) == "" {
		return errUnavailable
	}
	_, err := c.lookup(ctx, orgID)
	return err
}

func (c *Client) lookup(ctx context.Context, orgID string) (grants, error) {
	now := c.now()
	if value, ok := c.cache.get(orgID, now); ok {
		return value, nil
	}
	result, err := c.fetch(ctx, orgID)
	if err != nil {
		c.cache.delete(orgID)
		return grants{}, err
	}
	ttl := c.negative
	if result.runtime {
		ttl = c.positive
	}
	c.cache.put(orgID, result, now.Add(ttl))
	return result, nil
}

// Check verifies the authenticated Dev Health service-health contract before
// ACR accepts the entitlement provider as ready.
func (c *Client) Check(ctx context.Context) error {
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, healthURL(c.baseURL), nil)
	if err != nil {
		return errUnavailable
	}
	if !c.authorize(ctx, request) {
		return errUnavailable
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errUnavailable
	}
	parsed, err := decodeHealthResponse(response.Body, c.maxBody)
	if err != nil || parsed.SchemaVersion != "acr_service_health.v1" || parsed.Service != "dev-health-ops" || parsed.Status != "ok" {
		return errUnavailable
	}
	return nil
}

// Close releases idle transport connections held by the client. It is safe to
// call more than once and never fails: the entitlement client owns no other
// closable resource.
func (c *Client) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

func (c *Client) fetch(ctx context.Context, orgID string) (grants, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, entitlementURL(c.baseURL, orgID), nil)
	if err != nil {
		return grants{}, rejected(reasonTransport)
	}
	if !c.authorize(ctx, request) {
		return grants{}, rejected(reasonTransport)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return grants{}, rejected(reasonTransport)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		switch notFoundCode(response.Body, c.maxBody) {
		case unknownOrganizationCode:
			return grants{}, ErrOrganizationUnknown
		case disabledOrganizationCode:
			return grants{}, ErrOrganizationDisabled
		}
	}
	if response.StatusCode != http.StatusOK {
		return grants{}, rejected(reasonStatus)
	}
	parsed, err := decodeResponse(response.Body, c.maxBody)
	if err != nil {
		if errors.Is(err, errUnknownField) {
			return grants{}, rejected(reasonUnknownField)
		}
		return grants{}, rejected(reasonDecode)
	}
	if parsed.SchemaVersion != entitlementSchemaVersion || parsed.OrgID != orgID {
		return grants{}, rejected(reasonSchema)
	}
	return grants{runtime: parsed.AgentContextRuntime, serviceModel: parsed.ServiceModel, platformModelFallback: parsed.PlatformModelFallback}, nil
}

// OrganizationUnknown reports whether Dev Health positively says the
// organization does not exist: true only for a 404 carrying the stable
// unknown-organization code. An HTTP 200 (any body) and the coded 404 of a
// disabled organization are (false, nil). A malformed id, a transport
// failure, a timeout, any 5xx, a 404 without a known code and every other
// status are (false, errUnavailable), so an outage can never read as a
// deletion.
func (c *Client) OrganizationUnknown(ctx context.Context, orgID string) (bool, error) {
	state, err := c.OrganizationStateNow(ctx, orgID)
	if err != nil {
		return false, err
	}
	return state == OrganizationUnknown, nil
}

// OrganizationStateNow reads Dev Health's entitlement route for orgID, never
// the cache: an HTTP 200 (any body) is live, the 404 with the disabled code is
// disabled, the 404 with the unknown code is unknown. Every other answer is
// errUnavailable.
func (c *Client) OrganizationStateNow(ctx context.Context, orgID string) (OrganizationState, error) {
	if parsed, err := uuid.Parse(strings.TrimSpace(orgID)); err != nil || parsed.String() != orgID {
		return OrganizationLive, errUnavailable
	}
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, entitlementURL(c.baseURL, orgID), nil)
	if err != nil {
		return OrganizationLive, errUnavailable
	}
	if !c.authorize(ctx, request) {
		return OrganizationLive, errUnavailable
	}
	response, err := c.http.Do(request)
	if err != nil {
		return OrganizationLive, errUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return OrganizationLive, nil
	case http.StatusNotFound:
		switch notFoundCode(response.Body, c.maxBody) {
		case unknownOrganizationCode:
			return OrganizationUnknown, nil
		case disabledOrganizationCode:
			return OrganizationDisabled, nil
		}
	}
	return OrganizationLive, errUnavailable
}

// notFoundCode is the code a Dev Health 404 body carries, or "" when the body
// is unreadable, too large or carries none.
func notFoundCode(body io.Reader, maxBytes int64) string {
	payload, err := readBounded(body, maxBytes)
	if err != nil {
		return ""
	}
	var parsed struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(payload, &parsed) != nil {
		return ""
	}
	return parsed.Code
}

func entitlementURL(baseURL *url.URL, orgID string) string {
	copy := *baseURL
	copy.Path = "/api/v1/internal/acr/entitlements/" + url.PathEscape(orgID)
	return copy.String()
}

func healthURL(baseURL *url.URL) string {
	copy := *baseURL
	copy.Path = "/api/v1/internal/acr/health"
	return copy.String()
}

func newTransport(config Config) (*http.Transport, error) {
	pool, err := certificatePool(config.CACertPath)
	if err != nil {
		return nil, err
	}
	transport := outboundhttp.Transport()
	if config.ProxyURL != nil {
		transport.Proxy = http.ProxyURL(config.ProxyURL)
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return transport, nil
}

func certificatePool(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pemBytes, err := readRestrictedFile(path, 1<<20)
	if err != nil {
		return nil, errors.New("Dev Health entitlement CA bundle is invalid")
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("Dev Health entitlement CA bundle is invalid")
	}
	return pool, nil
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func (c *Client) String() string {
	return fmt.Sprintf("Dev Health entitlement client (%s)", c.baseURL.Scheme)
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
