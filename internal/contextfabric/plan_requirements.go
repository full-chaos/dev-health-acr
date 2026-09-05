package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The plan-requirement projection: the derivation's own rows, put on the
// wire.
//
// DeriveRequirements produces a full account of what this turn planned --
// every obligation/role/subject coordinate, which fact kinds could serve it,
// which server step computes it, what that step consumes, whether anything
// executes it, and why it is unservable when it is. Until now that account
// lived only in telemetry and in the parity artifact: the served document
// carried the outcome of each requirement and nothing about the requirement
// itself, so a reader could see that something was narrowed but not what it
// was narrowed from.
//
// This file is the projection onto the published type, and it is a straight
// field-for-field copy on purpose. Every closed vocabulary it carries is
// MIRRORED on the wire rather than shared, because contextfabric imports
// contracts/v1 and the dependency cannot run the other way; the mirrors are
// held equal in both directions by the parity test in this package.
//
// TWO PROJECTIONS OF ONE PURE DERIVATION, and the distinction matters enough
// to state precisely rather than to claim more than is true.
//
// The plan's requirement rows and the seed outcome rows are built at two
// different points -- the rows are stamped onto the plan where the plan is
// created, and the outcome set is seeded during finalization -- so
// DeriveRequirements is called twice per turn, not once. They cannot disagree
// because the derivation is a pure, deterministic function of the frame and
// the registry's declarations, evaluated on the same frame both times.
//
// An earlier revision of this file derived once and projected twice, which
// reads better and was wrong: finalizeResult takes the plan BY VALUE, and the
// engine re-stamps the plan from its own variable after the budget fit, so
// rows written onto finalization's copy were discarded and the served document
// carried a full outcome set beside an empty requirement array. Purity is what
// makes two evaluation points safe; the join test on the served document is
// what checks the two agree in fact rather than in argument.

// PlanRequirementsFromDerived projects the derivation's rows onto the wire type.
//
// Exported because it is one of the two projections of the derivation -- the
// other being SeedRequirementOutcomes -- and a caller outside the engine
// that needs to build a document carrying these rows must build them THROUGH
// this function rather than by hand. The store validates on the way in, and a
// hand-built row whose closed-vocabulary field holds a Go zero value is
// invalid in a way a reader cannot see: that is not hypothetical, it is how
// every case in the shared store parity table failed once, in CI's container
// job only.
//
// Returns nil for an empty input rather than an empty slice. NOT because the
// two differ on the wire -- MEASURED, they do not: under `omitempty`
// encoding/json omits an empty slice exactly as it omits a nil one, so both
// produce an absent key and no consumer can tell them apart. The reason is
// ROUND-TRIP EQUALITY: an absent key DECODES to nil, so a projection that
// emitted an empty slice would not equal itself after a store round trip, and
// the equality check that guards the persisted document would fail on a
// difference that never reached the document.
func PlanRequirementsFromDerived(rows []DerivedRequirement) []contractsv1.ContextFabricPlanRequirement {
	if len(rows) == 0 {
		return nil
	}
	out := make([]contractsv1.ContextFabricPlanRequirement, 0, len(rows))
	for _, row := range rows {
		out = append(out, planRequirement(row))
	}
	return out
}

// planRequirement projects ONE derived row.
//
// The slices are COPIED rather than aliased. The derivation hands out slices
// backed by its own arrays, and a document that shared them would let a later
// mutation of either side reach through into the other -- the same reason
// InputsForComputedStep returns a copy.
func planRequirement(row DerivedRequirement) contractsv1.ContextFabricPlanRequirement {
	return contractsv1.ContextFabricPlanRequirement{
		Requirement:    requirementIdentity(row),
		Obligation:     string(row.Obligation),
		Role:           string(row.Role),
		Subject:        row.Subject,
		Kind:           string(row.Kind),
		FactKinds:      copyFactKinds(row.FactKinds),
		Step:           string(row.Step),
		StepExecution:  string(row.StepExecution),
		InputClass:     string(row.InputClass),
		InputFactKinds: copyFactKinds(row.InputFactKinds),
		Scope:          string(row.Scope),
		Quantifier:     string(row.Quantifier),
		Unavailable:    string(row.Unavailable),
	}
}

// copyFactKinds returns an independent copy, preserving nil.
//
// An earlier revision of this comment claimed nil and empty differ on the
// wire -- that an empty slice encodes as `[]` while a nil one is omitted. That
// is FALSE for an omitempty slice, and the mutation battery is what exposed
// it: deleting the nil branch below left the test asserting key-absence green,
// because `omitempty` omits BOTH. Measured directly, `{A: nil}` and
// `{A: []string{}}` marshal to identical bytes.
//
// The branch stays because it IS load-bearing, for a different reason than the
// one first written down. An absent key decodes to nil, so returning empty
// here would make a projected row unequal to itself across a store round trip
// -- and that equality is what TestPlanRequirementRowsSurviveAStoreRoundTrip
// checks. What actually tells a read row from a computed one on the wire is
// `kind` and `input_class`, both of which are always present, never the
// presence or absence of a key.
func copyFactKinds(kinds []FactKind) []FactKind {
	if kinds == nil {
		return nil
	}
	out := make([]FactKind, len(kinds))
	copy(out, kinds)
	return out
}

// deriveTurnRequirements derives this turn's requirement rows ONCE.
//
// It is the single entry point both published arrays are built from. nil
// frame or nil deriver yields no rows, which is an honest absence and one the
// document shows: no plan requirements, no seed outcomes, and a completeness
// state of `not_derived` rather than a vacuous `complete`.
func deriveTurnRequirements(frame *QuestionFrame, deriver RequirementDeriver) []DerivedRequirement {
	if frame == nil || deriver == nil {
		return nil
	}
	return deriver.DeriveRequirements(*frame)
}
