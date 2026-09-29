package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const dataRelationshipsPath = "/api/v1/context-fabric/data/relationships"

// ReadDirectRelationships posts one read_relationships request (CHAOS-7074) to
// the hosted direct data route and returns the response document verbatim.
//
// The body is forwarded as given: the hosted route owns validation, the
// cursor and the caller's authorization, and this client narrows and rewrites
// nothing, so a second reading of the request cannot drift from the hosted
// one. The response is returned raw for the same reason; the MCP tool
// republishes it.
func (c *Client) ReadDirectRelationships(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(request) {
		return nil, fmt.Errorf("invalid read_relationships request: not valid JSON")
	}
	var response json.RawMessage
	if _, err := c.callWithHeaders(ctx, http.MethodPost, dataRelationshipsPath, request, &response, nil); err != nil {
		return nil, err
	}
	return response, nil
}
