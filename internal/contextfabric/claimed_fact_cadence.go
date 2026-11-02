package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// DeclaredCadence returns the cadence capability's declaration states for a
// table of field on a fact about subject (CHAOS-11985): `day` for a daily
// series (FactFieldDeclaration.DailySeries, one row per day keyed by
// FactDayColumn), and none for any other field. The declaration is the only
// source of a served cadence.
func DeclaredCadence(capability FactCapability, subject SubjectKind, field string) contractsv1.ContextFabricFactTableCadence {
	declaration, ok := capability.FieldDeclaration(field, subject)
	if !ok || !declaration.DailySeries {
		return ""
	}
	return contractsv1.ContextFabricFactTableCadenceDay
}

// FactTableCadenceLookup returns the declared cadence of a table of one field
// of a fact, or "" for a field that declares none.
type FactTableCadenceLookup func(kind FactKind, subject SubjectKind, field string) contractsv1.ContextFabricFactTableCadence

// declaredCadenceOf reads cadences off the fact declarations of every kind.
// With no declarations no table has a cadence.
func declaredCadenceOf(capabilities []FactCapability) FactTableCadenceLookup {
	byKind := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	return func(kind FactKind, subject SubjectKind, field string) contractsv1.ContextFabricFactTableCadence {
		return DeclaredCadence(byKind[kind], subject, field)
	}
}

// withDeclaredCadence states the declared cadence of every claimed table that
// is a time_series and whose rows keep it: the table with the claim's Rows,
// the time_series table with its TimeSeriesRows. It REPLACES what a table
// carries, so a cadence no declaration states, or one the rows do not keep,
// never reaches the wire. finalizeResult runs it on every pass, and it gives
// the same answer each time.
//
// A table is copied before it changes, so a table another document shares is
// never rewritten.
func withDeclaredCadence(claims []ClaimedFact, cadenceOf FactTableCadenceLookup) []ClaimedFact {
	if len(claims) == 0 {
		return claims
	}
	out := make([]ClaimedFact, len(claims))
	for index, claim := range claims {
		claim.Table = tableWithDeclaredCadence(claim.Table, claim.Rows, claim, cadenceOf)
		claim.TimeSeriesTable = tableWithDeclaredCadence(claim.TimeSeriesTable, claim.TimeSeriesRows, claim, cadenceOf)
		out[index] = claim
	}
	return out
}

func tableWithDeclaredCadence(table *contractsv1.ContextFabricClaimedFactTable, rows []contractsv1.ContextFabricClaimedFactRow, claim ClaimedFact, cadenceOf FactTableCadenceLookup) *contractsv1.ContextFabricClaimedFactTable {
	if table == nil {
		return nil
	}
	cadence := keptCadence(table, rows, claim, cadenceOf)
	if cadence == table.Cadence {
		return table
	}
	stated := *table
	stated.Cadence = cadence
	return &stated
}

// keptCadence is the declared cadence of a time_series table whose rows keep
// it, and none otherwise.
func keptCadence(table *contractsv1.ContextFabricClaimedFactTable, rows []contractsv1.ContextFabricClaimedFactRow, claim ClaimedFact, cadenceOf FactTableCadenceLookup) contractsv1.ContextFabricFactTableCadence {
	if cadenceOf == nil || table.Shape != contractsv1.ContextFabricFactTableShapeTimeSeries {
		return ""
	}
	cadence := cadenceOf(claim.Kind, claim.Subject.Kind, table.Field)
	if cadence == "" {
		return ""
	}
	candidate := *table
	candidate.Cadence = cadence
	if candidate.CadenceAligned(rows) != nil {
		return ""
	}
	return cadence
}

// claimedTablesCadenceWithheld counts the claimed tables whose declaration
// states a cadence the table does not carry: its rows do not keep it, or it is
// not a time_series. A consumer then joins the line across a gap, as it does
// for a table with no cadence, so the count is the only trace of the loss.
func claimedTablesCadenceWithheld(claims []ClaimedFact, cadenceOf FactTableCadenceLookup) int {
	if cadenceOf == nil {
		return 0
	}
	withheld := 0
	for _, claim := range claims {
		for _, table := range []*contractsv1.ContextFabricClaimedFactTable{claim.Table, claim.TimeSeriesTable} {
			if table != nil && table.Cadence == "" && cadenceOf(claim.Kind, claim.Subject.Kind, table.Field) != "" {
				withheld++
			}
		}
	}
	return withheld
}
