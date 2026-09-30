package contextpacket_test

import (
	"regexp"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

var (
	orgScopeTablePattern   = regexp.MustCompile(`(?:FROM|JOIN)\s+([a-z_]+)(?:\s+AS\s+([a-z]+))?`)
	orgScopeBindingPattern = regexp.MustCompile(`(?:toString\()?(?:([a-z]+)\.)?org_id\)?\s*=\s*\{org_id:String\}`)
	orgScopeJoinPattern    = regexp.MustCompile(`(?:toString\()?([a-z]+)\.org_id\)?\s*=\s*(?:toString\()?([a-z]+)\.org_id\)?`)
)

// orgScopeViolations names every table of statement that is not scoped to
// the caller's organization: not bound to {org_id} directly, and not joined
// on org_id to a table that is. A table devhealthschema does not declare is
// held to the rule too (every ops table the catalog reads carries org_id:
// git_commits and git_commit_stats since ops migration 027); only a
// declared table without an org_id column is exempt. It returns an error
// when it finds no table at all, so a statement shape its patterns cannot
// parse fails instead of passing.
func orgScopeViolations(statement string) ([]string, bool) {
	constrained := map[string]bool{}
	unaliasedConstrained := false
	for _, match := range orgScopeBindingPattern.FindAllStringSubmatch(statement, -1) {
		if match[1] == "" {
			unaliasedConstrained = true
		} else {
			constrained[match[1]] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, match := range orgScopeJoinPattern.FindAllStringSubmatch(statement, -1) {
			if constrained[match[1]] != constrained[match[2]] {
				constrained[match[1]], constrained[match[2]], changed = true, true, true
			}
		}
	}
	tables := orgScopeTablePattern.FindAllStringSubmatch(statement, -1)
	var violations []string
	for _, table := range tables {
		columns, declared := devhealthschema.ProductionColumns[table[1]]
		hasOrg := !declared
		for _, column := range columns {
			hasOrg = hasOrg || column.Name == "org_id"
		}
		if !hasOrg {
			continue
		}
		alias := table[2]
		if (alias == "" && !unaliasedConstrained) || (alias != "" && !constrained[alias]) {
			violations = append(violations, "table "+table[1]+" (alias \""+alias+"\")")
		}
	}
	return violations, len(tables) > 0
}

// CHAOS-7237: every table of every packet catalog statement is scoped to the
// caller's organization. ops mints repos.id deterministically from the
// repository name, not per organization (providersync repositoryIdentity,
// externalRepoUUID, internal ingest NewSHA1(repo URL)), so two organizations
// that sync one repository share its UUID, and a join on repo_id alone
// returns the other organization's rows into this one's context packet.
func TestEveryCatalogStatementScopesEveryTableToTheOrganization(t *testing.T) {
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		violations, parsed := orgScopeViolations(query.Statement)
		if !parsed {
			t.Fatalf("%s: no table found: the sweep matched nothing", query.ID)
		}
		for _, violation := range violations {
			t.Errorf("%s: %s is not scoped to the organization: another organization's rows can join\n%s", query.ID, violation, query.Statement)
		}
	}
}

// The sweep sees the defect it exists for: a repo_id-only join to repos.
func TestOrgScopeSweepFlagsARepositoryOnlyJoin(t *testing.T) {
	violations, parsed := orgScopeViolations(`SELECT 1 FROM git_pull_requests AS p FINAL INNER JOIN repos AS repo FINAL ON repo.id = p.repo_id WHERE repo.org_id = {org_id:String} AND repo.id = {repo_id:UUID}`)
	if !parsed || len(violations) != 1 || violations[0] != `table git_pull_requests (alias "p")` {
		t.Fatalf("violations = %v, parsed %v", violations, parsed)
	}
	if violations, _ := orgScopeViolations(`SELECT 1 FROM git_pull_requests AS p FINAL INNER JOIN repos AS repo FINAL ON repo.id = p.repo_id AND repo.org_id = p.org_id WHERE repo.org_id = {org_id:String}`); len(violations) != 0 {
		t.Fatalf("a join-scoped table flagged: %v", violations)
	}
	if violations, _ := orgScopeViolations(`SELECT 1 FROM file_hotspot_daily WHERE repo_id = {repo_id:UUID}`); len(violations) != 1 {
		t.Fatalf("an unaliased unscoped table passed: %v", violations)
	}
}
