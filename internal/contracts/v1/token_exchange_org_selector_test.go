package v1

import "testing"

func TestTokenExchangeOrgSelector_absentIsNotEmpty(t *testing.T) {
	const org = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, present, err := (TokenExchangeRequest{}).OrgSelector(); err != nil || present {
		t.Fatalf("no resource: present = %v err = %v, want absent and no error", present, err)
	}
	if got, present, err := (TokenExchangeRequest{Resources: []string{TokenExchangeResourceOrgPrefix + org}}).OrgSelector(); err != nil || !present || got != org {
		t.Fatalf("one resource: org = %q present = %v err = %v", got, present, err)
	}
	for name, values := range map[string][]string{
		"present but empty": {""},
		"two values":        {TokenExchangeResourceOrgPrefix + org, TokenExchangeResourceOrgPrefix + org},
	} {
		if _, _, err := (TokenExchangeRequest{Resources: values}).OrgSelector(); err == nil {
			t.Fatalf("%s: OrgSelector accepted %q, want an error", name, values)
		}
	}
}
