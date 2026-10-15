package contextfabric

import (
	"fmt"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const degradedFactClaimIDPrefix = "server:fact:"

// mintDegradedFactClaims builds the claims a degraded answer carries when the
// model call failed: one per scalar canonical field that was read, in the
// order the facts were read, with the evidence those facts cite. Only the
// model's judgment is missing from such an answer, never the values.
//
// Each claim carries the value of the field it was minted from, so it is
// grounded in those facts by construction; Validate refuses what a claim cannot
// carry (a padded field name, a value that is not finite). The claim count stops at the claimed-fact bound
// and the evidence count at the evidence bound; truncated reports that facts
// were left out: for the bound, or because a value could not be carried as a claim.
func mintDegradedFactClaims(facts []CanonicalFact, claimLimit int) (claims []ClaimedFact, evidence []string, truncated bool) {
	claims, evidence, truncated, _ = mintDegradedFactClaimsWithholding(facts, claimLimit, nil)
	return claims, evidence, truncated
}

// withheldScore is a declared score field the degraded answer did not claim, and the drivers table whose absence
// withheld it.
type withheldScore struct {
	Kind FactKind
	// SubjectKind is the subject kind of the fact: a field can declare a different drivers table per subject kind.
	SubjectKind  SubjectKind
	Field        string
	DriversTable string
}

// mintDegradedFactClaimsWithholding is mintDegradedFactClaims with decision K7 applied: a field the lookup says is a
// score is never claimed, because this composition serves no drivers table beside it. withheld lists each such field once
// per fact kind. A nil lookup claims every scalar, as before.
func mintDegradedFactClaimsWithholding(facts []CanonicalFact, claimLimit int, scoreOf func(CanonicalFact, string) (driversTable string, isScore bool)) (claims []ClaimedFact, evidence []string, truncated bool, withheld []withheldScore) {
	omitted := false
	// full is set when the claim limit or the evidence bound is reached: from then on no field is claimed, and the scan
	// goes on only to find every declared score the answer withheld, so the trace names all of them.
	full := false
	seenWithheld := map[string]struct{}{}
	claims = []ClaimedFact{}
	evidence = []string{}
	seenEvidence := map[string]struct{}{}
	for _, fact := range facts {
		fields := make([]string, 0, len(fact.Fields))
		for field := range fact.Fields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			value, ok := scalarFromFactValue(fact.Fields[field])
			if !ok {
				continue
			}
			if scoreOf != nil {
				if table, isScore := scoreOf(fact, field); isScore {
					key := string(fact.Kind) + "/" + string(fact.Subject.Kind) + "/" + field
					if _, seen := seenWithheld[key]; !seen {
						seenWithheld[key] = struct{}{}
						withheld = append(withheld, withheldScore{Kind: fact.Kind, SubjectKind: fact.Subject.Kind, Field: field, DriversTable: table})
					}
					continue
				}
			}
			claim := ClaimedFact{
				ClaimID: fmt.Sprintf("%s%d", degradedFactClaimIDPrefix, len(claims)),
				Kind:    fact.Kind,
				Subject: fact.Subject,
				Field:   field,
				Value:   value,
			}
			if full {
				continue
			}
			if claim.Validate() != nil {
				// A value the contract cannot carry as a claim is left out,
				// and the answer says part of the facts is missing.
				omitted = true
				continue
			}
			added := make([]string, 0, len(fact.EvidenceRefIDs))
			for _, ref := range fact.EvidenceRefIDs {
				if _, seen := seenEvidence[ref]; !seen {
					added = append(added, ref)
				}
			}
			if len(claims) >= claimLimit ||
				len(evidence)+len(added) > contractsv1.ContextFabricEvidenceRefIDsMaxCount {
				full = true
				continue
			}
			for _, ref := range added {
				seenEvidence[ref] = struct{}{}
				evidence = append(evidence, ref)
			}
			claims = append(claims, claim)
		}
	}
	return claims, evidence, omitted || full, withheld
}

// degradedClaimLimit is how many claims a degraded answer may carry: the
// claimed-fact bound, held to what the answer's own item ceiling leaves after
// every item the engine charges besides these claims: the subject candidates,
// the cohort member rows, the one count claim the engine may add, and, when
// the cohort carries a ranked driver (narration's own gate), the narration
// grant, which cohort driver narration spends after composition on its driver
// judgments and their claims. No ceiling in force (a zero allocation) leaves
// the claimed-fact bound alone.
func degradedClaimLimit(input SynthesisInput) int {
	limit := contractsv1.ContextFabricClaimedFactsMaxCount
	if input.Allocation.MaxItems <= 0 {
		return limit
	}
	room := input.Allocation.MaxItems - input.Allocation.MemberRowCommitment - len(input.Graph.Resolution.Candidates) - 1
	if input.Graph.Cohort != nil && cohortHasAnyRankedDriver(input.Graph.Cohort) {
		room -= input.Allocation.NarrationGrant
	}
	if room < limit {
		limit = room
	}
	if limit < 0 {
		limit = 0
	}
	return limit
}

func scalarFromFactValue(v FactValue) (ScalarValue, bool) {
	switch {
	case v.String != nil:
		return ScalarValue{String: v.String}, true
	case v.Integer != nil:
		return ScalarValue{Integer: v.Integer}, true
	case v.Number != nil:
		return ScalarValue{Number: v.Number}, true
	case v.Boolean != nil:
		return ScalarValue{Boolean: v.Boolean}, true
	case v.Null:
		return ScalarValue{Null: true}, true
	}
	return ScalarValue{}, false
}

// composeFailedSynthesisCurrentState is the current_state of a degraded answer
// for a failed model call: the canonical values that were read, named for what
// they are and not as current (the answer may speak for another period), or
// the failure statement when no value could be carried.
func composeFailedSynthesisCurrentState(claims []ClaimedFact) string {
	sentences := claimedFactSentences(claims)
	if len(sentences) == 0 {
		return synthesisFailureCurrentState
	}
	return truncateAtSentenceBoundary("Canonical values read: "+strings.Join(sentences, "; ")+".", currentStateMaxLength)
}

// composeFailureCurrentState is composeFailedSynthesisCurrentState for a class:
// a facts-only class with no value to carry says why no current state is
// written, and never says a model call failed.
func composeFailureCurrentState(claims []ClaimedFact, class SynthesisFailureClass) string {
	state := composeFailedSynthesisCurrentState(claims)
	if state != synthesisFailureCurrentState {
		return state
	}
	switch class {
	case contractsv1.ContextFabricSynthesisFailureDeadline:
		return "No second synthesis was started because the time budget was spent, so no current state is written here; the sources that were read are listed in coverage."
	case contractsv1.ContextFabricSynthesisFailureNotNarrowable:
		return "The written answer was over the response budget and the cohort could not be narrowed further, so no current state is written here; the sources that were read are listed in coverage."
	}
	return state
}

// scoreLookup is the K7 lookup of a synthesizer given the fact capabilities: whether a field of a fact is a declared
// score, and the drivers table it needs. Nil when it was given none, so a hand-built synthesizer claims every scalar.
func scoreLookup(capabilities []FactCapability) func(CanonicalFact, string) (string, bool) {
	if len(capabilities) == 0 {
		return nil
	}
	byKind := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	return func(fact CanonicalFact, field string) (string, bool) {
		capability, ok := byKind[fact.Kind]
		if !ok {
			return "", false
		}
		declaration, declared := capability.FieldDeclaration(field, fact.Subject.Kind)
		if !declared || !declaration.Score {
			return "", false
		}
		return declaration.DriversTable, true
	}
}
