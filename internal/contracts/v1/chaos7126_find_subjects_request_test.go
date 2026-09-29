package v1

import "testing"

// CHAOS-7126: the find_subjects request takes exactly one mode; handle mode
// takes no kind; owned_by may narrow by kind.
func TestChaos7126FindSubjectsRequestModes(t *testing.T) {
	for name, request := range map[string]MCPFindSubjectsRequest{
		"list":             {Kind: "repository"},
		"name":             {Query: "acme/payments"},
		"owned_by":         {OwnedBy: "team:T"},
		"owned_by + kind":  {OwnedBy: "team:T", Kind: "repository"},
		"owned_by + kinds": {OwnedBy: "team:T", Kinds: []string{"repository", "project"}},
		"handle":           {Handle: "PR 532"},
	} {
		if err := request.Validate(); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	long := make([]byte, MCPFindSubjectsQueryMax+1)
	for i := range long {
		long[i] = 'a'
	}
	for name, request := range map[string]MCPFindSubjectsRequest{
		"query + owned_by":  {Query: "x", OwnedBy: "team:T"},
		"query + handle":    {Query: "x", Handle: "PR 1"},
		"owned_by + handle": {OwnedBy: "team:T", Handle: "PR 1"},
		"handle + kind":     {Handle: "PR 1", Kind: "pull_request"},
		"handle + kinds":    {Handle: "PR 1", Kinds: []string{"pull_request"}},
		"owned_by too long": {OwnedBy: string(long)},
		"handle too long":   {Handle: string(long)},
		"nothing":           {},
	} {
		if err := request.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
