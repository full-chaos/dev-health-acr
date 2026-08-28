package contextpacket

import (
	dhgoclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// ClickHouseBinding is a type alias for the generic binding primitive now
// owned by the shared dev-health-go library (CHAOS-4377). It is the exact
// same type, so every existing call site keeps compiling unchanged.
type ClickHouseBinding = dhgoclickhouse.Binding

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
		{Name: "org_id", Value: p.OrgID},
		{Name: "repo_id", Value: p.RepoID},
		{Name: "repo_slug", Value: p.RepoSlug},
		{Name: "branch", Value: p.Branch},
		{Name: "commit_sha", Value: p.CommitSHA},
		{Name: "task_ref", Value: p.TaskRef},
		{Name: "files", Value: append([]string(nil), p.Files...)},
		{Name: "as_of", Value: asOf},
		{Name: "time_window_days", Value: uint16(p.TimeWindowDays)},
		{Name: "include_low_confidence", Value: includeLowConfidence},
	}
}

func scopeBindings(plan ReadPlan) []ClickHouseBinding {
	return []ClickHouseBinding{{Name: "org_id", Value: plan.OrgID}, {Name: "repo_slug", Value: plan.RepoSlug}}
}
