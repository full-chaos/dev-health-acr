package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// read_relationships bounds, mirrored from the hosted contract (the hosted
// route is authoritative and re-validates every request).
const (
	readRelationshipsMaxTypes       = 12
	readRelationshipsMaxDepth       = 2
	readRelationshipsMaxLimit       = 100
	readRelationshipsMaxFieldLength = 256
	readRelationshipsMaxCursor      = 2048
)

var readRelationshipsTypes = []string{
	"BELONGS_TO_PROJECT", "BELONGS_TO_PULL_REQUEST", "BELONGS_TO_REPOSITORY", "BLOCKS",
	"CORRELATED_WITH_INCIDENT", "DOCUMENTED_BY", "DUPLICATES", "HAS_EPISODE",
	"OWNED_BY_TEAM", "PART_OF", "RELATED_TO", "RELATES_TO",
}

type readRelationshipsSubjectInput struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
}

type readRelationshipsInput struct {
	Subject   readRelationshipsSubjectInput `json:"subject"`
	Types     []string                      `json:"types"`
	Direction string                        `json:"direction"`
	Depth     int                           `json:"depth"`
	AsOf      string                        `json:"as_of"`
	Limit     int                           `json:"limit"`
	Cursor    string                        `json:"cursor"`
}

func (in readRelationshipsInput) validate() error {
	s := in.Subject
	if s.Kind == "" || s.CanonicalID == "" || len(s.Kind) > readRelationshipsMaxFieldLength || len(s.CanonicalID) > readRelationshipsMaxFieldLength {
		return fmt.Errorf("subject needs a kind and a canonical_id")
	}
	if len(in.Types) > readRelationshipsMaxTypes {
		return fmt.Errorf("types must hold at most %d items", readRelationshipsMaxTypes)
	}
	for _, t := range in.Types {
		if !slices.Contains(readRelationshipsTypes, t) {
			return fmt.Errorf("types holds a value outside the relationship vocabulary")
		}
	}
	switch in.Direction {
	case "", "out", "in", "both":
	default:
		return fmt.Errorf("direction must be out, in or both")
	}
	if in.Depth != 0 && (in.Depth < 1 || in.Depth > readRelationshipsMaxDepth) {
		return fmt.Errorf("depth must be 1 or 2")
	}
	if in.Limit != 0 && (in.Limit < 1 || in.Limit > readRelationshipsMaxLimit) {
		return fmt.Errorf("limit must be between 1 and %d", readRelationshipsMaxLimit)
	}
	if in.AsOf != "" {
		if _, err := time.Parse(time.RFC3339Nano, in.AsOf); err != nil {
			return fmt.Errorf("as_of must be an RFC 3339 instant")
		}
	}
	if len(in.Cursor) > readRelationshipsMaxCursor {
		return fmt.Errorf("cursor is too long")
	}
	return nil
}

// handleReadRelationships implements the read_relationships tool
// (CHAOS-7074): validate the request shape, forward it unchanged to the
// hosted direct data route with the caller's own credential, and return the
// hosted response document as the tool result. No model runs on this path
// and nothing is narrowed or re-summarised here: the hosted route owns
// subject and edge authorization, and pages the result by keyset cursor.
func handleReadRelationships(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolReadRelationships), nil
	}

	raw := rawArgs(req)
	var input readRelationshipsInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "read_relationships arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "read_relationships arguments failed schema validation: " + err.Error()}), nil
	}

	response, err := caller.client.ReadDirectRelationships(ctx, json.RawMessage(raw))
	if err != nil {
		return toolErrorResult(err), nil
	}
	// The hosted document carries no "tool" member (unlike read_facts), so
	// the envelope check is the contract version plus a status.
	var envelope struct {
		ContractVersion string `json:"contract_version"`
		Status          string `json:"status"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil || envelope.ContractVersion != "acr-data.v1" || envelope.Status == "" {
		return toolErrorResult(&classifiedError{category: "internal", message: "the hosted read_relationships response was not the expected document"}), nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, response); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the hosted read_relationships response was not valid JSON"}), nil
	}
	return buildToolResult(json.RawMessage(compact.Bytes()), compact.String())
}
