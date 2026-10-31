// Package kinds plants one function per site kind the gate must find.
package kinds

import (
	"fmt"

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
