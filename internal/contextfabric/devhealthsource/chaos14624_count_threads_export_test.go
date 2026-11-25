package devhealthsource

// TotalCountStatementsForTest lists the SQL of the total count statements
// ProjectionSourceCounts runs (the ClickHouse source, then teams/projects).
func TotalCountStatementsForTest() []string {
	var out []string
	for _, set := range [][]kindCountStatement{clickHouseKindCountStatements, teamsProjectsKindCountStatements} {
		for _, st := range set {
			out = append(out, st.sql)
		}
	}
	return out
}
