package v1

import (
	"fmt"
	"strings"
)

// ContextFabricClaimedInvestmentMix (CHAOS-12160) says which attribution
// produced the value of an investment claim about a project, and over how
// many work units. CHAOS-5930 decision A makes a project's mix a DISCLOSED
// roll-up of its owning teams' repositories when the project has no
// measurable work of its own in the window, and CHAOS-5935 replaces the
// roll-up with the project's own mix when it has some. A served claim must
// say which of the two its number is: a roll-up stated as the project's own
// investment answers the nearest measurable question instead of the asked
// one. The server sets it from the claim's grounding fact; it is never
// model-authored. Additive and optional: absent on a claim of another kind, on
// a team, repository or organization claim, and on an answer stored before it
// existed.
type ContextFabricClaimedInvestmentMix struct {
	// InvestmentMixSource is the grounding fact's investment_mix_source.
	InvestmentMixSource ContextFabricInvestmentMixSource `json:"investment_mix_source"`
	// RollupBasis is the grounding fact's rollup_basis: the join the value
	// was computed over.
	RollupBasis string `json:"rollup_basis"`
	// WorkUnitCount is the number of work units the value is computed over in
	// the read window: the owning teams' units for owning_team_rollup, the
	// project's own for project_native.
	WorkUnitCount int64 `json:"work_unit_count"`
	// ProjectOwnWorkUnitCount is the number of the project's own work units
	// in the read window. For owning_team_rollup it is why the roll-up is
	// served: 0, or units with no measurable effort.
	ProjectOwnWorkUnitCount int64 `json:"project_own_work_unit_count"`
}

// ContextFabricInvestmentMixSource is the closed vocabulary of the
// attribution behind a project's investment mix.
type ContextFabricInvestmentMixSource string

const (
	// ContextFabricInvestmentMixSourceProjectNative: the project's own work
	// units (its issues' linked work).
	ContextFabricInvestmentMixSourceProjectNative ContextFabricInvestmentMixSource = "project_native"
	// ContextFabricInvestmentMixSourceOwningTeamRollup: the work units of the
	// repositories the project's owning teams own, served because the project
	// has no measurable work of its own in the window.
	ContextFabricInvestmentMixSourceOwningTeamRollup ContextFabricInvestmentMixSource = "owning_team_rollup"
)

// ContextFabricInvestmentMixSources lists the closed vocabulary.
func ContextFabricInvestmentMixSources() []ContextFabricInvestmentMixSource {
	return []ContextFabricInvestmentMixSource{ContextFabricInvestmentMixSourceProjectNative, ContextFabricInvestmentMixSourceOwningTeamRollup}
}

// ValidContextFabricInvestmentMixSource reports a member of the vocabulary.
func ValidContextFabricInvestmentMixSource(source ContextFabricInvestmentMixSource) bool {
	for _, known := range ContextFabricInvestmentMixSources() {
		if source == known {
			return true
		}
	}
	return false
}

func (m ContextFabricClaimedInvestmentMix) validate(bounds contextFabricBounds) error {
	if !bounds.additiveOK(ValidContextFabricInvestmentMixSource(m.InvestmentMixSource), string(m.InvestmentMixSource)) {
		return fmt.Errorf("investment_mix_source %q is not a member of the closed vocabulary", m.InvestmentMixSource)
	}
	if !stringLengthBetween(m.RollupBasis, 1, ContextFabricClaimedFieldMaxLength) || strings.TrimSpace(m.RollupBasis) != m.RollupBasis {
		return fmt.Errorf("rollup_basis violates v1 bounds")
	}
	if m.WorkUnitCount < 0 || m.ProjectOwnWorkUnitCount < 0 {
		return fmt.Errorf("work unit counts must not be negative")
	}
	return nil
}

// validateClaimedInvestmentMix admits a mix disclosure only on an investment
// claim: it describes how an investment value was attributed.
func validateClaimedInvestmentMix(kind ContextFabricFactKind, mix *ContextFabricClaimedInvestmentMix, bounds contextFabricBounds) error {
	if mix == nil {
		return nil
	}
	if kind != ContextFabricFactInvestment {
		return fmt.Errorf("investment_mix is set on a %q claim; it describes an investment value only", kind)
	}
	return mix.validate(bounds)
}
