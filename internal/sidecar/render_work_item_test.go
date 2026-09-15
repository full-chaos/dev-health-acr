package sidecar

import (
	"fmt"
	"strings"
	"testing"

	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func workItemCountProjection(value int64) v1.ContextFabricAnswerProjection {
	return v1.ContextFabricAnswerProjection{KeyFacts: []v1.ContextFabricProjectedFact{{ClaimID: "opaque", Kind: v1.ContextFabricFactCardinality, Field: "work_item_count", Value: v1.ContextFabricScalarValue{Integer: &value}}}}
}

func TestWorkItemCountQualificationAtomic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    int64
		kind     v1.ContextFabricSubjectKind
		declared int
		floor    bool
	}{
		{"floor", 2000, v1.ContextFabricSubjectWorkItem, 2000, true},
		{"mismatch", 2000, v1.ContextFabricSubjectWorkItem, 1999, false},
		{"other_kind", 2000, v1.ContextFabricSubjectTeam, 2000, false},
		{"other_code", 2000, v1.ContextFabricSubjectWorkItem, 2000, false},
		{"missing_declared", 2000, v1.ContextFabricSubjectWorkItem, 2000, false},
		{"metadata_unavailable", 2000, "", 0, false},
		{"exact_recorded", 12, "", 0, false},
		{"zero_recorded", 0, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := workItemCountProjection(tc.value)
			p.CoveragePartial = true
			p.CurrentState = "At least 2000 items according to old prose"
			p.Limitations = []string{"Unmeasured historical census"}
			if tc.kind != "" {
				p.CoverageDetails = []v1.ContextFabricCoverageDetail{{Code: v1.ContextFabricCoverageDetailKindCensusTruncated, Kind: tc.kind, Declared: &tc.declared}}
			}
			if tc.name == "other_code" {
				p.CoverageDetails[0].Code = v1.ContextFabricCoverageDetailCode("source_unavailable")
			}
			if tc.name == "missing_declared" {
				p.CoverageDetails[0].Declared = nil
			}
			want := fmt.Sprintf("- Recorded work-item count: %d. This view does not establish whether the population count is exact.", tc.value)
			if tc.floor {
				want = fmt.Sprintf("- Work-item count: at least %d.", tc.value)
			}
			rendered, truncated := RenderAnswerProjectionMarkdown(p, 24000)
			if truncated || !strings.Contains(rendered, want) {
				t.Errorf("qualified count missing: %s", rendered)
			}
			// Every boundary around this actual count unit must retain all or none,
			// even when the finish step appends its own truncation notice.
			for budget := 1; budget <= len(rendered)+len(truncationNotice)+1; budget++ {
				got, cut := RenderAnswerProjectionMarkdown(p, budget)
				if len(got) > budget {
					t.Fatalf("budget=%d length=%d", budget, len(got))
				}
				for _, line := range strings.Split(got, "\n") {
					if strings.Contains(line, "work-item count:") || strings.Contains(line, "Work-item count:") {
						if line != want {
							t.Fatalf("split count at budget%d: %q", budget, line)
						}
					}
				}
				if !strings.Contains(got, want) && !cut {
					t.Fatalf("unreported count omission at %d", budget)
				}
			}
		})
	}
	p := workItemCountProjection(0)
	p.KeyFacts = nil
	got, _ := RenderAnswerProjectionMarkdown(p, 24000)
	if strings.Contains(got, "count:") {
		t.Error("unmeasured gained a count")
	}
	p = workItemCountProjection(7)
	p.KeyFacts[0].Field = "team_count"
	got, _ = RenderAnswerProjectionMarkdown(p, 24000)
	if !strings.Contains(got, " = ") {
		t.Error("changed other cardinality rendering")
	}
}

func TestWorkItemCountQualificationDomain(t *testing.T) {
	for _, kind := range []string{"fact_kind", "field", "integer"} {
		t.Run(kind, func(t *testing.T) {
			p := workItemCountProjection(2000)
			n := 2000
			p.CoverageDetails = []v1.ContextFabricCoverageDetail{{Code: v1.ContextFabricCoverageDetailKindCensusTruncated, Kind: v1.ContextFabricSubjectWorkItem, Declared: &n}}
			switch kind {
			case "fact_kind":
				p.KeyFacts[0].Kind = v1.ContextFabricFactStatus
			case "field":
				p.KeyFacts[0].Field = "team_count"
			case "integer":
				p.KeyFacts[0].Value.Integer = nil
				text := "2000"
				p.KeyFacts[0].Value.String = &text
			}
			got, _ := RenderAnswerProjectionMarkdown(p, 24000)
			if strings.Contains(got, "Work-item count:") || strings.Contains(got, "Recorded work-item count:") {
				t.Error("non-count became a qualified count")
			}
		})
	}
}

func TestWorkItemMarkdownMemberStatusJoin(t *testing.T) {
	p := v1.ContextFabricAnswerProjection{Cohort: &v1.ContextFabricProjectedCohort{Kind: v1.ContextFabricSubjectWorkItem, Total: 3}}
	for i := 0; i < 3; i++ {
		subject := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectWorkItem, CanonicalID: fmt.Sprint(i), Label: fmt.Sprintf("title-%d", i)}
		p.Cohort.Members = append(p.Cohort.Members, v1.ContextFabricProjectedCohortMember{Subject: subject, Rank: i + 1})
		if i != 1 {
			status := "waiting"
			if i == 2 {
				status = "unknown"
			}
			p.KeyFacts = append(p.KeyFacts, v1.ContextFabricProjectedFact{ClaimID: fmt.Sprint(i), Kind: v1.ContextFabricFactStatus, Subject: subject, Field: "status", Value: v1.ContextFabricScalarValue{String: &status}})
		}
		label := subject.Label
		p.KeyFacts = append(p.KeyFacts, v1.ContextFabricProjectedFact{ClaimID: "title" + fmt.Sprint(i), Kind: v1.ContextFabricFactWork, Subject: subject, Field: "title", Value: v1.ContextFabricScalarValue{String: &label}})
	}
	collision := p.KeyFacts[0]
	collision.ClaimID = "collision"
	collision.Subject.Kind = v1.ContextFabricSubjectTeam
	wrong := "wrong_kind"
	collision.Value.String = &wrong
	p.KeyFacts = append(p.KeyFacts, collision)
	got, cut := RenderAnswerProjectionMarkdown(p, 24000)
	if cut {
		t.Fatal("unexpected render cut")
	}
	for i, want := range []string{"waiting", "No status evidence in this answer", "unknown"} {
		var row string
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "- Work item:") && strings.Contains(line, fmt.Sprintf("title-%d", i)) {
				row = line
			}
		}
		if !strings.Contains(row, want) || strings.Contains(row, "wrong") || !strings.Contains(row, untrustedDataHeader) {
			t.Errorf("member%d wrong row: %s", i, row)
		}
		if strings.Count(got, fmt.Sprintf("title-%d", i)) != 1 && i != 0 {
			t.Errorf("duplicate title%d", i)
		}
	}
	if strings.Contains(got, "1. work_item") || strings.Contains(got, "## Rows") {
		t.Error("unranked member displayed as ranking")
	}
}

func TestWorkItemMarkdownOnlyDeduplicatesIdenticalWorkTitle(t *testing.T) {
	for _, mode := range []string{"identical", "other_kind", "other_field", "integer", "different_title"} {
		t.Run(mode, func(t *testing.T) {
			subject := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectWorkItem, CanonicalID: "member", Label: "source title"}
			value := subject.Label
			fact := v1.ContextFabricProjectedFact{ClaimID: "claim", Kind: v1.ContextFabricFactWork, Subject: subject, Field: "title", Value: v1.ContextFabricScalarValue{String: &value}}
			switch mode {
			case "other_kind":
				fact.Kind = v1.ContextFabricFactIdentity
			case "other_field":
				fact.Field = "description"
			case "integer":
				n := int64(7)
				fact.Value = v1.ContextFabricScalarValue{Integer: &n}
			case "different_title":
				value = "another observed title"
			}
			p := v1.ContextFabricAnswerProjection{Cohort: &v1.ContextFabricProjectedCohort{Kind: v1.ContextFabricSubjectWorkItem, Total: 1, Members: []v1.ContextFabricProjectedCohortMember{{Subject: subject, Rank: 1}}}, KeyFacts: []v1.ContextFabricProjectedFact{fact}}
			got, cut := RenderAnswerProjectionMarkdown(p, 24000)
			if cut || strings.Contains(got, " = ") != (mode != "identical") {
				t.Errorf("deduplication suppressed independent content: %s", got)
			}
		})
	}
}

func TestWorkItemMarkdownPlainDisplayDomain(t *testing.T) {
	for _, mode := range []string{"ranked", "other_kind"} {
		t.Run(mode, func(t *testing.T) {
			kind := v1.ContextFabricSubjectWorkItem
			if mode == "other_kind" {
				kind = v1.ContextFabricSubjectTeam
			}
			p := v1.ContextFabricAnswerProjection{Cohort: &v1.ContextFabricProjectedCohort{Kind: kind, Members: []v1.ContextFabricProjectedCohortMember{{Subject: v1.ContextFabricSubjectRef{Kind: kind, CanonicalID: "member", Label: "source title"}, Rank: 1, RankingComputed: mode == "ranked"}}}}
			got, cut := RenderAnswerProjectionMarkdown(p, 24000)
			if cut || strings.Contains(got, "### Work item / Status") || strings.Contains(got, "- Work item:") {
				t.Errorf("plain display escaped its domain: %s", got)
			}
		})
	}
}
