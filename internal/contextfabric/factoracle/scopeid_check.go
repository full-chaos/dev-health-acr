package factoracle

import (
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// Classes of a raw ops REPO scopeId that is not a raw repository id of the
// extract. They name the form of the difference only, never a value.
const (
	scopeIDCase      = "case"
	scopeIDPrefix    = "prefix"
	scopeIDFormat    = "format"
	scopeIDKind      = "id_kind"
	scopeIDOtherOrg  = "other_org_run"
	scopeIDNotString = "not_string"
)

// ScopeIDCheck counts the REPO rows of the venue's compoundingRisk answers
// whose scopeId is, before any scrubbing, a repository id of the same
// extract (Matched), and the others by class. A replay can join a REPO row
// only when both sides were the same id before scrubbing.
type ScopeIDCheck struct {
	Compared int            `json:"compared"`
	Matched  int            `json:"matched"`
	Misses   map[string]int `json:"misses,omitempty"`
}

// Summary prints the counts and the classes; it holds no id.
func (c ScopeIDCheck) Summary() string {
	parts := make([]string, 0, len(c.Misses))
	for class, n := range c.Misses {
		parts = append(parts, fmt.Sprintf("%s=%d", class, n))
	}
	sort.Strings(parts)
	return fmt.Sprintf("compoundingRisk REPO scopeId raw check: compared %d, matched %d, misses [%s]", c.Compared, c.Matched, strings.Join(parts, " "))
}

// checkRepoScopeIDs reads the raw (unscrubbed) answers and the raw extract.
func checkRepoScopeIDs(extract *Extract, answers []any) ScopeIDCheck {
	repoIDs := map[string]bool{}
	for _, row := range extract.Tables[tableRepos] {
		if id, ok := rowString(row, "id"); ok && id != "" {
			repoIDs[id] = true
		}
	}
	otherIDs := map[string]bool{}
	add := func(table, column string, keep func(Row) bool) {
		for _, row := range extract.Tables[table] {
			if keep != nil && !keep(row) {
				continue
			}
			if v, ok := rowString(row, column); ok && v != "" {
				otherIDs[strings.ToLower(v)] = true
			}
		}
	}
	isRepoRisk := func(row Row) bool { s, _ := rowString(row, "scope"); return strings.EqualFold(s, "repo") }
	for _, row := range extract.Tables[tableCompoundingRiskDaily] {
		if isRepoRisk(row) {
			if id, ok := rowString(row, "scope_id"); ok && id != "" {
				repoIDs[id] = true
			}
		}
	}
	add(tableCompoundingRiskDaily, "scope_id", func(r Row) bool { return !isRepoRisk(r) })
	add(tableTeams, "id", nil)
	add(tableTeamRepoOwnership, "team_id", nil)
	add(tableRepos, "repo", nil)
	add(tableTeamRepoOwnership, "repo_full_name", nil)

	folded, hexOnly := map[string]bool{}, map[string]bool{}
	for id := range repoIDs {
		folded[strings.ToLower(id)] = true
		hexOnly[hexDigits(id)] = true
	}
	check := ScopeIDCheck{Misses: map[string]int{}}
	for _, data := range answers {
		object, _ := data.(map[string]any)
		root, _ := object["compoundingRisk"].(map[string]any)
		for _, row := range items(root["rows"]) {
			if scope, _ := row["scope"].(string); !strings.EqualFold(scope, "repo") {
				continue
			}
			check.Compared++
			id, ok := row["scopeId"].(string)
			if !ok {
				check.Misses[scopeIDNotString]++
				continue
			}
			class := classifyRepoScopeID(id, repoIDs, folded, hexOnly, otherIDs)
			if class == "" {
				check.Matched++
				continue
			}
			check.Misses[class]++
		}
	}
	if len(check.Misses) == 0 {
		check.Misses = nil
	}
	return check
}

// repoScopeKind is the kind prefix ops puts on a REPO scopeId; the id after
// it is the repository id (opsScopeKey removes it for the live compare).
const repoScopeKind = "repository"

// scrubRiskScopeID scrubs a compoundingRisk scopeId as the extract scrubs
// the stored scope_id, so a replay joins the two: a REPO id keeps its kind
// prefix and its repository id is replaced like repos.id.
func scrubRiskScopeID(s *Scrubber, scope, id string) string {
	if !strings.EqualFold(scope, "repo") {
		return s.Token("team", id)
	}
	if bare, ok := strings.CutPrefix(id, repoScopeKind+":"); ok {
		return repoScopeKind + ":" + s.UUID(bare)
	}
	return s.UUID(id)
}

func classifyRepoScopeID(id string, exact, folded, hexOnly, other map[string]bool) string {
	if exact[opsScopeKey(repoScopeKind, id)] {
		return ""
	}
	lower := strings.ToLower(id)
	if folded[lower] {
		return scopeIDCase
	}
	bare := lower
	if i := strings.LastIndex(lower, ":"); i >= 0 {
		bare = lower[i+1:]
		if folded[bare] {
			return scopeIDPrefix
		}
	}
	if h := hexDigits(bare); h != "" && hexOnly[h] {
		return scopeIDFormat
	}
	if other[lower] || other[bare] {
		return scopeIDKind
	}
	return scopeIDOtherOrg
}

// hexDigits keeps the hex digits of an id, lower case: a UUID in any
// printing (dashes, braces, none) gives one string.
func hexDigits(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	if b.Len() != 32 {
		return ""
	}
	return b.String()
}

// servedData is the data of every served answer of the run, graphql_query
// and run_operation, as the venue gave it.
func servedData(o *Oracle) ([]any, error) {
	var out []any
	for _, call := range o.Calls {
		raw, ok := o.Answer(call)
		if !ok {
			continue
		}
		var answer GraphQLAnswer
		if err := decodeNumbered(raw, &answer); err != nil {
			return nil, err
		}
		if answer.Call != string(directread.CallServed) {
			continue
		}
		data, err := decodeJSON(answer.Data)
		if err != nil {
			return nil, fmt.Errorf("call %s: data is not JSON", call.ShapeID)
		}
		out = append(out, data)
	}
	for _, call := range o.OperationCalls {
		raw, ok := o.OperationAnswer(call)
		if !ok {
			continue
		}
		var answer OperationAnswer
		if err := decodeNumbered(raw, &answer); err != nil {
			return nil, err
		}
		if answer.Call != string(directread.CallServed) {
			continue
		}
		data, err := decodeJSON(answer.Data)
		if err != nil {
			return nil, fmt.Errorf("run_operation %s: data is not JSON", call.ShapeID)
		}
		out = append(out, data)
	}
	return out, nil
}
