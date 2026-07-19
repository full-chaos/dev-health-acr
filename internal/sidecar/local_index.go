package sidecar

import (
	"context"
	"errors"
	"strings"
	"unicode"
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
	maxLocalEvidenceItems              = 8
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
}

// LocalContextRequest is a bounded, provider-neutral request for local evidence.
type LocalContextRequest struct {
	TaskID          string
	Task            string
	MaxItems        int
	MaxOutputTokens int
}

// LocalEvidenceBundle is an ordered, bounded local evidence result.
type LocalEvidenceBundle struct {
	ProviderID      string
	ProviderVersion string
	QueryID         string
	QueryVersion    string
	Evidence        []LocalExpandedEvidence
}

// LocalExpandedEvidence is a bounded local evidence item addressed by an opaque ID.
type LocalExpandedEvidence struct {
	ID              string
	Locator         string
	Title           string
	Excerpt         string
	EstimatedTokens int
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

func ValidateLocalIndexCapabilities(capabilities LocalIndexCapabilities) error {
	if !capabilities.Available {
		if capabilities.ProviderID != "" || capabilities.ProviderVersion != "" || capabilities.MaxItems != 0 || capabilities.MaxOutputTokens != 0 {
			return invalidLocalIndexValue(ErrInvalidLocalIndexCapabilities, "unavailable capabilities")
		}
		return nil
	}
	if !boundedNonEmpty(capabilities.ProviderID, maxLocalIndexProviderIDBytes) || !boundedNonEmpty(capabilities.ProviderVersion, maxLocalIndexProviderVersionBytes) {
		return invalidLocalIndexValue(ErrInvalidLocalIndexCapabilities, "provider")
	}
	if !boundedPositive(capabilities.MaxItems, maxLocalEvidenceItems) || !boundedPositive(capabilities.MaxOutputTokens, maxLocalEvidenceTokens) {
		return invalidLocalIndexValue(ErrInvalidLocalIndexCapabilities, "limits")
	}
	return nil
}

func ValidateLocalContextRequest(request LocalContextRequest) error {
	if !boundedNonEmpty(request.TaskID, maxLocalTaskIDBytes) || !boundedNonEmpty(request.Task, maxLocalTaskBytes) {
		return invalidLocalIndexValue(ErrInvalidLocalContextRequest, "task")
	}
	if !boundedPositive(request.MaxItems, maxLocalEvidenceItems) || !boundedPositive(request.MaxOutputTokens, maxLocalEvidenceTokens) {
		return invalidLocalIndexValue(ErrInvalidLocalContextRequest, "limits")
	}
	return nil
}

func ValidateLocalEvidenceBundle(bundle LocalEvidenceBundle) error {
	_, _, _, err := localEvidenceBundleUsage(bundle)
	return err
}

func ValidateLocalEvidenceBundleForRequest(request LocalContextRequest, capabilities LocalIndexCapabilities, bundle LocalEvidenceBundle) error {
	if err := ValidateLocalContextRequest(request); err != nil {
		return err
	}
	if err := ValidateLocalIndexCapabilities(capabilities); err != nil {
		return err
	}
	if !capabilities.Available {
		return ErrLocalIndexUnavailable
	}
	if bundle.ProviderID != capabilities.ProviderID || bundle.ProviderVersion != capabilities.ProviderVersion {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "provider metadata")
	}
	if len(bundle.Evidence) > request.MaxItems || len(bundle.Evidence) > capabilities.MaxItems {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence count")
	}
	_, tokens, _, err := localEvidenceBundleUsage(bundle)
	if err != nil {
		return err
	}
	if tokens > request.MaxOutputTokens || tokens > capabilities.MaxOutputTokens {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence tokens")
	}
	return nil
}

func ValidateLocalExpandedEvidence(evidence LocalExpandedEvidence) error {
	if !boundedNonEmpty(evidence.ID, maxLocalEvidenceIDBytes) || !boundedLocalLocator(evidence.Locator) || !boundedNonEmpty(evidence.Title, maxLocalEvidenceTitleBytes) || len(evidence.Excerpt) > maxLocalEvidenceExcerptBytes || evidence.EstimatedTokens < 0 || evidence.EstimatedTokens > maxLocalEvidenceTokens {
		return invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence")
	}
	return nil
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

func boundedNonEmpty(value string, maximum int) bool {
	return len(value) <= maximum && strings.TrimSpace(value) != ""
}

func boundedPositive(value, maximum int) bool {
	return value > 0 && value <= maximum
}

func boundedLocalLocator(value string) bool {
	if !boundedNonEmpty(value, maxLocalEvidenceLocatorBytes) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") || hasWindowsAbsolutePathPrefix(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func hasWindowsAbsolutePathPrefix(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

var _ LocalIndexProvider = DisabledLocalIndexProvider{}
