package contextpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const RankingVersionV1 = "ranker.v2"

func rankEvidence(evidence []contractsv1.EvidenceRef, scope contractsv1.ResolvedScope, goal string, includeLowConfidence bool, requested []contractsv1.PacketCategory, maxItems int) ([]contractsv1.ContextPacketItem, bool) {
	refs := make([]contractsv1.EvidenceRef, 0, len(evidence))
	for _, ref := range evidence {
		if !includeLowConfidence && ref.Confidence < 0.5 {
			continue
		}
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return evidenceBefore(refs[i], refs[j], goal) })
	refs = deduplicateEvidence(refs)
	refs = requestedEvidenceCategories(refs, requested)
	refs, truncated := allocateCategories(refs, goal, maxItems)
	items := make([]contractsv1.ContextPacketItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, evidenceItem(ref, scope))
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return items, truncated
}

func requestedEvidenceCategories(refs []contractsv1.EvidenceRef, requested []contractsv1.PacketCategory) []contractsv1.EvidenceRef {
	if len(requested) == 0 {
		return refs
	}
	allowed := map[contractsv1.PacketCategory]bool{}
	for _, category := range requested {
		allowed[category] = true
	}
	filtered := make([]contractsv1.EvidenceRef, 0, len(refs))
	for _, ref := range refs {
		if allowed[evidenceRule(ref).category] {
			filtered = append(filtered, ref)
		}
	}
	return filtered
}

func evidenceBefore(left, right contractsv1.EvidenceRef, goal string) bool {
	if provenanceRank(left.Provenance) != provenanceRank(right.Provenance) {
		return provenanceRank(left.Provenance) < provenanceRank(right.Provenance)
	}
	if !left.ObservedAt.Equal(right.ObservedAt) {
		return left.ObservedAt.After(right.ObservedAt)
	}
	if goalScore(left, goal) != goalScore(right, goal) {
		return goalScore(left, goal) > goalScore(right, goal)
	}
	l, r := evidenceRule(left), evidenceRule(right)
	if l.categoryRank != r.categoryRank {
		return l.categoryRank < r.categoryRank
	}
	if left.Confidence != right.Confidence {
		return left.Confidence > right.Confidence
	}
	return evidenceTieKey(left) < evidenceTieKey(right)
}

func provenanceRank(value string) int {
	switch value {
	case "native":
		return 0
	case "explicit_text":
		return 1
	case "derived":
		return 2
	case "heuristic":
		return 3
	default:
		return 4
	}
}
func goalScore(ref contractsv1.EvidenceRef, goal string) int {
	score := 0
	for _, word := range strings.Fields(strings.ToLower(goal)) {
		if len(word) > 2 && strings.Contains(strings.ToLower(ref.Citation+" "+ref.Source.DisplayLabel), word) {
			score++
		}
	}
	return score
}
func deduplicateEvidence(refs []contractsv1.EvidenceRef) []contractsv1.EvidenceRef {
	seen, unique := map[string]bool{}, make([]contractsv1.EvidenceRef, 0, len(refs))
	for _, ref := range refs {
		keys := canonicalEvidenceKeys(ref)
		duplicate := false
		for _, key := range keys {
			duplicate = duplicate || seen[key]
		}
		if duplicate {
			continue
		}
		for _, key := range keys {
			seen[key] = true
		}
		unique = append(unique, ref)
	}
	return unique
}

func canonicalEvidenceKeys(ref contractsv1.EvidenceRef) []string {
	keys := []string{"id:" + ref.EvidenceRefID, "entity:" + strings.Join([]string{ref.Source.System, ref.Source.EntityType, ref.Source.EntityID}, "\000")}
	if ref.ContentDigest != "" {
		keys = append(keys, "content:"+ref.ContentDigest)
	}
	return keys
}

func evidenceTieKey(ref contractsv1.EvidenceRef) string {
	return strings.Join([]string{ref.EvidenceRefID, ref.Source.System, ref.Source.EntityType, ref.Source.EntityID, ref.ContentDigest, ref.Citation, ref.Source.DisplayLabel}, "\000")
}

func allocateCategories(refs []contractsv1.EvidenceRef, goal string, maxItems int) ([]contractsv1.EvidenceRef, bool) {
	if len(refs) <= maxItems {
		return refs, false
	}
	categories := []contractsv1.PacketCategory{contractsv1.CategoryState, contractsv1.CategoryPressure, contractsv1.CategoryCause, contractsv1.CategoryEvidence, contractsv1.CategoryAction}
	quotas, selected := map[contractsv1.PacketCategory]int{}, make([]contractsv1.EvidenceRef, 0, maxItems)
	for index, category := range categories {
		quotas[category] = maxItems / len(categories)
		if index < maxItems%len(categories) {
			quotas[category]++
		}
	}
	selectedIDs := map[string]bool{}
	for _, category := range categories {
		for _, ref := range refs {
			if evidenceRule(ref).category == category && quotas[category] > 0 {
				selected, selectedIDs[ref.EvidenceRefID], quotas[category] = append(selected, ref), true, quotas[category]-1
			}
		}
	}
	for _, ref := range refs {
		if len(selected) == maxItems {
			break
		}
		if !selectedIDs[ref.EvidenceRefID] {
			selected, selectedIDs[ref.EvidenceRefID] = append(selected, ref), true
		}
	}
	sort.Slice(selected, func(i, j int) bool { return evidenceBefore(selected[i], selected[j], goal) })
	return selected, true
}

func evidenceItem(ref contractsv1.EvidenceRef, scope contractsv1.ResolvedScope) contractsv1.ContextPacketItem {
	rule := evidenceRule(ref)
	return contractsv1.ContextPacketItem{SchemaVersion: contractsv1.ContextPacketItemSchema, PacketItemID: itemID(ref.EvidenceRefID),
		Category: rule.category, ClaimKind: rule.claim, Title: truncateRunes(ref.Source.DisplayLabel, 500), Summary: ref.Citation,
		WhyIncluded: "Untrusted retrieved evidence matched the resolved scope.", RuleID: rule.id, Confidence: ref.Confidence,
		Severity: rule.severity, ValidityScope: contractsv1.ValidityScope{Branch: scope.Branch, CommitSHA: scope.CommitSHA},
		Flags:           contractsv1.ItemFlags{Stale: ref.Availability == contractsv1.EvidenceStale, Uncertain: ref.Confidence < 0.5, UntrustedContent: true},
		RelatedEntities: []contractsv1.RelatedEntity{{Type: ref.Source.EntityType, ID: ref.Source.EntityID, Label: ref.Source.DisplayLabel, URL: ref.Source.SafeURI}}, EvidenceRefIDs: []string{ref.EvidenceRefID}}
}

func itemID(evidenceRefID string) string {
	sum := sha256.Sum256([]byte(evidenceRefID))
	return "item_" + hex.EncodeToString(sum[:12])
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
