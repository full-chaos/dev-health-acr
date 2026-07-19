package sidecar

import "encoding/json"

func rejectCodeGraphProvenance(payload []byte, status bool) error {
	var root json.RawMessage
	if json.Unmarshal(payload, &root) != nil {
		return errCodeGraphDecode
	}
	return rejectIndexedCommitFields(root, status)
}

func rejectIndexedCommitFields(raw json.RawMessage, status bool) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		for field, value := range object {
			if forbiddenIndexedCommitField(field) || (status && forbiddenStatusProvenanceField(field)) {
				return errCodeGraphDecode
			}
			if err := rejectIndexedCommitFields(value, status && field == "index"); err != nil {
				return err
			}
		}
		return nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) == nil {
		for _, entry := range entries {
			if err := rejectIndexedCommitFields(entry, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func forbiddenIndexedCommitField(field string) bool {
	return map[string]bool{"indexedCommit": true, "indexed_commit": true, "indexedRef": true, "indexed_ref": true, "indexedGitSha": true, "indexedRevision": true, "gitCommit": true, "gitRef": true, "gitRevision": true, "gitSha": true, "gitSHA": true, "git_sha": true, "GitSha": true, "GitSHA": true, "GIT_SHA": true, "commitSha": true, "commitSHA": true, "commit_sha": true, "CommitSha": true, "CommitSHA": true, "COMMIT_SHA": true}[field]
}

func forbiddenStatusProvenanceField(field string) bool {
	return map[string]bool{"commit": true, "ref": true, "revision": true, "commitId": true, "commit_id": true, "sha": true}[field]
}
