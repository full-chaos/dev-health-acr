package answerprojection

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// projectRenderShapes carries the canonical result's render shapes
// (CHAOS-4415) into the bounded projection, dropping any shape whose
// numbers the projection no longer lets a reader check.
//
// A shape is admitted WHOLE or not at all. Dropping individual points would
// silently change what a chart says -- a bar chart missing its top team is
// a different claim about the cohort, not a shorter one -- so the citing
// SHAPE is dropped instead, the same rule projectDrivers already applies to
// a driver whose evidence would not fit. Every drop is counted so
// ProjectionBudget stays the honest record of what a caller is not reading.
//
// The projection re-checks resolvability rather than trusting that a
// canonical shape stays valid here, because the two documents genuinely
// differ: the budget may cut a cohort member or a key fact the shape cites,
// and a projected cohort member carries no Drivers array at all (its driver
// numbers survive as ranking-table cells). Shipping a shape that
// ContextFabricAnswerProjection.Validate would then reject is the one
// outcome this function exists to make impossible.
func projectRenderShapes(
	result contractsv1.ContextFabricInvestigationResult,
	cohort *contractsv1.ContextFabricProjectedCohort,
	facts []contractsv1.ContextFabricProjectedFact,
) (shapes []contractsv1.ContextFabricRenderShape, omitted int) {
	if len(result.RenderShapes) == 0 {
		return nil, 0
	}
	available := projectedRenderSources{cohort: cohort, facts: facts}
	for _, shape := range result.RenderShapes {
		if !available.carriesEveryPoint(shape) {
			omitted++
			continue
		}
		shapes = append(shapes, cloneRenderShape(shape))
	}
	return shapes, omitted
}

// cloneRenderShape deep-copies a shape so the projection and the canonical
// result never share a Series or Points backing array.
//
// Copying the struct alone is NOT enough and looks like it is: a
// ContextFabricRenderShape holds slice headers, so an assignment leaves both
// documents pointing at the same numbers. A caller that then adjusted one
// (a test, a re-projection under a different budget, anything holding the
// result) would silently rewrite the other -- and Project is documented as
// a pure function of (result, budget), which this is the only place that
// could quietly stop being true.
func cloneRenderShape(shape contractsv1.ContextFabricRenderShape) contractsv1.ContextFabricRenderShape {
	series := make([]contractsv1.ContextFabricRenderSeries, 0, len(shape.Series))
	for _, source := range shape.Series {
		points := make([]contractsv1.ContextFabricRenderPoint, 0, len(source.Points))
		for _, point := range source.Points {
			if point.Source.RowIndex != nil {
				index := *point.Source.RowIndex
				point.Source.RowIndex = &index
			}
			points = append(points, point)
		}
		source.Points = points
		series = append(series, source)
	}
	shape.Series = series
	return shape
}

// projectedRenderSources answers "can a reader of THIS projection check
// this number?" for each point source kind. It deliberately mirrors
// contracts/v1's own renderShapeSourcesFromProjection resolution rules
// rather than re-implementing a looser version: a shape this says yes to
// must be one Validate also accepts.
type projectedRenderSources struct {
	cohort *contractsv1.ContextFabricProjectedCohort
	facts  []contractsv1.ContextFabricProjectedFact
}

func (s projectedRenderSources) carriesEveryPoint(shape contractsv1.ContextFabricRenderShape) bool {
	for _, series := range shape.Series {
		for _, point := range series.Points {
			if !s.carries(point.Source, point.Value) {
				return false
			}
		}
	}
	return true
}

func (s projectedRenderSources) carries(source contractsv1.ContextFabricRenderPointSource, value float64) bool {
	switch source.Kind {
	case contractsv1.ContextFabricRenderSourceCohortMemberScore:
		if s.cohort == nil {
			return false
		}
		for _, member := range s.cohort.Members {
			if member.Subject.CanonicalID == source.SubjectCanonicalID {
				return member.Score != nil && *member.Score == value
			}
		}
		return false
	case contractsv1.ContextFabricRenderSourceCohortDriverWeight:
		if s.cohort == nil {
			return false
		}
		return rankingTableCarriesDriverWeight(s.cohort.RankingTable, source.SubjectCanonicalID, source.Signal, value)
	case contractsv1.ContextFabricRenderSourceClaimedFactRow:
		if source.RowIndex == nil {
			return false
		}
		for _, fact := range s.facts {
			if fact.ClaimID != source.ClaimID {
				continue
			}
			// CHAOS-4682 (§5.1 P2): mirrors contracts/v1's own
			// ContextFabricProjectedFact.renderableRows -- TimeSeriesRows
			// is the only array datedFactTrendShape (the sole producer of
			// this source kind) ever addresses on a dual-table fact, so
			// preferring it here is the same deterministic rule the
			// canonical result's own resolver applies, not a guess.
			rows := fact.TimeSeriesRows
			if len(rows) == 0 {
				rows = fact.Rows
			}
			if *source.RowIndex < 0 || *source.RowIndex >= len(rows) {
				return false
			}
			cell, ok := rows[*source.RowIndex].Fields[source.Field]
			if !ok {
				return false
			}
			switch {
			case cell.Number != nil:
				return *cell.Number == value
			case cell.Integer != nil:
				// Bounded exactly as contractsv1's own resolver bounds it:
				// past 2^53 a float64 cannot tell adjacent integers apart,
				// so an equal comparison there would admit a shape
				// ContextFabricAnswerProjection.Validate then rejects.
				if *cell.Integer > contractsv1.ContextFabricRenderPointExactIntegerBound ||
					*cell.Integer < -contractsv1.ContextFabricRenderPointExactIntegerBound {
					return false
				}
				return float64(*cell.Integer) == value
			default:
				return false
			}
		}
		return false
	default:
		return false
	}
}

// rankingTableCarriesDriverWeight finds a member's driver weight in the
// projected ranking table. The table surfaces only the top
// rankingTableTopDrivers per member (never a bare score: the strongest
// evidence rides with the number), so a shape citing a WEAKER driver family
// legitimately fails to resolve here and its whole shape is dropped -- a
// stacked breakdown whose smallest segment the reader cannot check is not a
// breakdown the projection should claim to have made.
func rankingTableCarriesDriverWeight(table []contractsv1.ContextFabricClaimedFactRow, canonicalID, signal string, value float64) bool {
	for _, row := range table {
		id, ok := row.Fields["team_canonical_id"]
		if !ok || id.String == nil || *id.String != canonicalID {
			continue
		}
		for key, cell := range row.Fields {
			name, isDriver := strings.CutPrefix(key, "driver_")
			if !isDriver || !strings.HasSuffix(name, "_signal") || cell.String == nil || *cell.String != signal {
				continue
			}
			weight, ok := row.Fields["driver_"+strings.TrimSuffix(name, "_signal")+"_weight_contributed"]
			if !ok || weight.Number == nil {
				return false
			}
			return *weight.Number == value
		}
		return false
	}
	return false
}
