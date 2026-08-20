package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

const (
	workloadTokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange"
	workloadSubjectTokenType       = "urn:ietf:params:oauth:token-type:jwt"
	maxSubjectTokenFileBytes       = 16 << 10 // 16 KiB; a k8s projected SA JWT is a few KiB at most.
	maxTokenExchangeResponseBytes  = 16 << 10
	// workloadRefreshMargin re-exchanges before the cached access token's
	// own expiry, so a caller is never handed a token that expires
	// mid-request.
	workloadRefreshMargin = 30 * time.Second
)

// WorkloadCredentialSourceOptions configures NewWorkloadCredentialSource.
type WorkloadCredentialSourceOptions struct {
	// HTTPClient performs the token-exchange request. Nil uses
	// http.DefaultClient.
	HTTPClient *http.Client
	// TokenEndpoint is the hosted ACR token endpoint (ACR_TOKEN_ENDPOINT),
	// e.g. https://acr-api.internal.example/api/v1/oauth/token. Required.
	TokenEndpoint *url.URL
	// SubjectTokenFile is the path to the k8s projected ServiceAccount JWT
	// (ACR_SUBJECT_TOKEN_FILE). Reread on every exchange, never cached, so
	// kubelet's in-place rotation of the projected file is always
	// honored. Required.
	SubjectTokenFile string
	Now              func() time.Time
}

// workloadCredentialSource implements CredentialSource by RFC 8693 token
// exchange: it caches the exchanged access token in memory until shortly
// before its own expiry, and re-exchanges (rereading the subject token
// file fresh each time) once the cache is stale. The whole
// check-then-refresh sequence runs under one mutex, so concurrent callers
// share a single in-flight exchange rather than each starting their own.
type workloadCredentialSource struct {
	http             *http.Client
	tokenEndpoint    *url.URL
	subjectTokenFile string
	now              func() time.Time

	mu        sync.Mutex
	cached    CredentialResult
	expiresAt time.Time
}

// NewWorkloadCredentialSource builds a CredentialSource for RFC 8693
// workload token exchange (CHAOS-4013). Pass the result to NewClient as
// its credentialSource argument.
func NewWorkloadCredentialSource(options WorkloadCredentialSourceOptions) (CredentialSource, error) {
	if options.TokenEndpoint == nil || options.TokenEndpoint.Host == "" {
		return nil, errors.New("acr: workload credential source requires a token endpoint")
	}
	// The k8s projected ServiceAccount JWT goes in this request's body --
	// it must never be sendable to a plaintext or unexpected origin, and a
	// redirect response must never be able to retarget where it lands.
	if options.TokenEndpoint.Scheme != "https" && !isLoopbackHost(options.TokenEndpoint.Hostname()) {
		return nil, errors.New("acr: workload token endpoint must use https (plain http is only allowed for a loopback origin)")
	}
	if strings.TrimSpace(options.SubjectTokenFile) == "" {
		return nil, errors.New("acr: workload credential source requires a subject token file")
	}
	client := options.HTTPClient
	if client == nil {
		// The default client refuses redirects -- the subject token in the
		// request body must never be forwarded to a retargeted host. A
		// caller-supplied HTTPClient is trusted as-is (never mutated here)
		// rather than second-guessed.
		client = &http.Client{CheckRedirect: refuseRedirect}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	source := &workloadCredentialSource{
		http: client, tokenEndpoint: options.TokenEndpoint, subjectTokenFile: options.SubjectTokenFile, now: now,
	}
	return source.load, nil
}

func (s *workloadCredentialSource) load() (CredentialResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if s.cached.Token != "" && now.Before(s.expiresAt) {
		return s.cached, nil
	}
	result, expiresIn, err := s.exchange()
	if err != nil {
		return CredentialResult{}, err
	}
	s.cached = result
	if expiresIn > workloadRefreshMargin {
		s.expiresAt = now.Add(expiresIn - workloadRefreshMargin)
	} else {
		// A token this short-lived leaves no safe margin; treat the cache
		// as immediately stale so the NEXT call re-exchanges rather than
		// risking a request in flight when it expires.
		s.expiresAt = now
	}
	return result, nil
}

func (s *workloadCredentialSource) exchange() (CredentialResult, time.Duration, error) {
	subjectTokenBytes, _, err := readBoundedRegularFile(s.subjectTokenFile, maxSubjectTokenFileBytes)
	if err != nil {
		return CredentialResult{}, 0, fmt.Errorf("acr: read subject token file: %w", describeBoundedFileError(err))
	}
	subjectToken := strings.TrimSpace(string(subjectTokenBytes))
	if subjectToken == "" {
		return CredentialResult{}, 0, errors.New("acr: subject token file is empty")
	}
	form := url.Values{}
	form.Set("grant_type", workloadTokenExchangeGrantType)
	form.Set("subject_token", subjectToken)
	form.Set("subject_token_type", workloadSubjectTokenType)
	request, err := http.NewRequest(http.MethodPost, s.tokenEndpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return CredentialResult{}, 0, fmt.Errorf("acr: build token exchange request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.http.Do(request)
	if err != nil {
		return CredentialResult{}, 0, fmt.Errorf("acr: token exchange request failed: %w", err)
	}
	defer response.Body.Close()
	// This call happens before a Client exists (it resolves the bearer
	// credential a Client will use), so it cannot go through the
	// configured MaxResponseBytes ceiling readLimited applies everywhere
	// else in this package -- it uses its own small, fixed
	// maxTokenExchangeResponseBytes bound instead, the same "audited,
	// negligible, fixed bound instead of the operator ceiling" pattern
	// redirectDrainBytes already establishes (see
	// auditedBoundedBodyReads in response_ceiling_test.go). A real token
	// exchange response is well under 1 KiB.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenExchangeResponseBytes))
	if err != nil {
		return CredentialResult{}, 0, fmt.Errorf("acr: read token exchange response: %w", err)
	}
	if len(body) >= maxTokenExchangeResponseBytes {
		return CredentialResult{}, 0, errors.New("acr: token exchange response too large")
	}
	if response.StatusCode != http.StatusOK {
		return CredentialResult{}, 0, fmt.Errorf("acr: token exchange failed with status %d", response.StatusCode)
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return CredentialResult{}, 0, fmt.Errorf("acr: decode token exchange response: %w", err)
	}
	if !auth.IsTokenShapeValid(decoded.AccessToken) {
		return CredentialResult{}, 0, ErrCredentialShapeInvalid
	}
	if decoded.ExpiresIn <= 0 {
		return CredentialResult{}, 0, errors.New("acr: token exchange response has no positive expires_in")
	}
	return CredentialResult{Token: decoded.AccessToken, Source: "workload_token_exchange"}, time.Duration(decoded.ExpiresIn) * time.Second, nil
}

// describeBoundedFileError narrows a readBoundedRegularFile failure to a
// fixed, value-free description -- never echoing the configured path,
// matching describeFileError's contract elsewhere in this package (see
// config.go/api_client.go's own CA-bundle reads).
func describeBoundedFileError(err error) error {
	if errors.Is(err, ErrBoundedFileReadsUnsupported) {
		return ErrBoundedFileReadsUnsupported
	}
	return errors.New("subject token file could not be read")
}
