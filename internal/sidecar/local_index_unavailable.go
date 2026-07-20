package sidecar

import (
	"context"
	"errors"
)

type unavailableLocalIndexProvider struct{ cause error }

func newUnavailableLocalIndexProvider(cause error) LocalIndexProvider {
	return unavailableLocalIndexProvider{cause: cause}
}

func (p unavailableLocalIndexProvider) Capabilities(ctx context.Context) (LocalIndexCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return LocalIndexCapabilities{}, err
	}
	return LocalIndexCapabilities{Status: LocalIndexStatusUnavailable, Freshness: LocalIndexFreshnessUnknown}, localIndexFailure(errors.Join(p.cause, ErrLocalIndexUnavailable))
}

func (p unavailableLocalIndexProvider) ContextForTask(ctx context.Context, request LocalContextRequest) (LocalEvidenceBundle, error) {
	if err := ctx.Err(); err != nil {
		return LocalEvidenceBundle{}, err
	}
	if err := ValidateLocalContextRequest(request); err != nil {
		return LocalEvidenceBundle{}, err
	}
	return LocalEvidenceBundle{}, localIndexFailure(errors.Join(p.cause, ErrLocalIndexUnavailable))
}

func (unavailableLocalIndexProvider) ResolveEvidence(ctx context.Context, _ string) (LocalExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return LocalExpandedEvidence{}, err
	}
	return LocalExpandedEvidence{}, ErrLocalEvidenceNotFound
}
