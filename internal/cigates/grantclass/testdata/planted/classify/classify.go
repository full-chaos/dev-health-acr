// Package classify declares the planted grant class: the gate recognizes a
// type named Class under the planted fixtures as a class type, and Classify
// and RawGrant as a classifier and a raw-grant classifier.
package classify

// Class is the planted class type.
type Class string

const (
	A Class = "a"
	B Class = "b"
	C Class = "c"
)

// Classify is the planted classifier.
func Classify(scopes []string) Class {
	switch {
	case len(scopes) == 0:
		return A
	case len(scopes) == 1:
		return B
	}
	return C
}

// RawGrant is the planted raw-grant classifier.
func RawGrant(scopes []string) bool { return len(scopes) == 0 }
