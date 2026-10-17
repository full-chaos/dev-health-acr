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
)

const entitlementSchemaVersion = "acr_entitlement.v1"

// unknownOrganizationCode is the stable code Dev Health sends on the 404 for a
// well-formed organization id that has no organization row.
const unknownOrganizationCode = "acr_organization_unknown"

var (
	errUnavailable = errors.New("Dev Health entitlement service unavailable")

	// ErrOrganizationUnknown means Dev Health positively answered that the
	// organization does not exist. It is never a grant and never cached.
	ErrOrganizationUnknown = errors.New("Dev Health does not know the organization")
)

// Client implements api.EntitlementProvider without exposing credentials or
// upstream response bodies through errors.
type Client struct {
	baseURL  *url.URL
	token    string
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

// New parses all untrusted configuration and token-file data before accepting
// a client. It never permits environment proxy settings.
func New(config Config) (*Client, error) {
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}
	config.BaseURL = cloneURL(config.BaseURL)
	config.ProxyURL = cloneURL(config.ProxyURL)
	token, err := readToken(config.TokenFile)
	if err != nil {
		return nil, err
	}
	transport, err := newTransport(config)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:    config.BaseURL,
		token:      token,
		timeout:    config.Timeout,
		maxBody:    config.MaxResponseBytes,
		http:       &http.Client{Transport: transport, CheckRedirect: rejectRedirect},
		positive:   config.PositiveCacheTTL,
		negative:   config.NegativeCacheTTL,
		cache:      newCache(config.CacheCapacity),
		now:        time.Now,
		llmConfigs: &llmConfigCache{},
	}, nil
}

const (
	agentContextRuntimeEntitlement = "agent_context_runtime"
	serviceModelEntitlement        = "acr_service_model"
)

// HasEntitlement answers agent_context_runtime or acr_service_model from one
// cached response per organization. The cache lifetime follows
// agent_context_runtime, so a changed acr_service_model grant is seen within
// the positive or negative cache TTL.
func (c *Client) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	if strings.TrimSpace(orgID) == "" || (entitlement != agentContextRuntimeEntitlement && entitlement != serviceModelEntitlement) {
		return false, errors.New("unsupported entitlement request")
	}
	now := c.now()
	value, ok := c.cache.get(orgID, now)
	if !ok {
		result, err := c.fetch(ctx, orgID)
		if err != nil {
			c.cache.delete(orgID)
			return false, err
		}
		ttl := c.negative
		if result.runtime {
			ttl = c.positive
		}
		c.cache.put(orgID, result, now.Add(ttl))
		value = result
	}
	if entitlement == serviceModelEntitlement {
		return value.serviceModel, nil
	}
	return value.runtime, nil
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
	request.Header.Set("Authorization", "Bearer "+c.token)
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
		return grants{}, errUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return grants{}, errUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && isUnknownOrganizationBody(response.Body, c.maxBody) {
		return grants{}, ErrOrganizationUnknown
	}
	if response.StatusCode != http.StatusOK {
		return grants{}, errUnavailable
	}
	parsed, err := decodeResponse(response.Body, c.maxBody)
	if err != nil || parsed.SchemaVersion != entitlementSchemaVersion || parsed.OrgID != orgID {
		return grants{}, errUnavailable
	}
	return grants{runtime: parsed.AgentContextRuntime, serviceModel: parsed.ServiceModel}, nil
}

// OrganizationUnknown reports whether Dev Health positively says the
// organization does not exist: true only for a 404 carrying the stable
// unknown-organization code. An HTTP 200 (any body) is (false, nil). A
// malformed id, a transport failure, a timeout, any 5xx, a 404 without the
// code and every other status are (false, errUnavailable), so an outage can
// never read as a deletion.
func (c *Client) OrganizationUnknown(ctx context.Context, orgID string) (bool, error) {
	if parsed, err := uuid.Parse(strings.TrimSpace(orgID)); err != nil || parsed.String() != orgID {
		return false, errUnavailable
	}
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, entitlementURL(c.baseURL, orgID), nil)
	if err != nil {
		return false, errUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return false, errUnavailable
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		return false, nil
	case response.StatusCode == http.StatusNotFound && isUnknownOrganizationBody(response.Body, c.maxBody):
		return true, nil
	default:
		return false, errUnavailable
	}
}

func isUnknownOrganizationBody(body io.Reader, maxBytes int64) bool {
	payload, err := readBounded(body, maxBytes)
	if err != nil {
		return false
	}
	var parsed struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(payload, &parsed) == nil && parsed.Code == unknownOrganizationCode
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
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

func readToken(path string) (string, error) {
	contents, err := readRestrictedFile(path, 8<<10)
	if err != nil {
		return "", errors.New("Dev Health entitlement token file is invalid")
	}
	token := strings.TrimSuffix(string(contents), "\n")
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("Dev Health entitlement token file is invalid")
	}
	return token, nil
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
