// Package emissionprobe is a POSITIVE FIXTURE for the emission reachability
// walk. It is not part of the runtime: nothing imports it outside the test
// that walks it.
//
// WHY IT IS A REAL PACKAGE AND NOT A .txt FIXTURE. The walk type-checks its
// subject through go/packages, so a fixture it can read has to compile. A
// fixture that cannot be loaded the way the real subject is loaded would be
// testing a different instrument.
//
// EVERY SHAPE HERE IS ONE THE WALK GOT WRONG. Each was found by an
// adversarial review round, constructed here so the behaviour is pinned in
// BOTH directions: the shapes the walk now follows must stay followed, and
// the shapes it still cannot follow must stay COUNTED rather than silently
// reported as "emits nothing".
package emissionprobe

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// The fixture mimics the provider shape the walk keys on: a receiver type
// whose Capability method names a fact kind through newCapability.
func newCapability(kind contextfabric.FactKind) contextfabric.FactCapability {
	return contextfabric.FactCapability{Kind: kind}
}

const (
	// ProbeDirect is the ordinary case: a direct call to a package-local
	// helper, which the walk has always followed.
	ProbeDirect contextfabric.FactKind = "zz_probe_direct"
	// ProbeFuncValue is the case the walk MISSED: the helper is called
	// through a function VALUE, so the callee resolves to a variable
	// rather than to a function and no call-graph edge was queued.
	ProbeFuncValue contextfabric.FactKind = "zz_probe_func_value"
	// ProbeIndirect is the case the walk still CANNOT follow: the function
	// value comes from a parameter, so which function runs is not known
	// statically. It must be COUNTED, never reported as emitting nothing.
	ProbeIndirect contextfabric.FactKind = "zz_probe_indirect"
	// ProbeDeleted is the case the walk MIS-REPORTED: a field written and
	// then deleted was reported as emitted, and the delete counter that was
	// supposed to notice could never fire.
	ProbeDeleted contextfabric.FactKind = "zz_probe_deleted"
)

type DirectProvider struct{}

func (DirectProvider) Capability() contextfabric.FactCapability { return newCapability(ProbeDirect) }

func (DirectProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	writeDirect(fields)
	return fields
}

func writeDirect(fields map[string]contextfabric.FactValue) {
	fields["direct_field"] = contextfabric.FactValue{}
}

type FuncValueProvider struct{}

func (FuncValueProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeFuncValue)
}

func (FuncValueProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	emit := writeViaFuncValue
	emit(fields)
	return fields
}

func writeViaFuncValue(fields map[string]contextfabric.FactValue) {
	fields["func_value_field"] = contextfabric.FactValue{}
}

type IndirectProvider struct{}

func (IndirectProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeIndirect)
}

func (IndirectProvider) build(emit func(map[string]contextfabric.FactValue)) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	// WHICH function this is cannot be known from the source. The walk must
	// COUNT this site, not pass over it silently.
	emit(fields)
	return fields
}

type DeletedProvider struct{}

func (DeletedProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeDeleted)
}

func (DeletedProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{"removed_later": contextfabric.FactValue{}}
	delete(fields, "removed_later")
	return fields
}
