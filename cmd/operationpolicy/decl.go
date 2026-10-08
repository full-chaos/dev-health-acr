package main

import (
	"encoding/json"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// policyDeclaration is the hand-authored half of the artifact. Every
// registry row appears once: in Served or in NotServed.
type policyDeclaration struct {
	Served    map[string]operationDecl
	NotServed map[string]notServedDecl
}

type notServedDecl struct {
	Reason string
}

// operationDecl declares one served operation. Variables is the variable
// ALLOWLIST (paths as the SDL walk names them); every other path is
// refused, with the code from RefusedPaths or variable_not_allowed.
type operationDecl struct {
	DocumentName      string
	Cost              directread.CostClass
	DeadlineSeconds   int
	MaxInFlightPerOrg int
	Variables         map[string]variableDecl
	RefusedPaths      map[string]directread.Refusal
	Constraints       []directread.Constraint
	Unrestricted      directread.CallerScope
	Restricted        directread.CallerScope
	// PrimaryList names the list a cut page cuts when the operation has more
	// than one top-level list (for example "hotspots.rows").
	PrimaryList       string
	AdditionalOutputs []string
	OutputExceptions  map[string]string
	WithheldOutputs   map[string]string
	Disclosure        []directread.DisclosureField
	Notes             []string
}

type variableDecl struct {
	Source        directread.VariableSource
	ForcedValue   json.RawMessage
	AllowedValues []string
	RefusedValues []directread.ValueRefusal
	Min, Max      *int64
	MaxItems      int
	MaxLength     int
	Subject       *directread.SubjectConversion
}

func i64(v int64) *int64 { return &v }
