// Package r12marshal probes the ENCODER family from the other side: a
// custom marshaller whose bytes reach the wire without our code ever
// calling it. encoding/json calls it, so its RESULTS are the boundary.
package r12marshal

import (
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Answer is a served type with a hand-written marshaller.
type Answer struct {
	Family contractsv1.ContextFabricQuestionFamily
}

// MarshalJSON derives prose from the family and returns it as the
// serialized form. Nothing in this package calls it.
func (a Answer) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("{\"judgment\":\"a %s question needs one subject\"}", a.Family)), nil
}
