// Package evalfixture loads and verifies the deterministic, public-safe
// evaluation corpus under testdata/evaluation/v1. It is a read-only corpus
// loader and integrity verifier: it never fabricates evidence, never
// mutates tracked fixtures, and never depends on customer or production
// data. CHAOS-2905 imports this package as a test helper.
package evalfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	// CorpusRelPath is the repository-relative path to the evaluation
	// corpus root.
	CorpusRelPath = "testdata/evaluation/v1"
	scenarioFile  = "scenario.json"
	tasksFile     = "tasks.json"
	manifestFile  = "manifest.json"
	evidenceDir   = "evidence"

	// MinTaskCount is the minimum number of fixed tasks the corpus must
	// declare.
	MinTaskCount = 3
)

// LoadCorpus parses scenario.json, tasks.json, manifest.json, and every
// file under evidence/ into typed values. Unknown fields are rejected so
// drift between the corpus and this package is caught at parse time.
func LoadCorpus(dir string) (Corpus, error) {
	var scenario Scenario
	if err := decodeStrict(filepath.Join(dir, scenarioFile), &scenario); err != nil {
		return Corpus{}, &FileError{Path: scenarioFile, Err: err}
	}

	var tasks taskSet
	if err := decodeStrict(filepath.Join(dir, tasksFile), &tasks); err != nil {
		return Corpus{}, &FileError{Path: tasksFile, Err: err}
	}

	evidenceEntries, err := os.ReadDir(filepath.Join(dir, evidenceDir))
	if err != nil {
		return Corpus{}, &FileError{Path: evidenceDir, Err: err}
	}
	evidence := make(map[string]EvidenceRecord, len(evidenceEntries))
	for _, entry := range evidenceEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		relative := filepath.Join(evidenceDir, entry.Name())
		var record EvidenceRecord
		if err := decodeStrict(filepath.Join(dir, relative), &record); err != nil {
			return Corpus{}, &FileError{Path: relative, Err: err}
		}
		evidence[record.EvidenceID] = record
	}

	return Corpus{
		Dir:           dir,
		CorpusVersion: tasks.SchemaVersion,
		Scenario:      scenario,
		Tasks:         tasks.Tasks,
		Evidence:      evidence,
	}, nil
}

// VerifyCorpus loads the corpus at dir, verifies its SHA-256 manifest,
// validates the fixed task set, and confirms every evidence reference
// resolves. It never mutates dir. Two calls over the same unmodified
// directory return identical results.
func VerifyCorpus(dir string) (Corpus, error) {
	corpus, err := LoadCorpus(dir)
	if err != nil {
		return Corpus{}, fmt.Errorf("load corpus: %w", err)
	}

	var manifestDoc manifest
	if err := decodeStrict(filepath.Join(dir, manifestFile), &manifestDoc); err != nil {
		return Corpus{}, &FileError{Path: manifestFile, Err: err}
	}
	if err := verifyManifest(dir, manifestDoc); err != nil {
		return Corpus{}, err
	}

	if err := validateTaskSet(corpus.Tasks); err != nil {
		return Corpus{}, err
	}
	if err := validateReferences(corpus.Tasks, corpus.Evidence); err != nil {
		return Corpus{}, err
	}

	corpus.CorpusVersion = manifestDoc.CorpusVersion
	return corpus, nil
}

// verifyManifest recomputes the SHA-256 digest of every file listed in
// manifestDoc, and confirms the file set on disk (excluding manifest.json
// itself) matches the listed set exactly.
func verifyManifest(dir string, manifestDoc manifest) error {
	listed := make(map[string]string, len(manifestDoc.Files))
	for _, entry := range manifestDoc.Files {
		listed[entry.Path] = entry.SHA256
	}

	present := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == manifestFile {
			return nil
		}
		present[relative] = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk corpus directory: %w", err)
	}

	for _, path := range sortedKeys(listed) {
		if !present[path] {
			return &FileError{Path: path, Err: ErrMissingFile}
		}
		digest, err := hashFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return &FileError{Path: path, Err: err}
		}
		if digest != listed[path] {
			return &FileError{Path: path, Err: ErrManifestMismatch}
		}
	}
	for _, path := range sortedKeys(present) {
		if _, ok := listed[path]; !ok {
			return &FileError{Path: path, Err: ErrUnlistedFile}
		}
	}
	return nil
}

// validateTaskSet enforces the minimum task count, required branch/commit
// scope coverage, and the presence of at least one controlled
// degraded/empty task.
func validateTaskSet(tasks []Task) error {
	if len(tasks) < MinTaskCount {
		return fmt.Errorf("%w: got %d, want >= %d", ErrTaskCount, len(tasks), MinTaskCount)
	}

	var sawCommitScope, sawBranchOnlyScope, sawDegradedOrEmpty bool
	for _, task := range tasks {
		if task.Scope.CommitSHA != "" {
			sawCommitScope = true
		}
		if task.Scope.Branch != "" && task.Scope.CommitSHA == "" {
			sawBranchOnlyScope = true
		}
		if task.ExpectedStatus == TaskStatusDegraded || task.ExpectedStatus == TaskStatusEmpty {
			sawDegradedOrEmpty = true
		}
	}
	if !sawCommitScope || !sawBranchOnlyScope {
		return fmt.Errorf("%w: need at least one exact-commit task and one branch-only task", ErrScopeCoverage)
	}
	if !sawDegradedOrEmpty {
		return fmt.Errorf("%w: need at least one task with expected_status degraded or empty", ErrDegradedCase)
	}
	return nil
}

// validateReferences confirms every task's ExpectedEvidenceIDs resolve to
// a known evidence record.
func validateReferences(tasks []Task, evidence map[string]EvidenceRecord) error {
	for _, task := range tasks {
		for _, evidenceID := range task.ExpectedEvidenceIDs {
			if _, ok := evidence[evidenceID]; !ok {
				return fmt.Errorf("%w: task %s references %q", ErrDanglingEvidence, task.TaskID, evidenceID)
			}
		}
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func decodeStrict(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return fmt.Errorf("trailing content: %w", err)
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
