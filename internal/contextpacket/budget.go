package contextpacket

import (
	"encoding/json"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// applyBudget keeps candidates in rank order but spends the item and token
// budgets on every first row of a subject before any later row of one.
func applyBudget(packet *contractsv1.ContextPacket, candidates []contractsv1.ContextPacketItem, later map[string]bool) error {
	keep, tokens := make([]bool, len(candidates)), 0
	used := 0
	for _, pass := range []bool{false, true} {
		for index, candidate := range candidates {
			if later[candidate.PacketItemID] != pass {
				continue
			}
			cost := estimateTokens(candidate)
			if used >= packet.Budget.MaxItems || tokens+cost > packet.Budget.MaxOutputTokens {
				packet.Budget.Truncated = true
				continue
			}
			keep[index], used, tokens = true, used+1, tokens+cost
		}
	}
	items := []contractsv1.ContextPacketItem{}
	for index, candidate := range candidates {
		if keep[index] {
			items = append(items, candidate)
		}
	}
	packet.Items, packet.Budget.ItemsUsed, packet.Budget.EstimatedTokens = items, len(items), tokens
	return nil
}

func finalizePacket(packet *contractsv1.ContextPacket, later map[string]bool, criteria map[string][]string) error {
	for {
		ensurePacketArrays(packet)
		packet.RequiredChecks, packet.RecommendedNextSteps = actionsFor(packet.Items, criteria)
		if packet.Budget.Truncated {
			packet.Warnings = append(packet.Warnings, "context_truncated")
		}
		packet.Warnings = sortedUnique(packet.Warnings)
		normalizeRanks(packet.Items)
		packet.Budget.ItemsUsed, packet.Budget.EstimatedTokens = len(packet.Items), packetTokens(packet.Items)
		bytes, err := stableSerializedBytes(packet)
		if err != nil {
			return err
		}
		if bytes <= packet.Budget.MaxSerializedBytes {
			return nil
		}
		if len(packet.Items) == 0 {
			return fmt.Errorf("base packet serialized bytes %d exceeds maximum %d", bytes, packet.Budget.MaxSerializedBytes)
		}
		drop := len(packet.Items) - 1
		for index := len(packet.Items) - 1; index >= 0; index-- {
			if later[packet.Items[index].PacketItemID] {
				drop = index
				break
			}
		}
		packet.Items, packet.Budget.Truncated = append(packet.Items[:drop], packet.Items[drop+1:]...), true
	}
}

func normalizeRanks(items []contractsv1.ContextPacketItem) {
	for index := range items {
		items[index].Rank = index + 1
	}
}

func stableSerializedBytes(packet *contractsv1.ContextPacket) (int, error) {
	for {
		bytes, err := serializedBytes(*packet)
		if err != nil {
			return 0, err
		}
		if packet.Budget.SerializedBytes == bytes {
			return bytes, nil
		}
		packet.Budget.SerializedBytes = bytes
	}
}

func packetTokens(items []contractsv1.ContextPacketItem) int {
	total := 0
	for _, item := range items {
		total += estimateTokens(item)
	}
	return total
}
func estimateTokens(item contractsv1.ContextPacketItem) int {
	return (len(item.Title) + len(item.Summary) + len(item.WhyIncluded) + len(item.RuleID) + 3) / 4
}
func serializedBytes(packet contractsv1.ContextPacket) (int, error) {
	encoded, err := json.Marshal(packet)
	if err != nil {
		return 0, fmt.Errorf("serialize packet: %w", err)
	}
	return len(encoded), nil
}
