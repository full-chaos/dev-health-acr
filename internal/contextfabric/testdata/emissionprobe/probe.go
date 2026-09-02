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

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

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

// ---------------------------------------------------------------------------
// One fixture per call disposition. The set is DERIVED FROM THE VOCABULARY,
// not from the shapes anyone happened to think of: a disposition with no
// fixture that lands in it fails by name. Each non-leak fixture emits a
// uniquely-named SALTED field that the walk must recover, so a disposition
// that classifies correctly and still drops the write fails too.
// ---------------------------------------------------------------------------

const (
	// ProbeClosure exercises `accounted`: a named local closure.
	ProbeClosure contextfabric.FactKind = "zz_probe_closure"
	// ProbeConversion exercises `not_a_call`: a type conversion, which
	// parses as a CallExpr and is not a call.
	ProbeConversion contextfabric.FactKind = "zz_probe_conversion"
	// ProbeCrossPackage exercises `excluded_by_design`: a call into another
	// package, which the walk cannot see into and says so.
	ProbeCrossPackage contextfabric.FactKind = "zz_probe_cross_package"
	// ProbeInterface exercises the `interface_method` leak: which body runs
	// is not knowable from the source.
	ProbeInterface contextfabric.FactKind = "zz_probe_interface"
)

type ClosureProvider struct{}

func (ClosureProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeClosure)
}

func (ClosureProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	// A NAMED LOCAL CLOSURE. Its body is a child of this one, so its writes
	// are already recorded and the call needs no edge -- but the call site
	// must still be classified `accounted` rather than counted as a leak.
	emit := func(target map[string]contextfabric.FactValue) {
		target["closure_field"] = contextfabric.FactValue{}
	}
	emit(fields)
	return fields
}

type ConversionProvider struct{}

func (ConversionProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeConversion)
}

func (ConversionProvider) build(raw int) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	// Three conversions. None is a call, and none may be counted as a leak.
	_ = int64(raw)
	_ = float64(raw)
	_ = string(rune(raw))
	fields["conversion_field"] = contextfabric.FactValue{}
	return fields
}

type CrossPackageProvider struct{}

func (CrossPackageProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeCrossPackage)
}

func (CrossPackageProvider) build(name string) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	// A call into another package: excluded by design, not a leak. The walk
	// cannot see into it, and the header says so rather than counting every
	// stdlib call and drowning the signal.
	trimmed := strings.TrimSpace(name)
	fields["cross_package_field"] = contextfabric.FactValue{String: &trimmed}
	return fields
}

// fieldEmitter is the interface whose dispatch the walk cannot follow.
type fieldEmitter interface {
	emit(map[string]contextfabric.FactValue)
}

type concreteEmitter struct{}

func (concreteEmitter) emit(fields map[string]contextfabric.FactValue) {
	// This field is emitted at runtime and is INVISIBLE to any static walk
	// through the interface. The requirement is not that the walk find it --
	// it is that the walk NAME it as lost.
	fields["interface_field"] = contextfabric.FactValue{}
}

type InterfaceProvider struct{ emitter fieldEmitter }

func (InterfaceProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeInterface)
}

func (p InterfaceProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	p.emitter.emit(fields)
	return fields
}

// Keep the concrete implementation referenced so it is not dead code; the
// walk must still refuse to follow the interface call above.
var _ fieldEmitter = concreteEmitter{}

// ProbeIndexedCall exercises the `unresolvable` leak cause: a call whose
// callee expression has no identifier at all.
//
// It exists because a mutation SURVIVED without it. Making that branch
// return an out-of-vocabulary disposition changed nothing, because no call
// in the real provider package reaches it -- so the totality assertion,
// which quantifies over the population it is given, had no population for
// this case. An assertion is only as strong as the inputs that reach it.
const ProbeIndexedCall contextfabric.FactKind = "zz_probe_indexed_call"

type IndexedCallProvider struct{}

func (IndexedCallProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeIndexedCall)
}

func (IndexedCallProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	handlers := []func(map[string]contextfabric.FactValue){
		func(target map[string]contextfabric.FactValue) {
			target["indexed_field"] = contextfabric.FactValue{}
		},
	}
	// The callee is an index expression: no identifier, so which function
	// runs is not knowable from the source.
	handlers[0](fields)
	return fields
}

// ProbeCleared exercises the `clear` half of key destruction. A review round
// constructed it after the `delete` half was fixed: enumerating one member
// of a pair is how the previous walk failed five times.
const ProbeCleared contextfabric.FactKind = "zz_probe_cleared"

type ClearedProvider struct{}

func (ClearedProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeCleared)
}

func (ClearedProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{"wiped_field": contextfabric.FactValue{}}
	// Everything recorded above is destroyed. The walk cannot know the map
	// is empty at return, so it must at least SAY the recorded set is an
	// overstatement.
	clear(fields)
	return fields
}

// ProbeRebind exercises the `rebound_func_value` leak: a local holding a
// function value, assigned twice. A review round constructed it and the walk
// reported the provider as emitting NOTHING with zero leaks -- it followed
// the LAST binding regardless of where the call sat.
const ProbeRebind contextfabric.FactKind = "zz_probe_rebind"

func writeEarly(fields map[string]contextfabric.FactValue) {
	fields["early_field"] = contextfabric.FactValue{}
}

func writeNothing(map[string]contextfabric.FactValue) {}

type RebindProvider struct{}

func (RebindProvider) Capability() contextfabric.FactCapability {
	return newCapability(ProbeRebind)
}

func (RebindProvider) build() map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	emit := writeEarly
	emit(fields) // reaches writeEarly, which the last binding does not say
	emit = writeNothing
	_ = emit
	return fields
}
