package devhealthfacts

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The repository-scope investment fact read from ops (CHAOS-12690 I1a). With
// ProviderOptions.InvestmentRepositoryFromOps on, a repository's mix is the
// ops answer of investmentBreakdownRepository (THEME and SUBCATEGORY under
// ISSUE_SIZE plus evidenceQualityStats), the split of a unit across
// repositories being the ops one (commit churn, then PR churn;
// work_unit_repo_effort). ACR writes no SQL for it; the PR-ref count rule of
// repoSplitCoreScoped is not used. Default off: nothing here runs.
const (
	repoOpsSource = "ops.investmentBreakdownRepository"
	repoOpsBasis  = "ops_repo_effort_lines_changed"

	// repositoryOpsReadCap is the most repositories one read asks ops for:
	// each costs two calls. A repository past it is named, never dropped.
	repositoryOpsReadCap = 10

	repoOpsWithheldPrefix = "investment_repository_from_ops_withheld"

	RepoOpsWithheldCap      = "read_cap"
	RepoOpsWithheldNoData   = "no_data"
	RepoOpsWithheldNoSizing = "sizing_coverage_not_served"
)

// repoOpsWithheld is why one repository carries no fact, or no sizing
// coverage. The reason is a coverage reason: missing is not zero.
type repoOpsWithheld struct {
	subject contextfabric.SubjectRef
	reason  string
	call    string
}

func (w repoOpsWithheld) coverageReason() string {
	reason := w.reason
	if w.call != "" {
		reason += ", call " + w.call
	}
	return repoOpsWithheldPrefix + ": " + reason + "; it is missing, not zero"
}

// evidenceQualityAnswer is the part of an ops analytics answer that holds the
// unit counts. SizedUnitCount is absent until the pinned ops schema serves it.
type evidenceQualityAnswer struct {
	Analytics *struct {
		EvidenceQualityStats *struct {
			Total              *int64 `json:"total"`
			UncategorizedCount *int64 `json:"uncategorizedCount"`
			SizedUnitCount     *int64 `json:"sizedUnitCount"`
		} `json:"evidenceQualityStats"`
	} `json:"analytics"`
}

type opsUnitCounts struct {
	total, uncategorized int64
	sized                *int64
}

func parseEvidenceQuality(data json.RawMessage) (opsUnitCounts, bool) {
	var answer evidenceQualityAnswer
	if err := json.Unmarshal(data, &answer); err != nil || answer.Analytics == nil || answer.Analytics.EvidenceQualityStats == nil {
		return opsUnitCounts{}, false
	}
	stats := answer.Analytics.EvidenceQualityStats
	if stats.Total == nil || stats.UncategorizedCount == nil || *stats.Total < 0 || *stats.UncategorizedCount < 0 || *stats.UncategorizedCount > *stats.Total {
		return opsUnitCounts{}, false
	}
	counts := opsUnitCounts{total: *stats.Total, uncategorized: *stats.UncategorizedCount}
	if stats.SizedUnitCount != nil && *stats.SizedUnitCount >= 0 && *stats.SizedUnitCount <= *stats.Total-*stats.UncategorizedCount {
		counts.sized = stats.SizedUnitCount
	}
	return counts, true
}

// readRepositoryMixFromOps emits the repository-scope investment fact from the
// ops answers, and returns the repositories it could not read or serve.
func (p *InvestmentProvider) readRepositoryMixFromOps(ctx context.Context, principal storage.Principal, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, bound factTimeBound) (rejected int, withheld []repoOpsWithheld) {
	ids, bySubject, rejected := subjectIndex(subjects, repositoryPrefix)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for index, id := range ids {
		subject := bySubject[id]
		if index >= repositoryOpsReadCap {
			withheld = append(withheld, repoOpsWithheld{subject: subject, reason: RepoOpsWithheldCap})
			continue
		}
		fact, notes := p.repositoryFactFromOps(ctx, principal, subject, bound)
		withheld = append(withheld, notes...)
		if fact != nil {
			*facts = append(*facts, *fact)
		}
	}
	return rejected, withheld
}

func (p *InvestmentProvider) repositoryFactFromOps(ctx context.Context, principal storage.Principal, subject contextfabric.SubjectRef, bound factTimeBound) (*contextfabric.CanonicalFact, []repoOpsWithheld) {
	why := func(reason, call string) []repoOpsWithheld {
		return []repoOpsWithheld{{subject: subject, reason: reason, call: call}}
	}
	startDate, endDate, ok := subcategoryBreakdownDates(bound)
	if !ok {
		return nil, why(SubcategoryWithheldWindow, "")
	}
	if !p.operations.IsSet() {
		return nil, why(SubcategoryWithheldCallerUnset, "")
	}
	answers := make(map[string]map[string]float64, 2)
	var counts opsUnitCounts
	for _, dimension := range []string{"THEME", "SUBCATEGORY"} {
		outcome, err := p.operations.CallOperation(ctx, principal, InvestmentBreakdownRepositoryOperationName, subcategoryBreakdownVariables(dimension, "REPO", subject.CanonicalID, startDate, endDate))
		switch {
		case err != nil:
			return nil, why(SubcategoryWithheldCallFailed, "")
		case !outcome.Served:
			call := "unknown"
			if closedCallStatus.MatchString(outcome.Reason) {
				call = outcome.Reason
			}
			return nil, why(SubcategoryWithheldNotServed, call)
		case !outcome.Complete:
			return nil, why(SubcategoryWithheldIncomplete, "")
		}
		values, readable := parseInvestmentBreakdown(outcome.Data, dimension)
		if !readable {
			return nil, why(SubcategoryWithheldUnreadable, "")
		}
		answers[dimension] = values
		if dimension == "THEME" {
			var countsOK bool
			if counts, countsOK = parseEvidenceQuality(outcome.Data); !countsOK {
				return nil, why(SubcategoryWithheldUnreadable, "")
			}
		}
	}
	themes, subcategories := answers["THEME"], answers["SUBCATEGORY"]
	for key, value := range themes {
		if !slices.Contains(canonicalInvestmentThemes[:], key) && value > 0 {
			return nil, why(SubcategoryWithheldUnknownKey, "")
		}
	}
	total := 0.0
	for _, theme := range canonicalInvestmentThemes {
		total += themes[theme]
	}
	categorized := counts.total - counts.uncategorized
	recordInvestmentUncategorized(ctx, subject, categorized, counts.uncategorized, false)
	var notes []repoOpsWithheld
	if total <= 0 {
		return nil, why(RepoOpsWithheldNoData, "")
	}
	fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+8)
	totals := &repoThemeTotals{theme: make(map[string]float64, len(canonicalInvestmentThemes))}
	for _, theme := range canonicalInvestmentThemes {
		totals.theme[theme] = themes[theme]
		fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(themes[theme] / total))
	}
	fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(subcategories["quality.bugfix"] / total))
	fields["work_unit_count"] = contextfabric.IntegerFactValue(categorized)
	if counts.sized != nil {
		investmentSizingFields(fields, total, categorized, *counts.sized)
	} else {
		fields["measure"] = contextfabric.StringFactValue(investmentMeasureIssueSize)
		fields["denominator"] = contextfabric.NumberFactValue(roundMixEffort(total))
		notes = append(notes, repoOpsWithheld{subject: subject, reason: RepoOpsWithheldNoSizing})
	}
	investmentUncategorizedFields(fields, categorized, counts.uncategorized)
	fields["mix_source"] = contextfabric.StringFactValue(repoOpsSource)
	fields["attribution_basis"] = contextfabric.StringFactValue(repoOpsBasis)
	fields["theme_breakdown"] = repoOpsThemeTable(totals, total, bound.effectiveGrain(grainDaily))
	repoID := subject.CanonicalID[len(repositoryPrefix):]
	return &contextfabric.CanonicalFact{
		Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
		EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, repoID)},
	}, notes
}

// repoOpsThemeTable is themeBreakdownTable over the ops answer, with the
// ops-read provenance.
func repoOpsThemeTable(t *repoThemeTotals, total float64, grain contextfabric.TemporalGrain) contextfabric.FactValue {
	rows := make([]contextfabric.FactValueRow, 0, len(canonicalInvestmentThemes))
	for _, theme := range canonicalInvestmentThemes {
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			"theme":           contextfabric.StringFactValue(theme),
			"share":           contextfabric.NumberFactValue(t.theme[theme] / total),
			"weighted_effort": contextfabric.NumberFactValue(t.theme[theme]),
			"source":          contextfabric.StringFactValue(repoOpsSource),
			"attribution":     contextfabric.StringFactValue(repoOpsBasis),
		}})
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"theme"},
		Measures:     []string{"share", "weighted_effort"},
		Observations: []string{"source", "attribution"},
		Grain:        grain,
		Rows:         rows,
	})
}
