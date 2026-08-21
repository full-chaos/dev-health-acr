package panelharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// investigationsPath mirrors internal/sidecar's own fixed-path constant
// exactly (internal/sidecar/api_client.go / api_client_investigation.go) --
// this package deliberately does NOT import internal/sidecar.Client and call
// it directly, even though that client already POSTs this exact endpoint:
// sidecar.Client.Investigate hard-codes Consumer.Surface = "mcp" on every
// call (api_client_investigation.go), and this repo is explicit, repeatedly,
// that Consumer.Surface identity must be TRUTHFUL because it is how the
// hosted side and any later differential check tell surfaces apart
// (api_client_investigation.go's own doc comment: "letting a tool argument
// set it would let one surface impersonate another"). A P6 panelist call is
// neither MCP nor workbench nor a human panel -- it is its own new, honest
// surface (contextFabricConsumerSurface below), so reusing sidecar.Client
// would mean spoofing "mcp" on every request this harness makes. This
// package is a separate, honestly-identified client instead.
const investigationsPath = "/api/v1/context-fabric/investigations"

// contextFabricConsumerSurface is this harness's own, honest Consumer.Surface
// value (internal/contractsv1.ContextFabricConsumerInfo.Surface is a free
// string, not a closed enum -- internal/contextfabric/clarification_capture.go's
// clarificationSelectionProvenance maps anything other than "mcp"/"workbench"
// to the credential_other provenance bucket, which is exactly the honest
// answer for this surface).
const contextFabricConsumerSurface = "context_fabric_panel_harness"

// consumerName/consumerVersion identify this harness on every request, the
// same way every other hosted-API caller in this repo stamps its own
// consumer identity (sidecar's ClientName/ClientVersion, the workbench's
// "context-fabric-workbench" seen in contracts/examples/v1's own golden
// fixture).
const (
	consumerName    = "acr-panel-harness"
	consumerVersion = "0.1.0"
)

// ErrUnexpectedStatus is returned when the hosted API responds with a
// non-2xx status this client does not specially classify.
var ErrUnexpectedStatus = errors.New("panelharness: unexpected hosted API status")

// maxResponseBytes bounds how much of a single investigation response body
// this client will read -- the same defense-in-depth every other hosted-API
// caller in this repo applies (a misbehaving or malicious upstream cannot
// force unbounded memory growth here), sized generously above the hosted
// side's own MaxSerializedBytes ceiling (contracts/examples/v1's own golden
// request sets max_serialized_bytes: 262144) so a legitimate large answer is
// never truncated.
const maxResponseBytes = 8 << 20 // 8 MiB

// Client is a minimal, hardened HTTP client for the hosted Context Fabric
// investigation endpoint, built for THIS harness's own honest consumer
// identity (see contextFabricConsumerSurface's doc comment for why it is
// not internal/sidecar.Client). One Client is bound to one bearer
// credential -- construct one per panelist, mirroring
// internal/sidecar.Client's own per-credential-source design.
type Client struct {
	http        *http.Client
	baseURL     *url.URL
	bearerToken string
	// tokenFingerprint is a SHA-256 hex digest of bearerToken, computed
	// once here -- NEVER the token itself -- so a caller (Run's own
	// duplicate-credential check, codex round 1 HIGH) can detect two
	// panelists sharing the same underlying credential without needing
	// raw access to it.
	tokenFingerprint string
}

// TokenFingerprint returns a SHA-256 hex digest of this Client's own
// bearer token. It exists ONLY for equality comparison (Run's
// duplicate-credential guard) -- it is a one-way digest, never reversible
// to the token, and is safe to log or compare across panelists.
func (c *Client) TokenFingerprint() string {
	return c.tokenFingerprint
}

// NewClient builds a Client against baseURL, authenticating every call with
// bearerToken. baseURL must be an absolute http(s) URL; https is required
// unless the host is a loopback address (127.0.0.1/localhost/::1), matching
// internal/sidecar's own local-development carve-out
// (internal/sidecar/api_client.go's isLoopbackHost check) -- this harness is
// meant to run against a real deployment's hosted API, and a real
// deployment is never plain HTTP over a non-loopback host.
func NewClient(baseURL, bearerToken string, timeout time.Duration) (*Client, error) {
	trimmedToken := strings.TrimSpace(bearerToken)
	if trimmedToken == "" {
		return nil, errors.New("panelharness: bearer token is required")
	}
	// codex adversarial review (round 1, HIGH): a caller could point
	// bearer_token_env at ANY nonblank environment value -- a license key,
	// an unrelated secret -- and this client would happily transmit it as
	// an ACR bearer credential. auth.IsTokenShapeValid is the SAME shape
	// gate internal/auth/middleware.go itself enforces server-side (and
	// scripts/e2e/compose.sh's own `[[ "$token" == fcacr_* ]]` sanity
	// check after minting one); failing closed on an obviously-wrong shape
	// here, client-side, is a cheap, precedented defense that costs
	// nothing for a genuine credential minted by `acr-api credentials
	// create`.
	if !auth.IsTokenShapeValid(trimmedToken) {
		return nil, errors.New("panelharness: bearer token does not have the expected ACR credential shape (mint one with `acr-api credentials create`, never a license key or unrelated secret)")
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("panelharness: invalid base URL %q", baseURL)
	}
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return nil, fmt.Errorf("panelharness: base URL %q must use https (loopback hosts may use http for local development)", baseURL)
	}
	// codex round 1 (MEDIUM): unlike internal/sidecar/config_parse.go's own
	// APIBaseURL validation, nothing here rejected a path/query/fragment/
	// userinfo component -- a misconfigured base URL could carry a secret
	// in a query string to every request, or silently redirect every call
	// under an unexpected path prefix. The base URL names an ORIGIN only;
	// investigationsPath is the one fixed path this client ever appends.
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("panelharness: base URL %q must be a bare origin (no path, query, fragment, or userinfo)", baseURL)
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// Never follow a redirect: the bearer token must only ever
				// be sent to the configured origin, mirroring
				// internal/sidecar.Client's own refuseRedirect exactly.
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		},
		baseURL:          parsed,
		bearerToken:      trimmedToken,
		tokenFingerprint: tokenFingerprint(trimmedToken),
	}, nil
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

// Investigate POSTs one investigation request and returns the canonical
// result. It stamps SchemaVersion and Consumer (name/version/surface) onto
// request itself, the same way internal/sidecar.Client.Investigate does for
// its own surface -- request identity and consumer identity are transport
// metadata this client owns, not caller-chosen values, for the identical
// reason that comment gives.
//
// requestID is caller-supplied (not minted here) because a two-turn caller
// (run.go) needs the SAME request correlating both turns of one panelist's
// drive, and because this package has no server-side request-id middleware
// to lean on the way the in-process engine does.
func (c *Client) Investigate(ctx context.Context, requestID string, request contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
	request.SchemaVersion = contractsv1.ContextFabricInvestigationRequestSchema
	request.RequestID = requestID
	request.Consumer = contractsv1.ContextFabricConsumerInfo{Name: consumerName, Version: consumerVersion, Surface: contextFabricConsumerSurface}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: invalid investigation request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: encode investigation request: %w", err)
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + investigationsPath
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: build request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.bearerToken)

	response, err := c.http.Do(httpRequest)
	if err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: investigation request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: read investigation response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: investigation response exceeded %d bytes", maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// codex round 1 (MEDIUM): the raw response body is untrusted
		// upstream content that could reflect request data (including,
		// pathologically, the Authorization header a misconfigured or
		// compromised endpoint echoes back) or carry control characters
		// into a terminal. AGENTS.md's own ANTI-PATTERNS list is explicit:
		// "Never log ... raw request bodies" -- this repo's standing rule
		// extends the same discipline to a raw upstream response body
		// reaching an error a CLI then prints. Only the status code and a
		// byte length ever reach the returned error now, mirroring the
		// SAME "class + length, never content" idiom fileexchange.go
		// already applies to a responder's own error text.
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("%w: status %d (%d response bytes)", ErrUnexpectedStatus, response.StatusCode, len(body))
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(body, &result); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: decode investigation response: %w", err)
	}
	// codex round 1 (MEDIUM): a permissive json.Unmarshal alone accepts a
	// well-formed-but-empty `{}` as a "successful" decisive result.
	// internal/sidecar.Client.Investigate calls this SAME method
	// (validateStoredInvestigationResult, unexported to that package) after
	// every decode; this client applies the identical gate rather than
	// inventing a second one.
	if err := contractsv1.ValidateStoredResult(result); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("panelharness: hosted API returned an invalid investigation result: %w", err)
	}
	return result, nil
}
