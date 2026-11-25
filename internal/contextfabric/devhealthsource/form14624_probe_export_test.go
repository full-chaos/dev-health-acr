package devhealthsource

// Form14624Statement is one TOTAL kind count statement of the CHAOS-14624 marker-absent probe, for the prod FORM exec leg.
type Form14624Statement struct{ Kind, SQL string }

// Form14624ProbeStatementsForTest lists the statements ProjectionSourceCounts runs (the ClickHouse source, then teams/projects), in order.
func Form14624ProbeStatementsForTest() []Form14624Statement {
	var out []Form14624Statement
	for _, set := range [][]kindCountStatement{clickHouseKindCountStatements, teamsProjectsKindCountStatements} {
		for _, st := range set {
			out = append(out, Form14624Statement{Kind: string(st.kind), SQL: st.sql})
		}
	}
	return out
}
