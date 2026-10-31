package graphrank

import "testing"

// CHAOS-12299 (c3 of CHAOS-12228), shared with CHAOS-12229: one exact
// project-membership predicate over authorization_project_identities.
func TestProjectMembershipOfIsExactAndNeverAWildcard(t *testing.T) {
	t.Parallel()
	wanted := []string{"project.v2:github:P-1", "project.v2:jira:ENG"}
	cases := []struct {
		name   string
		value  interface{}
		absent bool
		wanted []string
		want   ProjectMembership
	}{
		{name: "key absent", absent: true, wanted: wanted, want: ProjectMembershipUnstated},
		{name: "key present but nil (a removed property)", value: nil, wanted: wanted, want: ProjectMembershipUnstated},
		{name: "a bare string is not a list", value: "project.v2:github:P-1", wanted: wanted, want: ProjectMembershipUnstated},
		{name: "the star string is not a list", value: "*", wanted: wanted, want: ProjectMembershipUnstated},
		{name: "a list of another type", value: []int{1}, wanted: wanted, want: ProjectMembershipUnstated},
		{name: "a read list with a non-string item", value: []interface{}{"project.v2:github:P-1", 7}, wanted: wanted, want: ProjectMembershipUnstated},
		{name: "a stated empty list is in no project", value: []string{}, wanted: wanted, want: ProjectMembershipNotMember},
		{name: "a stated empty read list", value: []interface{}{}, wanted: wanted, want: ProjectMembershipNotMember},
		{name: "a member ([]string)", value: []string{"project.v2:jira:ENG"}, wanted: wanted, want: ProjectMembershipMember},
		{name: "a member (read []interface{})", value: []interface{}{"project.v2:other:X", "project.v2:github:P-1"}, wanted: wanted, want: ProjectMembershipMember},
		{name: "another project only", value: []string{"project.v2:github:P-2"}, wanted: wanted, want: ProjectMembershipNotMember},
		{name: "a star entry is an ordinary string", value: []string{"*"}, wanted: wanted, want: ProjectMembershipNotMember},
		{name: "case-sensitive", value: []string{"project.v2:github:p-1"}, wanted: wanted, want: ProjectMembershipNotMember},
		{name: "a raw id never matches a canonical one", value: []string{"P-1"}, wanted: wanted, want: ProjectMembershipNotMember},
		{name: "nothing wanted is never a member", value: []string{"project.v2:github:P-1"}, wanted: nil, want: ProjectMembershipNotMember},
		{name: "a wanted star matches nothing", value: []string{"project.v2:github:P-1"}, wanted: []string{"*"}, want: ProjectMembershipNotMember},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attributes := map[string]interface{}{
				// The legacy key says "*" throughout: it must never decide.
				authorizationProjectsAttr: "*",
			}
			if !tc.absent {
				attributes[AuthorizationProjectIdentitiesAttr] = tc.value
			}
			if got := ProjectMembershipOf(attributes, tc.wanted); got != tc.want {
				t.Fatalf("ProjectMembershipOf(%#v, %v) = %v, want %v", attributes, tc.wanted, got, tc.want)
			}
		})
	}
}
