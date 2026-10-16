package contextfabric

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strconv"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// withholdBareScores is decision K7 at the input: a declared score whose drivers table is not among the fields of the
// SAME fact is removed before a model or a client synthesizer sees the facts, so neither can write it. Another subject's
// fact of the same kind is judged on its own fields. The caller's input is not changed.
func (r RuntimeAnswerSynthesizer) withholdBareScores(ctx context.Context, principal storage.Principal, input SynthesisInput) SynthesisInput {
	scoreOf := scoreLookup(r.FactCapabilities)
	if scoreOf == nil {
		return input
	}
	var withheld []withheldScore
	var facts []CanonicalFact
	for index, fact := range input.Facts.Facts {
		var bare []string
		for field := range fact.Fields {
			table, isScore := scoreOf(fact, field)
			if !isScore {
				continue
			}
			if _, served := fact.Fields[table]; served && table != "" {
				continue
			}
			bare = append(bare, field)
			withheld = append(withheld, withheldScore{Kind: fact.Kind, SubjectKind: fact.Subject.Kind, Field: field, DriversTable: table})
		}
		if len(bare) == 0 {
			continue
		}
		if facts == nil {
			facts = slices.Clone(input.Facts.Facts)
		}
		fields := maps.Clone(fact.Fields)
		for _, field := range bare {
			delete(fields, field)
		}
		fact.Fields = fields
		facts[index] = fact
	}
	if len(withheld) == 0 {
		return input
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
		// A derived claim is computed from other claims' values and never carries a table, so a derived claim of a score is
		// never served with its drivers.
		if isScore && (len(claim.DerivedFrom) > 0 || !driversTableServed(input.Facts.Facts, claim, table)) {
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
	var droppedDrivers, droppedFindings int
	draft.Drivers, droppedDrivers = dropItemsLeftWithoutTheirClaim(ctx, draft.Drivers, removed, "driver",
		func(driver DriverJudgment) (string, []string) { return driver.Category, driver.ClaimedFactIDs },
		func(driver *DriverJudgment, ids []string) { driver.ClaimedFactIDs = ids })
	draft.RemainingWork, droppedFindings = dropFindingsLeftWithoutTheirClaim(ctx, draft.RemainingWork, removed)
	var dropped int
	draft.ReadinessGaps, dropped = dropFindingsLeftWithoutTheirClaim(ctx, draft.ReadinessGaps, removed)
	droppedFindings += dropped
	draft.Conflicts, dropped = dropFindingsLeftWithoutTheirClaim(ctx, draft.Conflicts, removed)
	droppedFindings += dropped
	if recorder, ok := r.Telemetry.(scoreDependentItemDropRecorder); ok && droppedDrivers+droppedFindings > 0 {
		recorder.RecordScoreDependentItemsDropped(ctx, principal, droppedDrivers, droppedFindings)
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

// scoreDependentItemDropRecorder is the optional telemetry for drivers and findings left out of an answer because the only
// claim they rested on was a score withheld for want of its drivers table.
type scoreDependentItemDropRecorder interface {
	RecordScoreDependentItemsDropped(ctx context.Context, principal storage.Principal, drivers, findings int)
}

// RecordScoreDependentItemsDropped implements scoreDependentItemDropRecorder. Content-safe: an org id and two counts.
func (t SlogEngineTelemetry) RecordScoreDependentItemsDropped(ctx context.Context, principal storage.Principal, drivers, findings int) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "cf_score_dependent_drivers_dropped", drivers, "cf_score_dependent_findings_dropped", findings}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer left out items resting only on a withheld score", args...)
}

// dropItemsLeftWithoutTheirClaim removes each removed claim ID from an item's citations and drops an item that cited one,
// is left with no claim, and is of a category that must cite a claimed fact. An item that keeps another claim, or never
// needed one, stays. It returns the kept items and how many it dropped.
func dropItemsLeftWithoutTheirClaim[T any](ctx context.Context, items []T, removed map[string]struct{}, noun string, read func(T) (category string, ids []string), write func(*T, []string)) ([]T, int) {
	kept := make([]T, 0, len(items))
	dropped := 0
	for _, item := range items {
		category, ids := read(item)
		left := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, gone := removed[id]; !gone {
				left = append(left, id)
			}
		}
		write(&item, left)
		if _, required := contractsv1.ContextFabricDriverCategoryRequiresClaimedFact(contractsv1.ContextFabricDriverCategory(category)); required && len(left) == 0 && len(left) < len(ids) {
			dropped++
			slog.Default().DebugContext(ctx, "context fabric answer item left out: its only claim was a withheld score",
				"item", SanitizeLogAttr(noun), "category", SanitizeLogAttr(category), "reason", "drivers_not_served")
			continue
		}
		kept = append(kept, item)
	}
	return kept, dropped
}

func dropFindingsLeftWithoutTheirClaim(ctx context.Context, findings []Finding, removed map[string]struct{}) ([]Finding, int) {
	return dropItemsLeftWithoutTheirClaim(ctx, cloneSlice(findings), removed, "finding",
		func(finding Finding) (string, []string) { return finding.Kind, finding.ClaimedFactIDs },
		func(finding *Finding, ids []string) { finding.ClaimedFactIDs = ids })
}
