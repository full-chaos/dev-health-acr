package contextpacket

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	RankingVersionV1          = "ranker.v9"
	minimumEvidenceConfidence = 0.5
)

func rankEvidence(evidence []contractsv1.EvidenceRef, scope contractsv1.ResolvedScope, goal string, includeLowConfidence bool, requested []contractsv1.PacketCategory, maxItems int) ([]contractsv1.ContextPacketItem, bool, map[string]bool) {
	roles := newRoleResolver(evidence)
	refs := make([]contractsv1.EvidenceRef, 0, len(evidence))
	for _, ref := range evidence {
		if !includeLowConfidence && ref.Confidence < minimumEvidenceConfidence {
			continue
		}
		refs = append(refs, ref)
	}
	type scoredRef struct {
		ref   contractsv1.EvidenceRef
		score int
	}
	scored := make([]scoredRef, len(refs))
	ownScores := make(map[string]int, len(refs))
	for i, ref := range refs {
		scored[i] = scoredRef{ref: ref, score: goalScore(ref, goal)}
		ownScores[ref.EvidenceRefID] = max(ownScores[ref.EvidenceRefID], scored[i].score)
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return evidenceBefore(scored[i].ref, scored[j].ref, ownScores, roles)
	})
	for i := range scored {
		refs[i] = scored[i].ref
	}
	beforeDedupe := len(refs)
	refs = deduplicateEvidence(refs)
	scores := effectiveGoalScores(refs, goal, roles)
	sort.Slice(refs, func(i, j int) bool { return evidenceBefore(refs[i], refs[j], scores, roles) })
	refs = requestedEvidenceCategories(refs, requested, roles)
	candidates := len(refs)
	refs, truncated, laterRows := allocateCategories(refs, scores, maxItems, roles)
	slog.Debug("context packet ranking", "event", "ranking_decided", "evidence_in", beforeDedupe, "duplicates_dropped", beforeDedupe-candidates, "kept", len(refs), "max_items", maxItems, "truncated", truncated, "goal_words", len(goalWords(goal)))
	items := make([]contractsv1.ContextPacketItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, evidenceItemWithRoles(ref, scope, roles))
	}
	deferred := make(map[string]bool, len(laterRows))
	for i := range items {
		items[i].Rank = i + 1
		deferred[items[i].PacketItemID] = laterRows[refs[i].EvidenceRefID]
	}
	return items, truncated, deferred
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

// citationKeyPattern matches the field name of a "name=value" pair a producer
// writes into a citation (release_ref=, status=). Only the value and the label
// say what a row is about; a field name never fits a goal word.
var citationKeyPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*=`)

func goalScore(ref contractsv1.EvidenceRef, goal string) int {
	haystack := strings.FieldsFunc(strings.ToLower(citationKeyPattern.ReplaceAllString(ref.Citation, " ")+" "+ref.Source.DisplayLabel), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
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
	// A pull request served as pull_request and as a projected work item keeps
	// the pull_request row whatever order scoring put them in.
	pullRequestOf := map[string]string{}
	for _, ref := range refs {
		if subject, _ := ref.Metadata[metadataSubjectKey].(string); subject != "" && ref.Source.EntityType == "pull_request" {
			if _, ok := pullRequestOf[subject]; !ok {
				pullRequestOf[subject] = ref.EvidenceRefID
			}
		}
	}
	seen, unique := map[string]bool{}, make([]contractsv1.EvidenceRef, 0, len(refs))
	for _, ref := range refs {
		if subject, _ := ref.Metadata[metadataSubjectKey].(string); subject != "" {
			if winner, ok := pullRequestOf[subject]; ok && winner != ref.EvidenceRefID {
				continue
			}
		}
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
	if subject, _ := ref.Metadata[metadataSubjectKey].(string); subject != "" {
		keys = append(keys, "subject:"+subject)
	}
	return keys
}

func evidenceTieKey(ref contractsv1.EvidenceRef) string {
	metadata, _ := json.Marshal(ref.Metadata)
	return strings.Join([]string{ref.EvidenceRefID, ref.Source.System, ref.Source.EntityType, ref.Source.EntityID, ref.ContentDigest, ref.Citation, ref.Source.DisplayLabel, ref.Source.SafeURI, ref.Provenance, string(ref.Availability), ref.SourceVersion, ref.SnapshotHash, string(metadata)}, "\000")
}

// allocateCategories gives one slot to each focus (the entity a row is about,
// such as one file) before a focus takes a second. Category quotas still come
// first: a second row of a focus takes a slot only when no first row can fill
// the quota or the slots that remain. Kept rows stay in rank order; the later
// rows of a focus are returned as a set so the output budgets drop them before
// another subject's row.
func allocateCategories(refs []contractsv1.EvidenceRef, scores map[string]int, maxItems int, roles roleResolver) ([]contractsv1.EvidenceRef, bool, map[string]bool) {
	primary, secondary := splitByFocus(refs)
	later := make(map[string]bool, len(secondary))
	for _, ref := range secondary {
		later[ref.EvidenceRefID] = true
	}
	if len(refs) <= maxItems {
		return refs, false, later
	}
	ordered := append(append(make([]contractsv1.EvidenceRef, 0, len(refs)), primary...), secondary...)
	categories := []contractsv1.PacketCategory{contractsv1.CategoryState, contractsv1.CategoryPressure, contractsv1.CategoryCause, contractsv1.CategoryEvidence, contractsv1.CategoryAction}
	quotas, selected := map[contractsv1.PacketCategory]int{}, make([]contractsv1.EvidenceRef, 0, maxItems)
	for index, category := range categories {
		quotas[category] = maxItems / len(categories)
		if index < maxItems%len(categories) {
			quotas[category]++
		}
	}
	selectedIDs := map[string]bool{}
	for _, ref := range primary {
		if len(selected) >= (maxItems+1)/2 || scores[ref.EvidenceRefID] == 0 {
			break
		}
		category := roles.rule(ref).category
		selected, selectedIDs[ref.EvidenceRefID] = append(selected, ref), true
		if quotas[category] > 0 {
			quotas[category]--
		}
	}
	// Causes fill their quota before state and pressure so goal matches plus
	// state and pressure rows cannot spend the whole budget before a cause.
	for _, category := range []contractsv1.PacketCategory{contractsv1.CategoryCause, contractsv1.CategoryState, contractsv1.CategoryPressure, contractsv1.CategoryEvidence, contractsv1.CategoryAction} {
		for _, ref := range ordered {
			if roles.rule(ref).category == category && quotas[category] > 0 && len(selected) < maxItems && !selectedIDs[ref.EvidenceRefID] {
				selected, selectedIDs[ref.EvidenceRefID], quotas[category] = append(selected, ref), true, quotas[category]-1
			}
		}
	}
	for _, ref := range ordered {
		if len(selected) == maxItems {
			break
		}
		if !selectedIDs[ref.EvidenceRefID] {
			selected, selectedIDs[ref.EvidenceRefID] = append(selected, ref), true
		}
	}
	sort.Slice(selected, func(i, j int) bool { return evidenceBefore(selected[i], selected[j], scores, roles) })
	slog.Debug("context packet focus cap", "event", "focus_cap_applied", "deferred", len(secondary), "kept", len(selected), "max_items", maxItems)
	return selected, true, later
}

func splitByFocus(refs []contractsv1.EvidenceRef) (primary, secondary []contractsv1.EvidenceRef) {
	seen := map[string]bool{}
	for _, ref := range refs {
		if focus, _ := ref.Metadata[metadataFocusKey].(string); focus != "" {
			if seen[focus] {
				secondary = append(secondary, ref)
				continue
			}
			seen[focus] = true
		}
		primary = append(primary, ref)
	}
	return primary, secondary
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

// effectiveGoalScores is the goal rank every item sorts by, encoded as
// 2*score plus one when the score is the item's own. A cause is ranked at least
// as high as every cause of a weaker match tier: its score is the largest goal
// score among the causes of its own tier and weaker ones. The rank is a
// per-item scalar, so the comparator built on it stays a strict weak order. An
// inherited score loses to an equal own score, so an unrelated exact cause
// never outranks an item that names the goal itself at an equal score; a cause that inherits a higher score can still tie and win by tier; among causes with no own
// match the tier order holds.
func effectiveGoalScores(refs []contractsv1.EvidenceRef, goal string, roles roleResolver) map[string]int {
	own := make(map[string]int, len(refs))
	bestAtOrBelow := map[int]int{}
	for _, ref := range refs {
		score := goalScore(ref, goal)
		own[ref.EvidenceRefID] = max(own[ref.EvidenceRefID], score)
		if rule := roles.rule(ref); rule.category == contractsv1.CategoryCause && rule.subRank > 0 {
			bestAtOrBelow[rule.subRank] = max(bestAtOrBelow[rule.subRank], score)
		}
	}
	scores := make(map[string]int, len(refs))
	// A goal that names a failure is about the rows that persist a failure
	// (failurePressure; blocked work is not one) or what caused one (cause); a goal that names code
	// health is about the hotspot and complexity rows. That fit outranks any
	// word score, which is at most 2*words+1.
	fit, failureGoal, codeHealthGoal := 2*len(goalWords(goal))+2, goalNamesFailure(goal), goalNamesCodeHealth(goal)
	for _, ref := range refs {
		effective, mine := own[ref.EvidenceRefID], own[ref.EvidenceRefID]
		if rule := roles.rule(ref); rule.category == contractsv1.CategoryCause && rule.subRank > 0 {
			for tier := 1; tier <= rule.subRank; tier++ {
				effective = max(effective, bestAtOrBelow[tier])
			}
		}
		scores[ref.EvidenceRefID] = effective * 2
		if effective > 0 && effective == mine {
			scores[ref.EvidenceRefID]++
		}
		category := roles.rule(ref).category
		if (failureGoal && (category == contractsv1.CategoryCause || failurePressure(ref))) ||
			(codeHealthGoal && codeHealthEntityTypes[ref.Source.EntityType]) {
			scores[ref.EvidenceRefID] += fit
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
