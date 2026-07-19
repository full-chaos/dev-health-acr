package sidecar

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	maxLocalIndexProviderIDBytes       = 64
	maxLocalIndexProviderVersionBytes  = 64
	maxLocalIndexQueryIDBytes          = 64
	maxLocalIndexQueryVersionBytes     = 64
	maxLocalTaskIDBytes                = 256
	maxLocalTaskBytes                  = 4096
	maxLocalEvidenceIDBytes            = 256
	maxLocalEvidenceLocatorBytes       = 512
	maxLocalEvidenceTitleBytes         = 512
	maxLocalEvidenceExcerptBytes       = 8192
	maxLocalEvidenceItems              = 12
	maxLocalEvidenceTokens             = 4000
	maxLocalEvidenceBundlePayloadBytes = 32768
)

var (
	ErrLocalIndexUnavailable         = errors.New("acr: local index is unavailable")
	ErrLocalEvidenceNotFound         = errors.New("acr: local index evidence was not found")
	ErrInvalidLocalIndexCapabilities = errors.New("acr: local index capabilities are invalid")
	ErrInvalidLocalContextRequest    = errors.New("acr: local context request is invalid")
	ErrInvalidLocalEvidenceBundle    = errors.New("acr: local evidence bundle is invalid")
)

// LocalIndexProvider supplies bounded local evidence without exposing a provider-specific transport.
type LocalIndexProvider interface {
	Capabilities(context.Context) (LocalIndexCapabilities, error)
	ContextForTask(context.Context, LocalContextRequest) (LocalEvidenceBundle, error)
	ResolveEvidence(context.Context, string) (LocalExpandedEvidence, error)
}

// LocalIndexCapabilities describes the bounded evidence surface of a local provider.
type LocalIndexCapabilities struct {
	ProviderID      string
	ProviderVersion string
	Available       bool
	MaxItems        int
	MaxOutputTokens int
	Status          LocalIndexStatus
	Freshness       LocalIndexFreshness
}

// LocalContextRequest is a bounded, provider-neutral request for local evidence.
type LocalContextRequest struct {
	TaskID              string
	Goal                string
	TaskRef             string
	RequestedCategories []contractsv1.PacketCategory
	Workspace           *LocalWorkspaceSnapshot
	MaxItems            int
	MaxOutputTokens     int
}

type LocalChangedFilesState string

const (
	LocalChangedFilesNotRequested LocalChangedFilesState = "not_requested"
	LocalChangedFilesComplete     LocalChangedFilesState = "complete"
	LocalChangedFilesTruncated    LocalChangedFilesState = "truncated"
)

type LocalRepositoryIdentity struct {
	Host string
	Slug string
}

// LocalWorkspaceSnapshot is a trusted, provider-neutral scope snapshot.
// GitRoot is operational-only and never enters an evidence result.
type LocalWorkspaceSnapshot struct {
	GitRoot           string
	Repository        LocalRepositoryIdentity
	Branch            string
	CommitSHA         string
	Detached          bool
	ChangedFiles      []string
	ChangedFilesState LocalChangedFilesState
}

// LocalEvidenceBundle is an ordered, bounded local evidence result.
type LocalEvidenceBundle struct {
	ProviderID      string
	ProviderVersion string
	QueryID         string
	QueryVersion    string
	IndexedAt       *time.Time
	IndexedRef      string
	IndexedCommit   string
	Warnings        []string
	Status          LocalIndexStatus
	Freshness       LocalIndexFreshness
	Truncated       bool
	Evidence        []LocalExpandedEvidence
}

// LocalExpandedEvidence is a bounded local evidence item addressed by an opaque ID.
type LocalExpandedEvidence struct {
	ID              string
	Locator         string
	Title           string
	Excerpt         string
	EstimatedTokens int
	QueryID         string
	Relation        string
	RepositoryPath  string
	StartLine       int
}

type localIndexValidationError struct {
	sentinel error
	field    string
}

func (e *localIndexValidationError) Error() string {
	return "acr: invalid local index " + e.field
}

func (e *localIndexValidationError) Unwrap() error { return e.sentinel }

func invalidLocalIndexValue(sentinel error, field string) error {
	return &localIndexValidationError{sentinel: sentinel, field: field}
}

func NormalizeLocalEvidenceBundle(bundle LocalEvidenceBundle) (LocalEvidenceBundle, error) {
	if err := ValidateLocalEvidenceBundle(bundle); err != nil {
		return LocalEvidenceBundle{}, err
	}
	return copyLocalEvidenceBundle(bundle), nil
}

func NormalizeLocalEvidenceBundleForRequest(request LocalContextRequest, capabilities LocalIndexCapabilities, bundle LocalEvidenceBundle) (LocalEvidenceBundle, error) {
	if err := ValidateLocalEvidenceBundleForRequest(request, capabilities, bundle); err != nil {
		return LocalEvidenceBundle{}, err
	}
	return copyLocalEvidenceBundle(bundle), nil
}

func copyLocalEvidenceBundle(bundle LocalEvidenceBundle) LocalEvidenceBundle {
	normalized := bundle
	if bundle.IndexedAt != nil {
		indexedAt := *bundle.IndexedAt
		normalized.IndexedAt = &indexedAt
	}
	normalized.Warnings = append([]string(nil), bundle.Warnings...)
	normalized.Evidence = make([]LocalExpandedEvidence, len(bundle.Evidence))
	copy(normalized.Evidence, bundle.Evidence)
	return normalized
}

type DisabledLocalIndexProvider struct{}

func NewDisabledLocalIndexProvider() DisabledLocalIndexProvider { return DisabledLocalIndexProvider{} }

func (DisabledLocalIndexProvider) Capabilities(ctx context.Context) (LocalIndexCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return LocalIndexCapabilities{}, err
	}
	return LocalIndexCapabilities{}, nil
}

func (DisabledLocalIndexProvider) ContextForTask(ctx context.Context, request LocalContextRequest) (LocalEvidenceBundle, error) {
	if err := ctx.Err(); err != nil {
		return LocalEvidenceBundle{}, err
	}
	if err := ValidateLocalContextRequest(request); err != nil {
		return LocalEvidenceBundle{}, err
	}
	return LocalEvidenceBundle{}, ErrLocalIndexUnavailable
}

func (DisabledLocalIndexProvider) ResolveEvidence(ctx context.Context, _ string) (LocalExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return LocalExpandedEvidence{}, err
	}
	return LocalExpandedEvidence{}, ErrLocalEvidenceNotFound
}

var _ LocalIndexProvider = DisabledLocalIndexProvider{}
