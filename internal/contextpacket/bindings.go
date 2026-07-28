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
	return []ClickHouseBinding{
		{"org_id", p.OrgID},
		{"repo_id", p.RepoID},
		{"repo_slug", p.RepoSlug},
		{"branch", p.Branch},
		{"branch_hash", p.BranchHash},
		{"commit_sha", p.CommitSHA},
		{"task_ref", p.TaskRef},
		{"files", append([]string(nil), p.Files...)},
		{"as_of", asOf},
		{"time_window_days", uint16(p.TimeWindowDays)},
	}
}

func scopeBindings(plan ReadPlan) []ClickHouseBinding {
	return []ClickHouseBinding{{"org_id", plan.OrgID}, {"repo_slug", plan.RepoSlug}}
}
