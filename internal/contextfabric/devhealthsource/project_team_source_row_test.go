package devhealthsource

import (
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The project-team source-row read and the projector read ONE store: the
// stored group the projector pages, looked up by its sort key with the row
// key the ref carries, so the cited row is the projected edge's own group.
func TestProjectTeamSourceRowReadsTheProjectorsStoredGroup(t *testing.T) {
	read := ProjectTeamSourceRowQuery()
	if read.ID != contextpacket.ProjectTeamSourceRowQueryID {
		t.Fatalf("id %q", read.ID)
	}
	for _, want := range []string{
		"FROM project_team_ownership_groups_current AS g FINAL WHERE g.org_id = {org_id:String} AND g.row_key = substring({evidence_locator:String}, " + projectTeamSourceRowIDOffset + ")",
		"g.is_deleted = 0",
		"g.observed_at observed_at",
	} {
		if !strings.Contains(read.Statement, want) {
			t.Fatalf("the source-row statement lacks %q:\n%s", want, read.Statement)
		}
	}
	if !strings.Contains(read.Statement, "if(g.source_name = 'native', 'native', 'heuristic') provenance") {
		t.Fatal("provenance must follow the group's source: native is the provider's own assertion, every other source is inferred")
	}
	if !strings.Contains(read.Statement, "edge_suppressed = 0") {
		t.Fatal("a suppressed group (retraction) must not be a readable row")
	}
}

// The row key the read looks up is the text of the ref after its kind prefix:
// the ref's id segment and the stored row key are the same four segments in
// the same order under the same escape, for any segment text.
func TestProjectTeamRefCarriesTheStoredRowKey(t *testing.T) {
	for _, values := range [][]string{
		{"github", "P-1", "TEAM-1", "native"},
		{"jira", "a:b", "%3A", "manual"},
		{"linear", "%", ":", ""},
	} {
		ref, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityProjectTeamV2, values...)
		offset := len(contractsv1.ContextFabricEvidenceRefPrefix+string(contractsv1.ContextFabricEvidenceEntityProjectTeamV2)+":") + 1
		if projectTeamSourceRowIDOffset != strconv.Itoa(offset) {
			t.Fatalf("offset %s, want %d", projectTeamSourceRowIDOffset, offset)
		}
		if got, want := ref[offset-1:], identity.JoinSegments(values...); got != want {
			t.Errorf("%v: ref id segment %q, stored row key %q", values, got, want)
		}
	}
}
