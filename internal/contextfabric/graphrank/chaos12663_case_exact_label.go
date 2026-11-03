package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12663: NodeCandidate matches a term to a label with strings.EqualFold,
// so in an organization holding a linear team "Fullchaos" and a gitlab team
// "fullchaos" each term exact-matched both teams, the exact-label tier saw two
// matches, and every question naming either team clarified -- a comparison of
// the two could never publish.
//
// Lead ruling: per term, a UNIQUE case-exact label match wins over case-folded
// matches. Any other casing (no case-exact match, or two or more) stays
// ambiguous exactly as before. Alias, provider-key and keyed-identity matches are
// untouched: they keep their own case-insensitive rules and collision checks.

// caseExactLabelPick is what narrowCaseExactLabel decided. winner indexes the
// candidates slice and is meaningful only when sentence is not "".
type caseExactLabelPick struct {
	winner int
	// sentence is the disclosure the commit owes (one per term): the subject the
	// term was read as and every case twin. "" when the rule did not apply.
	sentence string
}

// narrowCaseExactLabel applies the rule to exactIndex (the exact-label tier's
// raw population). It applies only when there are two or more exact matches,
// every one of them matched a term through its own label, the term was typed in
// ONE spelling -- across the matched terms and typedTerms, the spellings the
// question typed before the case-insensitive term dedup -- and exactly one label
// is that spelling byte for byte. One spelling folding to every label is also
// what proves the labels are one name: exact matches of two names carry two
// spellings. Anything else returns the zero pick and the caller keeps exactIndex
// as it is.
//
// The disclosure is composed here, before any commit: a sentence the composer
// cannot state in full refuses the pick, so a commit by case is never made
// without the sentence that tells the user how to name the twin.
func narrowCaseExactLabel(candidates []contextfabric.SubjectCandidate, exactIndex []int, typedTerms []string) caseExactLabelPick {
	if len(exactIndex) < 2 {
		return caseExactLabelPick{}
	}
	name := strings.TrimSpace(candidates[exactIndex[0]].Subject.Label)
	spellings := make(map[string]struct{}, 1)
	for _, index := range exactIndex {
		label := strings.TrimSpace(candidates[index].Subject.Label)
		matchedByLabel := false
		for _, term := range candidates[index].MatchedTerms {
			if term = strings.TrimSpace(term); term != "" && strings.EqualFold(term, label) {
				matchedByLabel = true
				spellings[term] = struct{}{}
			}
		}
		if !matchedByLabel {
			// The exact match came through the node name, not the label: the
			// label is not the term's name in another case, so naming it as a
			// twin would be false.
			return caseExactLabelPick{}
		}
	}
	for _, term := range typedTerms {
		if term = strings.TrimSpace(term); strings.EqualFold(term, name) {
			spellings[term] = struct{}{}
		}
	}
	if len(spellings) != 1 {
		// The question typed the name in two spellings; neither one is THE
		// term, so case cannot choose.
		return caseExactLabelPick{}
	}
	var spelling string
	for term := range spellings {
		spelling = term
	}
	winner := -1
	for _, index := range exactIndex {
		if strings.TrimSpace(candidates[index].Subject.Label) != spelling {
			continue
		}
		if winner >= 0 {
			// Two labels spell the term with its case: still ambiguous.
			return caseExactLabelPick{}
		}
		winner = index
	}
	if winner < 0 {
		return caseExactLabelPick{}
	}
	twins := make([]contractsv1.ContextFabricCaseTwinSubject, 0, len(exactIndex)-1)
	for _, index := range exactIndex {
		if index != winner {
			twins = append(twins, caseTwinSubject(candidates[index]))
		}
	}
	// A sentence the composer cannot state in full is "", and "" is no pick.
	return caseExactLabelPick{winner: winner, sentence: contractsv1.ContextFabricCaseTwinReadingLimitation(spelling, caseTwinSubject(candidates[winner]), twins)}
}

// caseTwinSubject names a candidate as the clarification prompt does: its
// served label, its kind, and its provider word.
func caseTwinSubject(candidate contextfabric.SubjectCandidate) contractsv1.ContextFabricCaseTwinSubject {
	return contractsv1.ContextFabricCaseTwinSubject{Kind: candidate.Subject.Kind, Label: candidate.Subject.Label, Provider: candidate.Provider}
}

// slotsSpellingServedLabel keeps, of the slots a comparison selection matched,
// the ones with a term that spells the selected subject's SERVED label byte for
// byte (trimmed). The label is the one the graph re-read for the selection,
// never the label the caller sent with it. Called only when the selection
// matched two or more slots; what it returns decides as before: exactly one
// binds, none or several leave the selection unbound.
func slotsSpellingServedLabel(slots []contextfabric.ComparisonOperandSlot, matches []int, node CandidateNode) []int {
	served, ok := NodeSubject(node)
	if !ok {
		return matches
	}
	kept := make([]int, 0, len(matches))
	for _, index := range matches {
		for _, term := range slots[index].Terms {
			if strings.TrimSpace(term) == served.Label {
				kept = append(kept, index)
				break
			}
		}
	}
	return kept
}
