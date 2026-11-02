package v1

import (
	"fmt"
	"strings"
)

// validateSankeyShape holds a sankey to what one team's effort flowing from its
// repositories to the themes means (CHAOS-12578). Every point has already
// resolved to its source and equals it exactly. What is left:
//
//   - the rule and the kind name each other;
//   - the claim's subject is a team: the table splits that team's effort (codex
//     r1 on #1939). Which question asks for it is the producer's rule
//     (CHAOS-12582), not this check's;
//   - the points are ALL the links of ONE claim's declared
//     repository_theme_breakdown table that carry effort, each once: a link left
//     out makes the chart a different claim, and a zero link draws nothing (it
//     stays in the value table);
//   - a series is one theme, keyed by its token and labelled with its served
//     word, and a point is one of that theme's links, labelled with its
//     repository's served word;
//   - a node is a repository or a theme: a repository row names a repository
//     canonical id, never a person, and no two nodes share a word, so no link can
//     join a node to itself;
//   - between 2 and 64 repositories carry effort: past 64 the shape is refused,
//     never folded into an "other" node (lead ruling on CHAOS-4610);
//   - per theme, the links add up to the team's effort in that theme within
//     ContextFabricRenderShareSumTolerance. Every plotted number stays exact.
func validateSankeyShape(shape ContextFabricRenderShape, sources renderShapeSources) error {
	if (shape.Kind == ContextFabricRenderKindSankey) != (shape.SelectedBy == ContextFabricRenderRuleInvestmentRepositoryFlow) {
		return fmt.Errorf("render shape %q: selected_by %q cannot yield kind %q", shape.ShapeID, shape.SelectedBy, shape.Kind)
	}
	if shape.Kind != ContextFabricRenderKindSankey {
		return nil
	}
	if shape.AxisKind != ContextFabricRenderAxisCategory {
		return fmt.Errorf("render shape %q: a sankey needs a category axis", shape.ShapeID)
	}
	if shape.ValueUnit != "" {
		return fmt.Errorf("render shape %q: a sankey plots weighted effort, which has no value_unit", shape.ShapeID)
	}
	claim := shape.Series[0].Points[0].Source.ClaimID
	if _, declared := sources.repositoryFlowClaims[claim]; !declared {
		return fmt.Errorf("render shape %q: a sankey plots a declared %q table", shape.ShapeID, ContextFabricRenderRepositoryFlowTableField)
	}
	if subject := sources.claimValues[claim].subject.Kind; subject != ContextFabricSubjectTeam {
		return fmt.Errorf("render shape %q: a sankey plots one team's effort, and its claim's subject is a %q", shape.ShapeID, subject)
	}
	table := sources.factRows[claim]
	flows, err := sankeyFlowsOf(shape.ShapeID, table)
	if err != nil {
		return err
	}
	if len(shape.Series) != len(flows.themeEffort) {
		return fmt.Errorf("render shape %q: a sankey holds one series per theme with effort, %d, and has %d", shape.ShapeID, len(flows.themeEffort), len(shape.Series))
	}
	plotted := make(map[int]struct{}, flows.links)
	for _, series := range shape.Series {
		total, hasEffort := flows.themeEffort[series.Key]
		if !hasEffort {
			return fmt.Errorf("render shape %q series %q: a sankey series is a theme with effort", shape.ShapeID, series.Key)
		}
		if word, _ := ContextFabricInvestmentThemeLabel(series.Key); series.Label != word {
			return fmt.Errorf("render shape %q series %q: a theme's label must be its served word", shape.ShapeID, series.Key)
		}
		sum := 0.0
		for _, point := range series.Points {
			source := point.Source
			// The kind is what makes RowIndex safe to read below: the resolver
			// has already refused a claimed_fact_row source with no row index.
			if source.Kind != ContextFabricRenderSourceClaimedFactRow || source.Field != ContextFabricRenderEffortField {
				return fmt.Errorf("render shape %q point %q: a sankey point must cite the %q field of a claimed fact row", shape.ShapeID, point.Label, ContextFabricRenderEffortField)
			}
			if source.ClaimID != claim {
				return fmt.Errorf("render shape %q point %q: a sankey plots one claim", shape.ShapeID, point.Label)
			}
			if _, repeated := plotted[*source.RowIndex]; repeated {
				return fmt.Errorf("render shape %q point %q: a sankey plots each link once", shape.ShapeID, point.Label)
			}
			plotted[*source.RowIndex] = struct{}{}
			link := flows.byRow[*source.RowIndex]
			if link.theme != series.Key {
				return fmt.Errorf("render shape %q point %q: its link's theme %q is not its series %q", shape.ShapeID, point.Label, link.theme, series.Key)
			}
			if point.Label != link.word {
				return fmt.Errorf("render shape %q point %q: a link's label must be the served word of its repository", shape.ShapeID, point.Label)
			}
			if !(point.Value > 0) {
				return fmt.Errorf("render shape %q point %q: a sankey link carries effort above 0", shape.ShapeID, point.Label)
			}
			sum += point.Value
		}
		if !partsSumToWhole(sum, total) {
			return fmt.Errorf("render shape %q series %q: its links add up to %v, and the team's effort in that theme is %v", shape.ShapeID, series.Key, sum, total)
		}
	}
	if len(plotted) != flows.links {
		return fmt.Errorf("render shape %q: a sankey plots every link with effort, and has %d of %d", shape.ShapeID, len(plotted), flows.links)
	}
	return nil
}

// sankeyLink is one row of a repository_theme_breakdown table.
type sankeyLink struct {
	repository string
	word       string
	theme      string
	effort     float64
}

// sankeyFlows is what a repository_theme_breakdown table holds, checked.
type sankeyFlows struct {
	byRow map[int]sankeyLink
	// themeEffort maps each theme that has a link with effort to the team's
	// served effort in that theme.
	themeEffort map[string]float64
	// links counts the rows with effort above 0: the links a sankey draws.
	links int
}

// sankeyFlowsOf reads a repository_theme_breakdown table and refuses one a sankey
// cannot draw truthfully: a row that is not a repository's link to a canonical
// theme, two words for one repository or one word for two nodes, a link twice,
// a theme effort the rows disagree on, or a count of repositories with effort
// outside 2..64.
func sankeyFlowsOf(shapeID string, table []ContextFabricClaimedFactRow) (sankeyFlows, error) {
	flows := sankeyFlows{byRow: make(map[int]sankeyLink, len(table)), themeEffort: map[string]float64{}}
	words := map[string]string{}  // repository id -> its word
	owners := map[string]string{} // word -> the node that holds it
	seen := map[[2]string]struct{}{}
	themeTotal := map[string]float64{}
	withEffort := map[string]struct{}{}
	for index, row := range table {
		repository, word, theme := row.Fields[ContextFabricRenderRepositoryField].String, row.Fields[ContextFabricRenderRepositoryWordField].String, row.Fields[ContextFabricRenderThemeField].String
		effort, total := row.Fields[ContextFabricRenderEffortField].Number, row.Fields[ContextFabricRenderThemeEffortField].Number
		if repository == nil || word == nil || theme == nil || effort == nil || total == nil {
			return sankeyFlows{}, fmt.Errorf("render shape %q: link row %d lacks a repository, its word, a theme or an effort", shapeID, index)
		}
		if !strings.HasPrefix(*repository, ContextFabricRepositoryCanonicalIDPrefix) || len(*repository) == len(ContextFabricRepositoryCanonicalIDPrefix) {
			return sankeyFlows{}, fmt.Errorf("render shape %q: link row %d's node is not a repository", shapeID, index)
		}
		themeWord, canonical := ContextFabricInvestmentThemeLabel(*theme)
		if !canonical {
			return sankeyFlows{}, fmt.Errorf("render shape %q: link row %d's theme is not a canonical theme", shapeID, index)
		}
		if strings.TrimSpace(*word) == "" {
			return sankeyFlows{}, fmt.Errorf("render shape %q: link row %d's repository has no word", shapeID, index)
		}
		if known, ok := words[*repository]; ok && known != *word {
			return sankeyFlows{}, fmt.Errorf("render shape %q: a repository is named by two words", shapeID)
		}
		words[*repository] = *word
		for _, node := range [2][2]string{{*word, *repository}, {themeWord, "theme:" + *theme}} {
			if holder, taken := owners[node[0]]; taken && holder != node[1] {
				return sankeyFlows{}, fmt.Errorf("render shape %q: two nodes share the word %q", shapeID, node[0])
			}
			owners[node[0]] = node[1]
		}
		key := [2]string{*repository, *theme}
		if _, repeated := seen[key]; repeated {
			return sankeyFlows{}, fmt.Errorf("render shape %q: a link appears twice", shapeID)
		}
		seen[key] = struct{}{}
		if !(*effort >= 0) || !(*total >= 0) {
			return sankeyFlows{}, fmt.Errorf("render shape %q: link row %d carries a negative effort", shapeID, index)
		}
		if known, ok := themeTotal[*theme]; ok && known != *total {
			return sankeyFlows{}, fmt.Errorf("render shape %q: the rows disagree on the team's effort in theme %q", shapeID, *theme)
		}
		themeTotal[*theme] = *total
		flows.byRow[index] = sankeyLink{repository: *repository, word: *word, theme: *theme, effort: *effort}
		if *effort > 0 {
			flows.links++
			flows.themeEffort[*theme] = *total
			withEffort[*repository] = struct{}{}
		}
	}
	for theme, total := range themeTotal {
		if _, linked := flows.themeEffort[theme]; !linked && total > 0 {
			return sankeyFlows{}, fmt.Errorf("render shape %q: theme %q has effort and no link with effort", shapeID, theme)
		}
	}
	if len(withEffort) < ContextFabricRenderRepositoryFlowMinRepositories || len(withEffort) > ContextFabricRenderRepositoryFlowMaxRepositories {
		return sankeyFlows{}, fmt.Errorf("render shape %q: a sankey draws %d to %d repositories with effort, and this table has %d", shapeID, ContextFabricRenderRepositoryFlowMinRepositories, ContextFabricRenderRepositoryFlowMaxRepositories, len(withEffort))
	}
	return flows, nil
}
