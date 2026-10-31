package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12298 (c2): the digests and the reuse key are keyed by grant class. A
// project-only principal's are built from its canonical project set (framed),
// never from its repository list, and never equal another class's.

const reservedRepositoryScopeForTest = "acr-reserved:project-only"

var projectGrantDigestShape = regexp.MustCompile(`^p:[0-9a-f]{32}$`)

func projectPrincipal(repositories []string, projects ...string) storage.Principal {
	return storage.Principal{OrgID: "org_1", RepositoryScopes: repositories, ProjectScopes: projects}
}

// legacyGrantDigestForTest is the CHAOS-7127/7145 formula, written out: other
// classes must keep it byte for byte.
func legacyGrantDigestForTest(scopes []string) string {
	seen := map[string]struct{}{}
	slugs := []string{}
	for _, scope := range scopes {
		slug := strings.TrimSpace(scope)
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	sum := sha256.Sum256([]byte(strings.Join(slugs, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func TestAProjectGrantDigestIsPPlusAFramedDigestOfTheProjectSet(t *testing.T) {
	wide := projectPrincipal(nil, "project.v2:github:P-1", "project.v2:github:P-2")
	got := StoredResultGrantDigest(wide)
	if !projectGrantDigestShape.MatchString(got) {
		t.Fatalf("StoredResultGrantDigest(project_only) = %q, want p: plus 32 lowercase hex", got)
	}
	same := []storage.Principal{
		projectPrincipal(nil, "project.v2:github:P-2", "project.v2:github:P-1"),
		projectPrincipal(nil, "project.v2:github:P-1", "project.v2:github:P-2", "project.v2:github:P-1"),
		// No digest hashes the reserved repository value, or any repository
		// list, for a project-only principal.
		projectPrincipal([]string{reservedRepositoryScopeForTest}, "project.v2:github:P-1", "project.v2:github:P-2"),
		projectPrincipal([]string{"*"}, "project.v2:github:P-1", "project.v2:github:P-2"),
		projectPrincipal([]string{"acme/api"}, "project.v2:github:P-1", "project.v2:github:P-2"),
	}
	for _, principal := range same {
		if digest := StoredResultGrantDigest(principal); digest != got {
			t.Errorf("%v / %v: digest %q, want %q", principal.RepositoryScopes, principal.ProjectScopes, digest, got)
		}
	}
	differ := []storage.Principal{
		projectPrincipal(nil, "project.v2:github:P-1"),
		projectPrincipal(nil, "project.v2:github:P-1", "project.v2:github:P-3"),
		// Exact: a project id is never trimmed or case-folded.
		projectPrincipal(nil, " project.v2:github:P-1", "project.v2:github:P-2"),
		projectPrincipal(nil, "project.v2:github:p-1", "project.v2:github:P-2"),
	}
	for _, principal := range differ {
		if digest := StoredResultGrantDigest(principal); digest == got {
			t.Errorf("%v: digest equals the {P-1,P-2} digest", principal.ProjectScopes)
		}
	}
}

// Framed, not joined: an id holding the join separator cannot move a boundary.
func TestAProjectGrantDigestIsFramed(t *testing.T) {
	joined := projectPrincipal(nil, "p\x00q")
	split := projectPrincipal(nil, "p", "q")
	if StoredResultGrantDigest(joined) == StoredResultGrantDigest(split) {
		t.Fatal(`the project set {"p\x00q"} digests the same as {"p","q"}`)
	}
	if GrantScopedTimeAxisKey(joined, "k") == GrantScopedTimeAxisKey(split, "k") {
		t.Fatal(`the reuse key of {"p\x00q"} equals that of {"p","q"}`)
	}
}

func TestOtherGrantClassesKeepTheirGrantDigestAndReuseKeyByteForByte(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/web", " acme/tools", "acme/web"}}
	if got, want := StoredResultGrantDigest(restricted), "g:"+legacyGrantDigestForTest(restricted.RepositoryScopes); got != want {
		t.Errorf("restricted digest = %q, want %q", got, want)
	}
	if got, want := GrantScopedTimeAxisKey(restricted, "current"), "current+g:"+legacyGrantDigestForTest(restricted.RepositoryScopes); got != want {
		t.Errorf("restricted reuse key = %q, want %q", got, want)
	}
	for _, open := range []storage.Principal{{OrgID: "org_1"}, {OrgID: "org_1", RepositoryScopes: []string{"*"}}} {
		if got := StoredResultGrantDigest(open); got != "open" {
			t.Errorf("%v: digest %q, want open", open.RepositoryScopes, got)
		}
		if got := GrantScopedTimeAxisKey(open, "current"); got != "current" {
			t.Errorf("%v: reuse key %q, want the axis key unchanged", open.RepositoryScopes, got)
		}
	}
}

// The reuse key is class-tagged: a project-only key never equals a restricted,
// universal or unrestricted key, and two different project grants never share
// one (the CHAOS-12299 r1 F4 case).
func TestTheReuseKeyIsClassTaggedAndSeparatesProjectGrants(t *testing.T) {
	const axis = "current"
	wide := projectPrincipal(nil, "project.v2:github:P-1", "project.v2:github:P-2")
	narrow := projectPrincipal(nil, "project.v2:github:P-1")
	wideKey, narrowKey := GrantScopedTimeAxisKey(wide, axis), GrantScopedTimeAxisKey(narrow, axis)
	if !strings.HasPrefix(wideKey, axis+"+p:") || !strings.HasPrefix(narrowKey, axis+"+p:") {
		t.Fatalf("project-only keys %q / %q, want the +p: class tag", wideKey, narrowKey)
	}
	if wideKey == narrowKey {
		t.Fatal("two different project grants share a reuse key")
	}
	if StoredResultGrantDigest(wide) == StoredResultGrantDigest(narrow) {
		t.Fatal("two different project grants share a stored-result digest")
	}
	if got := GrantScopedTimeAxisKey(projectPrincipal([]string{reservedRepositoryScopeForTest}, "project.v2:github:P-1", "project.v2:github:P-2"), axis); got != wideKey {
		t.Errorf("the repository list moved the project-only key: %q vs %q", got, wideKey)
	}
	others := []storage.Principal{
		{OrgID: "org_1"},
		{OrgID: "org_1", RepositoryScopes: []string{"*"}},
		{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}},
		{OrgID: "org_1", RepositoryScopes: []string{reservedRepositoryScopeForTest}},
	}
	for _, other := range others {
		if key := GrantScopedTimeAxisKey(other, axis); key == wideKey || key == narrowKey {
			t.Errorf("%v: reuse key %q equals a project-only key", other.RepositoryScopes, key)
		}
		if digest := StoredResultGrantDigest(other); digest == StoredResultGrantDigest(wide) || strings.HasPrefix(digest, "p:") {
			t.Errorf("%v: digest %q collides with the project-only form", other.RepositoryScopes, digest)
		}
	}
}

// legacyWorkItemAuthorizationDigestForTest is the work-item census digest's
// v1 document, written out: other classes keep it byte for byte.
func legacyWorkItemAuthorizationDigestForTest(t *testing.T, grants, requested []string) string {
	t.Helper()
	sortedCopy := func(values []string) []string {
		out := append([]string{}, values...)
		sort.Strings(out)
		return out
	}
	encoded, err := json.Marshal(struct {
		Version                   string   `json:"version"`
		PrincipalRepositoryGrants []string `json:"principal_repository_grants"`
		RequestedRepositoryScope  []string `json:"requested_repository_scope"`
	}{WorkItemAuthorizationDigestVersion, sortedCopy(grants), sortedCopy(requested)})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func TestTheWorkItemCensusDigestIsClassTagged(t *testing.T) {
	requested := []string{"acme/api"}
	for _, principal := range []storage.Principal{
		{OrgID: "org_1"},
		{OrgID: "org_1", RepositoryScopes: []string{"*"}},
		{OrgID: "org_1", RepositoryScopes: []string{"acme/web", "acme/api"}},
	} {
		got, err := WorkItemAuthorizationDigest(principal, requested)
		if err != nil {
			t.Fatal(err)
		}
		if want := legacyWorkItemAuthorizationDigestForTest(t, principal.RepositoryScopes, requested); got != want {
			t.Errorf("%v: census digest changed: %s, want %s", principal.RepositoryScopes, got, want)
		}
	}
	unrestricted, _ := WorkItemAuthorizationDigest(storage.Principal{OrgID: "org_1"}, requested)
	project, err := WorkItemAuthorizationDigest(projectPrincipal(nil, "project.v2:github:P-1"), requested)
	if err != nil {
		t.Fatal(err)
	}
	if len(project) != sha256.Size*2 || strings.ToLower(project) != project {
		t.Fatalf("project-only census digest %q is not the 64-hex envelope", project)
	}
	if project == unrestricted {
		t.Fatal("a project-only census digest with an empty repository list equals the unrestricted one")
	}
	withSentinel, _ := WorkItemAuthorizationDigest(projectPrincipal([]string{reservedRepositoryScopeForTest}, "project.v2:github:P-1"), requested)
	if withSentinel != project {
		t.Error("the census digest hashed the project-only repository list")
	}
	other, _ := WorkItemAuthorizationDigest(projectPrincipal(nil, "project.v2:github:P-1", "project.v2:github:P-2"), requested)
	if other == project {
		t.Error("two different project grants share a census digest")
	}
	narrowed, _ := WorkItemAuthorizationDigest(projectPrincipal(nil, "project.v2:github:P-1"), []string{"acme/web"})
	if narrowed == project {
		t.Error("the requested scope no longer moves the project-only census digest")
	}
}

// R3-3: a census-backed work-item tuple computed under a project grant is
// not-found to an unrestricted reader (the census digest is recomputed for the
// reader), and the reverse.
func TestACensusBackedTupleIsNotFoundAcrossGrantClasses(t *testing.T) {
	project := projectPrincipal(nil, "project.v2:github:P-1")
	unrestricted := storage.Principal{OrgID: "org_1"}
	for _, tc := range []struct {
		name             string
		computer, reader storage.Principal
	}{
		{"project-only tuple, unrestricted reader", project, unrestricted},
		{"unrestricted tuple, project-only reader", unrestricted, project},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := workItemTupleSemanticStateFixture()
			state.WorkItemCensus = validStoredWorkItemCensus(t, tc.computer, WorkItemMembershipCensusFloor, 2000, 1, []string{})
			got := ServeStoredWorkItemTuple(workItemTuplePayloadFixture(t), state, SemanticStateReadAvailable, tc.reader)
			if got.Disposition != WorkItemTupleByIDNotFound || got.Event.Basis != "digest_changed" {
				t.Fatalf("decision %s / %s, want not_found on digest_changed", got.Disposition, got.Event.Basis)
			}
		})
	}
	// Control: the computing unrestricted reader is still served.
	state := workItemTupleSemanticStateFixture()
	state.WorkItemCensus = validStoredWorkItemCensus(t, unrestricted, WorkItemMembershipCensusFloor, 2000, 1, []string{})
	if got := ServeStoredWorkItemTuple(workItemTuplePayloadFixture(t), state, SemanticStateReadAvailable, unrestricted); got.Disposition != WorkItemTupleByIDServed {
		t.Fatalf("the computing reader: %s / %s, want served", got.Disposition, got.Event.Basis)
	}
}

// The project-only census document, written out: the class tag, the sorted
// project set and an empty repository grant list, in this field order.
func TestTheProjectOnlyCensusDocumentIsPinned(t *testing.T) {
	principal := projectPrincipal([]string{reservedRepositoryScopeForTest}, "project.v2:github:P-2", "project.v2:github:P-1")
	got, err := WorkItemAuthorizationDigest(principal, []string{"acme/web", "acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	document := `{"version":"work-item-authorization.v1","grant_class":"project_only","principal_project_grants":["project.v2:github:P-1","project.v2:github:P-2"],"principal_repository_grants":[],"requested_repository_scope":["acme/api","acme/web"]}`
	sum := sha256.Sum256([]byte(document))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("project-only census digest %s, want the digest of %s", got, document)
	}
}
