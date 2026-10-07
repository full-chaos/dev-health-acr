package hostedmetrics_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
)

// The "Metric mapping" table of docs/observability.md is the pin: every
// documented metric has a producer and every producer is documented.
func TestDocumentedMetricsAreExactlyTheProducedOnes(t *testing.T) {
	raw, err := os.ReadFile("../../docs/observability.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	start := strings.Index(doc, "## Metric mapping")
	if start < 0 {
		t.Fatal("docs/observability.md has no Metric mapping section")
	}
	section := doc[start+len("## Metric mapping"):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	row := regexp.MustCompile("(?m)^\\| `(acr_[a-z_]+)` \\|")
	var documented []string
	for _, match := range row.FindAllStringSubmatch(section, -1) {
		documented = append(documented, match[1])
	}
	produced := hostedmetrics.Names()
	for _, name := range documented {
		if !slices.Contains(produced, name) {
			t.Errorf("documented metric %s has no producer", name)
		}
	}
	for _, name := range produced {
		if !slices.Contains(documented, name) {
			t.Errorf("produced metric %s is not documented", name)
		}
	}
	if len(documented) == 0 {
		t.Fatal("parsed no documented metrics")
	}
}
