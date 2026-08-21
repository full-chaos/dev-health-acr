package sidecar

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Fixed hosted API paths. Callers never influence these; the only
// caller-controlled path segment (the evidence reference ID) is always
// percent-escaped as a single path segment before being appended, see
// buildURL and Evidence.
const (
	capabilitiesPath   = "/api/v1/agent-context/capabilities"
	contextPacketsPath = "/api/v1/agent-context/context-packets"
	episodesPath       = "/api/v1/agent-context/episodes"
	evidencePathPrefix = "/api/v1/agent-context/evidence/"
)

// CredentialSource resolves the bearer credential for a hosted API call. It
// is invoked once per request so a rotated environment variable or token
// file is always honored; LoadCredential is the production default.
type CredentialSource func() (CredentialResult, error)

// Client is a hardened HTTP client for the hosted ACR read API
// (capabilities, context packets, evidence). It enforces HTTPS (except an
// explicit loopback fixture mode), disables redirect following so the
// bearer token can never be forwarded to a different host, bounds request
// and response sizes, and applies a configurable per-call timeout on top
// of the caller's context.
type Client struct {
	http       *http.Client
	baseURL    *url.URL
	cfg        Config
	credential CredentialSource
}

// NewClient builds a Client from a validated Config. credentialSource may
// be nil, in which case LoadCredential is used.
func NewClient(cfg Config, credentialSource CredentialSource) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sidecar configuration: %w", err)
	}
	if credentialSource == nil {
		credentialSource = LoadCredential
	}

	// Defensively clone the caller-supplied API and proxy origins. Config
	// is passed by value, but APIBaseURL/ProxyURL are pointers the caller
	// may still hold (and mutate) after this call returns; without a
	// clone, that later mutation would silently change the origin or
	// proxy this already-validated, already-running Client uses for every
	// subsequent request. buildTransport below must receive the cloned
	// cfg so its proxy closure captures the clone, not the caller's URL.
	cfg.APIBaseURL = cloneURL(cfg.APIBaseURL)
	cfg.ProxyURL = cloneURL(cfg.ProxyURL)

	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: refuseRedirect,
			// No client-level Timeout: enforced per call via
			// context.WithTimeout in call(), so an already-shorter caller
			// deadline is preserved instead of being overridden.
		},
		baseURL:    cfg.APIBaseURL,
		cfg:        cfg,
		credential: credentialSource,
	}, nil
}

// refuseRedirect stops the client from ever following a redirect. Per
// net/http, returning ErrUseLastResponse causes Do to return the 3xx
// response itself (body unclosed, no error), which call() then turns into
// a typed ErrUnexpectedRedirect APIError. This guarantees the Authorization
// header is only ever sent to the configured origin.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// buildTransport constructs an *http.Transport honoring the configured
// proxy (explicit override or standard environment resolution), an
// optional additional trusted CA bundle layered on the system pool, and a
// TLS floor of 1.2.
func buildTransport(cfg Config) (*http.Transport, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertPath != "" {
		pool, err := loadCACertPool(cfg.CACertPath)
		if err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = pool
	}

	proxyFunc := http.ProxyFromEnvironment
	if cfg.APIBaseURL.Scheme == "http" && (isLoopbackHost(cfg.APIBaseURL.Hostname()) || cfg.AllowInsecureInternalHTTP) {
		proxyFunc = nil
	} else if cfg.ProxyURL != nil {
		fixed := cfg.ProxyURL
		proxyFunc = func(*http.Request) (*url.URL, error) { return fixed, nil }
	}

	return &http.Transport{
		Proxy:                 proxyFunc,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nil
}

// maxCACertBundleBytes bounds how many bytes of a configured CA bundle
// loadCACertPool will read. Real CA bundles, even large ones layering many
// corporate intermediates, are a few KiB to low hundreds of KiB; this
// exists to bound memory and I/O if CACertPath is misconfigured to point
// at an oversized or pathological file, not to accommodate legitimate
// bundle growth.
const maxCACertBundleBytes = 1 << 20 // 1 MiB

// loadCACertPool starts from the system trust store (falling back to an
// empty pool if it is unavailable, matching crypto/x509's own documented
// behavior) and layers the configured PEM bundle on top, so the hosted API
// certificate can still be trusted normally while also trusting a private
// or corporate CA.
//
// The bounded read itself -- lstat-before-open type check, fstat-on-the-
// open-descriptor re-check, and the maxCACertBundleBytes size ceiling --
// is readBoundedRegularFile (boundedfile.go), shared with
// Config.Validate's own CA parity check and credential.go's token-file
// read, so there is exactly one such implementation in this package, not
// several that could silently diverge. The subsequent ownership check
// (verifyTrustedCABundleOwnership, boundedfile_ownership_unix.go) is the
// same shared implementation Config.Validate calls too, in the same
// order, so a CA bundle owned by another user or writable by group/world
// is rejected here exactly as it would be at `acr-mcp doctor` time. Any
// error is passed through describeFileError so the configured path itself
// is never echoed into an error an operator-facing surface (acr-mcp
// doctor) might print.
func loadCACertPool(path string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}

	pemBytes, info, err := readBoundedRegularFile(path, maxCACertBundleBytes)
	if err != nil {
		return nil, describeFileError(CACertPathEnvironment, err)
	}
	if err := verifyTrustedCABundleOwnership(info); err != nil {
		return nil, &ConfigError{Field: CACertPathEnvironment, Detail: err.Error()}
	}
	if ok := pool.AppendCertsFromPEM(pemBytes); !ok {
		return nil, fmt.Errorf("%s: no valid PEM certificates found", CACertPathEnvironment)
	}
	return pool, nil
}

// cloneURL returns a defensive, independent copy of u (nil in, nil out) so
// the returned *url.URL shares no mutable state with the caller's original.
// url.URL's only pointer field is User (*Userinfo); every other field is a
// value, so a shallow struct copy plus a deep copy of User is sufficient.
func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	clone := *u
	if u.User != nil {
		userCopy := *u.User
		clone.User = &userCopy
	}
	return &clone
}

var errEmptyEvidenceReferenceID = errors.New("acr: evidence reference id is required")

var errEmptyInvestigationResultID = errors.New("acr: investigation result id is required")
