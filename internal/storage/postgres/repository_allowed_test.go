package postgres

import "testing"

func TestRepositoryAllowedAcceptsADotLeadingRepositoryName(t *testing.T) {
	if !repositoryAllowed([]string{"full-chaos/*"}, "full-chaos/.github") || !repositoryAllowed([]string{"full-chaos/.github"}, "Full-Chaos/.github") {
		t.Fatal("dot-leading repository was refused")
	}
	if repositoryAllowed([]string{"*"}, "full-chaos/..") || repositoryAllowed([]string{"*"}, "full-chaos/.") {
		t.Fatal("path-traversal repository name was allowed")
	}
}
