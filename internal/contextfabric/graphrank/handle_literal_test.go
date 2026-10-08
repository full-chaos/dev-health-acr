package graphrank

import "testing"

func TestIsHandleLiteral(t *testing.T) {
	for value, want := range map[string]bool{
		"747": true, "CHAOS-8929": true, "123456": true,
		"project_granted": false, "pull_request:repo:747": false, "repository:abc": false, "": false,
	} {
		if got := IsHandleLiteral(value); got != want {
			t.Errorf("IsHandleLiteral(%q) = %v, want %v", value, got, want)
		}
	}
}
