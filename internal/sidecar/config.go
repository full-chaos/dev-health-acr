package sidecar

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

// Environment variables read by LoadConfig. All are optional except
// APIURLEnvironment.
const (
	APIURLEnvironment                    = "ACR_API_URL"
	TimeoutEnvironment                   = "ACR_API_TIMEOUT"
	MaxResponseBytesEnvironment          = "ACR_API_MAX_RESPONSE_BYTES"
	MaxRequestBodyBytesEnvironment       = "ACR_API_MAX_REQUEST_BODY_BYTES"
	ProxyURLEnvironment                  = "ACR_API_PROXY_URL"
	CACertPathEnvironment                = "ACR_API_CA_BUNDLE"
	AllowInsecureLoopbackEnvironment     = "ACR_API_ALLOW_INSECURE_LOOPBACK"
	AllowInsecureInternalHTTPEnvironment = "ACR_API_ALLOW_INSECURE_INTERNAL_HTTP"
	EnableWritebackEnvironment           = "ACR_ENABLE_WRITEBACK"
	EnableTranscriptCaptureEnvironment   = "ACR_ENABLE_TRANSCRIPT_CAPTURE"
	ClientNameEnvironment                = "ACR_SIDECAR_CLIENT_NAME"
	ClientVersionEnvironment             = "ACR_SIDECAR_CLIENT_VERSION"
	SidecarVersionEnvironment            = "ACR_SIDECAR_VERSION"
	LogLevelEnvironment                  = "ACR_LOG_LEVEL"
	// SubjectTokenFileEnvironment and TokenEndpointEnvironment configure
	// RFC 8693 workload token exchange (CHAOS-4013) -- see
	// NewWorkloadCredentialSource. Deliberately separate from
	// TokenFileEnvironment (ACR_API_TOKEN_FILE): that loader accepts only
	// an already-minted fcacr_ token, never a k8s projected ServiceAccount
	// JWT, so overloading it would silently break both readers instead of
	// cleanly supporting neither.
	SubjectTokenFileEnvironment = "ACR_SUBJECT_TOKEN_FILE"
	TokenEndpointEnvironment    = "ACR_TOKEN_ENDPOINT"
)

const (
	defaultTimeout             = 20 * time.Second
	defaultMaxResponseBytes    = 1 << 20   // 1 MiB; hosted read responses are bounded server-side well under this.
	defaultMaxRequestBodyBytes = 256 << 10 // 256 KiB; only outgoing context-packet requests carry a body.
	defaultClientName          = "dev-health-acr-mcp"
	defaultClientVersion       = "dev"
	defaultSidecarVersion      = "dev"
	defaultLogLevel            = slog.LevelInfo

	minTimeout = 1 * time.Second
	maxTimeout = 2 * time.Minute
	// redirectDrainBytes bounds the ONLY hosted response body this client
	// reads outside the configured ceiling: the body of an unexpected
	// redirect, which is discarded before the request is failed.
	//
	// The drain is BEST-EFFORT connection reuse, and the distinction is
	// load-bearing rather than pedantic. A redirect body within this bound
	// is read to EOF, so net/http can keep the connection; a larger one is
	// deliberately left unread, which costs the connection and nothing
	// else. That trade is the point -- reading an unbounded body to save a
	// socket would hand a hostile or broken server exactly the unmetered
	// read the ceiling exists to prevent.
	//
	// Losing the connection has no correctness consequence here: the body
	// is discarded, an ErrUnexpectedRedirect is returned, and no state
	// crosses the boundary. Only a reconnect is paid.
	//
	// Fixed rather than operator-tunable because nothing is kept, and
	// deliberately small: the audit in response_ceiling_test.go accepts
	// this exemption only while the bound stays negligible.
	redirectDrainBytes      = 4096
	minResponseBytes        = 8 << 10 // 8 KiB
	maxResponseBytesCeil    = 8 << 20 // 8 MiB
	minRequestBodyBytes     = 1 << 10 // 1 KiB
	maxRequestBodyBytesCeil = 4 << 20 // 4 MiB
)

// ConfigError is the typed error every configuration parsing and
// validation failure in this file (and describeFileError in
// boundedfile.go, which config.go's CA-bundle check shares with
// api_client.go) returns. Its Error() text is always safe to surface
// verbatim on an operator-facing diagnostic surface (notably `acr-mcp
// doctor`'s JSON output): it names the offending environment variable
// and a fixed, value-free description of what is wrong, and never
// echoes the raw configured value, a URL, userinfo, a bearer-shaped
// token, or a filesystem path -- even when the underlying
// strconv/time/url parser's own error text would have included it.
type ConfigError struct {
	// Field is the environment variable name the error concerns (see the
	// *Environment constants above).
	Field string
	// Detail is a fixed, value-free description of what is wrong and, for
	// bounded values, the expected format or range. It must never contain
	// any part of the raw configured value.
	Detail string
}

func (e *ConfigError) Error() string {
	return e.Field + ": " + e.Detail
}

// DescribeConfigError returns a value-free description of a LoadConfig
// or Config.Validate failure, safe to surface verbatim on an operator-
// facing diagnostic (acr-mcp doctor's JSON output). Every error this
// package's config parsing/validation returns is a *ConfigError, whose
// Error() text is already guaranteed safe -- but this function still
// narrows to that concrete type via errors.As rather than calling
// err.Error() directly, so a future config-parsing code path that
// forgets to build a *ConfigError -- and instead wraps a raw
// strconv/time/url parser error the way this file's duration/int64/bool
// parsers used to -- degrades to a fixed, generic description instead
// of leaking whatever that error's text happens to contain.
func DescribeConfigError(err error) string {
	if err == nil {
		return ""
	}
	var configErr *ConfigError
	if errors.As(err, &configErr) {
		return configErr.Error()
	}
	return "configuration is invalid (unclassified error)"
}

// proxyURLInvalidDetail is shared by loadConfig's initial proxy URL
// parse and Config.Validate's redundant re-check, so both failure
// points report identical, actionable guidance without echoing the raw
// configured value.
const proxyURLInvalidDetail = "must be a valid absolute URL with a host (e.g. \"http://proxy.example.com:3128\")"

// Config is the typed sidecar configuration for talking to the hosted ACR
// read API. It intentionally excludes the bearer credential itself; see
// LoadCredential for the separate, precedence-ordered credential seam.
type Config struct {
	// APIBaseURL is the hosted API origin. Fixed sub-paths are joined onto
	// it by the API client; callers never supply arbitrary paths.
	APIBaseURL *url.URL
	// Timeout bounds a single hosted API call end to end (dial, TLS,
	// request, response). It is applied via context.WithTimeout around the
	// caller-supplied context, so an already-shorter caller deadline wins.
	Timeout time.Duration
	// MaxResponseBytes bounds how many bytes of a hosted response body the
	// client will read before treating the response as too large.
	MaxResponseBytes int64
	// MaxRequestBodyBytes bounds the serialized size of outgoing request
	// bodies sent to the hosted API.
	MaxRequestBodyBytes int64
	// EnableWriteback permits write-capable sidecar operations. It defaults to
	// false and is only enabled by the exact environment value "true".
	EnableWriteback         bool
	EnableTranscriptCapture bool
	// ProxyURL, when set, is used for all hosted API requests instead of
	// the standard HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment resolution.
	ProxyURL *url.URL
	// CACertPath, when set, is an additional PEM CA bundle trusted for the
	// hosted API TLS connection, layered on top of the system trust store.
	CACertPath string
	// AllowInsecureLoopback opts into plain HTTP, and only for a loopback
	// host (127.0.0.1, ::1, or localhost). It exists solely for local
	// httptest-style fixture drivers and must never be set against a
	// non-loopback host; LoadConfig enforces that pairing.
	AllowInsecureLoopback bool
	// AllowInsecureInternalHTTP opts into plain HTTP for a NON-loopback
	// host too (CHAOS-4013): a cluster-internal Service DNS name such as
	// http://acr-api.<namespace>.svc:8080, where TLS termination happens
	// only at a gateway if the endpoint is ever exposed externally.
	// Distinct from AllowInsecureLoopback -- see that field's own doc
	// comment -- and, unlike it, has no implicit host restriction beyond
	// "not https", so operators must only set this within a trusted
	// cluster network boundary.
	AllowInsecureInternalHTTP bool
	// ClientName, ClientVersion, and SidecarVersion identify this sidecar
	// to the hosted API (client info payload, X-ACR-Client-Version header).
	ClientName     string
	ClientVersion  string
	SidecarVersion string
	// LogLevel controls the sidecar's structured diagnostic verbosity (see
	// internal/mcp.Serve). ACR_LOG_LEVEL accepts "debug", "info", "warn", or
	// "error" (case-insensitive); default is "info". LogLevel never gates
	// what gets redacted -- secrets and bodies are never logged regardless
	// of level -- only how much non-secret operational detail is emitted.
	LogLevel slog.Level
}

type lookupEnv func(string) (string, bool)

// LoadConfig reads sidecar configuration from the process environment and
// validates it.
func LoadConfig() (Config, error) {
	return loadConfig(os.LookupEnv)
}

func loadConfig(lookup lookupEnv) (Config, error) {
	rawURL, ok := lookup(APIURLEnvironment)
	rawURL = strings.TrimSpace(rawURL)
	if !ok || rawURL == "" {
		return Config{}, &ConfigError{Field: APIURLEnvironment, Detail: "is required"}
	}
	baseURL, err := url.Parse(rawURL)
	if err != nil {
		// url.Parse's own error text embeds the full raw input verbatim
		// (e.g. an invalid percent-escape inside a malformed userinfo
		// component still echoes the whole "scheme://user:password@host"
		// string). That text must never reach an operator-facing surface
		// such as acr-mcp doctor's JSON output, so this branch reports a
		// fixed, path-free description instead of wrapping err.
		return Config{}, &ConfigError{Field: APIURLEnvironment, Detail: "is invalid: malformed URL"}
	}

	cfg := Config{
		APIBaseURL:     baseURL,
		ClientName:     stringOrDefault(lookup, ClientNameEnvironment, defaultClientName),
		ClientVersion:  stringOrDefault(lookup, ClientVersionEnvironment, defaultClientVersion),
		SidecarVersion: stringOrDefault(lookup, SidecarVersionEnvironment, defaultSidecarVersion),
		CACertPath:     strings.TrimSpace(firstOrEmpty(lookup, CACertPathEnvironment)),
	}
	if cfg.LogLevel, err = logLevelOrDefault(lookup, LogLevelEnvironment, defaultLogLevel); err != nil {
		return Config{}, err
	}

	if cfg.Timeout, err = durationOrDefault(lookup, TimeoutEnvironment, defaultTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxResponseBytes, err = int64OrDefault(lookup, MaxResponseBytesEnvironment, defaultMaxResponseBytes); err != nil {
		return Config{}, err
	}
	if cfg.MaxRequestBodyBytes, err = int64OrDefault(lookup, MaxRequestBodyBytesEnvironment, defaultMaxRequestBodyBytes); err != nil {
		return Config{}, err
	}
	if cfg.AllowInsecureLoopback, err = boolOrDefault(lookup, AllowInsecureLoopbackEnvironment, false); err != nil {
		return Config{}, err
	}
	if cfg.AllowInsecureInternalHTTP, err = boolOrDefault(lookup, AllowInsecureInternalHTTPEnvironment, false); err != nil {
		return Config{}, err
	}
	if cfg.EnableWriteback, err = strictBoolOrDefault(lookup, EnableWritebackEnvironment, false); err != nil {
		return Config{}, err
	}
	if cfg.EnableTranscriptCapture, err = strictBoolOrDefault(lookup, EnableTranscriptCaptureEnvironment, false); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(firstOrEmpty(lookup, ProxyURLEnvironment)); raw != "" {
		proxyURL, err := url.Parse(raw)
		if err != nil || proxyURL.Host == "" {
			return Config{}, &ConfigError{Field: ProxyURLEnvironment, Detail: proxyURLInvalidDetail}
		}
		cfg.ProxyURL = proxyURL
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks structural and security-relevant invariants. When
// CACertPath is set it performs a bounded local file read and a PEM
// validity check (readBoundedRegularFile, shared with loadCACertPool in
// api_client.go) so this method -- which acr-mcp doctor calls via
// LoadConfig without ever constructing a Client -- has the same fail-
// closed behavior NewClient will have, without any network I/O.
func (c Config) Validate() error {
	if c.APIBaseURL == nil || c.APIBaseURL.Host == "" {
		return &ConfigError{Field: APIURLEnvironment, Detail: "base URL is required"}
	}
	if err := validateOriginOnly(c.APIBaseURL); err != nil {
		return err
	}
	if err := validateScheme(c.APIBaseURL, c.AllowInsecureInternalHTTP); err != nil {
		return err
	}
	if c.Timeout < minTimeout || c.Timeout > maxTimeout {
		return &ConfigError{Field: TimeoutEnvironment, Detail: fmt.Sprintf("must be between %s and %s", minTimeout, maxTimeout)}
	}
	if c.MaxResponseBytes < minResponseBytes || c.MaxResponseBytes > maxResponseBytesCeil {
		return &ConfigError{Field: MaxResponseBytesEnvironment, Detail: fmt.Sprintf("must be between %d and %d bytes", minResponseBytes, maxResponseBytesCeil)}
	}
	if c.MaxRequestBodyBytes < minRequestBodyBytes || c.MaxRequestBodyBytes > maxRequestBodyBytesCeil {
		return &ConfigError{Field: MaxRequestBodyBytesEnvironment, Detail: fmt.Sprintf("must be between %d and %d bytes", minRequestBodyBytes, maxRequestBodyBytesCeil)}
	}
	if c.ProxyURL != nil && c.ProxyURL.Host == "" {
		return &ConfigError{Field: ProxyURLEnvironment, Detail: proxyURLInvalidDetail}
	}
	if c.ProxyURL != nil && c.APIBaseURL.Scheme == "http" && isLoopbackHost(c.APIBaseURL.Hostname()) {
		return &ConfigError{Field: ProxyURLEnvironment, Detail: "must not be configured for an insecure loopback API URL"}
	}
	if c.ProxyURL != nil && c.APIBaseURL.Scheme == "http" && c.AllowInsecureInternalHTTP {
		return &ConfigError{Field: ProxyURLEnvironment, Detail: "must not be configured for an insecure internal-HTTP API URL"}
	}
	if strings.TrimSpace(c.CACertPath) != "" {
		// Parity check with the authoritative load path (loadCACertPool,
		// api_client.go): the exact same bounded lstat+open+fstat+size-
		// bounded read (readBoundedRegularFile, boundedfile.go), the same
		// trusted-ownership/no-group-or-world-write check
		// (verifyTrustedCABundleOwnership, boundedfile_ownership_unix.go),
		// and the same AppendCertsFromPEM validity check, in the same
		// order, so a CA bundle path that is the wrong type, oversized,
		// untrusted, or not valid PEM is rejected here -- not just when a
		// Client is later constructed. Both call sites share the one
		// bounded-read implementation and the one ownership check, so
		// there is no second, potentially divergent implementation of
		// either. Any error is passed through describeFileError or built
		// as a fixed-detail ConfigError so the configured path itself is
		// never echoed into this method's return value, which doctor
		// prints verbatim.
		pemBytes, info, err := readBoundedRegularFile(c.CACertPath, maxCACertBundleBytes)
		if err != nil {
			return describeFileError(CACertPathEnvironment, err)
		}
		if err := verifyTrustedCABundleOwnership(info); err != nil {
			return &ConfigError{Field: CACertPathEnvironment, Detail: err.Error()}
		}
		if ok := x509.NewCertPool().AppendCertsFromPEM(pemBytes); !ok {
			return &ConfigError{Field: CACertPathEnvironment, Detail: "no valid PEM certificates found"}
		}
	}
	return nil
}
