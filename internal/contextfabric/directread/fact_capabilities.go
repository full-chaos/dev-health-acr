package directread

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// Capabilities returns the declarations of the registry this reader reads
// (CHAOS-7073). It is empty when the source does not list them; the direct
// fact tool then serves no kind.
func (r *FactReader) Capabilities() []contextfabric.FactCapability {
	if r == nil || r.source == nil {
		return nil
	}
	source, ok := r.source.(CapabilitySource)
	if !ok {
		return nil
	}
	return source.Capabilities()
}
