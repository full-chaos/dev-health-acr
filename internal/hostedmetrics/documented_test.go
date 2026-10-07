package hostedmetrics_test

import (
	"context"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
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
	produced := producedInstruments(t)
	for _, name := range documented {
		if _, ok := produced[name]; !ok {
			t.Errorf("documented metric %s has no producer", name)
		}
	}
	for name := range produced {
		if !slices.Contains(documented, name) {
			t.Errorf("produced metric %s is not documented", name)
		}
	}
	if !slices.Equal(slices.Sorted(maps.Keys(produced)), slices.Sorted(slices.Values(hostedmetrics.Names()))) {
		t.Errorf("Names() %v differs from the instruments New creates %v", hostedmetrics.Names(), slices.Sorted(maps.Keys(produced)))
	}
	labelCell := regexp.MustCompile("(?m)^\\| `(acr_[a-z_]+)` \\| [a-z]+ \\| ([^|]*) \\|")
	parenthetical := regexp.MustCompile(`\([^)]*\)`)
	identifier := regexp.MustCompile("`([a-z_]+)`")
	for _, match := range labelCell.FindAllStringSubmatch(section, -1) {
		var labels []string
		for _, id := range identifier.FindAllStringSubmatch(parenthetical.ReplaceAllString(match[2], ""), -1) {
			labels = append(labels, id[1])
		}
		slices.Sort(labels)
		if got := produced[match[1]]; !slices.Equal(labels, got) {
			t.Errorf("%s documents labels %v, the instrument records %v", match[1], labels, got)
		}
	}
	if len(documented) == 0 {
		t.Fatal("parsed no documented metrics")
	}
}

// producedInstruments records every instrument once and returns each created
// instrument's name with the label keys it recorded.
func producedInstruments(t *testing.T) map[string][]string {
	t.Helper()
	ctx := context.Background()
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	instruments.ToolCall(ctx, "t", "r", 200, time.Second)
	instruments.Answer(ctx, "complete", "t")
	instruments.BudgetRefusal(ctx)
	instruments.AnswerReuse(ctx, "hit")
	instruments.RequirementOutcome(ctx, "served", 1)
	instruments.FactReadAbort(ctx, "other")
	instruments.InvestigationLatency(ctx, "complete", time.Second)
	produced := map[string][]string{}
	for cell := range read() {
		name, rest, _ := strings.Cut(cell, "{")
		var keys []string
		for _, pair := range strings.Split(strings.TrimSuffix(rest, "}"), ",") {
			if key, _, ok := strings.Cut(pair, "="); ok {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		produced[name] = keys
	}
	return produced
}
