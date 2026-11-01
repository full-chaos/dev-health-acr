package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12308: a pull request's stated membership proves only when it stands
// on native links. Under a requested narrowing a lower tier neither proves nor
// denies; a project-only caller is admitted only on a native tier.
func pullRequestAttributes(tier string, identities ...string) map[string]interface{} {
	attributes := map[string]interface{}{
		"subject_kind": string(contextfabric.SubjectPullRequest), "canonical_id": "pull_request:r:1",
		"authorization_repositories": []string{"acme/api"}, authorizationProjectsAttr: "*", authorizationTeamsAttr: "*",
	}
	if identities != nil {
		attributes[AuthorizationProjectIdentitiesAttr] = identities
	}
	if tier != "" {
		attributes[ProjectMembershipTierAttr] = tier
	}
	return attributes
}

func TestAPullRequestMembershipProvesAProjectNarrowingOnlyOnNativeLinks(t *testing.T) {
	const wanted, other = "project.v2:linear:wanted", "project.v2:linear:other"
	scope := contextfabric.RequestedScope{ProjectIDs: []string{"wanted"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{wanted}, Complete: true}}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	for name, tc := range map[string]struct {
		attributes         map[string]interface{}
		admitted, unproven bool
	}{
		"native, a member":            {pullRequestAttributes("native", wanted), true, false},
		"native, another project":     {pullRequestAttributes("native", other), false, true},
		"lower tier, a member":        {pullRequestAttributes("explicit_text", wanted), true, true},
		"lower tier, another project": {pullRequestAttributes("heuristic", other), true, true},
		"stated with no tier":         {pullRequestAttributes("", wanted), true, true},
		"unstated":                    {pullRequestAttributes(""), true, true},
	} {
		if got := AuthorizedAttributes(principal, scope, tc.attributes); got != tc.admitted {
			t.Errorf("%s: admitted = %v, want %v", name, got, tc.admitted)
		}
		if got := ProjectNarrowingUnproven(scope, tc.attributes); got != tc.unproven {
			t.Errorf("%s: unproven = %v, want %v", name, got, tc.unproven)
		}
	}
}

func TestAProjectOnlyCallerIsAdmittedToAPullRequestOnlyOnANativeMembership(t *testing.T) {
	const granted = "project.v2:linear:granted"
	principal := storage.Principal{OrgID: "org-1", ProjectScopes: []string{granted}}
	if auth.ClassifyGrant(principal) != auth.GrantProjectOnly {
		t.Fatalf("premise: class = %v", auth.ClassifyGrant(principal))
	}
	for name, tc := range map[string]struct {
		attributes map[string]interface{}
		want       bool
	}{
		"native, a member":     {pullRequestAttributes("native", granted), true},
		"native, not a member": {pullRequestAttributes("native", "project.v2:linear:other"), false},
		"a lower tier":         {pullRequestAttributes("heuristic", granted), false},
		"stated with no tier":  {pullRequestAttributes("", granted), false},
		"unstated":             {pullRequestAttributes(""), false},
	} {
		if got := AuthorizedAttributes(principal, contextfabric.RequestedScope{}, tc.attributes); got != tc.want {
			t.Errorf("%s: admitted = %v, want %v", name, got, tc.want)
		}
	}
}

// The tier a consumer must name: only a pull request's stated lower tier, and
// only under a requested narrowing it cannot prove.
func TestLowerTierMembershipNamesOnlyAStatedLowerTier(t *testing.T) {
	const wanted = "project.v2:linear:wanted"
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"wanted"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{wanted}, Complete: true}}
	// Of another project, so the narrowing is unproven for it and only the kind
	// keeps it out.
	workItem := pullRequestAttributes("heuristic", "project.v2:linear:other")
	workItem["subject_kind"] = string(contextfabric.SubjectWorkItem)
	for name, tc := range map[string]struct {
		scope      contextfabric.RequestedScope
		attributes map[string]interface{}
		want       string
	}{
		"explicit text":          {narrowed, pullRequestAttributes("explicit_text", wanted), "explicit_text"},
		"heuristic":              {narrowed, pullRequestAttributes("heuristic", wanted), "heuristic"},
		"another project":        {narrowed, pullRequestAttributes("heuristic", "project.v2:linear:other"), "heuristic"},
		"native":                 {narrowed, pullRequestAttributes("native", wanted), ""},
		"an unknown tier":        {narrowed, pullRequestAttributes("inferred", wanted), ""},
		"unstated":               {narrowed, pullRequestAttributes(""), ""},
		"a tier and no set":      {narrowed, withoutIdentities(pullRequestAttributes("heuristic", wanted)), ""},
		"a work item":            {narrowed, workItem, ""},
		"no requested narrowing": {contextfabric.RequestedScope{}, pullRequestAttributes("heuristic", wanted), ""},
	} {
		if got := LowerTierMembershipOf(tc.scope, tc.attributes); got != tc.want {
			t.Errorf("%s: tier = %q, want %q", name, got, tc.want)
		}
	}
	if got := LowerTierEndpoints(narrowed, pullRequestAttributes("native", wanted), pullRequestAttributes("native", wanted)); got != nil {
		t.Errorf("no lower-tier endpoint: %v, want nil", got)
	}
	got := LowerTierEndpoints(narrowed, pullRequestAttributes("native", wanted), pullRequestAttributes("explicit_text", wanted))
	if len(got) != 1 || got["pull_request:r:1"] != "explicit_text" {
		t.Errorf("endpoints = %v", got)
	}
}

func withoutIdentities(attributes map[string]interface{}) map[string]interface{} {
	delete(attributes, AuthorizationProjectIdentitiesAttr)
	return attributes
}
