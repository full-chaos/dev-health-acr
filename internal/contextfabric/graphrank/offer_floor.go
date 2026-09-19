package graphrank

import (
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The offer floor: an offer is only ever built from a candidate whose match
// provenance is stronger than similarity alone, or whose similarity clears a
// floor derived from the scoring that produced it.
//
// WHERE THE FLOOR COMES FROM. The full-text adapter scores one candidate by
// the share of the query's terms its own indexed text contains:
//
//	confidence = LexicalBandFloor + (LexicalBandCeiling-LexicalBandFloor) * matched/termCount
//
// (falkorgraph.fulltextRelevanceFromMatchedTerms). A hit that covers at most
// half of the query's terms therefore scores at most the value at
// matched/termCount = 1/2, and every hit the trial stores surfaced for a
// subject that does not exist (an unrelated team, CI runs) sat in that
// lower half. The floor is that value, compared with a strict `>`: a
// similarity-only candidate must cover MORE than half of the query's terms to
// be offered. Nothing here is tuned; a change to the band or to the coverage
// share moves the floor with it.
const (
	// LexicalBandFloor and LexicalBandCeiling bound a lexical hit's
	// confidence. The full-text adapter reads them from here so the band and
	// the floor derived from it cannot drift apart.
	LexicalBandFloor   = 0.50
	LexicalBandCeiling = 0.75

	// OfferMinTermCoverage is the share of query terms a similarity-only
	// candidate must EXCEED to be offered: a majority.
	OfferMinTermCoverage = 0.5

	// OfferSimilarityFloor is the confidence a similarity-only candidate must
	// exceed to be offered.
	OfferSimilarityFloor = LexicalBandFloor + (LexicalBandCeiling-LexicalBandFloor)*OfferMinTermCoverage
)

// OfferAdmission is the closed vocabulary naming why one candidate was, or was
// not, allowed to become an offer.
type OfferAdmission string

const (
	// OfferAdmittedCommitted: the resolution committed this subject. An offer
	// pool never withholds a commit's own subject.
	OfferAdmittedCommitted OfferAdmission = "committed"
	// OfferAdmittedIdentity: matched by an exact label, an alias or a provider
	// key -- provenance stronger than similarity.
	OfferAdmittedIdentity OfferAdmission = "identity"
	// OfferAdmittedAboveFloor: similarity only, and the confidence exceeds
	// OfferSimilarityFloor.
	OfferAdmittedAboveFloor OfferAdmission = "above_floor"
	// OfferRefusedVectorOnly: matched by vector proximity alone.
	OfferRefusedVectorOnly OfferAdmission = "vector_only"
	// OfferRefusedAtOrBelowFloor: similarity only, confidence at or below
	// OfferSimilarityFloor.
	OfferRefusedAtOrBelowFloor OfferAdmission = "at_or_below_floor"
)

// OfferAdmissions is every member, in a fixed order. The enumeration test
// pins the decision table against it.
var OfferAdmissions = []OfferAdmission{
	OfferAdmittedCommitted, OfferAdmittedIdentity, OfferAdmittedAboveFloor,
	OfferRefusedVectorOnly, OfferRefusedAtOrBelowFloor,
}

// Admitted reports whether the admission lets the candidate be offered.
func (a OfferAdmission) Admitted() bool {
	switch a {
	case OfferAdmittedCommitted, OfferAdmittedIdentity, OfferAdmittedAboveFloor:
		return true
	default:
		return false
	}
}

// ClassifyOffer decides whether one candidate may become an offer. It is the
// ONE place the question is answered: every site that turns candidates into
// user-facing offers reads it, none spells the threshold itself.
func ClassifyOffer(candidate contextfabric.SubjectCandidate) OfferAdmission {
	if candidate.State == contextfabric.ResolutionCommitted {
		return OfferAdmittedCommitted
	}
	if isVectorOnlyCandidate(candidate.MatchMechanisms) {
		return OfferRefusedVectorOnly
	}
	for _, mechanism := range candidate.MatchMechanisms {
		switch mechanism {
		case contextfabric.MatchExact, contextfabric.MatchAlias, contextfabric.MatchProviderKey:
			return OfferAdmittedIdentity
		}
	}
	if candidate.Confidence > OfferSimilarityFloor {
		return OfferAdmittedAboveFloor
	}
	return OfferRefusedAtOrBelowFloor
}

// OfferFloorRow is one candidate's decision, for the trace.
type OfferFloorRow struct {
	Kind        string
	CanonicalID string
	Mechanisms  string
	Confidence  float64
	Admission   OfferAdmission
}

// offerFloorRowCap bounds the per-request row list carried on the trace.
const offerFloorRowCap = 20

func offerFloorRow(candidate contextfabric.SubjectCandidate, admission OfferAdmission) OfferFloorRow {
	mechanisms := MergeMechanisms(candidate.MatchMechanisms)
	names := make([]string, 0, len(mechanisms))
	for _, mechanism := range mechanisms {
		names = append(names, string(mechanism))
	}
	joined := ""
	for index, name := range names {
		if index > 0 {
			joined += "+"
		}
		joined += name
	}
	return OfferFloorRow{
		Kind: string(candidate.Subject.Kind), CanonicalID: candidate.Subject.CanonicalID,
		Mechanisms: joined, Confidence: candidate.Confidence, Admission: admission,
	}
}

// boundOfferFloorRows orders rows by confidence (ties by canonical id) and
// caps them at offerFloorRowCap, so the trace line stays bounded and stable.
func boundOfferFloorRows(rows []OfferFloorRow) []OfferFloorRow {
	sorted := append([]OfferFloorRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Confidence == sorted[j].Confidence {
			if sorted[i].Kind == sorted[j].Kind {
				return sorted[i].CanonicalID < sorted[j].CanonicalID
			}
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Confidence > sorted[j].Confidence
	})
	if len(sorted) > offerFloorRowCap {
		sorted = sorted[:offerFloorRowCap]
	}
	return sorted
}

// offerAdmissionOf is ClassifyOffer with one addition the resolution seam
// owns: a subject the decision committed is never withheld, whatever route
// committed it.
func offerAdmissionOf(candidate contextfabric.SubjectCandidate, committed map[string]bool) OfferAdmission {
	if committed[SubjectKey(candidate.Subject)] {
		return OfferAdmittedCommitted
	}
	return ClassifyOffer(candidate)
}

// searchedOfferKinds returns the distinct kinds of the candidates the
// retrieval pool held, sorted -- the kinds the no-match names as searched.
func searchedOfferKinds(candidates []contextfabric.SubjectCandidate) []string {
	seen := make(map[string]bool, len(candidates))
	kinds := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		kind := string(candidate.Subject.Kind)
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// dedupOfferFloorRows folds the rows every offer site classified into one row
// per subject (a candidate reaches more than one site), and counts the
// refused ones.
func dedupOfferFloorRows(rows []OfferFloorRow) ([]OfferFloorRow, int) {
	seen := make(map[string]bool, len(rows))
	out := make([]OfferFloorRow, 0, len(rows))
	refused := 0
	for _, row := range rows {
		key := row.Kind + "\x00" + row.CanonicalID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, row)
		if !row.Admission.Admitted() {
			refused++
		}
	}
	return out, refused
}

// offerFloorCandidateLines renders the bounded pre-decision list:
// "kind|canonical_id|mechanisms|confidence|admission".
func offerFloorCandidateLines(rows []OfferFloorRow) []string {
	bounded := boundOfferFloorRows(rows)
	lines := make([]string, 0, len(bounded))
	for _, row := range bounded {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%.4f|%s", row.Kind, row.CanonicalID, row.Mechanisms, row.Confidence, row.Admission))
	}
	return lines
}

// offerFloorOfferLines renders the post-decision offers, bounded.
func offerFloorOfferLines(kind, candidate, handle contextfabric.StructureOfferMaterial) []string {
	lines := make([]string, 0, offerFloorRowCap)
	for _, option := range kind.KindOptions {
		lines = append(lines, "kind|"+string(option.Kind))
	}
	for _, option := range candidate.CandidateOptions {
		lines = append(lines, "candidate|"+string(option.Kind)+"|"+option.CanonicalID)
	}
	for _, option := range handle.HandleOptions {
		lines = append(lines, "handle|"+string(option.Kind)+"|"+option.Value)
	}
	if len(lines) > offerFloorRowCap {
		lines = lines[:offerFloorRowCap]
	}
	return lines
}

// offerFloorOutcome names the decision and its reason for the trace.
func offerFloorOutcome(poolCount, refused int, kind, candidate, handle contextfabric.StructureOfferMaterial) (decision, reason string) {
	offers := len(kind.KindOptions) + len(candidate.CandidateOptions) + len(handle.HandleOptions)
	switch {
	case offers > 0:
		return "offered", "admitted_candidates_offered"
	case poolCount == 0:
		return "no_offer", "empty_pool"
	case refused == poolCount:
		return "no_offer", "every_candidate_below_floor"
	default:
		return "no_offer", "no_offer_material"
	}
}

// offerFloorDecisionOrDefault / offerFloorReasonOrDefault name the fold that
// fires when the offer builders were never reached (an upstream exit): the
// decision was not taken, and the line says so rather than leaving it empty.
func offerFloorDecisionOrDefault(decision string) string {
	if decision == "" {
		return "no_offer"
	}
	return decision
}

func offerFloorReasonOrDefault(reason string) string {
	if reason == "" {
		return "not_evaluated"
	}
	return reason
}
