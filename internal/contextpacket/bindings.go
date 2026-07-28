package contextpacket

type ClickHouseBinding struct {
	Name  string
	Value any
}

func (p ReadPlan) Bindings() []ClickHouseBinding {
	var asOf any
	if p.AsOf != nil {
		asOf = p.AsOf.UTC()
	}
	includeLowConfidence := uint8(0)
	if p.IncludeLowConfidence {
		includeLowConfidence = 1
	}
	return []ClickHouseBinding{
		{"org_id", p.OrgID},
		{"repo_id", p.RepoID},
		{"repo_slug", p.RepoSlug},
		{"branch", p.Branch},
		{"commit_sha", p.CommitSHA},
		{"task_ref", p.TaskRef},
		{"files", append([]string(nil), p.Files...)},
		{"as_of", asOf},
		{"time_window_days", uint16(p.TimeWindowDays)},
		{"include_low_confidence", includeLowConfidence},
	}
}

func scopeBindings(plan ReadPlan) []ClickHouseBinding {
	return []ClickHouseBinding{{"org_id", plan.OrgID}, {"repo_slug", plan.RepoSlug}}
}
