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

const (
	investigationsPath      = "/api/v1/context-fabric/investigations"
	investigationPathPrefix = "/api/v1/context-fabric/investigations/"
	// contextFabricMCPSurface is the consumer surface this sidecar
	// declares on every investigation. It is a fixed constant, never a
	// caller-supplied value: surface identity is how the hosted side and
	// the API/MCP differential check tell the two surfaces apart, so a
	// tool argument that could set it would let one surface impersonate
	// the other and make a parity result meaningless.
	contextFabricMCPSurface = "mcp"
)

// Investigate asks the hosted Context Fabric a natural-language engineering
// question and returns the canonical investigation result (CHAOS-3746).
//
// The sidecar stamps SchemaVersion, a fresh RequestID, and its own consumer
// identity onto the outgoing request, overriding whatever the caller
// supplied for those fields -- the same rule ContextPacket follows, and for
// the same reason: request identity and consumer identity are transport
// metadata the client owns, not caller-chosen values. Consumer identity in
// particular must be truthful, because it is how the hosted side and any
// later differential check tell surfaces apart; letting a tool argument set
// it would let one surface impersonate another.
//
// This returns the FULL canonical result. Narrowing it for a bounded
// consumer is answerprojection.Project's job, never this client's: a
// transport that quietly summarised would be a second projection, which is
// exactly what CHAOS-3746 exists to prevent.
func (c *Client) Investigate(ctx context.Context, request contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
	requestID, err := newClientRequestID()
	if err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, err
	}
	request.SchemaVersion = contractsv1.ContextFabricInvestigationRequestSchema
	request.RequestID = requestID
	request.Consumer = contractsv1.ContextFabricConsumerInfo{
		Name:    c.cfg.ClientName,
		Version: c.cfg.ClientVersion,
		Surface: contextFabricMCPSurface,
	}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("invalid investigation request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, fmt.Errorf("encode investigation request: %w", err)
	}

	var result contractsv1.ContextFabricInvestigationResult
	if err := c.call(ctx, http.MethodPost, investigationsPath, encoded, &result); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, err
	}
	// Lenient, even though this is the "fresh answer" call: with CHAOS-3782
	// answer reuse the server may legitimately serve a STORED row here, so
	// a strict client gate would reject an answer the hosted side was right
	// to return (codex round-5 R5-1).
	if err := validateStoredInvestigationResult(result); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, err
	}
	return result, nil
}

// InvestigationResult re-reads one persisted investigation result by its
// opaque result_id.
//
// The ID is treated as an opaque handle -- matching the hosted API's own
// treatment -- and is always url.PathEscape-d into a single path segment
// before being appended to the fixed prefix, exactly as Evidence does. That
// is what stops an ID from introducing extra path segments, a query string,
// or a URL fragment and reaching a different endpoint than intended.
func (c *Client) InvestigationResult(ctx context.Context, resultID string) (contractsv1.ContextFabricInvestigationResult, error) {
	trimmed := strings.TrimSpace(resultID)
	if trimmed == "" {
		return contractsv1.ContextFabricInvestigationResult{}, errEmptyInvestigationResultID
	}
	subPath := investigationPathPrefix + url.PathEscape(trimmed)

	var result contractsv1.ContextFabricInvestigationResult
	if err := c.call(ctx, http.MethodGet, subPath, nil, &result); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, err
	}
	if err := validateStoredInvestigationResult(result); err != nil {
		return contractsv1.ContextFabricInvestigationResult{}, err
	}
	return result, nil
}

// validateStoredInvestigationResult rejects a hosted result that is not
// even readable under the historical bounds.
//
// BOTH client calls use this. The sidecar's validation is transport
// defense-in-depth, not the authoritative gate: the strict gates are the
// engine's own check on fresh model output and the store's check on Save.
// A client stricter than what the server legitimately serves rejects valid
// answers -- and with answer reuse, even the "fresh" POST can return an
// immutable row that predates a bound correction (codex round-5 R5-1).
func validateStoredInvestigationResult(result contractsv1.ContextFabricInvestigationResult) error {
	if err := contractsv1.ValidateStoredResult(result); err != nil {
		return fmt.Errorf("%w: investigation result: %w", ErrInvalidResponse, err)
	}
	return nil
}
