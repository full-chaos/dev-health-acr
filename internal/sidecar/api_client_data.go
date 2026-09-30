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

// Direct data routes (CHAOS-7072, design C.3, C.4, D.2). The three methods
// below are thin, model-free reads of the hosted API: they send the caller's
// own bearer (the per-caller Client's credential source, never a process
// identity), share the transport's limits (request size, timeout, response
// size, no redirects, sanitized errors), and return the hosted JSON
// UNCHANGED as json.RawMessage. Nothing is added to it, dropped from it or
// re-derived, so an MCP tool's structured content is byte for byte the
// API's answer. The response types live in internal/contextfabric/directread
// (which imports contracts/v1); the sidecar deliberately links neither them
// nor any engine, interpreter, synthesizer or embedding package.
const (
	dataCatalogPath    = "/api/v1/context-fabric/data/catalog"
	dataSubjectsPath   = "/api/v1/context-fabric/data/subjects"
	dataOperationsPath = "/api/v1/context-fabric/data/operations"
	dataGraphQLPath    = "/api/v1/context-fabric/data/graphql"

	// dataContractVersion is the contract family a data catalogue answer
	// must name.
	dataContractVersion = "acr-data.v1"
)

// DataCatalog reads the data catalogue for the caller's credential. sections
// filters it (empty is every section); each name is checked by the hosted API
// against its closed vocabulary.
func (c *Client) DataCatalog(ctx context.Context, sections []string) (json.RawMessage, error) {
	subPath := dataCatalogPath
	if len(sections) > 0 {
		subPath += "?sections=" + url.QueryEscape(strings.Join(sections, ","))
	}
	var body json.RawMessage
	if err := c.call(ctx, http.MethodGet, subPath, nil, &body); err != nil {
		return nil, err
	}
	if err := requireDataObject(body, "contract_version", "sections", "caller", "consistency", "untrusted_content"); err != nil {
		return nil, fmt.Errorf("%w: data catalog: %w", ErrInvalidResponse, err)
	}
	var version struct {
		ContractVersion string `json:"contract_version"`
	}
	if err := json.Unmarshal(body, &version); err != nil || version.ContractVersion != dataContractVersion {
		return nil, fmt.Errorf("%w: data catalog: unexpected contract version", ErrInvalidResponse)
	}
	return body, nil
}

// FindSubjects asks the hosted lookup for subjects by kind (list mode) or by
// exact name (name mode). Every subject in the answer passed the hosted
// subject gate for this credential.
func (c *Client) FindSubjects(ctx context.Context, request contractsv1.MCPFindSubjectsRequest) (json.RawMessage, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("invalid find_subjects request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode find_subjects request: %w", err)
	}
	var body json.RawMessage
	if err := c.call(ctx, http.MethodPost, dataSubjectsPath, encoded, &body); err != nil {
		return nil, err
	}
	if err := requireDataObject(body, "status", "subjects", "population", "page", "consistency", "request", "untrusted_content"); err != nil {
		return nil, fmt.Errorf("%w: find subjects: %w", ErrInvalidResponse, err)
	}
	return body, nil
}

// RunOperation runs one allowlisted operation through the hosted data route.
// The route needs the data:read scope: a credential without it is refused
// with insufficient_scope (a 403), which surfaces as ErrInsufficientScope.
// A policy refusal, an unavailable operation and an upstream error are all
// 200 answers whose call field says so; they are returned, not raised.
func (c *Client) RunOperation(ctx context.Context, request contractsv1.MCPRunOperationRequest) (json.RawMessage, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("invalid run_operation request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode run_operation request: %w", err)
	}
	var body json.RawMessage
	if err := c.call(ctx, http.MethodPost, dataOperationsPath, encoded, &body); err != nil {
		return nil, err
	}
	if err := requireDataObject(body, "call", "completeness", "operation", "source", "errors", "page", "consistency", "untrusted_content", "request"); err != nil {
		return nil, fmt.Errorf("%w: run operation: %w", ErrInvalidResponse, err)
	}
	return body, nil
}

// GraphQLQuery runs one validated free-form query through the hosted data
// route (CHAOS-7075). Same rules as RunOperation: data:read, and every
// typed result (refused, upstream_*) is a 200 answer returned as is.
func (c *Client) GraphQLQuery(ctx context.Context, request contractsv1.MCPGraphQLQueryRequest) (json.RawMessage, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("invalid graphql_query request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode graphql_query request: %w", err)
	}
	var body json.RawMessage
	if err := c.call(ctx, http.MethodPost, dataGraphQLPath, encoded, &body); err != nil {
		return nil, err
	}
	if err := requireDataObject(body, "call", "completeness", "source", "root_fields", "errors", "page", "consistency", "untrusted_content", "request"); err != nil {
		return nil, fmt.Errorf("%w: graphql query: %w", ErrInvalidResponse, err)
	}
	return body, nil
}

// requireDataObject checks that body is one JSON object carrying every named
// member with a non-null value. It reads member presence only; it never
// rewrites the body.
func requireDataObject(body json.RawMessage, members ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return fmt.Errorf("the answer is not a JSON object")
	}
	for _, member := range members {
		raw, present := object[member]
		if !present || isJSONNull(raw) {
			return fmt.Errorf("required member %q is missing or null", member)
		}
	}
	return nil
}
