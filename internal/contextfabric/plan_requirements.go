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
// ONE DERIVATION, TWO ARRAYS. The plan requirements and the seed outcome rows
// are projected from the SAME []DerivedRequirement, in one call, so the two
// published arrays cannot describe different turns. Deriving twice would put
// two authorities behind one join.

// planRequirements projects the derivation's rows onto the wire type.
//
// Returns nil for an empty input rather than an empty slice: nil encodes as
// an absent key under omitempty, an empty slice as `[]`, and "the derivation
// produced nothing" is the same statement as "there is no array here". The
// byte-minimality probe over the irreducible answer reads the difference.
func planRequirements(rows []DerivedRequirement) []contractsv1.ContextFabricPlanRequirement {
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
// Preserving nil is not tidiness: an empty non-nil slice encodes as `[]` and a
// nil one is omitted entirely, and a read row and a computed row are told
// apart on the wire by which of fact_kinds and input_fact_kinds is PRESENT.
// Turning nil into empty would put both keys on every row and erase that
// distinction.
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
