package devhealthsource

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12230. A caller that names a subject by canonical id (web's Ask Dev
// page context, requested_scope.subject_hints[] with source
// "ask_dev_page_context") commits on that identity only when the id it built
// is byte-equal to the id the projector minted; any other id silently falls
// back to label search. The caller cannot import Go, so the published
// contract carries test vectors instead, and this test is what keeps them
// honest: every canonical_id below is the OUTPUT of the builder the producer
// itself calls, never a transcription of its format.
//
// ACR_WRITE_CANONICAL_ID_VECTORS=1 rewrites the committed file from this same
// output, so regeneration and verification can never disagree about what
// "the builders' output" means.
const (
	canonicalIDVectorsSchemaVersion = "context_fabric_canonical_id_vectors.v1"
	canonicalIDVectorsWriteEnv      = "ACR_WRITE_CANONICAL_ID_VECTORS"
)

func canonicalIDVectorsPath(t *testing.T) string {
	t.Helper()
	return repopath.Path(t, "contracts", "examples", "v1", "context_fabric_canonical_id_vectors.v1.json")
}

// canonicalIDVector is one published vector. natural_key is a JSON object
// whose members are named after the source columns; encoding/json writes map
// keys sorted, so the rendering is deterministic.
type canonicalIDVector struct {
	Kind        string         `json:"kind"`
	NaturalKey  map[string]any `json:"natural_key"`
	CanonicalID string         `json:"canonical_id,omitempty"`
	Omitted     bool           `json:"omitted,omitempty"`
}

type canonicalIDVectorsDocument struct {
	SchemaVersion string              `json:"schema_version"`
	Vectors       []canonicalIDVector `json:"vectors"`
}

const (
	vectorRepoID      = "cd620f84-2602-8dea-7809-8d1f11825cf4"
	vectorOtherRepoID = "1b0e2a53-0e5d-4a53-9d3b-3e1c2b1f9a10"
)

// canonicalIDVectorsFromBuilders runs the producers' own builders over a fixed,
// hand-chosen list of natural keys. The edge cases are the ones a second
// implementation gets wrong: the zero repository UUID Linear work items carry
// (zeroRepositoryID), ':' and '%' in a .v2 segment (JoinSegments escapes '%'
// first), an input that already holds the literal text "%3A", a colon-bearing
// team id, the largest pull request number the UInt32 column holds, and the
// MaxNaturalKeyBytes bound on both sides (the last kept key and the first
// omitted one).
func canonicalIDVectorsFromBuilders(t *testing.T) canonicalIDVectorsDocument {
	t.Helper()
	var vectors []canonicalIDVector
	add := func(kind string, key map[string]any, id string, omitted bool) {
		vectors = append(vectors, canonicalIDVector{Kind: kind, NaturalKey: key, CanonicalID: id, Omitted: omitted})
	}

	for _, orgID := range []string{"6f1e3a2b-9c4d-4e8f-a1b2-c3d4e5f60718"} {
		add("organization", map[string]any{"org_id": orgID}, contextfabric.OrganizationCanonicalID(orgID), false)
	}
	for _, repoID := range []string{vectorRepoID, zeroRepositoryID} {
		add("repository", map[string]any{"repo_id": repoID}, contextfabric.RepositoryCanonicalID(repoID), false)
	}
	for _, teamID := range []string{"CHAOS", "gl:full.chaos"} {
		add("team", map[string]any{"team_id": teamID}, teamCanonicalID(teamID), false)
	}
	for _, pr := range []struct {
		repoID string
		number int64
	}{{vectorRepoID, 1}, {vectorOtherRepoID, 4294967295}} {
		add("pull_request", map[string]any{"repo_id": pr.repoID, "number": pr.number}, pullRequestCanonicalID(pr.repoID, pr.number), false)
	}

	// The longest work_item_id whose id is exactly MaxNaturalKeyBytes, and
	// one byte more.
	derivedPrefix := len(identity.KindWorkItem+".v2:") + len(identity.EncodeSegment(vectorRepoID)) + len(":")
	lastKept := strings.Repeat("a", identity.MaxNaturalKeyBytes-derivedPrefix)
	for _, wi := range []struct{ repoID, workItemID string }{
		{vectorRepoID, "gh:full-chaos/dev-health-acr#1823"},
		{zeroRepositoryID, "linear:CHAOS-12230"},
		{vectorRepoID, "jira:OPS-7%done"},
		{vectorRepoID, "gitlab:group/proj#7%3A"},
		{vectorRepoID, lastKept},
		{vectorRepoID, lastKept + "a"},
	} {
		id, omitted, err := identity.Derive(identity.KindWorkItem, []string{wi.repoID, wi.workItemID}, nil)
		if err != nil {
			t.Fatalf("derive work_item %q: %v", wi.workItemID, err)
		}
		add("work_item", map[string]any{"repo_id": wi.repoID, "work_item_id": wi.workItemID}, id, omitted)
	}

	for _, project := range []struct{ provider, projectID string }{
		{"linear", "a1b2c3d4-0000-4000-8000-00000000c0de"},
		{"gitlab", "full-chaos:gitlab:71133891"},
		{"jira", "acme:jira:OPS%20board"},
		{"jira", strings.Repeat("p", identity.MaxNaturalKeyBytes)},
	} {
		id, omitted, err := identity.Derive(identity.KindProject, []string{project.provider, project.projectID}, nil)
		if err != nil {
			t.Fatalf("derive project %q: %v", project.projectID, err)
		}
		add("project", map[string]any{"provider": project.provider, "project_id": project.projectID}, id, omitted)
	}
	return canonicalIDVectorsDocument{SchemaVersion: canonicalIDVectorsSchemaVersion, Vectors: vectors}
}

func renderCanonicalIDVectors(t *testing.T, document canonicalIDVectorsDocument) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		t.Fatalf("render canonical-id vectors: %v", err)
	}
	return buf.Bytes()
}

// TestCanonicalIDVectorsMatchTheBuilders is the freshness check: the committed
// contract artifact must be byte-for-byte what the builders produce now.
func TestCanonicalIDVectorsMatchTheBuilders(t *testing.T) {
	path := canonicalIDVectorsPath(t)
	want := renderCanonicalIDVectors(t, canonicalIDVectorsFromBuilders(t))
	if os.Getenv(canonicalIDVectorsWriteEnv) == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the committed canonical-id vectors: %v\nRegenerate with:\n  %s=1 go test ./internal/contextfabric/devhealthsource/ -run TestCanonicalIDVectorsMatchTheBuilders", err, canonicalIDVectorsWriteEnv)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("contracts/examples/v1/context_fabric_canonical_id_vectors.v1.json has drifted from the canonical-id builders' output.\nRegenerate with:\n  %s=1 go test ./internal/contextfabric/devhealthsource/ -run TestCanonicalIDVectorsMatchTheBuilders\n\nbuilders:\n%s\ncommitted:\n%s", canonicalIDVectorsWriteEnv, want, got)
	}
}

// TestCanonicalIDVectorsRoundTripThroughTheirParsers decodes each vector with
// the reader-side parser where one exists, so a vector is not only what the
// producer writes but also what the readers accept: identity.Segments for the
// .v2 kinds, contextfabric.TeamRawKey for team. It also pins that the list
// covers every kind the contract names and that both omission sides occur.
func TestCanonicalIDVectorsRoundTripThroughTheirParsers(t *testing.T) {
	document := canonicalIDVectorsFromBuilders(t)
	kinds := map[string]int{}
	omitted := 0
	for _, vector := range document.Vectors {
		kinds[vector.Kind]++
		if vector.Omitted {
			omitted++
			if vector.CanonicalID != "" {
				t.Errorf("%s %v: an omitted vector must carry no canonical_id, got %q", vector.Kind, vector.NaturalKey, vector.CanonicalID)
			}
			if vector.Kind != identity.KindWorkItem && vector.Kind != identity.KindProject {
				t.Errorf("%s %v: only .v2 kinds can be omitted", vector.Kind, vector.NaturalKey)
			}
			continue
		}
		if vector.CanonicalID == "" {
			t.Errorf("%s %v: a kept vector must carry a canonical_id", vector.Kind, vector.NaturalKey)
			continue
		}
		if len(vector.CanonicalID) > identity.MaxNaturalKeyBytes {
			t.Errorf("%s %v: kept id is %d bytes, over MaxNaturalKeyBytes", vector.Kind, vector.NaturalKey, len(vector.CanonicalID))
		}
		switch vector.Kind {
		case identity.KindWorkItem:
			assertSegments(t, vector, []string{vector.NaturalKey["repo_id"].(string), vector.NaturalKey["work_item_id"].(string)})
		case identity.KindProject:
			assertSegments(t, vector, []string{vector.NaturalKey["provider"].(string), vector.NaturalKey["project_id"].(string)})
		case "team":
			raw, ok := contextfabric.TeamRawKey(vector.CanonicalID)
			if !ok || raw != vector.NaturalKey["team_id"] {
				t.Errorf("team %v: TeamRawKey(%q) = %q, %v", vector.NaturalKey, vector.CanonicalID, raw, ok)
			}
		}
	}
	for _, kind := range []string{"organization", "repository", "team", "pull_request", "work_item", "project"} {
		if kinds[kind] == 0 {
			t.Errorf("no vector of kind %s", kind)
		}
	}
	if len(kinds) != 6 {
		t.Errorf("vector kinds = %v, want exactly the six the schema names", kinds)
	}
	if omitted == 0 {
		t.Error("no omitted vector: the MaxNaturalKeyBytes bound is unpinned")
	}
}

func assertSegments(t *testing.T, vector canonicalIDVector, want []string) {
	t.Helper()
	got, ok := identity.Segments(vector.Kind, vector.CanonicalID)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("%s: identity.Segments(%q) = %q, %v; want %q", vector.Kind, vector.CanonicalID, got, ok, want)
	}
}

// The vectors above pin the BUILDERS. These two pin that the producer sites no
// other test reaches actually call them (CHAOS-12230): a site routed back to an
// inline string would mint ids the vectors no longer describe. The repository,
// pull request and edge-endpoint sites are already held by the scan tests of
// this package; the organization node and the incident edge id were not.

// TestOrganizationNodeIsMintedByItsCanonicalIDBuilder: the organization node's
// id is OrganizationCanonicalID of its org id.
func TestOrganizationNodeIsMintedByItsCanonicalIDBuilder(t *testing.T) {
	const orgID = "6f1e3a2b-9c4d-4e8f-a1b2-c3d4e5f60718"
	node := organizationCandidate(orgID, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	if node.entity == nil {
		t.Fatal("organizationCandidate built no entity")
	}
	if got, want := node.entity.Subject.CanonicalID, contextfabric.OrganizationCanonicalID(orgID); got != want {
		t.Fatalf("organization node id = %q, want OrganizationCanonicalID(%q) = %q", got, orgID, want)
	}
}

// TestIncidentRepositoryEdgeIDIsDerivedFromItsOwnEndpoints: an incident's
// BELONGS_TO_REPOSITORY edge id is the relationship digest of the endpoints the
// edge itself carries, and its repository endpoint is RepositoryCanonicalID.
// The edge id is computed apart from the endpoint, so an id built from a
// different repository string would name an edge between subjects that do not
// exist, and a retraction by that id would never reach the asserted edge.
func TestIncidentRepositoryEdgeIDIsDerivedFromItsOwnEndpoints(t *testing.T) {
	const repoID = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a01"
	observed := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	row := incidentRow{
		id: "8e4b1d70-2c9a-4f63-b815-000000000001", title: "checkout timeouts", observedAt: observed, cursorAt: observed,
		activeRepos: parseIncidentRepos([]string{repoID + "|acme/checkout"}),
	}
	var stats incidentPageStats
	edges := 0
	for _, c := range incidentRowCandidates(row, &stats) {
		if c.relationship == nil {
			continue
		}
		edges++
		edge := c.relationship
		if got, want := edge.To.CanonicalID, contextfabric.RepositoryCanonicalID(repoID); got != want {
			t.Errorf("incident edge To = %q, want RepositoryCanonicalID(%q) = %q", got, repoID, want)
		}
		if want := identity.DeriveRelationship(identity.RelationshipFamilyIncidentRepository, edge.From.CanonicalID, edge.To.CanonicalID, string(edge.Type)); edge.RelationshipID != want {
			t.Errorf("incident edge id = %q, want the digest of its own endpoints %q", edge.RelationshipID, want)
		}
	}
	if edges != 1 {
		t.Fatalf("incident row projected %d edges, want 1", edges)
	}
}
