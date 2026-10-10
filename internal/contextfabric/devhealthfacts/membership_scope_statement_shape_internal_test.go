package devhealthfacts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The membership scope's unit ids never reach a statement as a literal
// Array(String) parameter: at 36,000 ids a phase-0 statement carrying them that
// way ran into the server's execution limit with nothing read, while the same ids
// as one JSON string (the shape the project mixes already take their pin in)
// finish. Every statement that applies the scope takes the JSON shape.

func TestEveryStatementThatAppliesTheScopeIdsTakesThemAsJSON(t *testing.T) {
	t.Parallel()
	scope := membershipScope{mode: membershipScopeIDs, ids: []string{"wu-1", "wu-2"}}
	bound := factTimeBound{}
	statements := map[string]string{
		"repository/team mix": repoMixStatementScoped([]factTimeBound{bound}, scope, 100),
		"units page":          investmentUnitsStatementScoped(bound, false, scope),
		"units page (cursor)": investmentUnitsStatementScoped(bound, true, scope),
		"project phase 0":     projectMixScopeStatementScoped(bound, scope),
	}
	for name, statement := range statements {
		if !strings.Contains(statement, "JSONExtract({"+membershipScopeJSONParam+":String}, 'Array(String)')") {
			t.Errorf("%s: the scope ids are not applied as the JSON parameter", name)
		}
		if strings.Contains(statement, "scope_ids") || regexp.MustCompile(`work_unit_id IN \{\w+:Array\(String\)\}`).MatchString(statement) {
			t.Errorf("%s: the scope ids ride as a literal Array(String) parameter", name)
		}
	}
	for _, b := range scope.bindings() {
		if _, isString := b.Value.(string); !isString || b.Name != membershipScopeJSONParam {
			t.Errorf("binding %s is %T, want the %s string", b.Name, b.Value, membershipScopeJSONParam)
		}
	}
}

// A source census: no statement text of this package names a work unit set as an
// Array(String) parameter.
func TestNoStatementTextBindsAWorkUnitSetAsAnArrayParameter(t *testing.T) {
	t.Parallel()
	literalArray := regexp.MustCompile(`work_unit_id\s+(NOT\s+)?IN\s+\{\w+:Array\(String\)\}`)
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 50 {
		t.Fatalf("source census did not find the package files (%d, %v): measurement did not happen", len(files), err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if loc := literalArray.FindIndex(text); loc != nil {
			t.Errorf("%s: a work unit set is bound as an Array(String) parameter: %q", file, text[loc[0]:loc[1]])
		}
	}
}
