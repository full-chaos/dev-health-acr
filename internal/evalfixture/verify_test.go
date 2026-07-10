package evalfixture

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// corpusDir resolves the tracked evaluation corpus relative to this test
// file's package directory. It never mutates the returned directory.
func corpusDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", CorpusRelPath)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("tracked corpus missing at %s: %v", dir, err)
	}
	return dir
}

// copyCorpus copies the tracked corpus tree into an isolated temp directory
// so tests can safely corrupt files without touching tracked fixtures.
func copyCorpus(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy corpus: %v", err)
	}
	return dst
}

// mutateJSONFile decodes path as a JSON object, applies mutate, and writes
// the result back. It keeps the file valid JSON so parsing still succeeds
// and only the byte content (and therefore its hash) changes.
func mutateJSONFile(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	mutate(record)
	mutated, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Given the tracked evaluation corpus
// When VerifyCorpus runs twice
// Then both runs succeed and produce byte-identical, deterministic results.
func TestVerifyCorpus(t *testing.T) {
	dir := corpusDir(t)

	first, err := VerifyCorpus(dir)
	if err != nil {
		t.Fatalf("first VerifyCorpus: %v", err)
	}
	second, err := VerifyCorpus(dir)
	if err != nil {
		t.Fatalf("second VerifyCorpus: %v", err)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first result: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second result: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("VerifyCorpus is not deterministic:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}

	if len(first.Tasks) < MinTaskCount {
		t.Fatalf("expected at least %d tasks, got %d", MinTaskCount, len(first.Tasks))
	}

	var sawCommitScope, sawBranchOnlyScope, sawDegradedOrEmpty bool
	for _, task := range first.Tasks {
		if task.Scope.CommitSHA != "" {
			sawCommitScope = true
		}
		if task.Scope.Branch != "" && task.Scope.CommitSHA == "" {
			sawBranchOnlyScope = true
		}
		if task.ExpectedStatus == TaskStatusDegraded || task.ExpectedStatus == TaskStatusEmpty {
			if len(task.ExpectedEvidenceIDs) != 0 {
				t.Fatalf("degraded/empty task %s must have no expected evidence, got %v", task.TaskID, task.ExpectedEvidenceIDs)
			}
			sawDegradedOrEmpty = true
		} else if len(task.ExpectedEvidenceIDs) == 0 {
			t.Fatalf("normal task %s must have non-empty expected evidence", task.TaskID)
		}
	}
	if !sawCommitScope {
		t.Fatal("expected at least one task scoped to an exact commit SHA")
	}
	if !sawBranchOnlyScope {
		t.Fatal("expected at least one task scoped to a branch without a commit SHA")
	}
	if !sawDegradedOrEmpty {
		t.Fatal("expected at least one controlled degraded/empty task")
	}
}

// Given the corpus copied into an isolated temp directory
// When one evidence file's bytes are changed without updating manifest.json
// Then VerifyCorpus deterministically rejects it with ErrManifestMismatch,
// and the tracked corpus itself remains provably unaffected.
func TestVerifyCorpus_rejects_corrupted_hash(t *testing.T) {
	dir := copyCorpus(t, corpusDir(t))
	target := filepath.Join(dir, "evidence", "ev-ci-checkout-001.json")
	mutateJSONFile(t, target, func(record map[string]any) {
		record["summary"] = "corrupted for TestVerifyCorpus_rejects_corrupted_hash"
	})

	if _, err := VerifyCorpus(dir); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("expected ErrManifestMismatch, got %v", err)
	}

	if _, err := VerifyCorpus(corpusDir(t)); err != nil {
		t.Fatalf("tracked corpus must remain valid after corrupting the copy: %v", err)
	}
}

// Given the corpus copied into an isolated temp directory
// When an evidence file listed in manifest.json is deleted
// Then VerifyCorpus deterministically rejects it with ErrMissingFile.
func TestVerifyCorpus_rejects_missing_manifest_file(t *testing.T) {
	dir := copyCorpus(t, corpusDir(t))
	target := filepath.Join(dir, "evidence", "ev-pr-auth-002.json")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove %s: %v", target, err)
	}

	if _, err := VerifyCorpus(dir); !errors.Is(err, ErrMissingFile) {
		t.Fatalf("expected ErrMissingFile, got %v", err)
	}
}

// Given the corpus copied into an isolated temp directory
// When a file exists under evidence/ but is not listed in manifest.json
// Then VerifyCorpus deterministically rejects it with ErrUnlistedFile.
func TestVerifyCorpus_rejects_unlisted_file(t *testing.T) {
	dir := copyCorpus(t, corpusDir(t))
	stray := filepath.Join(dir, "evidence", "ev-not-in-manifest.json")
	if err := os.WriteFile(stray, []byte(`{"schema_version":"evaluation_evidence.v1"}`), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	if _, err := VerifyCorpus(dir); !errors.Is(err, ErrUnlistedFile) {
		t.Fatalf("expected ErrUnlistedFile, got %v", err)
	}
}

// Given a task set referencing an evidence ID that does not exist
// When validateReferences runs
// Then it deterministically rejects with ErrDanglingEvidence.
func TestValidateReferences_rejects_unknown_evidence_id(t *testing.T) {
	tasks := []Task{{
		TaskID:              "t1",
		ExpectedStatus:      TaskStatusComplete,
		ExpectedEvidenceIDs: []string{"ev-missing"},
	}}
	evidence := map[string]EvidenceRecord{}

	if err := validateReferences(tasks, evidence); !errors.Is(err, ErrDanglingEvidence) {
		t.Fatalf("expected ErrDanglingEvidence, got %v", err)
	}
}

// Given a task set that references a known evidence ID
// When validateReferences runs
// Then it succeeds.
func TestValidateReferences_accepts_known_evidence_id(t *testing.T) {
	tasks := []Task{{
		TaskID:              "t1",
		ExpectedStatus:      TaskStatusComplete,
		ExpectedEvidenceIDs: []string{"ev-known"},
	}}
	evidence := map[string]EvidenceRecord{"ev-known": {EvidenceID: "ev-known"}}

	if err := validateReferences(tasks, evidence); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateTaskSet(t *testing.T) {
	tests := []struct {
		name    string
		tasks   []Task
		wantErr error
	}{
		{
			name: "rejects fewer than the minimum task count",
			tasks: []Task{
				{TaskID: "a", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{CommitSHA: "sha"}},
				{TaskID: "b", ExpectedStatus: TaskStatusEmpty, Scope: TaskScope{Branch: "main"}},
			},
			wantErr: ErrTaskCount,
		},
		{
			name: "rejects missing branch/commit scope coverage",
			tasks: []Task{
				{TaskID: "a", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{CommitSHA: "sha", Branch: "main"}},
				{TaskID: "b", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{CommitSHA: "sha2", Branch: "main"}},
				{TaskID: "c", ExpectedStatus: TaskStatusEmpty, Scope: TaskScope{CommitSHA: "sha3", Branch: "main"}},
			},
			wantErr: ErrScopeCoverage,
		},
		{
			name: "rejects missing degraded/empty case",
			tasks: []Task{
				{TaskID: "a", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{CommitSHA: "sha"}},
				{TaskID: "b", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{Branch: "main"}},
				{TaskID: "c", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{Branch: "dev"}},
			},
			wantErr: ErrDegradedCase,
		},
		{
			name: "accepts a well-formed task set",
			tasks: []Task{
				{TaskID: "a", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{CommitSHA: "sha", Branch: "main"}},
				{TaskID: "b", ExpectedStatus: TaskStatusComplete, Scope: TaskScope{Branch: "main"}},
				{TaskID: "c", ExpectedStatus: TaskStatusEmpty, Scope: TaskScope{Branch: "dev"}},
			},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTaskSet(tt.tasks)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// Given a known file's bytes
// When hashFile computes its digest
// Then it matches the independently known SHA-256 hex digest.
func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello evalfixture"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	got, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	const want = "29e269031b056a748c962d91eac08b24f3c867405dbeb57e90a294d1e6aa6771"
	if got != want {
		t.Fatalf("hashFile() = %q, want %q", got, want)
	}
}

// Given a corpus copy whose scenario.json contains an unrecognized field
// When LoadCorpus parses it
// Then decoding fails because typed parsing rejects unknown fields.
func TestLoadCorpus_rejects_unknown_fields(t *testing.T) {
	dir := copyCorpus(t, corpusDir(t))
	target := filepath.Join(dir, "scenario.json")
	mutateJSONFile(t, target, func(record map[string]any) {
		record["unexpected_field"] = "should not parse"
	})

	if _, err := LoadCorpus(dir); err == nil {
		t.Fatal("expected LoadCorpus to reject an unrecognized scenario.json field")
	}
}
