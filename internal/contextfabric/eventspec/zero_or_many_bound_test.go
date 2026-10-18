package eventspec

import (
	"sort"
	"strings"
	"testing"
)

// unboundedEvents is the reviewed list of zero_or_many_per_request events that
// carry no MaxLinesPerScope. Each one is a devhealthfacts provider line whose
// number follows the reads a provider makes for a request (or the teams one
// read covers), a quantity the request does not fix; the certifier cannot
// count it per scope, so the declaration states why instead of a number.
// Adding an event here is a reviewed exemption.
var unboundedEvents = []string{
	"devhealthfacts.fact_bytes_bound_exceeded",
	"devhealthfacts.health_daily_health_cap_cut",
	"devhealthfacts.owning_teams_truncated",
	"devhealthfacts.pull_request_project_native_first",
	"devhealthfacts.rule_basis_day_absent",
	"devhealthfacts.rule_basis_day_invalid",
	"devhealthfacts.subject_id_shape_rejected",
	"devhealthfacts.team_linked_mix_native_first",
	"devhealthfacts.team_linked_mix_set",
	"devhealthfacts.team_pr_link_state_native_first",
}

// TestEveryZeroOrManyEventIsBoundedExceptTheListedOnes requires every
// zero_or_many_per_request event to carry a numeric MaxLinesPerScope, apart
// from the reviewed list above: a bound stated only in prose is not one the
// certifier can enforce.
func TestEveryZeroOrManyEventIsBoundedExceptTheListedOnes(t *testing.T) {
	t.Parallel()
	listed := map[string]bool{}
	for _, id := range unboundedEvents {
		listed[id] = true
	}
	var unbounded []string
	for _, event := range All {
		if event.Multiplicity == MultiplicityZeroOrManyPerRequest && event.MaxLinesPerScope <= 0 && !listed[event.ID] {
			unbounded = append(unbounded, event.ID)
		}
	}
	sort.Strings(unbounded)
	if len(unbounded) > 0 {
		t.Errorf("%d zero_or_many_per_request events have no numeric MaxLinesPerScope and are not a reviewed exemption: %s", len(unbounded), strings.Join(unbounded, ", "))
	}
}
