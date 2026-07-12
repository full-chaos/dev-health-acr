package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ContextPacket requests a bounded context packet for the given goal,
// repository, and scope. The sidecar always stamps SchemaVersion, a fresh
// RequestID, and this client's identity onto the outgoing request,
// overriding whatever the caller supplied for those fields, since the
// hosted API treats RequestID and Client identity as server/client
// transport metadata rather than caller-chosen values (the server
// overwrites RequestID itself; see internal/api/read_routes.go).
func (c *Client) ContextPacket(ctx context.Context, request contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error) {
	requestID, err := newClientRequestID()
	if err != nil {
		return contractsv1.ContextPacket{}, err
	}
	request.SchemaVersion = contractsv1.ContextPacketRequestSchema
	request.RequestID = requestID
	request.Client = contractsv1.ClientInfo{
		Name:           c.cfg.ClientName,
		Version:        c.cfg.ClientVersion,
		SidecarVersion: c.cfg.SidecarVersion,
	}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextPacket{}, fmt.Errorf("invalid context packet request: %w", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return contractsv1.ContextPacket{}, fmt.Errorf("encode context packet request: %w", err)
	}

	var packet contractsv1.ContextPacket
	if err := c.call(ctx, http.MethodPost, contextPacketsPath, encoded, &packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	if err := validateContextPacket(packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	return packet, nil
}
