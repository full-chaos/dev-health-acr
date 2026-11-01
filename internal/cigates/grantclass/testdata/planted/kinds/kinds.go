// Package kinds plants one function per site kind the gate must find.
package kinds

import (
	"fmt"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/cigates/grantclass/testdata/planted/classify"
)

// Compare: one comparison against a class constant.
func Compare(c classify.Class) {
	if c == classify.A {
		fmt.Println("a")
	}
}

// SwitchPartial: a switch that does not name every class.
func SwitchPartial(c classify.Class) int {
	switch c {
	case classify.A:
		return 1
	case classify.B:
		return 2
	}
	return 0
}

// SwitchDefault: a switch that names every class and also has a default.
func SwitchDefault(c classify.Class) int {
	switch c {
	case classify.A, classify.B, classify.C:
		return 1
	default:
		return 0
	}
}

// Options carries a derived bool out of the deciding function.
type Options struct{ Restricted bool }

// DerivedField: a comparison stored in a struct field (a derived bool).
func DerivedField(c classify.Class) Options {
	return Options{Restricted: c != classify.A}
}

// DerivedLocal: a comparison kept in a local variable that then leaves the
// function in a struct field (the derived bool of embedded_subject_gate.go).
func DerivedLocal(c classify.Class) Options {
	restricted := c == classify.B
	return Options{Restricted: restricted}
}

// Lookup: a decision by a table keyed by the class.
func Lookup(c classify.Class, limits map[classify.Class]int) int {
	return limits[c]
}

// MethodValue: the classifier used as a value, not called.
func MethodValue() func([]string) classify.Class {
	return classify.Classify
}

// TransportString: an explicit conversion to string.
func TransportString(c classify.Class) string {
	return string(c)
}

// TransportArgument: a class passed to an interface-typed parameter.
func TransportArgument(c classify.Class) {
	fmt.Println(c)
}

// TransportMap: a class stored as an interface value.
func TransportMap(c classify.Class) map[string]any {
	return map[string]any{"class": c}
}

// Classified: one classifier call, kept local.
func Classified(scopes []string) int {
	class := classify.Classify(scopes)
	return len(class)
}

// RawGranted: one raw-grant classifier call, kept local.
func RawGranted(scopes []string) int {
	open := classify.RawGrant(scopes)
	if open {
		return 1
	}
	return 0
}

// TwoDecisions: a second decision inside one function counts twice.
func TwoDecisions(c classify.Class) int {
	if c == classify.A {
		return 1
	}
	if c == classify.B {
		return 2
	}
	return 3
}

// CollectionSlice: a slice literal of classes.
func CollectionSlice() []classify.Class {
	return []classify.Class{classify.A}
}

// CollectionMapKey: a map keyed by a class.
func CollectionMapKey() map[classify.Class]int {
	return map[classify.Class]int{classify.A: 1}
}

// CollectionMapValue: a map whose values are classes.
func CollectionMapValue() map[string]classify.Class {
	return map[string]classify.Class{"a": classify.A}
}

// CollectionArgument: a membership check over a class collection.
func CollectionArgument(c classify.Class, known []classify.Class) bool {
	return slices.Contains(known, c)
}

// TransportReturn: a class returned as an interface.
func TransportReturn(c classify.Class) any {
	return c
}

// Wrapped is a named type of its own over the class: converting to it
// carries the class past every class-typed check.
type Wrapped classify.Class

// TransportNamed: a conversion to another named type.
func TransportNamed(c classify.Class) Wrapped {
	return Wrapped(c)
}

// TransportSend: a class sent on a channel of interface values.
func TransportSend(c classify.Class, out chan any) {
	out <- c
}

// Envelope carries an interface field.
type Envelope struct{ Value any }

// TransportField: a class stored in an interface-typed struct field.
func TransportField(c classify.Class) Envelope {
	return Envelope{Value: c}
}

// ClosureDecision: a decision inside a function literal is keyed to the
// enclosing declaration.
func ClosureDecision(c classify.Class) int {
	count := 0
	check := func() {
		if c == classify.C {
			count++
		}
	}
	check()
	return count
}

// SwitchExhaustive names every class and has no default: still a decision,
// recorded as a switch row (a class added with its case still needs one).
func SwitchExhaustive(c classify.Class) int {
	switch c {
	case classify.A:
		return 1
	case classify.B:
		return 2
	case classify.C:
		return 3
	}
	return 0
}

// TransportVar: a class stored in an interface variable by its declaration.
func TransportVar(c classify.Class) any {
	var exposed any = c
	return exposed
}

// TransportMapKey: a class used as an interface-typed map key.
func TransportMapKey(c classify.Class) map[any]int {
	return map[any]int{c: 1}
}

// ExternalConstant is a typed constant of the planted class declared outside
// its own package: a new class value (constant kind).
const ExternalConstant classify.Class = "external"

// TransportIndexKey converts the class to an interface map key by indexing.
func TransportIndexKey(c classify.Class) int {
	keyed := map[any]int{}
	return keyed[c]
}
