package contextfabric

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

var relevanceWindowStart = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
var relevanceWindowEnd = time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

func relevanceRanking() factRanking {
	return factRanking{windowStart: &relevanceWindowStart, windowEnd: &relevanceWindowEnd}
}

func relevanceFact(kind FactKind, id string, observed time.Time, event *time.Time) CanonicalFact {
	fact := boundFixtureFacts(kind, SubjectPullRequest, 1)[0]
	fact.Subject.CanonicalID = id
	fact.ObservedAt = &observed
	fact.EventAt = event
	return fact
}

func atDay(day int) *time.Time {
	value := time.Date(2026, 8, day, 0, 0, 0, 0, time.UTC)
	return &value
}

func keptIDs(facts []CanonicalFact) []string {
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.Subject.CanonicalID)
	}
	return ids
}

func TestBoundSynthesisFactsKeepsTheFactsAQuestionNamesOverTheFirstRead(t *testing.T) {
	facts := boundFixtureFacts(FactWork, SubjectWorkItem, 10)
	ranking := factRanking{requirements: []FactRequirement{{Kind: FactWork, Subjects: []SubjectRef{facts[8].Subject}}}}

	bounded, reduced, selection := boundSynthesisFacts(facts, ranking, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})

	if !reduced || selection != SynthesisInputSelectionRelevance {
		t.Fatalf("reduced = %v selection = %q, want a relevance cut", reduced, selection)
	}
	if got, want := keptIDs(bounded), []string{"work_000", "work_001", "work_002", "work_008"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept = %v, want %v: the named work_008 kept, the rest first read, all in read order", got, want)
	}
	_, _, position := boundSynthesisFacts(facts, factRanking{}, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})
	if position != SynthesisInputSelectionPosition {
		t.Fatalf("selection without ranking inputs = %q, want position", position)
	}
}

func TestBoundSynthesisFactsOrdersInsideAKindByTheFourRules(t *testing.T) {
	observed := func(day int) time.Time { return *atDay(day) }
	outside := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	committed := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pr_committed"}
	named := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pr_named"}
	facts := []CanonicalFact{
		relevanceFact(FactPullRequests, "pr_old_inside", observed(1), atDay(1)),
		relevanceFact(FactPullRequests, "pr_new_inside", observed(20), atDay(2)),
		relevanceFact(FactPullRequests, "pr_outside", observed(30), &outside),
		relevanceFact(FactPullRequests, "pr_named", observed(3), &outside),
		relevanceFact(FactPullRequests, "pr_committed", observed(2), &outside),
		relevanceFact(FactPullRequests, "pr_straddles", observed(25), func() *time.Time { v := relevanceWindowEnd.Add(24 * time.Hour); return &v }()),
	}
	ranking := relevanceRanking()
	ranking.committed = []SubjectRef{committed}
	ranking.requirements = []FactRequirement{{Kind: FactPullRequests, Subjects: []SubjectRef{named}}}
	ranking.windowStart, ranking.windowEnd = &relevanceWindowStart, &relevanceWindowEnd

	scores := make([]factRelevance, len(facts))
	order := make([]int, len(facts))
	for index, fact := range facts {
		scores[index], order[index] = ranking.score(fact), index
	}
	got := []string{}
	for len(order) > 0 {
		best := 0
		for i := range order {
			if scores[order[i]].before(scores[order[best]]) {
				best = i
			}
		}
		got = append(got, facts[order[best]].Subject.CanonicalID)
		order = append(order[:best], order[best+1:]...)
	}
	// pr_straddles and pr_outside both have an event outside the window; the
	// more recently observed pr_outside goes first.
	want := []string{"pr_committed", "pr_named", "pr_new_inside", "pr_old_inside", "pr_outside", "pr_straddles"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestBoundSynthesisFactsRanksAPeriodFullyInsideAbovePartlyInside(t *testing.T) {
	period := func(id, start, end string) CanonicalFact {
		fact := relevanceFact(FactMetrics, id, *atDay(1), nil)
		fact.Fields["window_start"], fact.Fields["window_end"] = StringFactValue(start), StringFactValue(end)
		return fact
	}
	ranking := relevanceRanking()
	inside := ranking.score(period("inside", "2026-08-01", "2026-08-31"))
	partly := ranking.score(period("partly", "2026-06-01", "2026-07-15"))
	outside := ranking.score(period("outside", "2026-01-01", "2026-02-01"))
	unknown := ranking.score(relevanceFact(FactMetrics, "unknown", *atDay(1), nil))
	if !(inside.overlap > partly.overlap && partly.overlap > unknown.overlap && unknown.overlap > outside.overlap) {
		t.Fatalf("overlap inside=%d partly=%d unknown=%d outside=%d, want inside > partly > unknown > outside", inside.overlap, partly.overlap, unknown.overlap, outside.overlap)
	}
}

func TestBoundSynthesisFactsKeepsTheReadOrderWhenNothingSeparatesTheFacts(t *testing.T) {
	facts := boundFixtureFacts(FactWork, SubjectWorkItem, 10)
	_, _, selection := boundSynthesisFacts(facts, relevanceRanking(), &ModelInputOverflow{Bytes: 200, MaxBytes: 100})
	if selection != SynthesisInputSelectionPosition {
		t.Fatalf("selection = %q, want position when no fact differs in rank", selection)
	}
	bounded, _, _ := boundSynthesisFacts(facts, factRanking{}, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})
	want := fmt.Sprint(keptIDs(facts[:len(bounded)]))
	if got := fmt.Sprint(keptIDs(bounded)); got != want {
		t.Fatalf("kept = %s, want the first %d in read order %s", got, len(bounded), want)
	}
}

func TestBoundSynthesisFactsLeavesAnInputThatNeedsNoCutAlone(t *testing.T) {
	facts := boundFixtureFacts(FactWork, SubjectWorkItem, 4)
	ranking := factRanking{requirements: []FactRequirement{{Kind: FactWork, Subjects: []SubjectRef{facts[3].Subject}}}}
	bounded, reduced, _ := boundSynthesisFacts(facts, ranking, &ModelInputOverflow{Bytes: 100, MaxBytes: 1000})
	if reduced || !reflect.DeepEqual(bounded, facts) {
		t.Fatalf("reduced = %v kept = %v, want every fact in read order", reduced, keptIDs(bounded))
	}
}
