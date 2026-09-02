// Package r12tmpl probes template rendering as a byte-egress path: the
// writer is the FIRST PARAMETER of a method on something that is not
// itself a writer.
package r12tmpl

import (
	"fmt"
	"html/template"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var page = template.Must(template.New("p").Parse("{{.}}"))

// Handler renders family-derived prose through a template. `page` is not
// a writer; `w` is the template method's first parameter.
func Handler(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	prose := fmt.Sprintf("The selected family is %s, so ask about one subject.", family)
	_ = page.Execute(w, prose)
}
