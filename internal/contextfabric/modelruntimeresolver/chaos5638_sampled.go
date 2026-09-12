package modelruntimeresolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5638: the per-sample interpret call, resolved per organization like
// every other model call this package routes.
//
// WHY AN ASSERTION HERE AND NOT A WIDER PORT. Resolver caches a
// contextfabric.ModelRuntime per organization, and that interface has no
// per-sample method. Widening it is the type-safe route and costs more than
// it buys: two non-test implementations, one of which (genkitruntime.Runtime)
// already has the method, against roughly two dozen test files whose own
// ModelRuntime doubles would each grow a method they never call. A checked
// assertion is narrower and, with the error and the log line below, loud in
// the one way that matters.
//
// THE ASSERTION IS CHECKED, NAMED AND LOGGED, which is the whole difference
// from the failure this package's siblings warn about. CommitAffirmationTelemetry
// was an optional interface whose assertion failed SILENTLY: nothing in
// production implemented it, every call fell through, the event disappeared,
// and the tests stayed green because nothing asserted the absence. A miss here
// returns a typed error the caller must handle and emits a Warn line naming the
// organization and the concrete type that failed the assertion -- so the
// operator learns which runtime is not sampled, for which org, the first time
// it happens, rather than inferring it from a family that never says
// model_consensus.
var _ contextfabric.SampledModelRuntime = (*Resolver)(nil)

// ErrRuntimeNotSampled reports that the runtime resolved for this
// organization cannot produce per-sample interpretations.
//
// SEPARATE FROM ErrModelUnavailable on purpose: the runtime is present and
// perfectly able to answer an ordinary interpret call. What it cannot do is
// the ensemble, and an operator reading "model unavailable" would go looking
// for a credential or an outage instead of for a composition that wired a
// runtime the ensemble cannot use.
var ErrRuntimeNotSampled = errors.New("modelruntimeresolver: resolved model runtime does not support per-sample interpretation")

// InterpretQuestionForSample resolves this organization's runtime and asks it
// for one indexed interpret sample.
//
// Resolution is the SAME runtimeFor every other method here calls -- the
// cache, the eviction fence and the generation guard all apply unchanged, so
// an ensemble cannot end up reading a different runtime from the one the
// ordinary interpret path would have used for the same request.
func (r *Resolver) InterpretQuestionForSample(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, sample int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	runtime, err := r.runtimeFor(ctx, principal.OrgID)
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}
	if runtime == nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
	}
	sampled, ok := runtime.(contextfabric.SampledModelRuntime)
	if !ok {
		r.logNotSampled(ctx, principal.OrgID, runtime)
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{},
			fmt.Errorf("%w (organization %q)", ErrRuntimeNotSampled, principal.OrgID)
	}
	return sampled.InterpretQuestionForSample(ctx, principal, request, sample)
}

// logNotSampled emits the one line that makes a failed assertion visible.
//
// WARN, not Info: this is a composition that cannot do what it was configured
// to do, and it will keep failing every turn until someone changes the wiring.
// The concrete type is named because it is the only thing that identifies
// WHICH runtime needs the method -- a message saying only "not sampled" leaves
// an operator with the whole build to search.
//
// The type is rendered with %T, never the value: a runtime holds credentials
// and clients, and formatting the value could put either in a log line.
//
// BOTH VALUES GO THROUGH SanitizeLogAttr AT THIS SITE, which is the rule the
// package's whole-tree instrument enforces and which caught the first version
// of this function. The org id is caller-supplied and the type name is
// derived from a build the composition chose, so neither is a constant this
// file controls -- and the instrument deliberately does not care whether a
// particular value looks safe, because "looks safe" is the judgement that
// stops being true the first time a source changes upstream.
func (r *Resolver) logNotSampled(ctx context.Context, orgID string, runtime contextfabric.ModelRuntime) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, "context fabric model runtime does not support per-sample interpretation",
		"org_id", contextfabric.SanitizeLogAttr(orgID),
		"runtime_type", contextfabric.SanitizeLogAttr(fmt.Sprintf("%T", runtime)),
	)
}
