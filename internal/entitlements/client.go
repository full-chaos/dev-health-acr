// Package entitlements provides the fail-closed Dev Health service client.
package entitlements

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const entitlementSchemaVersion = "acr_entitlement.v1"

var (
	errUnavailable = errors.New("Dev Health entitlement service unavailable")
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
		baseURL:  config.BaseURL,
		token:    token,
		timeout:  config.Timeout,
		maxBody:  config.MaxResponseBytes,
		http:     &http.Client{Transport: transport, CheckRedirect: rejectRedirect},
		positive: config.PositiveCacheTTL,
		negative: config.NegativeCacheTTL,
		cache:    newCache(config.CacheCapacity),
		now:      time.Now,
	}, nil
}

func (c *Client) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	if strings.TrimSpace(orgID) == "" || entitlement != "agent_context_runtime" {
		return false, errors.New("unsupported entitlement request")
	}
	now := c.now()
	if entitled, ok := c.cache.get(orgID, now); ok {
		return entitled, nil
	}
	result, err := c.fetch(ctx, orgID)
	if err != nil {
		c.cache.delete(orgID)
		return false, err
	}
	ttl := c.negative
	if result {
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

func (c *Client) fetch(ctx context.Context, orgID string) (bool, error) {
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
	if response.StatusCode != http.StatusOK {
		return false, errUnavailable
	}
	parsed, err := decodeResponse(response.Body, c.maxBody)
	if err != nil || parsed.SchemaVersion != entitlementSchemaVersion || parsed.OrgID != orgID {
		return false, errUnavailable
	}
	return parsed.AgentContextRuntime, nil
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
