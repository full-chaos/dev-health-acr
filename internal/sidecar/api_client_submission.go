package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const submissionPathPrefix = "/api/v1/context-fabric/investigations/submissions/"

// InvestigateAsync asks the hosted API for an accept-first investigation
// (POST with Prefer: respond-async and X-ACR-Submission-ID): the hosted API
// admits the submission under its run lease and answers 202 with its status
// before the run. No wait budget is sent: no caller waits on this request.
// The request is stamped exactly as InvestigateWithSynthesisInput stamps it.
func (c *Client) InvestigateAsync(ctx context.Context, request contractsv1.ContextFabricInvestigationRequest, submissionID string) (contractsv1.ContextFabricSubmissionStatus, error) {
	requestID, err := newClientRequestID()
	if err != nil {
		return contractsv1.ContextFabricSubmissionStatus{}, err
	}
	if !contractsv1.ValidContextFabricClientCorrelationID(submissionID) {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("invalid submission id")
	}
	request.SchemaVersion = contractsv1.ContextFabricInvestigationRequestSchema
	request.RequestID = requestID
	request.Consumer = contractsv1.ContextFabricConsumerInfo{
		Name:    c.cfg.ClientName,
		Version: c.cfg.ClientVersion,
		Surface: contextFabricMCPSurface,
	}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("invalid investigation request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("encode investigation request: %w", err)
	}
	headers := http.Header{"X-Request-ID": []string{requestID}}
	headers.Set(contractsv1.ContextFabricPreferHeader, contractsv1.ContextFabricRespondAsyncPreference)
	headers.Set(contractsv1.ContextFabricSubmissionIDHeader, submissionID)
	var status contractsv1.ContextFabricSubmissionStatus
	code, err := c.callWithHeaders(ctx, http.MethodPost, investigationsPath, encoded, &status, headers)
	if err != nil {
		return contractsv1.ContextFabricSubmissionStatus{}, err
	}
	if code != http.StatusAccepted {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("%w: accept-first create answered %d, want 202", ErrInvalidResponse, code)
	}
	if err := status.Validate(); err != nil || status.SubmissionID != submissionID {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("%w: accept-first create answered an invalid submission status", ErrInvalidResponse)
	}
	return status, nil
}

// SubmissionStatus reads the caller's own submission record. The id is an
// opaque handle, path-escaped into one segment.
func (c *Client) SubmissionStatus(ctx context.Context, submissionID string) (contractsv1.ContextFabricSubmissionStatus, error) {
	trimmed := strings.TrimSpace(submissionID)
	if trimmed == "" {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("empty submission id")
	}
	var status contractsv1.ContextFabricSubmissionStatus
	if err := c.call(ctx, http.MethodGet, submissionPathPrefix+url.PathEscape(trimmed), nil, &status); err != nil {
		return contractsv1.ContextFabricSubmissionStatus{}, err
	}
	if err := status.Validate(); err != nil || status.SubmissionID != trimmed {
		return contractsv1.ContextFabricSubmissionStatus{}, fmt.Errorf("%w: invalid submission status", ErrInvalidResponse)
	}
	return status, nil
}

// CancelSubmission asks the hosted API to cancel the caller's own leased
// run. It returns nil for 202 (cancel recorded) and 204 (already final).
func (c *Client) CancelSubmission(ctx context.Context, submissionID string) error {
	trimmed := strings.TrimSpace(submissionID)
	if trimmed == "" {
		return fmt.Errorf("empty submission id")
	}
	_, err := c.callWithHeaders(ctx, http.MethodPost, submissionPathPrefix+url.PathEscape(trimmed)+"/cancel", nil, nil, nil)
	return err
}
