package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const QueryVersionV1 = "context-query.v1"

const clickHouseEvidenceQueryV1 = `SELECT concat('acr:v1:commit:', hash) AS evidence_ref_id, 'dev_health' AS system, 'commit' AS entity_type, hash AS entity_id, ifNull(message, hash) AS display_label, '' AS safe_uri, 'native' AS provenance, 1.0 AS confidence, ifNull(message, '') AS citation, committer_when AS observed_at FROM git_commits FINAL WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND ({commit_sha:String} = '' OR hash = {commit_sha:String}) AND ({as_of:Nullable(DateTime)} IS NULL OR committer_when <= {as_of:Nullable(DateTime)}) AND ({time_window_days:UInt16} = 0 OR committer_when >= coalesce({as_of:Nullable(DateTime)}, now()) - INTERVAL {time_window_days:UInt16} DAY)`

var ErrPrincipalOrganization = errors.New("contextpacket: principal organization is required")

// ReadPlan is the typed, scoped input to a versioned evidence read adapter.
// Values always derive from authenticated principal and request scope.
type ReadPlan struct {
	Version        string
	OrgID          string
	RepoID         string
	RepoSlug       string
	Branch         string
	CommitSHA      string
	TaskRef        string
	Files          []string
	AsOf           *time.Time
	TimeWindowDays int
	Statement      string
}

// BuildReadPlanV1 creates the only ClickHouse read shape used by this package.
// Callers bind the named parameters rather than concatenating any request value.
func BuildReadPlanV1(principal storage.Principal, request contractsv1.ContextPacketRequest) (ReadPlan, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return ReadPlan{}, ErrPrincipalOrganization
	}
	if request.Scope.TimeWindowDays < 0 || request.Scope.TimeWindowDays > 65_535 {
		return ReadPlan{}, fmt.Errorf("contextpacket: time window must be between 0 and 65535 days")
	}
	slug, err := auth.NormalizeRepositorySlug(request.Repository.Slug)
	if err != nil {
		return ReadPlan{}, fmt.Errorf("normalize repository: %w", err)
	}
	if err := auth.AuthorizeRepository(principal, slug); err != nil {
		return ReadPlan{}, fmt.Errorf("authorize repository: %w", err)
	}
	return ReadPlan{
		Version:        QueryVersionV1,
		OrgID:          principal.OrgID,
		RepoSlug:       slug,
		Branch:         strings.TrimSpace(request.Scope.Branch),
		CommitSHA:      strings.TrimSpace(request.Scope.CommitSHA),
		TaskRef:        strings.TrimSpace(request.Scope.TaskRef),
		Files:          append([]string(nil), request.Scope.Files...),
		AsOf:           request.Scope.AsOf,
		TimeWindowDays: request.Scope.TimeWindowDays,
		Statement:      clickHouseEvidenceQueryV1,
	}, nil
}

// EvaluationStore is a read-only EvidenceStore backed only by the fixed corpus.
// It is suitable for deterministic assembler tests and never reaches a network.
type EvaluationStore struct {
	corpus evalfixture.Corpus
	orgID  string
}

func NewEvaluationStore(corpus evalfixture.Corpus, orgID string) (*EvaluationStore, error) {
	if strings.TrimSpace(orgID) == "" {
		return nil, ErrPrincipalOrganization
	}
	if strings.TrimSpace(corpus.Scenario.Repository.Slug) == "" {
		return nil, errors.New("contextpacket: evaluation corpus repository is required")
	}
	return &EvaluationStore{corpus: corpus, orgID: orgID}, nil
}

func (s *EvaluationStore) ResolveScope(ctx context.Context, principal storage.Principal, request contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	plan, err := BuildReadPlanV1(principal, request)
	if err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	if plan.OrgID != s.orgID || plan.RepoSlug != s.corpus.Scenario.Repository.Slug {
		return contractsv1.ResolvedScope{}, storage.ErrNotFound
	}
	resolution, fallbackReasons := contractsv1.ScopeRepoFallback, scopeFallbacks(plan)
	if plan.CommitSHA != "" {
		resolution = contractsv1.ScopeExactCommit
		fallbackReasons = scopeFallbacks(plan)
		fallbackReasons = withoutFallback(fallbackReasons, "commit_not_requested")
	} else if plan.Branch != "" {
		resolution = contractsv1.ScopeBranchFiltered
		fallbackReasons = withoutFallback(scopeFallbacks(plan), "branch_not_requested")
	}
	return contractsv1.ResolvedScope{
		RepoID:          "fixture:" + s.corpus.Scenario.ScenarioID,
		RepoSlug:        plan.RepoSlug,
		Branch:          plan.Branch,
		CommitSHA:       plan.CommitSHA,
		Resolution:      resolution,
		FallbackReasons: fallbackReasons,
	}, nil
}

func scopeFallbacks(plan ReadPlan) []string {
	values := []string{}
	if plan.CommitSHA == "" {
		values = append(values, "commit_not_requested")
	}
	if plan.Branch == "" {
		values = append(values, "branch_not_requested")
	}
	if plan.TaskRef == "" {
		values = append(values, "task_not_requested")
	}
	if len(plan.Files) == 0 {
		values = append(values, "files_not_requested")
	}
	if plan.AsOf == nil {
		values = append(values, "as_of_not_requested")
	}
	if plan.TimeWindowDays == 0 {
		values = append(values, "time_window_not_requested")
	}
	return values
}
func withoutFallback(values []string, excluded string) []string {
	out := []string{}
	for _, value := range values {
		if value != excluded {
			out = append(out, value)
		}
	}
	return out
}

func (s *EvaluationStore) ContextForTask(ctx context.Context, principal storage.Principal, request contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	if err := ctx.Err(); err != nil {
		return storage.EvidenceBundle{}, err
	}
	scope, err := s.ResolveScope(ctx, principal, request)
	if err != nil {
		return storage.EvidenceBundle{}, fmt.Errorf("resolve evaluation scope: %w", err)
	}
	task, found := s.matchTask(request)
	if !found {
		return storage.EvidenceBundle{ResolvedScope: scope, QueryVersion: QueryVersionV1}, nil
	}
	evidence, watermarks, err := s.taskEvidence(task, request)
	if err != nil {
		return storage.EvidenceBundle{}, err
	}
	return storage.EvidenceBundle{ResolvedScope: scope, Evidence: evidence, Watermarks: watermarks, QueryVersion: QueryVersionV1}, nil
}

func (s *EvaluationStore) ResolveEvidence(ctx context.Context, principal storage.Principal, evidenceRefID string) (contractsv1.ExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	if strings.TrimSpace(principal.OrgID) != s.orgID {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	if err := auth.AuthorizeRepository(principal, s.corpus.Scenario.Repository.Slug); err != nil {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("authorize evidence repository: %w", err)
	}
	record, ok := s.corpus.Evidence[evidenceRefID]
	if !ok {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	evidence, err := evidenceRef(record)
	if err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	expanded := contractsv1.ExpandedEvidence{SchemaVersion: contractsv1.ExpandedEvidenceSchema, Evidence: evidence, ResolvedAt: evidence.ObservedAt, Availability: evidence.Availability, Excerpt: record.Summary, Structured: map[string]any{}}
	if err := validateExpandedEvidence(expanded); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	return expanded, nil
}

func (s *EvaluationStore) matchTask(request contractsv1.ContextPacketRequest) (evalfixture.Task, bool) {
	for _, task := range s.corpus.Tasks {
		if request.Scope.CommitSHA != "" && task.Scope.CommitSHA == request.Scope.CommitSHA {
			return task, true
		}
		if request.Scope.CommitSHA != "" {
			continue
		}
		if request.Scope.TaskRef != "" && task.TaskID == request.Scope.TaskRef {
			return task, true
		}
		if request.Scope.CommitSHA == "" && request.Scope.Branch != "" && task.Scope.CommitSHA == "" && task.Scope.Branch == request.Scope.Branch {
			return task, true
		}
	}
	if request.Scope.CommitSHA == "" && request.Scope.Branch == "" && request.Scope.TaskRef == "" && len(s.corpus.Tasks) > 0 {
		return s.corpus.Tasks[0], true
	}
	return evalfixture.Task{}, false
}

func (s *EvaluationStore) taskEvidence(task evalfixture.Task, request contractsv1.ContextPacketRequest) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, error) {
	evidence := make([]contractsv1.EvidenceRef, 0, len(task.ExpectedEvidenceIDs))
	for _, id := range task.ExpectedEvidenceIDs {
		record, ok := s.corpus.Evidence[id]
		if !ok {
			return nil, nil, fmt.Errorf("evaluation evidence %q: %w", id, storage.ErrNotFound)
		}
		ref, err := evidenceRef(record)
		if err != nil {
			return nil, nil, err
		}
		if request.Scope.AsOf != nil && ref.ObservedAt.After(*request.Scope.AsOf) {
			continue
		}
		if request.Scope.TimeWindowDays > 0 && request.Scope.AsOf != nil && ref.ObservedAt.Before(request.Scope.AsOf.AddDate(0, 0, -request.Scope.TimeWindowDays)) {
			continue
		}
		evidence = append(evidence, ref)
	}
	return evidence, sourceWatermarks(evidence), nil
}

func evidenceRef(record evalfixture.EvidenceRecord) (contractsv1.EvidenceRef, error) {
	observedAt, err := time.Parse(time.RFC3339, record.ObservedAt)
	if err != nil {
		return contractsv1.EvidenceRef{}, fmt.Errorf("parse evidence %q observed_at: %w", record.EvidenceID, err)
	}
	ref := contractsv1.EvidenceRef{SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: record.EvidenceID, Source: contractsv1.EvidenceSource{System: record.System, EntityType: record.EntityType, EntityID: record.EntityID, DisplayLabel: record.DisplayLabel, SafeURI: record.SafeURI}, Provenance: "native", Confidence: 1, Citation: record.Summary, ObservedAt: observedAt, Availability: contractsv1.EvidenceAvailable}
	if err := validateEvidence(ref); err != nil {
		return contractsv1.EvidenceRef{}, err
	}
	return ref, nil
}

func sourceWatermarks(evidence []contractsv1.EvidenceRef) []contractsv1.SourceWatermark {
	latest := map[string]time.Time{}
	for _, ref := range evidence {
		if observed, ok := latest[ref.Source.System]; !ok || ref.ObservedAt.After(observed) {
			latest[ref.Source.System] = ref.ObservedAt
		}
	}
	sources := make([]string, 0, len(latest))
	for source := range latest {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	watermarks := make([]contractsv1.SourceWatermark, 0, len(sources))
	for _, source := range sources {
		observed := latest[source]
		watermarks = append(watermarks, contractsv1.SourceWatermark{Source: source, LastIngestedAt: &observed, Status: "fresh"})
	}
	return watermarks
}
