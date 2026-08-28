package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// instrumentedProvider decorates one contextfabric.FactProvider so every
// ReadFacts call carries instr on the context it hands to the wrapped
// provider -- specifically, so any
// github.com/full-chaos/dev-health-go/readers.QueryOrgScoped call the
// wrapped provider's readers.ReadXxx helpers make underneath (see e.g.
// metrics.go's readRepositoryMetrics) reports through instr instead of
// readers' default readers.NoopInstrumentation. QueryOrgScoped reads its
// Instrumentation off the SAME ctx its caller passed in (readers/query.go),
// so wiring it in here -- at the ReadFacts entry point, per call -- is the
// one seam that reaches every reader this package calls, without touching
// each domain file's ReadFacts body individually.
type instrumentedProvider struct {
	inner contextfabric.FactProvider
	instr readers.Instrumentation
}

func (p instrumentedProvider) Capability() contextfabric.FactCapability {
	return p.inner.Capability()
}

func (p instrumentedProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	return p.inner.ReadFacts(readers.ContextWithInstrumentation(ctx, p.instr), principal, query)
}

// NewInstrumentedProviders wraps NewProviders' result so every fact read
// this package serves reports through instr's
// github.com/full-chaos/dev-health-go/readers.Instrumentation hook
// (CHAOS-4377). instr may be nil -- e.g. a caller with no logger available
// yet -- in which case this returns NewProviders' own providers unchanged;
// a ctx that never had an Instrumentation wired in behaves exactly like
// readers.NoopInstrumentation, so this is never a required call.
//
// WHY the production caller (internal/runtime/hosted/open.go) always wires
// this with readers.NewSlogInstrumentation, never
// readers.NewOTelInstrumentation: acr imports go.opentelemetry.io/otel for
// exactly one reason -- internal/contextfabric/modelprovider/provider.go's
// suppressGenkitTelemetryExport unconditionally overwrites the GLOBAL
// otel.SetTracerProvider/otel.SetMeterProvider with no-op/discard providers
// whenever the model provider initializes, purely to stop Genkit exporting
// its own telemetry. There is no real, live OTel exporter anywhere in acr's
// deployment, so pointing readers.NewOTelInstrumentation at that suppressed
// global would silently discard every reader-telemetry event it produced --
// the exact failure mode a "wire it in and forget it" instrumentation hook
// must not have. acr's actual telemetry idiom (AGENTS.md: "Structured
// logging uses log/slog") is log/slog, which is why acr is dev-health-go's
// first readers.SlogInstrumentation consumer (see that repo's README
// "Boundary corrections" section for the matching reasoning on the library
// side).
func NewInstrumentedProviders(client contextpacket.ClickHouseQueryClient, instr readers.Instrumentation) []contextfabric.FactProvider {
	providers := NewProviders(client)
	if instr == nil {
		return providers
	}
	wrapped := make([]contextfabric.FactProvider, len(providers))
	for i, provider := range providers {
		wrapped[i] = instrumentedProvider{inner: provider, instr: instr}
	}
	return wrapped
}
