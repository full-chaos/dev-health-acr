package contextfabric

import (
	"context"
	"log/slog"
	"maps"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// withheldBareScoreFields lists the declared score fields of facts whose drivers table the fact does not carry. The
// lookup is the K7 lookup of the degraded composition. A nil lookup withholds nothing.
func withheldBareScoreFields(facts []CanonicalFact, scoreOf func(CanonicalFact, string) (string, bool)) []withheldScore {
	if scoreOf == nil {
		return nil
	}
	var withheld []withheldScore
	for _, fact := range facts {
		for field := range fact.Fields {
			table, isScore := scoreOf(fact, field)
			if !isScore {
				continue
			}
			if _, served := fact.Fields[table]; served && table != "" {
				continue
			}
			withheld = append(withheld, withheldScore{Kind: fact.Kind, SubjectKind: fact.Subject.Kind, Field: field, DriversTable: table})
		}
	}
	return withheld
}

// withholdBareScores is decision K7 at the input: a declared score whose drivers table is not among the fact's fields is
// removed before a model or a client synthesizer sees the facts, so neither can write it. The caller's input is not
// changed.
func (r RuntimeAnswerSynthesizer) withholdBareScores(ctx context.Context, principal storage.Principal, input SynthesisInput) SynthesisInput {
	scoreOf := scoreLookup(r.FactCapabilities)
	if scoreOf == nil {
		return input
	}
	withheld := withheldBareScoreFields(input.Facts.Facts, scoreOf)
	if len(withheld) == 0 {
		return input
	}
	facts := make([]CanonicalFact, len(input.Facts.Facts))
	for index, fact := range input.Facts.Facts {
		fields := maps.Clone(fact.Fields)
		for _, item := range withheld {
			if item.Kind == fact.Kind && item.SubjectKind == fact.Subject.Kind {
				if table, isScore := scoreOf(fact, item.Field); isScore && table == item.DriversTable {
					delete(fields, item.Field)
				}
			}
		}
		fact.Fields = fields
		facts[index] = fact
	}
	input.Facts.Facts = facts
	input.withheldScores = append(append([]withheldScore(nil), input.withheldScores...), withheld...)
	recordScoresWithheld(ctx, principal, "input", withheld)
	return input
}

// withholdBareScoreClaims removes from a draft every claim of a declared score whose drivers table is not among the
// facts the answer is composed from: defence in depth beside withholdBareScores, for a score that reached the writer
// another way (a caller's own draft). A driver loses its citation of a removed claim.
func (r RuntimeAnswerSynthesizer) withholdBareScoreClaims(ctx context.Context, principal storage.Principal, input SynthesisInput, draft SynthesisDraft) SynthesisDraft {
	scoreOf := scoreLookup(r.FactCapabilities)
	if scoreOf == nil || len(draft.ClaimedFacts) == 0 {
		return draft
	}
	var withheld []withheldScore
	removed := map[string]struct{}{}
	kept := make([]ClaimedFact, 0, len(draft.ClaimedFacts))
	for _, claim := range draft.ClaimedFacts {
		table, isScore := scoreOf(CanonicalFact{Kind: claim.Kind, Subject: claim.Subject}, claim.Field)
		if isScore && !driversTableServed(input.Facts.Facts, claim, table) {
			removed[claim.ClaimID] = struct{}{}
			withheld = append(withheld, withheldScore{Kind: claim.Kind, SubjectKind: claim.Subject.Kind, Field: claim.Field, DriversTable: table})
			continue
		}
		kept = append(kept, claim)
	}
	if len(withheld) == 0 {
		return draft
	}
	draft.ClaimedFacts = kept
	draft.Drivers = cloneSlice(draft.Drivers)
	for index := range draft.Drivers {
		ids := make([]string, 0, len(draft.Drivers[index].ClaimedFactIDs))
		for _, id := range draft.Drivers[index].ClaimedFactIDs {
			if _, gone := removed[id]; !gone {
				ids = append(ids, id)
			}
		}
		draft.Drivers[index].ClaimedFactIDs = ids
	}
	draft.scoresWithheld = append(append([]withheldScore(nil), draft.scoresWithheld...), withheld...)
	recordScoresWithheld(ctx, principal, "claim", withheld)
	return draft
}

// driversTableServed reports whether a fact the claim is about carries the drivers table beside it.
func driversTableServed(facts []CanonicalFact, claim ClaimedFact, table string) bool {
	if table == "" {
		return false
	}
	for _, fact := range facts {
		if fact.Kind == claim.Kind && fact.Subject.Kind == claim.Subject.Kind && fact.Subject.CanonicalID == claim.Subject.CanonicalID {
			if _, served := fact.Fields[table]; served {
				return true
			}
		}
	}
	return false
}

// recordScoresWithheld logs, at Info, which score fields an answer was kept from and the drivers table each needed:
// the response names the fact kinds, the trace names the fields and tables. stage is where it happened: input or claim.
func recordScoresWithheld(ctx context.Context, principal storage.Principal, stage string, withheld []withheldScore) {
	attrs := []any{"org_id", SanitizeLogAttr(principal.OrgID), "stage", SanitizeLogAttr(stage), "withheld", len(withheld), "reason", "drivers_not_served"}
	for index, item := range withheld {
		if index >= 8 {
			break
		}
		attrs = append(attrs, "field_"+strconv.Itoa(index), SanitizeLogAttr(string(item.Kind)+"."+item.Field+"@"+string(item.SubjectKind)), "drivers_table_"+strconv.Itoa(index), SanitizeLogAttr(item.DriversTable))
	}
	slog.Default().InfoContext(ctx, "context fabric synthesis withheld scores without their drivers", attrs...)
}
