package contextfabric

import (
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// DeclaredMeasureUnits states the served unit of each of measures, a table of
// field on a fact about subject, from capability's own declaration
// (CHAOS-11984). The producer that owns a column declares its unit there, and
// that declaration is the only source of a served unit. A measure the
// declaration does not name, or whose declared token is outside the closed wire
// vocabulary, gets no entry: absent means undeclared. The result is in
// measures order, and nil when no measure has a unit.
func DeclaredMeasureUnits(capability FactCapability, subject SubjectKind, field string, measures []string) []contractsv1.ContextFabricMeasureUnitDeclaration {
	declaration, ok := capability.FieldDeclaration(field, subject)
	if !ok {
		return nil
	}
	var units []contractsv1.ContextFabricMeasureUnitDeclaration
	for _, measure := range measures {
		column, ok := declaration.Column(measure)
		if !ok {
			continue
		}
		unit := contractsv1.ContextFabricMeasureUnit(column.Unit)
		if contractsv1.ValidContextFabricMeasureUnit(unit) {
			units = append(units, contractsv1.ContextFabricMeasureUnitDeclaration{Measure: measure, Unit: unit})
		}
	}
	return units
}

// factMeasureUnits states the units of one claimed table. See
// DeclaredMeasureUnits.
type factMeasureUnits func(kind FactKind, subject SubjectKind, field string, measures []string) []contractsv1.ContextFabricMeasureUnitDeclaration

// declaredMeasureUnitsOf reads units off the fact declarations of every kind.
// With no declarations no measure has a unit.
func declaredMeasureUnitsOf(capabilities []FactCapability) factMeasureUnits {
	byKind := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	return func(kind FactKind, subject SubjectKind, field string, measures []string) []contractsv1.ContextFabricMeasureUnitDeclaration {
		return DeclaredMeasureUnits(byKind[kind], subject, field, measures)
	}
}

// withDeclaredMeasureUnits states the declared unit of every measure of every
// claimed table. It REPLACES what a table carries: the units come from the
// declarations alone, so a unit no producer declared never reaches the wire,
// whoever wrote it. finalizeResult runs it on every pass, and it gives the same
// answer each time.
//
// A table is copied before it changes, so a table another document shares is
// never rewritten.
func withDeclaredMeasureUnits(claims []ClaimedFact, unitsOf factMeasureUnits) []ClaimedFact {
	if len(claims) == 0 {
		return claims
	}
	out := make([]ClaimedFact, len(claims))
	for index, claim := range claims {
		claim.Table = tableWithDeclaredUnits(claim.Table, claim, unitsOf)
		claim.TimeSeriesTable = tableWithDeclaredUnits(claim.TimeSeriesTable, claim, unitsOf)
		out[index] = claim
	}
	return out
}

func tableWithDeclaredUnits(table *contractsv1.ContextFabricClaimedFactTable, claim ClaimedFact, unitsOf factMeasureUnits) *contractsv1.ContextFabricClaimedFactTable {
	if table == nil {
		return nil
	}
	var units []contractsv1.ContextFabricMeasureUnitDeclaration
	if unitsOf != nil {
		units = unitsOf(claim.Kind, claim.Subject.Kind, table.Field, table.Measures)
	}
	if slices.Equal(units, table.MeasureUnits) {
		return table
	}
	stated := *table
	stated.MeasureUnits = units
	return &stated
}
