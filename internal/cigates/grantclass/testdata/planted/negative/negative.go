// Package negative uses a class in every way that needs no decision row.
package negative

import "github.com/full-chaos/dev-health-acr/internal/cigates/grantclass/testdata/planted/classify"

// Passed hands a class to a class-typed parameter and returns it as a class.
func Passed(c classify.Class) classify.Class {
	return identity(c)
}

func identity(c classify.Class) classify.Class { return c }

// Kept returns a class as a class: no conversion, no transport.
func Kept(c classify.Class) (classify.Class, error) {
	return c, nil
}
