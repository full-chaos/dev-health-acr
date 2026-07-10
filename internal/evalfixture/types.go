package evalfixture

import (
	"errors"
	"fmt"
)

// TaskStatus mirrors the subset of internal/contracts/v1.PacketStatus values a
// fixture task may declare as its expected outcome.
type TaskStatus string

const (
	TaskStatusComplete TaskStatus = "complete"
	TaskStatusPartial  TaskStatus = "partial"
	TaskStatusDegraded TaskStatus = "degraded"
	TaskStatusEmpty    TaskStatus = "empty"
)

var (
	ErrManifestMismatch = errors.New("evalfixture: manifest hash mismatch")
	ErrMissingFile      = errors.New("evalfixture: file listed in manifest is missing on disk")
	ErrUnlistedFile     = errors.New("evalfixture: file present on disk but not listed in manifest")
	ErrDanglingEvidence = errors.New("evalfixture: task references an unknown evidence id")
	ErrTaskCount        = errors.New("evalfixture: corpus has fewer than the minimum required tasks")
	ErrScopeCoverage    = errors.New("evalfixture: corpus missing required branch/commit scope coverage")
	ErrDegradedCase     = errors.New("evalfixture: corpus missing a controlled degraded/empty task")
)

// FileError wraps a corpus integrity failure with the offending path.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return fmt.Sprintf("evalfixture: %s: %v", e.Path, e.Err) }
func (e *FileError) Unwrap() error { return e.Err }

type RepositoryRef struct {
	Slug      string `json:"slug"`
	RemoteURL string `json:"remote_url"`
}

type Scenario struct {
	SchemaVersion   string        `json:"schema_version"`
	ScenarioID      string        `json:"scenario_id"`
	Description     string        `json:"description"`
	Repository      RepositoryRef `json:"repository"`
	DefaultBranch   string        `json:"default_branch"`
	GeneratedBy     string        `json:"generated_by"`
	CleanRoomNotice string        `json:"clean_room_notice"`
}

type TaskScope struct {
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
}

type Task struct {
	SchemaVersion       string     `json:"schema_version"`
	TaskID              string     `json:"task_id"`
	Goal                string     `json:"goal"`
	Scope               TaskScope  `json:"scope"`
	ExpectedStatus      TaskStatus `json:"expected_status"`
	ExpectedEvidenceIDs []string   `json:"expected_evidence_ids"`
	DegradedReason      string     `json:"degraded_reason,omitempty"`
	Notes               string     `json:"notes,omitempty"`
}

type taskSet struct {
	SchemaVersion string `json:"schema_version"`
	Tasks         []Task `json:"tasks"`
}

// EvidenceRecord is a synthetic source-evidence fixture referenced by
// Task.ExpectedEvidenceIDs.
type EvidenceRecord struct {
	SchemaVersion string `json:"schema_version"`
	EvidenceID    string `json:"evidence_id"`
	System        string `json:"system"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	DisplayLabel  string `json:"display_label"`
	SafeURI       string `json:"safe_uri,omitempty"`
	Summary       string `json:"summary"`
	ObservedAt    string `json:"observed_at"`
}

type manifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	SchemaVersion string          `json:"schema_version"`
	CorpusVersion string          `json:"corpus_version"`
	Files         []manifestEntry `json:"files"`
}

// Corpus is the typed, parsed evaluation fixture corpus.
type Corpus struct {
	Dir           string                    `json:"-"`
	CorpusVersion string                    `json:"corpus_version"`
	Scenario      Scenario                  `json:"scenario"`
	Tasks         []Task                    `json:"tasks"`
	Evidence      map[string]EvidenceRecord `json:"evidence"`
}
