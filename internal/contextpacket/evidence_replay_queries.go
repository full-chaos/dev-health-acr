package contextpacket

import "strings"

type branchReplayPredicate struct {
	catalog string
	replay  string
}

var branchReplayPredicates = map[string]branchReplayPredicate{
	"repository_freshness.v1": {catalog: `({branch:String} = '' OR ref = {branch:String})`, replay: `ref_sha256 = {branch_hash:String}`},
	"pull_requests.v1":        {catalog: `({branch:String} = '' OR p.head_branch = {branch:String} OR p.base_branch = {branch:String})`, replay: `(p.head_branch_sha256 = {branch_hash:String} OR p.base_branch_sha256 = {branch_hash:String})`},
	"pull_request_reviews.v1": {catalog: `({branch:String} = '' OR p.head_branch = {branch:String} OR p.base_branch = {branch:String})`, replay: `(p.head_branch_sha256 = {branch_hash:String} OR p.base_branch_sha256 = {branch_hash:String})`},
	"ci_pipeline_runs.v1":     {catalog: `({branch:String} = '' OR c.branch = {branch:String})`, replay: `c.branch_sha256 = {branch_hash:String}`},
	"file_complexity.v1":      {catalog: `({branch:String} = '' OR ref = {branch:String})`, replay: `ref_sha256 = {branch_hash:String}`},
}

func evidenceReplayStatement(query *SourceQuery, branchHash string, repositoryWide bool) (string, bool) {
	statement := query.Statement
	if branchHash == "" {
		return repositoryWideReplayStatement(statement, repositoryWide), true
	}
	predicate, ok := branchReplayPredicates[query.ID]
	if !ok || !strings.Contains(statement, predicate.catalog) {
		return "", false
	}
	return repositoryWideReplayStatement(strings.ReplaceAll(statement, predicate.catalog, predicate.replay), repositoryWide), true
}

func repositoryWideReplayStatement(statement string, repositoryWide bool) string {
	if !repositoryWide {
		return statement
	}
	return `SELECT evidence_ref_id, system, entity_type, entity_id, concat(display_label, '` + repositoryWideSourceLabelSuffix + `') display_label, safe_uri, provenance, confidence, citation, observed_at FROM (` + statement + `)`
}
