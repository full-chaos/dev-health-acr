package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	deviceAuthorizationPath = "/api/v1/oauth/device_authorization"
	deviceTokenPath         = "/api/v1/oauth/token"
	credentialRotatePath    = "/api/v1/auth/credentials/self/rotate"
	credentialRevokePath    = "/api/v1/auth/credentials/self/revoke"
)

// LifecycleClient is the hardened unauthenticated portion of the ACR device
// flow. It deliberately only exposes fixed OAuth paths and never sends a
// credential on device authorization or token polling requests.
type LifecycleClient struct {
	http    *http.Client
	baseURL *url.URL
	cfg     Config
}

// DevicePollingError is the bounded RFC 8628 error returned by a device token
// poll. It carries no server-provided text, device code, or credential.
type DevicePollingError struct {
	Code contractsv1.OAuthDeviceErrorCode
}

func (e *DevicePollingError) Error() string {
	switch e.Code {
	case contractsv1.OAuthDeviceErrorAuthorizationPending:
		return "device authorization is pending"
	case contractsv1.OAuthDeviceErrorSlowDown:
		return "device authorization polling must slow down"
	case contractsv1.OAuthDeviceErrorAccessDenied:
		return "device authorization was denied"
	case contractsv1.OAuthDeviceErrorExpiredToken:
		return "device authorization expired"
	case contractsv1.OAuthDeviceErrorInvalidGrant:
		return "device authorization can no longer be redeemed"
	default:
		return "device authorization failed"
	}
}

// NewLifecycleClient builds the public, device-flow client from the same
// validated transport configuration as the bearer-authenticated sidecar.
func NewLifecycleClient(cfg Config) (*LifecycleClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sidecar configuration: %w", err)
	}
	cfg.APIBaseURL = cloneURL(cfg.APIBaseURL)
	cfg.ProxyURL = cloneURL(cfg.ProxyURL)
	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &LifecycleClient{
		http:    &http.Client{Transport: transport, CheckRedirect: refuseRedirect},
		baseURL: cfg.APIBaseURL,
		cfg:     cfg,
	}, nil
}

// StartDeviceAuthorization creates a server-side device grant. Hints are
// optional client preferences; server-side authorization remains authoritative.
func (c *LifecycleClient) StartDeviceAuthorization(
	ctx context.Context,
	organizationIDHint *string,
	repositoryHints *[]string,
) (contractsv1.DeviceAuthorizationResponse, error) {
	request := contractsv1.DeviceAuthorizationRequest{
		SchemaVersion:      contractsv1.DeviceAuthorizationRequestSchema,
		OrganizationIDHint: organizationIDHint,
		RepositoryHints:    repositoryHints,
	}
	response := contractsv1.DeviceAuthorizationResponse{}
	if err := c.callPublic(ctx, deviceAuthorizationPath, request, &response, false); err != nil {
		return contractsv1.DeviceAuthorizationResponse{}, fmt.Errorf("start device authorization: %w", err)
	}
	if err := response.Validate(); err != nil {
		return contractsv1.DeviceAuthorizationResponse{}, fmt.Errorf("validate device authorization response: %w", err)
	}
	return response, nil
}

// PollDeviceToken redeems a device code once the user has approved it.
func (c *LifecycleClient) PollDeviceToken(ctx context.Context, deviceCode string) (contractsv1.DeviceTokenResponse, error) {
	request := contractsv1.DeviceTokenRequest{
		SchemaVersion: contractsv1.DeviceTokenRequestSchema,
		GrantType:     contractsv1.DeviceCodeGrantType,
		DeviceCode:    deviceCode,
	}
	response := contractsv1.DeviceTokenResponse{}
	if err := c.callPublic(ctx, deviceTokenPath, request, &response, true); err != nil {
		return contractsv1.DeviceTokenResponse{}, fmt.Errorf("poll device token: %w", err)
	}
	if err := response.Validate(); err != nil {
		return contractsv1.DeviceTokenResponse{}, fmt.Errorf("validate device token response: %w", err)
	}
	if !auth.IsTokenShapeValid(response.AccessToken) {
		return contractsv1.DeviceTokenResponse{}, ErrCredentialShapeInvalid
	}
	return response, nil
}

// RotateOwnCredential replaces the configured bearer credential and returns a
// rollback receipt used only if local persistence fails.
func (c *Client) RotateOwnCredential(ctx context.Context) (contractsv1.CredentialRotateResponse, error) {
	request := contractsv1.CredentialRotateRequest{SchemaVersion: contractsv1.CredentialRotateRequestSchema}
	payload, err := json.Marshal(request)
	if err != nil {
		return contractsv1.CredentialRotateResponse{}, fmt.Errorf("encode credential rotation request: %w", err)
	}
	response := contractsv1.CredentialRotateResponse{}
	if err := c.call(ctx, http.MethodPost, credentialRotatePath, payload, &response); err != nil {
		return contractsv1.CredentialRotateResponse{}, fmt.Errorf("rotate current credential: %w", err)
	}
	if err := response.Validate(); err != nil {
		return contractsv1.CredentialRotateResponse{}, fmt.Errorf("validate credential rotation response: %w", err)
	}
	if !auth.IsTokenShapeValid(response.AccessToken) {
		return contractsv1.CredentialRotateResponse{}, ErrCredentialShapeInvalid
	}
	return response, nil
}

// RevokeOwnCredential revokes exactly the credential supplied by this client's
// credential source. Callers construct a short-lived client for rollback of a
// newly issued successor credential.
func (c *Client) RevokeOwnCredential(ctx context.Context) (contractsv1.CredentialRevokeResponse, error) {
	return c.revokeOwnCredential(ctx, nil)
}

func (c *Client) RollbackOwnCredential(ctx context.Context, receipt contractsv1.CredentialRotationReceipt) (contractsv1.CredentialRevokeResponse, error) {
	return c.revokeOwnCredential(ctx, &receipt)
}

func (c *Client) revokeOwnCredential(ctx context.Context, receipt *contractsv1.CredentialRotationReceipt) (contractsv1.CredentialRevokeResponse, error) {
	request := contractsv1.CredentialRevokeRequest{SchemaVersion: contractsv1.CredentialRevokeRequestSchema, RollbackReceipt: receipt}
	payload, err := json.Marshal(request)
	if err != nil {
		return contractsv1.CredentialRevokeResponse{}, fmt.Errorf("encode credential revocation request: %w", err)
	}
	response := contractsv1.CredentialRevokeResponse{}
	if err := c.call(ctx, http.MethodPost, credentialRevokePath, payload, &response); err != nil {
		return contractsv1.CredentialRevokeResponse{}, fmt.Errorf("revoke current credential: %w", err)
	}
	if err := response.Validate(); err != nil {
		return contractsv1.CredentialRevokeResponse{}, fmt.Errorf("validate credential revocation response: %w", err)
	}
	return response, nil
}

func (c *LifecycleClient) callPublic(ctx context.Context, path string, request any, response any, isTokenPoll bool) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode hosted API request: %w", err)
	}
	if int64(len(payload)) > c.cfg.MaxRequestBodyBytes {
		return fmt.Errorf("%w: %d bytes exceeds the configured limit of %d", ErrRequestTooLarge, len(payload), c.cfg.MaxRequestBodyBytes)
	}
	requestURL, err := c.buildURL(path)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callCtx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build hosted API request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.cfg.ClientVersion != "" {
		httpRequest.Header.Set("X-ACR-Client-Version", c.cfg.ClientVersion)
	}
	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("hosted API request failed: %w", err)
		}
		return newTransportUnavailableError()
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode >= http.StatusMultipleChoices && httpResponse.StatusCode < http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, 4096))
		return &APIError{HTTPStatus: httpResponse.StatusCode, Message: "hosted API returned an unexpected redirect, which the client does not follow", RequestID: sanitizeMessage(httpResponse.Header.Get("X-Request-ID")), sentinel: ErrUnexpectedRedirect}
	}
	data, truncated, err := readLimited(httpResponse.Body, c.cfg.MaxResponseBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("read hosted API response: %w", err)
		}
		return newTransportUnavailableError()
	}
	if truncated {
		return &APIError{HTTPStatus: httpResponse.StatusCode, Message: "hosted API response exceeded the configured size limit", RequestID: sanitizeMessage(httpResponse.Header.Get("X-Request-ID")), sentinel: ErrResponseTooLarge}
	}
	if httpResponse.StatusCode/100 != 2 {
		if isTokenPoll && httpResponse.StatusCode == http.StatusBadRequest {
			var deviceError contractsv1.OAuthDeviceErrorResponse
			if err := decodeExact(data, &deviceError); err == nil && requiredFieldsPresent(data, &deviceError) == nil && deviceError.Validate() == nil {
				return &DevicePollingError{Code: deviceError.Error}
			}
		}
		return decodeAPIError(httpResponse.StatusCode, httpResponse.Header.Get("X-Request-ID"), httpResponse.Header.Get("Retry-After"), data)
	}
	if err := decodeExact(data, response); err != nil {
		return fmt.Errorf("decode hosted API response: %w", err)
	}
	if err := requiredFieldsPresent(data, response); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidResponse, err)
	}
	return nil
}

func (c *LifecycleClient) buildURL(path string) (*url.URL, error) {
	base := strings.TrimRight(c.baseURL.String(), "/")
	parsed, err := url.Parse(base + path)
	if err != nil {
		return nil, fmt.Errorf("build hosted API request URL: %w", err)
	}
	if parsed.Scheme != c.baseURL.Scheme || parsed.Host != c.baseURL.Host {
		return nil, errors.New("hosted API request URL escaped the configured origin")
	}
	return parsed, nil
}
