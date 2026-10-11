package devhealthsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The project-team source-row read and the projector select groups from ONE
// statement: both embed projectTeamsGroupSQL, and no second copy of the group
// (its arm list) is spelled anywhere in this package.
func TestProjectTeamSourceRowReadsTheProjectorsGroup(t *testing.T) {
	group := projectTeamsGroupSQL(false)
	read := ProjectTeamSourceRowQuery()
	if read.ID != contextpacket.ProjectTeamSourceRowQueryID || !strings.Contains(read.Statement, group) {
		t.Fatalf("the source-row statement does not embed the projector's group:\n%s", read.Statement)
	}
	for _, cursor := range []cursorState{{}, {After: "k"}} {
		if !strings.HasPrefix(projectTeamsStatement(cursor), group) {
			t.Fatalf("the projector's statement does not start with the shared group (cursor %+v)", cursor)
		}
	}
	if !strings.Contains(read.Statement, "if(g.source_name = 'native', 'native', 'heuristic') provenance") {
		t.Fatal("provenance must follow the group's source: native is the provider's own assertion, every other source is inferred")
	}
	if !strings.Contains(read.Statement, "edge_suppressed = 0") {
		t.Fatal("a suppressed group (retraction) must not be a readable row")
	}
}

func TestProjectTeamGroupIsSpelledOnce(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v %d", err, len(files))
	}
	uses := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		uses += strings.Count(string(body), "strings.Join(projectTeamsArmsFor(")
	}
	if uses != 1 {
		t.Fatalf("project-team arm composition found %d times, want exactly 1 (inside projectTeamsGroupSQL); a second statement copy has appeared", uses)
	}
}
