package v1

// RankingRowSubjectID returns a ranking-table row's subject canonical id:
// subject_canonical_id, else the deprecated team_canonical_id that rows from
// results stored before the subject_* keys carry.
func RankingRowSubjectID(row ContextFabricClaimedFactRow) (string, bool) {
	for _, key := range []string{"subject_canonical_id", "team_canonical_id"} {
		if v, ok := row.Fields[key]; ok && v.String != nil {
			return *v.String, true
		}
	}
	return "", false
}
