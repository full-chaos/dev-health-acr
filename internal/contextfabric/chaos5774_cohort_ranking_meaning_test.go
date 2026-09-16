package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5774: these tests cover two independent mechanisms that keep a
// cohort ranking's ATTENTION/adverse-pressure measure from being served as
// a performance judgment: the deterministic score-meaning/judgment-mismatch
// data RankCohort and the engine mint (never model-authored), and the
// structural guard that refuses a model-authored driver's ranking
// superlative about a member the formula never scored.

// --- scoreMeaningSupportsJudgmentKind ---

func TestScoreMeaningSupportsJudgmentKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		meaning CohortScoreMeaning
		kind    RequestedJudgmentKind
		want    bool
	}{
		{"attention meaning, attention kind", CohortScoreMeaningAttention, RequestedJudgmentKindAttention, true},
		{"attention meaning, performance kind", CohortScoreMeaningAttention, RequestedJudgmentKindPerformance, false},
		{"attention meaning, unspecified kind", CohortScoreMeaningAttention, "", true},
		{"unrecognized meaning, attention kind", CohortScoreMeaning("future_meaning"), RequestedJudgmentKindAttention, false},
		{"unrecognized meaning, unspecified kind", CohortScoreMeaning("future_meaning"), "", true},
		{"attention meaning, unrecognized kind", CohortScoreMeaningAttention, RequestedJudgmentKind("future_kind"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := scoreMeaningSupportsJudgmentKind(c.meaning, c.kind); got != c.want {
				t.Fatalf("scoreMeaningSupportsJudgmentKind(%q, %q) = %v, want %v", c.meaning, c.kind, got, c.want)
			}
		})
	}
}

// --- applyCohortJudgmentMismatch ---

func TestApplyCohortJudgmentMismatch(t *testing.T) {
	t.Parallel()
	t.Run("nil cohort is a no-op", func(t *testing.T) {
		t.Parallel()
		applyCohortJudgmentMismatch(nil, RequestedJudgmentKindPerformance)
	})
	t.Run("unranked cohort never gets a mismatch", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture"}
		applyCohortJudgmentMismatch(cohort, RequestedJudgmentKindPerformance)
		if cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = true, want false on an unranked cohort (ScoreMeaning empty)")
		}
	})
	t.Run("performance kind over an attention cohort mismatches", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", ScoreMeaning: CohortScoreMeaningAttention}
		applyCohortJudgmentMismatch(cohort, RequestedJudgmentKindPerformance)
		if !cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = false, want true for a performance kind over an attention cohort")
		}
	})
	t.Run("attention kind never mismatches", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", ScoreMeaning: CohortScoreMeaningAttention}
		applyCohortJudgmentMismatch(cohort, RequestedJudgmentKindAttention)
		if cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = true, want false: the requested kind already matches the cohort's own ScoreMeaning")
		}
	})
	t.Run("unspecified kind never mismatches", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", ScoreMeaning: CohortScoreMeaningAttention}
		applyCohortJudgmentMismatch(cohort, "")
		if cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = true, want false: an unspecified kind is never confident enough to flag a mismatch")
		}
	})
}

// --- RankCohort mints ScoreMeaning ---

func TestRankCohortMintsScoreMeaningAttention(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", Members: []CohortMember{rankTestMember("A")}}
	facts := []CanonicalFact{healthFact("A", "low")}
	ranked, event, _ := RankCohort(cohort, facts, Coverage{})
	if ranked.ScoreMeaning != CohortScoreMeaningAttention {
		t.Fatalf("cohort.ScoreMeaning = %q, want %q", ranked.ScoreMeaning, CohortScoreMeaningAttention)
	}
	if event.ScoreMeaning != CohortScoreMeaningAttention {
		t.Fatalf("event.ScoreMeaning = %q, want %q", event.ScoreMeaning, CohortScoreMeaningAttention)
	}
}

func TestRankCohortEmptyCohortNeverMintsScoreMeaning(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture"}
	ranked, event, _ := RankCohort(cohort, nil, Coverage{})
	if ranked.ScoreMeaning != "" {
		t.Fatalf("cohort.ScoreMeaning = %q, want empty for a cohort with no members", ranked.ScoreMeaning)
	}
	if event.ScoreMeaning != "" {
		t.Fatalf("event.ScoreMeaning = %q, want empty", event.ScoreMeaning)
	}
}

// --- requireNoSuperlativeClaimOverUnrankableMember / ValidateAgainst ---

// unrankableMemberFixture builds a SynthesisInput/valid draft pair whose
// cohort has two members: one QUALIFIED (a real Score), one
// INSUFFICIENT_EVIDENCE (no Score at all -- the formula never scored it).
func unrankableMemberFixture() (SynthesisInput, SynthesisDraft, SubjectRef, SubjectRef) {
	input, draft := closureFixture()
	qualified := input.Graph.Resolution.Committed[0]
	unrankable := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_unrankable", Label: "Unrankable"}
	score := 60.0
	input.Graph.Cohort = &Cohort{
		Kind: SubjectTeam, Rationale: "fixture", Complete: true,
		ScoreMeaning: CohortScoreMeaningAttention,
		Members: []CohortMember{
			{
				Subject: qualified, Rank: 1, InclusionReasons: []string{"matched"},
				RankingComputed: true, AttentionRank: 1, Score: &score,
				DataCompleteness: CohortDataComplete, Outcome: CohortOutcomeQualified,
			},
			{
				Subject: unrankable, Rank: 2, InclusionReasons: []string{"matched"},
				RankingComputed: true, AttentionRank: 2,
				DataCompleteness: CohortDataDegraded, Outcome: CohortOutcomeInsufficientEvidence,
				MissingSignals: []string{RankingSignalHealthRisk},
			},
		},
	}
	draft.Drivers[0].Category = "narrative"
	draft.Drivers[0].ClaimedFactIDs = nil
	return input, draft, qualified, unrankable
}

// superlativeTermCase names one term from CohortSuperlativeJudgmentTerms and
// a driver title that uses it about the fixture's own "Unrankable"/
// "Qualified" subject. Shared by both domain-coverage tests below so
// assertCoversCohortSuperlativeJudgmentTerms can check each against the
// SAME guard vocabulary the production code reads.
type superlativeTermCase struct {
	name  string
	title string
}

// assertCoversCohortSuperlativeJudgmentTerms fails the test unless cases
// names exactly CohortSuperlativeJudgmentTerms (order-independent) -- so a
// future term added to that one guard/prompt vocabulary cannot ship without
// its own domain cell in both the reject-side and allow-side table here.
func assertCoversCohortSuperlativeJudgmentTerms(t *testing.T, cases []superlativeTermCase) {
	t.Helper()
	got := make(map[string]bool, len(cases))
	for _, c := range cases {
		got[c.name] = true
	}
	want := make(map[string]bool, len(CohortSuperlativeJudgmentTerms))
	for _, term := range CohortSuperlativeJudgmentTerms {
		want[term] = true
	}
	if len(got) != len(want) {
		t.Fatalf("case names = %d, CohortSuperlativeJudgmentTerms = %d -- every guard term needs its own domain cell here", len(got), len(want))
	}
	for term := range want {
		if !got[term] {
			t.Fatalf("no domain cell for guard term %q", term)
		}
	}
}

func TestValidateAgainstRejectsSuperlativeAboutInsufficientEvidenceMember(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"best", "Unrankable is the best team in this cohort"},
		{"worst", "Unrankable is the worst performer"},
		{"top", "Unrankable is the top team in this cohort"},
		{"bottom", "Unrankable is at the bottom of this cohort"},
		{"first", "Unrankable ranks first in this cohort"},
		{"last", "Unrankable ranks last in this cohort"},
		{"leads", "Unrankable leads this cohort in attention pressure"},
		{"trails", "Unrankable trails this cohort in readiness"},
		{"leading", "Unrankable is the leading team in this cohort's attention ranking"},
		{"trailing", "Unrankable is the trailing team in this cohort's readiness ranking"},
	}
	assertCoversCohortSuperlativeJudgmentTerms(t, cases)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection for a superlative claim about an insufficient_evidence member")
			}
			if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
				t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
			}
		})
	}
}

// TestValidateAgainstAllowsSuperlativeAboutAQualifiedMember is the scope
// control: the rule is about a member the formula could NOT score, never
// about disagreeing with a superlative over a member that DOES have a real
// attention position -- covering every guard term, not just one.
func TestValidateAgainstAllowsSuperlativeAboutAQualifiedMember(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"best", "Qualified is the best-supported attention signal in this cohort"},
		{"worst", "Qualified shows the worst attention pressure in this cohort"},
		{"top", "Qualified is the top attention signal in this cohort"},
		{"bottom", "Qualified is at the bottom of this cohort's attention ranking"},
		{"first", "Qualified ranks first in this cohort's attention ranking"},
		{"last", "Qualified ranks last in this cohort's attention ranking"},
		{"leads", "Qualified leads this cohort in attention pressure"},
		{"trails", "Qualified trails this cohort in readiness"},
		{"leading", "Qualified is the leading team in this cohort's attention ranking"},
		{"trailing", "Qualified is the trailing team in this cohort's readiness ranking"},
	}
	assertCoversCohortSuperlativeJudgmentTerms(t, cases)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, qualified, _ := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{qualified}
			draft.Drivers[0].Title = c.title
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("ValidateAgainst() error = %v, want a superlative about a QUALIFIED member to be admitted", err)
			}
		})
	}
}

// TestValidateAgainstRejectsComparativeToGroupOverUnrankableMember and
// TestValidateAgainstAllowsComparativeToGroupOverAQualifiedMember cover the
// two constructions CohortSuperlativeJudgmentTerms cannot express as single
// words: "more <adjective> than any/all/every other" and "#1"/"number one".
func TestValidateAgainstRejectsComparativeToGroupOverUnrankableMember(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"more-than-any-other", "Unrankable is more pressured than any other team in this cohort"},
		{"more-than-all-other", "Unrankable is more critically behind than all other teams here"},
		{"more-than-every-other", "Unrankable is more concerning than every other team in this cohort"},
		{"hash-1", "Unrankable is #1 in this cohort's attention ranking"},
		{"number-one", "Unrankable is number one in this cohort's attention ranking"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection for a comparative-to-the-group claim about an insufficient_evidence member")
			}
			if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
				t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
			}
		})
	}
}

func TestValidateAgainstAllowsComparativeToGroupOverAQualifiedMember(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"more-than-any-other", "Qualified is more pressured than any other team in this cohort"},
		{"more-than-all-other", "Qualified is more critically behind than all other teams here"},
		{"more-than-every-other", "Qualified is more concerning than every other team in this cohort"},
		{"hash-1", "Qualified is #1 in this cohort's attention ranking"},
		{"number-one", "Qualified is number one in this cohort's attention ranking"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, qualified, _ := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{qualified}
			draft.Drivers[0].Title = c.title
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("ValidateAgainst() error = %v, want a comparative-to-the-group claim about a QUALIFIED member to be admitted", err)
			}
		})
	}
}

// TestValidateAgainstAllowsAnOrdinaryMoreThanComparisonOverAnUnrankableMember
// guards against the OVER-strict failure mode symmetric to
// TestContainsWordRequiresWholeWordMatch: an ordinary "more X than Y"
// sentence that is not the "than any/all/every other" construction must
// still be admitted.
func TestValidateAgainstAllowsAnOrdinaryMoreThanComparisonOverAnUnrankableMember(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable has more evidence available now than it did before"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want an ordinary more-than-Y comparison (not any/all/every other) to be admitted", err)
	}
}

// TestValidateAgainstAllowsNonSuperlativeCommentaryAboutUnrankableMember
// proves the guard is not a ban on ever mentioning a low-evidence member --
// an honest disclosure of its outcome must still be admitted.
func TestValidateAgainstAllowsNonSuperlativeCommentaryAboutUnrankableMember(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable has insufficient evidence to compute an attention score"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want an honest outcome disclosure to be admitted", err)
	}
}

// TestContainsWordRequiresWholeWordMatch guards against the OVER-strict
// failure mode: a fixed-term substring match would reject ordinary prose
// that merely CONTAINS one of the closed terms as a substring of another
// word (e.g. "asbestos" contains "best").
func TestContainsWordRequiresWholeWordMatch(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable's asbestos remediation backlog is unrelated to ranking"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a substring-only match (asbestos contains \"best\") to be admitted", err)
	}
}

// --- the general -est/-iest superlative CONSTRUCTION (isCohortSuperlativeEstSuffixWord) ---

// TestValidateAgainstRejectsAnEstSuffixSuperlativeNearACohortNoun covers the
// construction, not a fixed word list: strongest/weakest/highest/lowest
// (the original 4) and four domain adjectives that form their superlative
// the exact same way (riskiest/unhealthiest/neediest/readiest) all reject,
// every one of them a WORD isCohortSuperlativeEstSuffixWord recognizes by
// its own -est/-iest ending, never enumerated.
func TestValidateAgainstRejectsAnEstSuffixSuperlativeNearACohortNoun(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"strongest", "Unrankable is provisionally the strongest team in this cohort"},
		{"weakest", "Unrankable is provisionally the weakest team in this cohort"},
		{"highest", "Unrankable shows the highest attention pressure in this cohort"},
		{"lowest", "Unrankable shows the lowest attention pressure in this cohort"},
		{"riskiest", "Unrankable is provisionally the riskiest team in this cohort"},
		{"unhealthiest", "Unrankable is provisionally the unhealthiest team in this cohort"},
		{"neediest", "Unrankable is provisionally the neediest team in this cohort"},
		{"readiest", "Unrankable is provisionally the readiest team in this cohort"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection for an -est-suffix superlative near a cohort noun")
			}
			if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
				t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
			}
		})
	}
}

// TestValidateAgainstAllowsAnEstSuffixWordWithoutACohortNounNearby is the
// scope control for the SAME construction: an -est/-iest word with no
// cohort noun or rank word in its own sentence is not a ranking claim.
func TestValidateAgainstAllowsAnEstSuffixWordWithoutACohortNounNearby(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable's riskiest finding this cycle was a stale certificate"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want an -est-suffix word with no cohort noun nearby to be admitted", err)
	}
}

// TestValidateAgainstAllowsAnEstSuffixExceptionWordNearACohortNoun covers
// EVERY word in cohortSuperlativeEstSuffixExceptions -- each is an ordinary
// English word that happens to end in "est" and must never be treated as a
// superlative, even sitting right next to a cohort noun.
func TestValidateAgainstAllowsAnEstSuffixExceptionWordNearACohortNoun(t *testing.T) {
	t.Parallel()
	exceptions := []string{
		"test", "rest", "request", "interest", "guest", "west", "chest",
		"forest", "harvest", "invest", "digest", "manifest", "contest",
		"protest", "arrest", "honest", "modest", "earnest", "suggest", "latest",
		"attest", "priest", "tempest", "bequest", "conquest", "inquest", "quest",
	}
	got := make(map[string]bool, len(exceptions))
	for _, word := range exceptions {
		got[word] = true
	}
	if len(got) != len(cohortSuperlativeEstSuffixExceptions) {
		t.Fatalf("exceptions listed here = %d, cohortSuperlativeEstSuffixExceptions = %d -- every guard exception needs its own positive-control cell here", len(got), len(cohortSuperlativeEstSuffixExceptions))
	}
	for word := range cohortSuperlativeEstSuffixExceptions {
		if !got[word] {
			t.Fatalf("no positive-control cell for guard exception %q", word)
		}
	}
	for _, word := range exceptions {
		t.Run(word, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = "Unrankable's team status update mentions " + word + " in this cohort's report"
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("ValidateAgainst() error = %v, want the exception word %q (next to a cohort noun) to be admitted", err, word)
			}
		})
	}
}

// --- positional-term same-sentence scoping: benign phrasing that must never reject ---

// TestValidateAgainstAllowsAPositionalWordFarFromACohortNoun is the domain
// table for every shape a bare positional word appears in ORDINARY English
// prose that is not a ranking claim at all: a time span, a numeral-unit
// span, and the fixed compounds/idioms "top-level", "bottom line", "first
// turn", "at first", "at last", and "last-minute" -- none of these share a
// sentence with a cohort noun or rank word, so none of them reject.
func TestValidateAgainstAllowsAPositionalWordFarFromACohortNoun(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"time-span", "Unrankable's history here covers the last 30 days"},
		{"numeral-unit-span", "Unrankable's dashboard shows the top 5 open items from this week"},
		{"top-level-compound", "Unrankable's summary sits at the top-level of the org chart"},
		{"bottom-line-idiom", "Unrankable's report gives the bottom line up front"},
		{"first-turn-idiom", "Unrankable's issue was the first turn in the rotation"},
		{"at-first-idiom", "At first, Unrankable's numbers looked fine before the correction"},
		{"at-last-idiom", "At last, Unrankable's pipeline stabilized after the fix"},
		{"last-minute-compound", "This was a last-minute finding about a stale credential"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("ValidateAgainst() error = %v, want %q (no cohort noun in the same sentence) to be admitted", err, c.title)
			}
		})
	}
}

// --- most/least: adjacency to a cohort noun, never a bare quantifier ---

// TestValidateAgainstRejectsMostLeastAdjacentToACohortNoun covers the
// adjacency construction cohortMostLeastAdjacencyPattern requires: "most"/
// "least" followed, within at most two filler words, by a cohort noun or
// rank word.
func TestValidateAgainstRejectsMostLeastAdjacentToACohortNoun(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"most-pressured-team", "Unrankable is the most pressured team in this cohort"},
		{"least-ready-team", "Unrankable is the least ready team in this cohort"},
		{"most-concerning-project", "Unrankable is the most concerning project in this cohort"},
		{"least-prepared-repository", "Unrankable is the least prepared repository in this cohort"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection for most/least adjacent to a cohort noun")
			}
			if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
				t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
			}
		})
	}
}

// TestValidateAgainstAllowsMostLeastAdjacentToACohortNoun is the qualified-
// member control: citesUnrankable short-circuits regardless of text.
func TestValidateAgainstAllowsMostLeastAdjacentToACohortNoun(t *testing.T) {
	t.Parallel()
	input, draft, qualified, _ := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{qualified}
	draft.Drivers[0].Title = "Qualified is the most pressured team in this cohort"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want most/least about a QUALIFIED member to be admitted", err)
	}
}

// TestValidateAgainstAllowsABareMostLeastQuantifier proves an ordinary
// quantifier ("most of", "at least N", "least-recently-used") is never a
// ranking claim, even when it shares a sentence with an unrelated cohort
// noun.
func TestValidateAgainstAllowsABareMostLeastQuantifier(t *testing.T) {
	t.Parallel()
	cases := []superlativeTermCase{
		{"most-of-its-incidents", "Unrankable's team resolved most of its incidents late this cycle"},
		{"at-least-n", "Unrankable's queue is at least 3 items behind for this team"},
		{"least-recently-used", "Unrankable's team applied a least-recently-used eviction policy"},
		{"at-most-n", "Unrankable's team reported at most 5 alerts this cycle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("ValidateAgainst() error = %v, want a bare quantifier (not adjacent to a cohort noun) to be admitted", err)
			}
		})
	}
}

// TestValidateAgainstAllowsAnEstSuffixWordNotAdjacentToACohortNoun is the
// scope control for the TIGHTENED -est/-iest rule: a real superlative word,
// present in the SAME sentence as a cohort noun but more than two filler
// words away from it and not preceded by "the", is not adjacent and must
// not reject -- "anywhere in the sentence" is not the rule, adjacency is.
func TestValidateAgainstAllowsAnEstSuffixWordNotAdjacentToACohortNoun(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable was rated riskiest among several unrelated external vendors this quarter, well before any team review"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want an -est word more than two words from the nearest cohort noun to be admitted", err)
	}
}

// --- sentence-boundary tokenizer: a real claim must survive a wrapped line, and an abbreviation must not force a split ---

// TestValidateAgainstRejectsASuperlativeClaimSplitAcrossALineWrap proves a
// single newline (a wrapped line or a markdown bullet) is NOT a sentence
// boundary -- a real claim written across one must still be caught, not
// silently admitted because the word and the cohort noun landed in what an
// over-eager tokenizer would treat as two different "sentences".
func TestValidateAgainstRejectsASuperlativeClaimSplitAcrossALineWrap(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	// "leads" is a POSITIONAL word: it only rejects when a cohort noun
	// shares its sentence. An -est word like "riskiest" would reject here
	// via the unrelated "the <word>" adjacency shape regardless of how the
	// text is split, which would prove nothing about the tokenizer.
	draft.Drivers[0].Title = "Unrankable leads\n- the cohort in attention pressure"
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatalf("ValidateAgainst() = nil, want a rejection for a claim split only by a line wrap, not a real sentence boundary")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
	}
}

// TestValidateAgainstRejectsASuperlativeClaimAcrossAnInlineAbbreviation
// proves "e.g." followed by a capitalized word (the shape that otherwise
// looks exactly like a real sentence boundary: a period, whitespace, an
// uppercase letter) never forces a split that would separate a positional
// claim from the cohort noun it names.
func TestValidateAgainstRejectsASuperlativeClaimAcrossAnInlineAbbreviation(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable leads e.g. Project Foo, well within the same cohort review this cycle"
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatalf("ValidateAgainst() = nil, want a rejection -- \"e.g.\" followed by a capitalized word must not split the claim from the cohort noun it names")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
	}
}

// TestValidateAgainstAllowsTextAroundAKnownAbbreviation proves a known
// abbreviation (vs./etc./Inc., each ending in a period immediately
// followed by whitespace and an uppercase letter -- the usual look of a
// real sentence boundary) does not force a split either, so unrelated text
// on its far side is not pulled into the same sentence as a driver's own
// superlative claim.
func TestValidateAgainstAllowsTextAroundAKnownAbbreviation(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Team Unrankable vs. Team Qualified showed mixed results this cycle"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want ordinary text around a known abbreviation, with no superlative claim at all, to be admitted", err)
	}
}

// TestValidateAgainstRejectsLeadsTheGroup proves "group" (added to
// cohortNounOrRankWords) makes "leads"/"trails" reject the same way "leads
// the cohort"/"leads the team" already did.
func TestValidateAgainstRejectsLeadsTheGroup(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable leads the group in attention pressure"
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatalf("ValidateAgainst() = nil, want a rejection for \"leads the group\" about an unrankable member")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
	}
}

// --- Engine.Investigate: judgment framing across the real pipeline ---

// judgmentFramingEngineFixture builds a 3-member discovered-cohort
// investigation (two PROVISIONAL members via health severity + investment
// mix, in opposite directions, and one INSUFFICIENT_EVIDENCE member with NO
// facts at all -- deficiencySeveritySignal's own available-zero exception
// still counts it as one available family, weight 20, below the 50/2-family
// qualification floor) and runs it with requestedJudgment/kind as the
// interpreter's own RequestedJudgment/RequestedJudgmentKind, through the
// REAL Engine.Investigate pipeline (RankCohort + applyCohortJudgmentMismatch
// both run inside engine.go, never mocked). kind is set directly on the
// stub interpretation -- exactly as a real interpreter call would set it:
// the judgment's KIND is the interpreter's own closed-vocabulary pick,
// never re-derived downstream from the free text.
func judgmentFramingEngineFixture(t *testing.T, requestedJudgment string, kind RequestedJudgmentKind) (InvestigationResult, *recordingTelemetry) {
	t.Helper()
	strugglingTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:STRUGGLING", Label: "Struggling"}
	healthyTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:HEALTHY", Label: "Healthy"}
	sparseTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SPARSE", Label: "Sparse"}
	cohort := &Cohort{
		Kind: SubjectTeam, Rationale: "kind census match",
		Members: []CohortMember{
			{Subject: healthyTeam, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: strugglingTeam, Rank: 2, InclusionReasons: []string{"matched"}},
			{Subject: sparseTeam, Rank: 3, InclusionReasons: []string{"matched"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: requestedJudgment, RequestedJudgmentKind: kind,
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	store := &resultStoreStub{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{
					healthFact("STRUGGLING", "high"),
					healthFact("HEALTHY", "low"),
					investmentFact("STRUGGLING", balancedThemes(), 0),
					investmentFact("HEALTHY", balancedThemes(), 0),
					// SPARSE gets NO facts at all. deficiencySeveritySignal's
					// own "available-zero" exception (cohort_ranking.go) still
					// counts it as one available family (weight 20) with
					// nothing else available, so availableWeight=20<50 keeps
					// it below the qualification floor -- insufficient_evidence,
					// never provisional.
				},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "placeholder", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "placeholder", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(577400000, 0).UTC() }, NewResultID: func() string { return "result_57740001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_57740001"
	request.Question = requestedJudgment
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, telemetry
}

func TestEngineJudgmentMismatchAcrossRequestedJudgmentFraming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		requestedJudgment string
		kind              RequestedJudgmentKind
		wantMismatch      bool
	}{
		{"rank best to worst (performance)", "Rank teams by current overall performance from strongest to weakest.", RequestedJudgmentKindPerformance, true},
		{"rank most struggling (attention)", "Rank teams by how much they are struggling, most to least.", RequestedJudgmentKindAttention, false},
		{"who needs attention (attention)", "Which teams need the most attention right now?", RequestedJudgmentKindAttention, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			result, telemetry := judgmentFramingEngineFixture(t, c.requestedJudgment, c.kind)
			if result.Cohort == nil || len(result.Cohort.Members) != 3 {
				t.Fatalf("result.Cohort = %#v, want the ranked 3-member cohort", result.Cohort)
			}
			if result.Cohort.ScoreMeaning != CohortScoreMeaningAttention {
				t.Fatalf("result.Cohort.ScoreMeaning = %q, want %q", result.Cohort.ScoreMeaning, CohortScoreMeaningAttention)
			}
			if result.Cohort.JudgmentMismatch != c.wantMismatch {
				t.Fatalf("result.Cohort.JudgmentMismatch = %v, want %v for requested_judgment %q", result.Cohort.JudgmentMismatch, c.wantMismatch, c.requestedJudgment)
			}
			// The SAME decision must reach the telemetry line too, through
			// the real Engine.Investigate call path -- not just the served
			// Cohort a caller of the API sees.
			if len(telemetry.cohortRanked) != 1 {
				t.Fatalf("telemetry.cohortRanked = %#v, want exactly 1 event", telemetry.cohortRanked)
			}
			if got := telemetry.cohortRanked[0].JudgmentMismatch; got != c.wantMismatch {
				t.Fatalf("telemetry event JudgmentMismatch = %v, want %v", got, c.wantMismatch)
			}
			if got := telemetry.cohortRanked[0].RequestedJudgmentKind; got != c.kind {
				t.Fatalf("telemetry event RequestedJudgmentKind = %q, want %q", got, c.kind)
			}
			// The sparse member never cleared the qualification floor --
			// this is the fixture's own precondition, not the behavior
			// under test, but a drifted fixture would make every
			// assertion above meaningless.
			var sparse *CohortMember
			for i := range result.Cohort.Members {
				if result.Cohort.Members[i].Subject.CanonicalID == "team:SPARSE" {
					sparse = &result.Cohort.Members[i]
				}
			}
			if sparse == nil || sparse.Outcome != CohortOutcomeInsufficientEvidence || sparse.Score != nil {
				t.Fatalf("fixture drift: sparse member = %#v, want Outcome=insufficient_evidence, Score=nil", sparse)
			}
			if err := result.Cohort.Validate(); err != nil {
				t.Fatalf("result.Cohort.Validate() = %v", err)
			}
		})
	}
}

// TestEngineJudgmentMismatchFalseWhenRequestedJudgmentIsBlank is a control:
// a request whose interpreter made no kind pick at all (RequestedJudgmentKind
// unset) must never be flagged as a mismatch.
func TestEngineJudgmentMismatchFalseWhenRequestedJudgmentIsBlank(t *testing.T) {
	t.Parallel()
	result, telemetry := judgmentFramingEngineFixture(t, "teams_under_pressure", "")
	if result.Cohort == nil {
		t.Fatalf("result.Cohort = nil")
	}
	if result.Cohort.JudgmentMismatch {
		t.Fatalf("result.Cohort.JudgmentMismatch = true, want false for a non-performance-worded requested judgment")
	}
	if len(telemetry.cohortRanked) != 1 || telemetry.cohortRanked[0].JudgmentMismatch {
		t.Fatalf("telemetry.cohortRanked = %#v, want exactly 1 event with JudgmentMismatch=false", telemetry.cohortRanked)
	}
	if got := telemetry.cohortRanked[0].RequestedJudgmentKind; got != "" {
		t.Fatalf("telemetry event RequestedJudgmentKind = %q, want empty", got)
	}
	if !strings.Contains(strings.ToLower(RankingFormulaVersion), "cohort-ranking") {
		t.Fatalf("fixture sanity: RankingFormulaVersion = %q", RankingFormulaVersion)
	}
}

// --- narrowSynthesisInput's own re-rank call site ---

// TestNarrowSynthesisInputCarriesTheJudgmentMismatchDecision drives the
// narrowing-retry re-rank directly (narrowSynthesisInput, chaos4636_budget_stage3.go)
// -- a SEPARATE RankCohort/applyCohortJudgmentMismatch call site from the
// engine's primary rank, exercised only when stage 3 narrows a cohort past
// its budget. Both fields must land on the returned event here too, not
// only on the primary rank's event.
func TestNarrowSynthesisInputCarriesTheJudgmentMismatchDecision(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1", "d1")
	params := synthesisAssemblyParams{
		Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{},
		Interpretation: InterpretedQuestion{RequestedJudgmentKind: RequestedJudgmentKindPerformance},
	}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if !result.Narrow {
		t.Fatalf("result.Narrow = false, want true -- a 4-member cohort narrowing to 2 must re-rank")
	}
	if result.Ranked.ScoreMeaning != CohortScoreMeaningAttention {
		t.Fatalf("result.Ranked.ScoreMeaning = %q, want %q", result.Ranked.ScoreMeaning, CohortScoreMeaningAttention)
	}
	if !result.Ranked.JudgmentMismatch {
		t.Fatalf("result.Ranked.JudgmentMismatch = false, want true -- a performance-kind request over an attention re-rank is a mismatch here too")
	}
	if result.Ranked.RequestedJudgmentKind != RequestedJudgmentKindPerformance {
		t.Fatalf("result.Ranked.RequestedJudgmentKind = %q, want %q", result.Ranked.RequestedJudgmentKind, RequestedJudgmentKindPerformance)
	}
}

// TestNarrowSynthesisInputJudgmentMismatchFalseForAttentionKind is the
// control: an attention-kind request over the SAME re-rank never mismatches.
func TestNarrowSynthesisInputJudgmentMismatchFalseForAttentionKind(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1", "d1")
	params := synthesisAssemblyParams{
		Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{},
		Interpretation: InterpretedQuestion{RequestedJudgmentKind: RequestedJudgmentKindAttention},
	}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if !result.Narrow {
		t.Fatalf("result.Narrow = false, want true")
	}
	if result.Ranked.JudgmentMismatch {
		t.Fatalf("result.Ranked.JudgmentMismatch = true, want false for an attention-kind request over an attention re-rank")
	}
	if result.Ranked.RequestedJudgmentKind != RequestedJudgmentKindAttention {
		t.Fatalf("result.Ranked.RequestedJudgmentKind = %q, want %q", result.Ranked.RequestedJudgmentKind, RequestedJudgmentKindAttention)
	}
}
