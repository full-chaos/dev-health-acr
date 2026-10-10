package devhealthsource

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every collapse of ownership rows to the row that represents a fact uses
// ownershipFactOrder. A second inline ordering would let the edge builders and
// the team authorization list disagree about which facts are current.
func TestOwnershipRowsCollapseThroughOneOrderingOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v files=%d", err, len(files))
	}
	inline := regexp.MustCompile(`argMax(If)?\(tuple\(o\.valid_to\)|argMax\(o\.[a-z_]+, *\(`)
	order := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, loc := range inline.FindAllStringIndex(text, -1) {
			after := text[loc[0]:min(len(text), loc[1]+120)]
			if !strings.Contains(after, "ownershipFactOrder") && !strings.Contains(after, "repositoryTeamsLatestOrder") {
				t.Errorf("%s: an ownership collapse orders by an inline key: %s", file, after)
			}
		}
		order += strings.Count(text, "o.valid_to IS NULL, if(")
	}
	if order != 1 {
		t.Fatalf("the ownership fact ordering is spelled %d times, want exactly once (ownershipFactOrder)", order)
	}
}
