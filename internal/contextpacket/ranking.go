package contextpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	RankingVersionV1          = "ranker.v4"
	minimumEvidenceConfidence = 0.5
)

func rankEvidence(evidence []contractsv1.EvidenceRef, scope contractsv1.ResolvedScope, goal string, includeLowConfidence bool, requested []contractsv1.PacketCategory, maxItems int) ([]contractsv1.ContextPacketItem, bool) {
	roles := newRoleResolver(evidence)
	refs := make([]contractsv1.EvidenceRef, 0, len(evidence))
	for _, ref := range evidence {
		if !includeLowConfidence && ref.Confidence < minimumEvidenceConfidence {
			continue
		}
		refs = append(refs, ref)
	}
	ownScores := make(map[string]int, len(refs))
	for _, ref := range refs {
		ownScores[ref.EvidenceRefID] = max(ownScores[ref.EvidenceRefID], goalScore(ref, goal))
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if l, r := goalScore(refs[i], goal), goalScore(refs[j], goal); l != r {
			return l > r
		}
		return evidenceBefore(refs[i], refs[j], ownScores, roles)
	})
	refs = deduplicateEvidence(refs)
	scores := effectiveGoalScores(refs, goal, roles)
	sort.Slice(refs, func(i, j int) bool { return evidenceBefore(refs[i], refs[j], scores, roles) })
	refs = requestedEvidenceCategories(refs, requested, roles)
	refs, truncated := allocateCategories(refs, scores, maxItems, roles)
	items := make([]contractsv1.ContextPacketItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, evidenceItemWithRoles(ref, scope, roles))
	}
	for i := range items {
		items[i].Rank = i + 1
	}
	return items, truncated
}

func requestedEvidenceCategories(refs []contractsv1.EvidenceRef, requested []contractsv1.PacketCategory, roles roleResolver) []contractsv1.EvidenceRef {
	if len(requested) == 0 {
		return refs
	}
	allowed := map[contractsv1.PacketCategory]bool{}
	for _, category := range requested {
		allowed[category] = true
	}
	filtered := make([]contractsv1.EvidenceRef, 0, len(refs))
	for _, ref := range refs {
		if allowed[roles.rule(ref).category] {
			filtered = append(filtered, ref)
		}
	}
	return filtered
}

func evidenceBefore(left, right contractsv1.EvidenceRef, scores map[string]int, roles roleResolver) bool {
	if l, r := scores[left.EvidenceRefID], scores[right.EvidenceRefID]; l != r {
		return l > r
	}
	// A stronger match tier ranks before a weaker one at an equal score,
	// before any provenance or recency key can reorder them.
	if l, r := causeTier(roles.rule(left)), causeTier(roles.rule(right)); l != r {
		return l > r
	}
	if provenanceRank(left.Provenance) != provenanceRank(right.Provenance) {
		return provenanceRank(left.Provenance) < provenanceRank(right.Provenance)
	}
	if !left.ObservedAt.Equal(right.ObservedAt) {
		return left.ObservedAt.After(right.ObservedAt)
	}
	l, r := roles.rule(left), roles.rule(right)
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
	case contractsv1.EvidenceProvenanceNative:
		return 0
	case contractsv1.EvidenceProvenanceExplicitText:
		return 1
	case contractsv1.EvidenceProvenanceDerived:
		return 2
	case contractsv1.EvidenceProvenanceHeuristic:
		return 3
	default:
		return 4
	}
}

var goalStopwords = map[string]bool{"the": true, "and": true, "for": true, "about": true, "what": true, "should": true, "know": true, "first": true, "change": true, "changes": true, "are": true, "with": true, "this": true, "that": true, "from": true, "into": true, "before": true, "how": true, "who": true, "when": true, "where": true, "which": true, "will": true, "can": true, "does": true, "need": true, "want": true}

func goalWords(goal string) []string {
	words := strings.FieldsFunc(strings.ToLower(goal), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	kept, seen := make([]string, 0, len(words)), map[string]bool{}
	for _, word := range words {
		if len(word) > 2 && !goalStopwords[word] && !seen[word] {
			seen[word] = true
			kept = append(kept, word)
		}
	}
	return kept
}

func goalScore(ref contractsv1.EvidenceRef, goal string) int {
	haystack := strings.FieldsFunc(strings.ToLower(ref.Citation+" "+ref.Source.DisplayLabel), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	score := 0
	for _, word := range goalWords(goal) {
		for _, token := range haystack {
			if token == word || (len(word) >= 5 && strings.HasPrefix(token, word)) {
				score++
				break
			}
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
	metadata, _ := json.Marshal(ref.Metadata)
	return strings.Join([]string{ref.EvidenceRefID, ref.Source.System, ref.Source.EntityType, ref.Source.EntityID, ref.ContentDigest, ref.Citation, ref.Source.DisplayLabel, ref.Source.SafeURI, ref.Provenance, string(ref.Availability), ref.SourceVersion, ref.SnapshotHash, string(metadata)}, "\000")
}

func allocateCategories(refs []contractsv1.EvidenceRef, scores map[string]int, maxItems int, roles roleResolver) ([]contractsv1.EvidenceRef, bool) {
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
	for _, ref := range refs {
		if len(selected) >= (maxItems+1)/2 || scores[ref.EvidenceRefID] == 0 {
			break
		}
		category := roles.rule(ref).category
		selected, selectedIDs[ref.EvidenceRefID] = append(selected, ref), true
		if quotas[category] > 0 {
			quotas[category]--
		}
	}
	for _, category := range categories {
		for _, ref := range refs {
			if roles.rule(ref).category == category && quotas[category] > 0 && len(selected) < maxItems && !selectedIDs[ref.EvidenceRefID] {
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
	sort.Slice(selected, func(i, j int) bool { return evidenceBefore(selected[i], selected[j], scores, roles) })
	return selected, true
}

func evidenceItem(ref contractsv1.EvidenceRef, scope contractsv1.ResolvedScope) contractsv1.ContextPacketItem {
	return evidenceItemWithRoles(ref, scope, nil)
}

func evidenceItemWithRoles(ref contractsv1.EvidenceRef, scope contractsv1.ResolvedScope, roles roleResolver) contractsv1.ContextPacketItem {
	rule := roles.rule(ref)
	logExplicitRuleRejection(ref)
	return contractsv1.ContextPacketItem{SchemaVersion: contractsv1.ContextPacketItemSchema, PacketItemID: itemID(ref.EvidenceRefID),
		Category: rule.category, ClaimKind: rule.claim, Title: truncateRunes(ref.Source.DisplayLabel, 500), Summary: evidenceSummary(ref),
		WhyIncluded: whyIncluded(rule), RuleID: rule.id, Confidence: ref.Confidence,
		Severity: rule.severity, ValidityScope: contractsv1.ValidityScope{Branch: scope.Branch, CommitSHA: scope.CommitSHA},
		Flags:           contractsv1.ItemFlags{Stale: ref.Availability == contractsv1.EvidenceStale, Uncertain: ref.Confidence < minimumEvidenceConfidence || (rule.why != "" && (rule.category != contractsv1.CategoryCause || rule.claim == contractsv1.ClaimInferred)), UntrustedContent: true},
		RelatedEntities: []contractsv1.RelatedEntity{{Type: ref.Source.EntityType, ID: ref.Source.EntityID, Label: ref.Source.DisplayLabel, URL: ref.Source.SafeURI}}, EvidenceRefIDs: itemEvidenceRefIDs(ref, rule)}
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

func whyIncluded(r rule) string {
	if r.why != "" {
		return r.why
	}
	return "Untrusted retrieved evidence matched the resolved scope."
}

func itemEvidenceRefIDs(ref contractsv1.EvidenceRef, r rule) []string {
	ids := []string{ref.EvidenceRefID}
	for _, cited := range r.cites {
		if cited != ref.EvidenceRefID && !slices.Contains(ids, cited) && len(ids) < 4 {
			ids = append(ids, cited)
		}
	}
	return ids
}

// effectiveGoalScores is the goal score every item ranks by. A cause is ranked
// at least as high as every cause of a weaker match tier: its score is the
// largest goal score among the causes of its own tier and weaker ones. The
// score is a per-item scalar, so the comparator built on it stays a strict
// weak order; a weaker tier can therefore never outrank, or survive an item
// budget ahead of, a stronger one, while a goal-less exact cause still ranks
// below ordinary evidence that names the goal when no weaker cause does.
func effectiveGoalScores(refs []contractsv1.EvidenceRef, goal string, roles roleResolver) map[string]int {
	scores := make(map[string]int, len(refs))
	bestAtOrBelow := map[int]int{}
	for _, ref := range refs {
		own := goalScore(ref, goal)
		if own > scores[ref.EvidenceRefID] {
			scores[ref.EvidenceRefID] = own
		}
		if rule := roles.rule(ref); rule.category == contractsv1.CategoryCause && rule.subRank > 0 {
			if own > bestAtOrBelow[rule.subRank] {
				bestAtOrBelow[rule.subRank] = own
			}
		}
	}
	for _, ref := range refs {
		rule := roles.rule(ref)
		if rule.category != contractsv1.CategoryCause || rule.subRank == 0 {
			continue
		}
		for tier := 1; tier <= rule.subRank; tier++ {
			if score := bestAtOrBelow[tier]; score > scores[ref.EvidenceRefID] {
				scores[ref.EvidenceRefID] = score
			}
		}
	}
	return scores
}

func causeTier(r rule) int {
	if r.category == contractsv1.CategoryCause {
		return r.subRank
	}
	return 0
}
