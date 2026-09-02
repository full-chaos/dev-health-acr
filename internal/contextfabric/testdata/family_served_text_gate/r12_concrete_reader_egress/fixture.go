// Package r12concrete pins the "no text-type test at egress" rule.
//
// R12a (io.Copy) does NOT pin it: io.Copy's second parameter is io.Reader,
// so the value at the boundary is interface-typed, and the text predicate
// accepts every interface. R12a would still be caught with the text test
// in place. A mutation that restored the test turned NO fixture green,
// which is how that was found -- the rule was justified by reasoning that
// happened to be wrong about its own example.
//
// Here the payload arrives at the boundary as a CONCRETE *strings.Reader,
// which is not text by any structural test, so only "anything derived at
// a boundary is a defect, whatever its Go type" catches it.
package r12concrete

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// stream is a local helper whose first parameter is writer-shaped and
// whose payload parameter is a concrete, non-text type.
func stream(w io.Writer, r *strings.Reader) {
	_, _ = io.Copy(w, r)
}

func Handler(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	prose := fmt.Sprintf("The selected family is %s, so ask about one subject.", family)
	stream(w, strings.NewReader(prose))
}
