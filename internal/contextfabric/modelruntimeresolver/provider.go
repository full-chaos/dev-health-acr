package modelruntimeresolver

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// NewModelProviderBuild returns a Build that constructs a per-organization
// runtime through modelprovider.New -- the only place in this repository
// that builds a production genkit.Genkit instance (TRD §19.3.6). An
// organization's Provider/BaseURL/Model/FallbackModel/Credential come from
// its own stored configuration; Timeout/MaxAttempts/MaxTransportRetries are
// inherited from defaults (the deployment-default surface's tuning) since
// §19.3.2 does not put those knobs in the per-organization contract.
// AllowInsecureBaseURL is always false here: an organization-supplied base
// URL always leaves ACR's trust boundary, and
// ContextFabricOrgModelConfigWriteRequest.Validate() already rejected
// anything other than https before this configuration could ever be
// stored.
func NewModelProviderBuild(defaults modelprovider.Config) Build {
	return func(ctx context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return modelprovider.New(ctx, orgModelProviderConfig(defaults, resolved))
	}
}

// orgModelProviderConfig builds the per-organization modelprovider.Config
// NewModelProviderBuild's closure passes to modelprovider.New. Split out as
// its own pure function (rather than a struct literal inline in the
// closure) so the field mapping -- which knobs come from the organization's
// stored configuration versus which are inherited from defaults -- is
// directly unit-testable without constructing a real genkit.Genkit
// instance.
func orgModelProviderConfig(defaults modelprovider.Config, resolved contextfabric.ResolvedOrgModelConfig) modelprovider.Config {
	return modelprovider.Config{
		Provider:             resolved.Provider,
		BaseURL:              resolved.BaseURL,
		Model:                resolved.Model,
		FallbackModel:        resolved.FallbackModel,
		APIKey:               resolved.Credential,
		Timeout:              defaults.Timeout,
		MaxAttempts:          defaults.MaxAttempts,
		MaxTransportRetries:  defaults.MaxTransportRetries,
		AllowInsecureBaseURL: false,
		// CHAOS-4355 follow-up: inherited from defaults, same as the
		// tuning knobs above, so a per-organization BYO runtime reports
		// RecordModelRowsStripped through the SAME sink the
		// deployment-default runtime uses -- never a second,
		// per-organization-scoped telemetry instance.
		Telemetry: defaults.Telemetry,
	}
}
