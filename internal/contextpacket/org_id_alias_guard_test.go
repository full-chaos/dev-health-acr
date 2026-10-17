package contextpacket

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// A ClickHouse SELECT that aliases an expression as org_id and filters org_id
// in the same scope applies the filter to the alias: the alias wins over the
// table column, the filter becomes a constant true, and the read crosses
// organizations. orgIDAliasPattern finds every `<left> AS org_id`; an alias is
// accepted only when its left side is the plain or a qualified org_id column.
var orgIDAliasPattern = regexp.MustCompile(`(?i)(\S+)\s+AS\s+org_id\b`)

var orgIDColumn = regexp.MustCompile(`(?i)^(?:[a-z_][a-z0-9_]*\.)?org_id$`)

// orgIDAliasViolations returns the left side of every alias of org_id that is
// not a column of that name.
func orgIDAliasViolations(src string) []string {
	var out []string
	for _, m := range orgIDAliasPattern.FindAllStringSubmatch(src, -1) {
		left := strings.TrimLeft(m[1], "(,\"'`")
		if !orgIDColumn.MatchString(left) {
			out = append(out, m[1])
		}
	}
	return out
}

// orgIDAliasAllowed names the one construction that adds org_id by alias: it
// does so over a folded derived table that has no org_id column, with no org
// filter in that scope.
var orgIDAliasAllowed = map[string]int{
	filepath.FromSlash("internal/contextfabric/devhealthfacts/investment_issue_size.go"): 1,
}

func TestOrgIDAliasDetection(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sql  string
		want int
	}{
		"parameter alias":       {`SELECT {org_id:String} AS org_id FROM t WHERE org_id = {org_id:String}`, 1},
		"expression alias":      {`SELECT toString(x) AS org_id FROM t`, 1},
		"lower case":            {`select 1 as org_id from t`, 1},
		"function with a paren": {`SELECT any(o) AS org_id FROM t`, 1},
		"plain column":          {`SELECT org_id AS org_id FROM t`, 0},
		"qualified column":      {`SELECT w.org_id AS org_id FROM t AS w`, 0},
		"a longer alias name":   {`SELECT 1 AS org_id_count FROM t`, 0},
		"no alias":              {`SELECT org_id FROM t WHERE org_id = {org_id:String}`, 0},
		"a table alias org_id":  {`SELECT 1 FROM t AS org_id`, 0},
		"two aliases, one bad":  {`SELECT w.org_id AS org_id, 1 AS org_id FROM t AS w`, 1},
	} {
		if name == "a table alias org_id" {
			// FROM t AS org_id is a table alias, not a column alias; the scan
			// cannot tell, so it must flag it for a human.
			tc.want = 1
		}
		if got := len(orgIDAliasViolations(tc.sql)); got != tc.want {
			t.Errorf("%s: %d violations, want %d (%s)", name, got, tc.want, tc.sql)
		}
	}
}

func TestNoOrgIDAliasShadowingInClickHouseStatements(t *testing.T) {
	t.Parallel()
	root := repopath.Root(t)
	seen := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if (ext != ".go" && ext != ".sql") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, left := range orgIDAliasViolations(string(body)) {
			seen[rel]++
			if seen[rel] > orgIDAliasAllowed[rel] {
				t.Errorf("%s: `%s AS org_id` aliases an expression as org_id; a filter on org_id in the same scope then reads the alias, not the column", rel, left)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range orgIDAliasAllowed {
		if seen[rel] != want {
			t.Errorf("%s: %d org_id aliases, the allow-list names %d; update the list with the change", rel, seen[rel], want)
		}
	}
}
