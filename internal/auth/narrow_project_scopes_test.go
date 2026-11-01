package auth

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

func contextWithGrant(grant []string) context.Context {
	return context.WithValue(context.Background(), principalKey{}, storage.Principal{OrgID: "org_1", RepositoryScopes: grant})
}

// CHAOS-12301 (CHAOS-12107 D2): only a "*" consent grant may become
// project_only. The principal then holds the projects and the reserved
// repository value, never "*" and never an empty list.
func TestAStarGrantNarrowsToTheProjectsAndTheReservedRepositoryValue(t *testing.T) {
	for _, grant := range [][]string{{"*"}, {"*", "acme/api"}} {
		projects := []string{"project.v2:jira:A", "project.v2:linear:B"}
		ctx, err := NarrowToProjectScopes(contextWithGrant(grant), projects)
		if err != nil {
			t.Fatalf("grant %v: %v", grant, err)
		}
		projects[0] = "project.v2:jira:CHANGED"
		principal, ok := PrincipalFromContext(ctx)
		if !ok {
			t.Fatal("no principal")
		}
		if !slices.Equal(principal.ProjectScopes, []string{"project.v2:jira:A", "project.v2:linear:B"}) {
			t.Fatalf("grant %v: ProjectScopes = %q, want a copy of the live projects", grant, principal.ProjectScopes)
		}
		if !slices.Equal(principal.RepositoryScopes, []string{contractsv1.ProjectOnlyRepositoryScope}) {
			t.Fatalf("grant %v: RepositoryScopes = %q, want only the reserved value", grant, principal.RepositoryScopes)
		}
		if got := ClassifyGrant(principal); got != GrantProjectOnly {
			t.Fatalf("grant %v: class %q, want project_only", grant, got)
		}
	}
}

// Every other grant keeps what it had: an explicit repository grant in an
// organization with no repositories stays no_repository (CHAOS-12107 D2).
func TestOnlyAStarGrantNarrowsToProjects(t *testing.T) {
	for name, grant := range map[string][]string{
		"no grant list": nil, "an empty grant list": {}, "one repository": {"acme/api"}, "an owner wildcard": {"acme/*"},
	} {
		ctx := contextWithGrant(grant)
		narrowed, err := NarrowToProjectScopes(ctx, []string{"project.v2:jira:A"})
		if !errors.Is(err, ErrProjectScopeNeedsStarGrant) {
			t.Fatalf("%s: err = %v, want ErrProjectScopeNeedsStarGrant", name, err)
		}
		principal, _ := PrincipalFromContext(narrowed)
		if len(principal.ProjectScopes) != 0 || !slices.Equal(principal.RepositoryScopes, grant) {
			t.Fatalf("%s: principal = %+v, want it unchanged", name, principal)
		}
	}
}

func TestAProjectGrantIsNotNarrowedAgain(t *testing.T) {
	ctx, err := NarrowToProjectScopes(contextWithGrant([]string{"*"}), []string{"project.v2:jira:A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NarrowToProjectScopes(ctx, []string{"project.v2:jira:B"}); !errors.Is(err, ErrProjectScopeNeedsStarGrant) {
		t.Fatalf("second narrowing: err = %v, want ErrProjectScopeNeedsStarGrant", err)
	}
}

// A non-empty answer never reaches here empty, and an empty project grant
// would be the empty (unrestricted) grant: it is refused by name.
func TestAnEmptyProjectSetIsRefused(t *testing.T) {
	for _, projects := range [][]string{nil, {}} {
		ctx := contextWithGrant([]string{"*"})
		narrowed, err := NarrowToProjectScopes(ctx, projects)
		if !errors.Is(err, ErrEmptyProjectScope) {
			t.Fatalf("projects %v: err = %v, want ErrEmptyProjectScope", projects, err)
		}
		principal, _ := PrincipalFromContext(narrowed)
		if !slices.Equal(principal.RepositoryScopes, []string{"*"}) {
			t.Fatalf("projects %v: principal = %+v, want it unchanged", projects, principal)
		}
	}
}

func TestNarrowingToProjectsNeedsAPrincipal(t *testing.T) {
	if _, err := NarrowToProjectScopes(context.Background(), []string{"project.v2:jira:A"}); err == nil {
		t.Fatal("no error without a principal")
	}
}

// The reserved value is what every raw-list reader sees: a restricted list
// that matches no repository.
func TestAProjectOnlyPrincipalMatchesNoRepositoryOnItsRawList(t *testing.T) {
	ctx, err := NarrowToProjectScopes(contextWithGrant([]string{"*"}), []string{"project.v2:jira:A"})
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := PrincipalFromContext(ctx)
	if RepositoryAllowed(principal.RepositoryScopes, "acme/api") {
		t.Fatal("the reserved value allowed a repository")
	}
	if err := AuthorizeRepository(principal, "acme/api"); err == nil {
		t.Fatal("a project-only principal was authorized a repository")
	}
	if set := WorkItemGrantSelectorSet(principal); !reflect.DeepEqual(set, readers.RepositorySelectorSet{}) {
		t.Fatalf("selector set = %+v, want the zero set", set)
	}
}
