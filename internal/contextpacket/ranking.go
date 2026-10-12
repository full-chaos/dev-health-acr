package contextpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	RankingVersionV1          = "ranker.v2"
	minimumEvidenceConfidence = 0.5
)

func rankEvidence(evidence []contractsv1.EvidenceRef, scope contractsv1.ResolvedScope, goal string, includeLowConfidence bool, requested []contractsv1.PacketCategory, maxItems int) ([]contractsv1.ContextPacketItem, bool) {
	refs := make([]contractsv1.EvidenceRef, 0, len(evidence))
	for _, ref := range evidence {
		if !includeLowConfidence && ref.Confidence < minimumEvidenceConfidence {
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
	if l, r := goalScore(left, goal), goalScore(right, goal); l != r {
		return l > r
	}
	if provenanceRank(left.Provenance) != provenanceRank(right.Provenance) {
		return provenanceRank(left.Provenance) < provenanceRank(right.Provenance)
	}
	if !left.ObservedAt.Equal(right.ObservedAt) {
		return left.ObservedAt.After(right.ObservedAt)
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

var goalStopwords = map[string]bool{"the": true, "and": true, "for": true, "about": true, "what": true, "should": true, "know": true, "first": true, "change": true, "changes": true, "are": true, "with": true, "this": true, "that": true, "from": true, "into": true, "before": true, "how": true, "who": true, "when": true, "where": true, "which": true, "will": true, "can": true, "does": true, "need": true, "want": true}

func goalWords(goal string) []string {
	words := strings.FieldsFunc(strings.ToLower(goal), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	kept := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) > 2 && !goalStopwords[word] {
			kept = append(kept, word)
		}
	}
	return kept
}

func goalScore(ref contractsv1.EvidenceRef, goal string) int {
	haystack := strings.ToLower(ref.Citation + " " + ref.Source.DisplayLabel)
	score := 0
	for _, word := range goalWords(goal) {
		if strings.Contains(haystack, word) {
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
	logExplicitRuleRejection(ref)
	return contractsv1.ContextPacketItem{SchemaVersion: contractsv1.ContextPacketItemSchema, PacketItemID: itemID(ref.EvidenceRefID),
		Category: rule.category, ClaimKind: rule.claim, Title: truncateRunes(ref.Source.DisplayLabel, 500), Summary: evidenceSummary(ref),
		WhyIncluded: "Untrusted retrieved evidence matched the resolved scope.", RuleID: rule.id, Confidence: ref.Confidence,
		Severity: rule.severity, ValidityScope: contractsv1.ValidityScope{Branch: scope.Branch, CommitSHA: scope.CommitSHA},
		Flags:           contractsv1.ItemFlags{Stale: ref.Availability == contractsv1.EvidenceStale, Uncertain: ref.Confidence < minimumEvidenceConfidence, UntrustedContent: true},
		RelatedEntities: []contractsv1.RelatedEntity{{Type: ref.Source.EntityType, ID: ref.Source.EntityID, Label: ref.Source.DisplayLabel, URL: ref.Source.SafeURI}}, EvidenceRefIDs: []string{ref.EvidenceRefID}}
}

var bareTokenPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func evidenceSummary(ref contractsv1.EvidenceRef) string {
	if ref.Source.EntityType == "work_graph_edge" && bareTokenPattern.MatchString(ref.Citation) {
		return "linked via " + strings.ReplaceAll(ref.Citation, "_", " ")
	}
	return ref.Citation
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
