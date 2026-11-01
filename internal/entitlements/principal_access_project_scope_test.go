package entitlements_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// projectScopeDocument is one access answer as ops CHAOS-12224 writes it: an
// organization with no repository inventory, plus project_scope.
func projectScopeDocument(projectScope string) string {
	return strings.TrimSuffix(accessDocument(false, `{"kind":"none"}`), "}") + `,"project_scope":` + projectScope + `}`
}

func projectPairs(count int) string {
	pairs := make([]string, 0, count)
	for index := range count {
		pairs = append(pairs, fmt.Sprintf(`{"provider":"linear","id":"P-%03d"}`, index))
	}
	return `{"kind":"projects","projects":[` + strings.Join(pairs, ",") + `]}`
}

// A client whose response bound fits the document under test.
func readProjectScope(t *testing.T, body string) (entitlements.PrincipalAccess, error) {
	t.Helper()
	server, _, _ := accessServer(t, http.StatusOK, body)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 16 << 10}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client.PrincipalAccess(context.Background(), accessOrg, accessUser, "")
}

func TestAProjectScopeIsDecodedToSortedCanonicalIdentities(t *testing.T) {
	for _, test := range []struct {
		name, scope string
		want        []string
	}{
		{"one pair", `{"kind":"projects","projects":[{"provider":"linear","id":"P-1"}]}`, []string{"project.v2:linear:P-1"}},
		{"sorted by canonical identity", `{"kind":"projects","projects":[{"provider":"linear","id":"Z"},{"provider":"jira","id":"A"},{"provider":"linear","id":"B"}]}`,
			[]string{"project.v2:jira:A", "project.v2:linear:B", "project.v2:linear:Z"}},
		{"the segments are encoded once", `{"kind":"projects","projects":[{"provider":"jira","id":"a:b%c"}]}`, []string{"project.v2:jira:a%3Ab%25c"}},
		{"an id is exact: case and spaces stay", `{"kind":"projects","projects":[{"provider":"Linear","id":" P-1 "}]}`, []string{"project.v2:Linear: P-1 "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			access, err := readProjectScope(t, projectScopeDocument(test.scope))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(access.Projects, test.want) {
				t.Fatalf("Projects = %q, want %q", access.Projects, test.want)
			}
		})
	}
}

// The one derivation: what the decoder grants is what the membership
// projection and the project node carry for the same pair.
func TestAProjectScopeIdentityIsTheOneIdentityDerives(t *testing.T) {
	access, err := readProjectScope(t, projectScopeDocument(`{"kind":"projects","projects":[{"provider":"git:hub","id":"ghprojv2:PVT_1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want, omitted, err := identity.Derive(identity.KindProject, []string{"git:hub", "ghprojv2:PVT_1"}, nil)
	if err != nil || omitted {
		t.Fatalf("Derive: %q %v %v", want, omitted, err)
	}
	if !slices.Equal(access.Projects, []string{want}) {
		t.Fatalf("Projects = %q, want %q", access.Projects, []string{want})
	}
}

func TestOneHundredProjectsAreAcceptedAndOneMoreIsRefused(t *testing.T) {
	access, err := readProjectScope(t, projectScopeDocument(projectPairs(100)))
	if err != nil {
		t.Fatalf("100 pairs: %v", err)
	}
	if len(access.Projects) != 100 || !slices.IsSorted(access.Projects) {
		t.Fatalf("100 pairs: %d projects, sorted %v", len(access.Projects), slices.IsSorted(access.Projects))
	}
	access, err = readProjectScope(t, projectScopeDocument(projectPairs(101)))
	if got := entitlements.RejectionReason(err); got != "schema" || len(access.Projects) != 0 {
		t.Fatalf("101 pairs: reason %q, projects %d, want a schema rejection and no grant", got, len(access.Projects))
	}
}

// Kind "none", or no project_scope at all (the ops flag off, or an org with
// repositories): the answer is the same as before project_scope existed.
func TestNoProjectScopeGrantsNoProject(t *testing.T) {
	for name, body := range map[string]string{
		"kind none": projectScopeDocument(`{"kind":"none"}`),
		"absent":    accessDocument(false, `{"kind":"none"}`),
	} {
		access, err := readProjectScope(t, body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if access.Projects != nil {
			t.Fatalf("%s: Projects = %q, want nil", name, access.Projects)
		}
	}
}

// Every malformed project_scope rejects the whole read: it never drops a pair,
// never narrows to what is left, and never changes the class.
func TestAMalformedProjectScopeRejectsTheRead(t *testing.T) {
	valid := `{"provider":"linear","id":"P-1"}`
	oversized := strings.Repeat("x", identity.MaxNaturalKeyBytes)
	for _, test := range []struct {
		name, scope, reason string
	}{
		{"projects with no list", `{"kind":"projects"}`, "schema"},
		{"projects with a null list", `{"kind":"projects","projects":null}`, "schema"},
		{"projects with an empty list", `{"kind":"projects","projects":[]}`, "schema"},
		{"none with a list", `{"kind":"none","projects":[` + valid + `]}`, "schema"},
		{"none with an empty list", `{"kind":"none","projects":[]}`, "schema"},
		{"no kind", `{"projects":[` + valid + `]}`, "schema"},
		{"a null kind", `{"kind":null,"projects":[` + valid + `]}`, "schema"},
		{"an unknown kind", `{"kind":"folders","projects":[` + valid + `]}`, "schema"},
		{"a kind of another case", `{"kind":"PROJECTS","projects":[` + valid + `]}`, "schema"},
		{"not an object", `["linear"]`, "schema"},
		{"a pair that is not an object", `{"kind":"projects","projects":["linear:P-1"]}`, "schema"},
		{"a null pair", `{"kind":"projects","projects":[null]}`, "schema"},
		{"no provider", `{"kind":"projects","projects":[{"id":"P-1"}]}`, "schema"},
		{"no id", `{"kind":"projects","projects":[{"provider":"linear"}]}`, "schema"},
		{"a null provider", `{"kind":"projects","projects":[{"provider":null,"id":"P-1"}]}`, "schema"},
		{"a null id", `{"kind":"projects","projects":[{"provider":"linear","id":null}]}`, "schema"},
		{"an empty provider", `{"kind":"projects","projects":[{"provider":"","id":"P-1"}]}`, "schema"},
		{"an empty id", `{"kind":"projects","projects":[{"provider":"linear","id":""}]}`, "schema"},
		{"a blank provider", `{"kind":"projects","projects":[{"provider":"  ","id":"P-1"}]}`, "schema"},
		{"a blank id", `{"kind":"projects","projects":[{"provider":"linear","id":"\t"}]}`, "schema"},
		{"a separator in the provider", `{"kind":"projects","projects":[{"provider":"lin|ear","id":"P-1"}]}`, "schema"},
		{"a separator in the id", `{"kind":"projects","projects":[{"provider":"linear","id":"P|1"}]}`, "schema"},
		{"an oversized identity", `{"kind":"projects","projects":[{"provider":"linear","id":"` + oversized + `"}]}`, "schema"},
		{"a bad pair after a good one", `{"kind":"projects","projects":[` + valid + `,{"provider":"linear","id":""}]}`, "schema"},
		{"a duplicate pair", `{"kind":"projects","projects":[` + valid + `,{"provider":"jira","id":"A"},` + valid + `]}`, "schema"},
		{"an unknown field beside kind", `{"kind":"projects","projects":[` + valid + `],"more":false}`, "unknown_field"},
		{"an unknown field on kind none", `{"kind":"none","more":false}`, "unknown_field"},
		{"an unknown field on a pair", `{"kind":"projects","projects":[{"provider":"linear","id":"P-1","name":"x"}]}`, "unknown_field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := accessServer(t, http.StatusOK, projectScopeDocument(test.scope))
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 16 << 10}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			access, err := client.PrincipalAccess(context.Background(), accessOrg, accessUser, "")
			if got := entitlements.RejectionReason(err); got != test.reason || access.Member || len(access.Projects) != 0 {
				t.Fatalf("reason %q, member %v, projects %q (err %v), want reason %q and no answer", got, access.Member, access.Projects, err, test.reason)
			}
		})
	}
}

// A scope ops should never send beside repositories still has to be well
// formed: the read is one document, and a malformed one is never an answer.
func TestAMalformedProjectScopeBesideRepositoriesRejectsTheRead(t *testing.T) {
	body := strings.TrimSuffix(accessDocument(false, `{"kind":"repos","repos":["acme/api"]}`), "}") + `,"project_scope":{"kind":"projects","projects":[]}}`
	if _, err := readProjectScope(t, body); entitlements.RejectionReason(err) != "schema" {
		t.Fatalf("err = %v, want a schema rejection", err)
	}
}
