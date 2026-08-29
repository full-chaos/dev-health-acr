package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS4418RepositoryMetricScalarsSurviveTheModelFacingProjection is
// codex R4 finding 1 (P1). The CHAOS-4418 series rewrite moved every
// repository metric value into the Rows-shaped daily_metrics table and left
// only day_count as a scalar. genkitruntime.modelFacingFacts (runtime.go)
// drops every Rows-shaped field before the fact set reaches synthesis, so
// the model could no longer see -- and therefore could no longer ground a
// claim in -- change_failure_rate, commits_count and their siblings, all of
// which WERE model-visible scalars before this PR.
//
// The assertion runs through genkitruntime.BuildSynthesisPrompt, not
// against the CanonicalFact directly: that exported function is the same
// synthesisInputFromDomain/modelFacingFacts path the real Synthesize call
// uses (exchange_support.go's own doc comment), so this observes the bytes
// the model actually receives rather than a re-implementation of the
// projection.
func TestCHAOS4418RepositoryMetricScalarsSurviveTheModelFacingProjection(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	prompt, err := genkitruntime.BuildSynthesisPrompt(contextfabric.SynthesisInput{
		Facts: contextfabric.CanonicalFactBundle{Facts: result.Facts},
	}, 1<<20)
	if err != nil {
		t.Fatalf("BuildSynthesisPrompt() error = %v", err)
	}
	var payload struct {
		CanonicalFacts []contextfabric.CanonicalFact `json:"canonical_facts"`
	}
	if err := json.Unmarshal([]byte(prompt), &payload); err != nil {
		t.Fatalf("decode synthesis prompt: %v", err)
	}
	if len(payload.CanonicalFacts) != 1 {
		t.Fatalf("canonical_facts = %#v, want exactly 1", payload.CanonicalFacts)
	}
	fields := payload.CanonicalFacts[0].Fields
	// Every scalar the pre-CHAOS-4418 reader emitted, by its ORIGINAL
	// name, so a grounding written against the old shape still resolves.
	// The value is the latest day's -- the single day that reader read.
	for name, want := range map[string]float64{
		"commits_count": 42, "prs_merged": 7, "median_pr_cycle_hours": 12.5,
		"change_failure_rate": 0.1, "mttr_hours": 3.5, "bus_factor": 4,
		"code_ownership_gini": 0.2,
	} {
		value, ok := fields[name]
		if !ok {
			t.Fatalf("model-facing fields = %#v, want the scalar %q the model can ground a claim in -- modelFacingFacts strips daily_metrics, so a Rows-only value is invisible to synthesis", fields, name)
		}
		switch {
		case value.Integer != nil:
			if float64(*value.Integer) != want {
				t.Fatalf("model-facing %q = %d, want %v", name, *value.Integer, want)
			}
		case value.Number != nil:
			if *value.Number != want {
				t.Fatalf("model-facing %q = %v, want %v", name, *value.Number, want)
			}
		default:
			t.Fatalf("model-facing %q = %#v, want a numeric scalar", name, value)
		}
	}
	if fields["day"].String == nil || *fields["day"].String != "2026-02-21" {
		t.Fatalf("model-facing day = %#v, want the latest day the scalars speak for", fields["day"])
	}
	if _, ok := fields["daily_metrics"]; ok {
		t.Fatalf("model-facing fields = %#v, want daily_metrics still stripped -- the scalars are siblings of the Rows table, never a replacement for modelFacingFacts' own contract", fields)
	}
}

// TestCHAOS4418RepositoryMetricScalarsSpeakForTheLatestDay pins WHICH day
// the scalar siblings above report, which the single-row case cannot: the
// query's own `ORDER BY repo_id, day DESC` puts the freshest day first, and
// the scalars must be that day's -- the same "latest day per repository"
// semantic the pre-CHAOS-4418 reader (readers.ReadRepositoryMetrics'
// row_number() PARTITION BY repo_id) had. Reporting the oldest day's values
// under the old field names would keep every grounding resolving while
// silently changing what it means.
func TestCHAOS4418RepositoryMetricScalarsSpeakForTheLatestDay(t *testing.T) {
	t.Parallel()
	newest := metricsRow("repo-1")
	newest[1] = "2026-02-22"
	newest[2] = int64(7)
	oldest := metricsRow("repo-1")
	// fakeClient replays rows verbatim, in the order given -- newest
	// first, mirroring the real statement's own day DESC ordering.
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{newest, oldest}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fields := result.Facts[0].Fields
	if fields["day"].String == nil || *fields["day"].String != "2026-02-22" {
		t.Fatalf("day = %#v, want the NEWEST day 2026-02-22", fields["day"])
	}
	if fields["commits_count"].Integer == nil || *fields["commits_count"].Integer != 7 {
		t.Fatalf("commits_count = %#v, want the newest day's own 7, not the older day's 42", fields["commits_count"])
	}
}

// TestCHAOS4418RepositoryMetricUnrecordedMTTRHasNoScalar keeps the scalar
// siblings honest about missing data (AGENTS.md North Star check 12,
// missing is not zero): an unrecorded mttr_hours is absent from the scalar
// set exactly as it is absent from its daily_metrics row, never a
// fabricated 0.
func TestCHAOS4418RepositoryMetricUnrecordedMTTRHasNoScalar(t *testing.T) {
	t.Parallel()
	row := metricsRow("repo-1")
	row[6] = uint8(0)
	row[7] = float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if _, ok := result.Facts[0].Fields["mttr_hours"]; ok {
		t.Fatalf("fields = %#v, want mttr_hours omitted, never a fabricated 0", result.Facts[0].Fields)
	}
}
