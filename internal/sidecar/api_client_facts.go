package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const dataFactsPath = "/api/v1/context-fabric/data/facts"

// ReadDirectFacts posts one read_facts request (CHAOS-7073) to the hosted direct
// data route and returns the response document verbatim.
//
// The body is forwarded as given: the hosted route owns validation and the
// caller's authorization, and this client narrows and rewrites nothing, so a
// second reading of the request cannot drift from the hosted one. The
// response is returned raw for the same reason; the MCP tool republishes it.
func (c *Client) ReadDirectFacts(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(request) {
		return nil, fmt.Errorf("invalid read_facts request: not valid JSON")
	}
	var response json.RawMessage
	if _, err := c.callWithHeaders(ctx, http.MethodPost, dataFactsPath, request, &response, nil); err != nil {
		return nil, err
	}
	return response, nil
}
