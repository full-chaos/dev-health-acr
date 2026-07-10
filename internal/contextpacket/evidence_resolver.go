package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const maxEvidenceExcerptRunes = 1_000

var ErrUnsupportedEvidenceSource = errors.New("contextpacket: unsupported evidence source")

type EvidenceExpansionInput struct {
	Evidence contractsv1.EvidenceRef
	Excerpt  string
}

type EvidenceExpansionObservation struct {
	System       string
	Availability contractsv1.EvidenceAvailability
	Duration     time.Duration
}

type EvidenceExpansionObserver interface {
	ObserveEvidenceExpansion(context.Context, EvidenceExpansionObservation)
}

type EvidenceResolverOptions struct {
	Now      func() time.Time
	Observer EvidenceExpansionObserver
}

// EvidenceResolver expands persisted, already-authorized evidence. It never
// dereferences the source URI or calls a provider.
type EvidenceResolver struct {
	now      func() time.Time
	observer EvidenceExpansionObserver
}

func NewEvidenceResolver(options EvidenceResolverOptions) *EvidenceResolver {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &EvidenceResolver{now: options.Now, observer: options.Observer}
}

func (r *EvidenceResolver) Expand(ctx context.Context, input EvidenceExpansionInput) (contractsv1.ExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	if err := validateEvidence(input.Evidence); err != nil {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("validate evidence expansion input: %w", err)
	}
	if input.Evidence.Availability == contractsv1.EvidenceUnauthorized {
		return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
	}
	adapter, ok := evidenceAdapterFor(input.Evidence)
	if !ok {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("source %q/%q: %w", input.Evidence.Source.System, input.Evidence.SourceVersion, ErrUnsupportedEvidenceSource)
	}
	resolvedAt := r.now().UTC()
	evidence := input.Evidence
	evidence.Source.SafeURI = adapter.safeURI(evidence.Source.SafeURI)
	evidence.Source.DisplayLabel = cleanEvidenceText(evidence.Source.DisplayLabel, 1_000)
	evidence.Citation = cleanEvidenceText(evidence.Citation, 2_000)

	expanded := contractsv1.ExpandedEvidence{
		SchemaVersion: contractsv1.ExpandedEvidenceSchema,
		Evidence:      evidence,
		ResolvedAt:    resolvedAt,
		Availability:  evidence.Availability,
		Structured:    map[string]any{},
	}
	if evidence.Availability == contractsv1.EvidenceAvailable || evidence.Availability == contractsv1.EvidenceStale {
		expanded.Excerpt = cleanEvidenceText(input.Excerpt, maxEvidenceExcerptRunes)
		expanded.Structured = adapter.structured(evidence)
	}
	if evidence.Availability == contractsv1.EvidenceRedacted {
		expanded.RedactionReason = "source_redacted"
	}
	if err := validateExpandedEvidence(expanded); err != nil {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("validate evidence expansion output: %w", err)
	}
	duration := r.now().UTC().Sub(resolvedAt)
	duration = max(duration, 0)
	r.observe(ctx, EvidenceExpansionObservation{
		System:       evidence.Source.System,
		Availability: evidence.Availability,
		Duration:     duration,
	})
	return expanded, nil
}

func (r *EvidenceResolver) observe(ctx context.Context, observation EvidenceExpansionObservation) {
	if r.observer != nil {
		r.observer.ObserveEvidenceExpansion(ctx, observation)
	}
}

func cleanEvidenceText(value string, maximum int) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return -1
		}
		return r
	}, value)
	runes := []rune(strings.TrimSpace(cleaned))
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}

func validOpaqueEvidenceRefID(value string) bool {
	runes := []rune(value)
	if len(runes) < 8 || len(runes) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) == -1
}
