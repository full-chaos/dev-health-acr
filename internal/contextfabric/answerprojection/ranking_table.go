package answerprojection

import (
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// rankingTableTopDrivers is the number of a member's own drivers the Rows
// panel surfaces per row -- "never a bare score": every scored row also
// carries the strongest evidence behind it, not just the number.
const rankingTableTopDrivers = 2

// buildRankingTable builds the Rows-panel rendering of a cohort's ranking
// (CHAOS-4398 PR3, design doc §4a/§8): one row per member RankCohort
// actually ranked (RankingComputed true), in AttentionRank order, from
// fields ContextFabricCohortMember/ContextFabricCohortMemberDriver already
// carry -- never re-derived, re-scored, or re-worded here. members is the
// ALREADY-projected (budget/evidence-cut) canonical member set, so a row
// never names a team the caller's Members list does not also carry.
//
// Returns nil when no member was ranked (RankCohort never ran for this
// cohort, or every ranked member was cut by the projection budget), the
// same "not computed" distinction RankingComputed itself makes.
func buildRankingTable(members []contractsv1.ContextFabricCohortMember) []contractsv1.ContextFabricClaimedFactRow {
	ranked := make([]contractsv1.ContextFabricCohortMember, 0, len(members))
	for _, member := range members {
		if member.RankingComputed {
			ranked = append(ranked, member)
		}
	}
	if len(ranked) == 0 {
		return nil
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].AttentionRank < ranked[j].AttentionRank
	})
	rows := make([]contractsv1.ContextFabricClaimedFactRow, 0, len(ranked))
	for _, member := range ranked {
		rows = append(rows, rankingTableRow(member))
	}
	return rows
}

func rankingTableRow(member contractsv1.ContextFabricCohortMember) contractsv1.ContextFabricClaimedFactRow {
	fields := map[string]contractsv1.ContextFabricScalarValue{
		"team_canonical_id": stringScalar(member.Subject.CanonicalID),
		"team_label":        stringScalar(member.Subject.Label),
		"attention_rank":    intScalar(int64(member.AttentionRank)),
		"outcome":           stringScalar(string(member.Outcome)),
		// Score is ALWAYS present, even null -- "never a bare score" cuts
		// both ways: a row never shows a number with no outcome next to it
		// (outcome is always the field above), and it never silently omits
		// the key when there is no score either, so a consumer can tell
		// "no score" from "field not rendered".
		"score": scoreScalar(member.Score),
		// window is member-level, not per-driver: today only investment_mix
		// ever carries "current_vs_prior" (ContextFabricCohortMemberDriver.Window's
		// own doc comment), so one row-level column -- "current_vs_prior" iff
		// ANY of this member's drivers used a prior-window comparison, else
		// "current" -- is a real, deterministic summary, not a fabricated one.
		"window": stringScalar(string(rowWindow(member.Drivers))),
	}
	for i, driver := range topDriversByWeightContributed(member.Drivers, rankingTableTopDrivers) {
		n := i + 1
		fields[fmt.Sprintf("driver_%d_signal", n)] = stringScalar(driver.Signal)
		fields[fmt.Sprintf("driver_%d_value", n)] = numberScalar(driver.Value)
		fields[fmt.Sprintf("driver_%d_weight_contributed", n)] = numberScalar(driver.WeightContributed)
		if len(driver.ThresholdLabels) > 0 {
			// The row carries the FIRST claimed threshold label only --
			// ThresholdLabels itself (every label the driver claimed) stays
			// available on the canonical/projected member for a caller that
			// needs the full set; the row is a summary, not a replacement.
			fields[fmt.Sprintf("driver_%d_threshold_label", n)] = stringScalar(driver.ThresholdLabels[0])
		}
	}
	return contractsv1.ContextFabricClaimedFactRow{Fields: fields}
}

// rowWindow summarizes a member's per-driver Window values into the single
// row-level "window" column: "current_vs_prior" iff any driver used a
// prior-window comparison, "current" otherwise (including when the member
// has no drivers at all -- a row that scored without carrying drivers has
// nothing to compare against a prior window).
func rowWindow(drivers []contractsv1.ContextFabricCohortMemberDriver) contractsv1.ContextFabricCohortMemberDriverWindow {
	for _, driver := range drivers {
		if driver.Window == contractsv1.ContextFabricCohortMemberDriverWindowCurrentVsPrior {
			return contractsv1.ContextFabricCohortMemberDriverWindowCurrentVsPrior
		}
	}
	return contractsv1.ContextFabricCohortMemberDriverWindowCurrent
}

// topDriversByWeightContributed returns up to n drivers, ordered by
// WeightContributed descending (ties broken by Signal for determinism) --
// the strongest evidence behind the score, never a re-judgment of it.
func topDriversByWeightContributed(drivers []contractsv1.ContextFabricCohortMemberDriver, n int) []contractsv1.ContextFabricCohortMemberDriver {
	ordered := append([]contractsv1.ContextFabricCohortMemberDriver(nil), drivers...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].WeightContributed != ordered[j].WeightContributed {
			return ordered[i].WeightContributed > ordered[j].WeightContributed
		}
		return ordered[i].Signal < ordered[j].Signal
	})
	if len(ordered) > n {
		ordered = ordered[:n]
	}
	return ordered
}

func stringScalar(value string) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{String: &value}
}

func intScalar(value int64) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{Integer: &value}
}

func numberScalar(value float64) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{Number: &value}
}

func scoreScalar(score *float64) contractsv1.ContextFabricScalarValue {
	if score == nil {
		return contractsv1.ContextFabricScalarValue{Null: true}
	}
	return numberScalar(*score)
}
