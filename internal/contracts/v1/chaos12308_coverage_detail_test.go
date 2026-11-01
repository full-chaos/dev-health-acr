package v1

import (
	"strings"
	"testing"
)

// CHAOS-12308: one non-degrading code per lower link tier, each with a required
// count and its own label. read_population_unverified keeps its rule.
func TestTheLowerTierCodesRequireACountAndNeverDegrade(t *testing.T) {
	t.Parallel()
	for code, word := range map[ContextFabricCoverageDetailCode]string{
		ContextFabricCoverageDetailGraphAnswerPullRequestsOnExplicitTextLinks: "a text reference",
		ContextFabricCoverageDetailGraphAnswerPullRequestsOnHeuristicLinks:    "an inferred link",
	} {
		count := 2
		detail := ContextFabricCoverageDetail{DetailID: "cov-01", Source: "context-fabric:graph", Code: code, Count: &count}
		detail.Label = ComposeCoverageDetailLabel(detail)
		if !strings.Contains(detail.Label, "2 pull requests") || !strings.Contains(detail.Label, word) {
			t.Errorf("%s: label = %q, want the count and %q", code, detail.Label, word)
		}
		if err := detail.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", code, err)
		}
		one := 1
		singular := detail
		singular.Count = &one
		if label := ComposeCoverageDetailLabel(singular); !strings.HasPrefix(label, "1 pull request ") {
			t.Errorf("%s: label = %q, want the singular", code, label)
		}
		if ContextFabricCoverageDetailCodeMayDegrade(code) {
			t.Errorf("%s may degrade; read_population_unverified degrades such an answer", code)
		}
		degrading := detail
		degrading.Degrading, degrading.Raw = true, "raw"
		if err := degrading.Validate(); err == nil {
			t.Errorf("%s: a degrading detail validated", code)
		}
		uncounted := detail
		uncounted.Count = nil
		if err := uncounted.Validate(); err == nil {
			t.Errorf("%s: a detail with no count validated", code)
		}
	}
}

// The existing disclosure stays as it was: no count on it.
func TestReadPopulationUnverifiedStillRefusesACount(t *testing.T) {
	t.Parallel()
	count := 2
	detail := ContextFabricCoverageDetail{DetailID: "cov-01", Source: "context-fabric:graph", Code: ContextFabricCoverageDetailReadPopulationUnverified, Count: &count}
	detail.Label = ComposeCoverageDetailLabel(detail)
	if err := detail.Validate(); err == nil {
		t.Error("read_population_unverified accepted a count")
	}
}
