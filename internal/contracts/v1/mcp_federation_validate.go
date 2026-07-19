package v1

import (
	"encoding/json"
	"fmt"
)

const (
	mcpLocalContextMaxItems        = 50
	mcpLocalContextMaxEvidenceRefs = 100
	mcpLocalContextMaxWarnings     = 100
)

func (c MCPLocalContext) Validate() error {
	if !stringLengthBetween(c.Provider, 1, 100) || !stringLengthBetween(c.ProviderVersion, 1, 200) || !stringLengthBetween(c.QueryVersion, 1, 200) {
		return fmt.Errorf("local context provider or version violates v1 bounds")
	}
	if !validMCPLocalContextStatus(c.Status) || !validMCPLocalFreshness(c.Freshness) {
		return fmt.Errorf("local context status or freshness violates v1 bounds")
	}
	if !stringLengthBetween(c.IndexedRef, 0, 512) || (c.IndexedCommit != "" && !commitSHAPattern.MatchString(c.IndexedCommit)) {
		return fmt.Errorf("local context index reference violates v1 bounds")
	}
	if c.Warnings == nil || len(c.Warnings) > mcpLocalContextMaxWarnings {
		return fmt.Errorf("local context warnings violates v1 bounds")
	}
	for _, warning := range c.Warnings {
		if !stringLengthBetween(warning, 0, 2000) {
			return fmt.Errorf("local context warnings violates v1 bounds")
		}
	}
	if c.Items == nil || len(c.Items) > mcpLocalContextMaxItems {
		return fmt.Errorf("local context items violates v1 bounds")
	}
	for index, item := range c.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("local context items[%d]: %w", index, err)
		}
	}
	if c.EvidenceRefs == nil || len(c.EvidenceRefs) > mcpLocalContextMaxEvidenceRefs {
		return fmt.Errorf("local context evidence_refs violates v1 bounds")
	}
	for index, evidence := range c.EvidenceRefs {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("local context evidence_refs[%d]: %w", index, err)
		}
		if err := validateMCPLocalMetadata(evidence.Metadata); err != nil {
			return fmt.Errorf("local context evidence_refs[%d] metadata: %w", index, err)
		}
	}
	return nil
}

func (c MCPLocalContext) packetContentSerializedBytes() (int, error) {
	encoded, err := json.Marshal(struct {
		Items        []ContextPacketItem `json:"items"`
		EvidenceRefs []EvidenceRef       `json:"evidence_refs"`
	}{Items: c.Items, EvidenceRefs: c.EvidenceRefs})
	if err != nil {
		return 0, fmt.Errorf("marshal local packet content: %w", err)
	}
	return len(encoded), nil
}

func validateMCPLocalMetadata(value any) error {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "codegraph_payload" {
				return fmt.Errorf("provider payload metadata is not allowed")
			}
			if err := validateMCPLocalMetadata(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := validateMCPLocalMetadata(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func validMCPLocalContextStatus(status MCPLocalContextStatus) bool {
	switch status {
	case MCPLocalContextAvailable, MCPLocalContextDegraded, MCPLocalContextUnavailable:
		return true
	default:
		return false
	}
}

func validMCPLocalFreshness(freshness MCPLocalFreshness) bool {
	switch freshness {
	case MCPLocalFreshnessFresh, MCPLocalFreshnessStale, MCPLocalFreshnessUnknown:
		return true
	default:
		return false
	}
}

func (b MCPFederatedBudget) Validate(hosted PacketBudget) error {
	if b.MaxItems < 1 || b.MaxItems > 50 || b.MaxOutputTokens < 500 || b.MaxOutputTokens > 16000 || b.MaxSerializedBytes < 8192 || b.MaxSerializedBytes > 1048576 {
		return fmt.Errorf("federated budget maxima violate v1 bounds")
	}
	if b.HostedItemsUsed < 0 || b.LocalItemsUsed < 0 || b.TotalItemsUsed < 0 || b.HostedEstimatedTokens < 0 || b.LocalEstimatedTokens < 0 || b.TotalEstimatedTokens < 0 || b.HostedSerializedBytes < 0 || b.LocalSerializedBytes < 0 || b.TotalSerializedBytes < 0 {
		return fmt.Errorf("federated budget usage violates v1 bounds")
	}
	if b.HostedItemsUsed != hosted.ItemsUsed || b.HostedEstimatedTokens != hosted.EstimatedTokens || b.HostedSerializedBytes != hosted.SerializedBytes {
		return fmt.Errorf("federated budget hosted usage must match structured packet content")
	}
	if b.HostedTruncated != hosted.Truncated || b.Truncated != (b.HostedTruncated || b.LocalTruncated) {
		return fmt.Errorf("federated budget truncation must match hosted or local packet content")
	}
	if b.TotalItemsUsed != b.HostedItemsUsed+b.LocalItemsUsed || b.TotalEstimatedTokens != b.HostedEstimatedTokens+b.LocalEstimatedTokens || b.TotalSerializedBytes != b.HostedSerializedBytes+b.LocalSerializedBytes {
		return fmt.Errorf("federated budget totals must equal hosted plus local packet content")
	}
	if b.TotalItemsUsed > b.MaxItems || b.TotalEstimatedTokens > b.MaxOutputTokens || b.TotalSerializedBytes > b.MaxSerializedBytes {
		return fmt.Errorf("federated packet content exceeds caller budget")
	}
	return nil
}
