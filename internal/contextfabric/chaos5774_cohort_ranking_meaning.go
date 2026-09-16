package contextfabric

import (
	"regexp"
	"strings"
)

// CHAOS-5774: a cohort ranking's Score/AttentionRank/RankingBasis/Drivers
// state an ATTENTION measure (adverse pressure -- operational deficiencies,
// readiness gaps, workload pressure, health risk, investment-mix
// concentration), never a performance, productivity, capability, or quality
// judgment. Two independent, general (never dataset- or question-specific)
// mechanisms close the gap an executed-answer observation found between
// that measure and a served "strongest/weakest performer" claim:
//
//  1. applyCohortJudgmentMismatch computes, server-side and
//     deterministically, whether the interpreter's own closed-vocabulary
//     RequestedJudgmentKind asks for a judgment the cohort's ScoreMeaning
//     does not support, and mints that as data (Cohort.JudgmentMismatch) --
//     seen by both the synthesis prompt (so the model can decline honestly)
//     and any consumer of the served result. The judgment's KIND is the
//     INTERPRETER's classification (a closed enum picked alongside its own
//     free-text RequestedJudgment), never re-derived downstream by pattern-
//     matching that free text: a keyword scan over free text is fitted to
//     wordings the same way a fabricated metric is fitted to a dataset, and
//     the judgment's basis is the interpreter's job to name, not a
//     downstream guess.
//  2. requireNoSuperlativeClaimOverUnrankableMember is the structural
//     backstop: a model-authored driver may never use a ranking superlative
//     about a member the formula could not score at all.

// scoreMeaningSupportsJudgmentKind reports whether meaning (a cohort's own
// ScoreMeaning) can honestly answer a judgment of kind. Fail-closed on BOTH
// arguments: an empty/unspecified kind never mismatches anything (there is
// nothing confident enough to contradict), and an unrecognized meaning or
// kind added by a FUTURE change is never assumed to satisfy a judgment it
// was never verified against -- only the exhaustively-listed pairs below
// return true.
func scoreMeaningSupportsJudgmentKind(meaning CohortScoreMeaning, kind RequestedJudgmentKind) bool {
	switch kind {
	case RequestedJudgmentKindAttention:
		return meaning == CohortScoreMeaningAttention
	case RequestedJudgmentKindPerformance:
		// No ScoreMeaning this formula can produce today is a performance
		// measure -- see cohort_ranking.go's weight* constants, every one
		// an adverse-pressure signal.
		return false
	default:
		// Empty (no pick) or any kind this function does not yet know --
		// never confident enough to call it a mismatch.
		return true
	}
}

// applyCohortJudgmentMismatch mints Cohort.JudgmentMismatch in place,
// deterministically, BEFORE synthesis runs -- so it rides in the
// model's own input payload (the model does not have to infer the mismatch
// itself) and, via the existing result.Cohort = graphContext.Cohort
// assignment, in the served answer too.
//
// A no-op whenever cohort is nil or unranked (ScoreMeaning empty): there is
// nothing to mismatch a judgment against.
func applyCohortJudgmentMismatch(cohort *Cohort, requestedJudgmentKind RequestedJudgmentKind) {
	if cohort == nil || cohort.ScoreMeaning == "" {
		return
	}
	cohort.JudgmentMismatch = !scoreMeaningSupportsJudgmentKind(cohort.ScoreMeaning, requestedJudgmentKind)
}

// requireNoSuperlativeClaimOverUnrankableMember checks a driver's text for a
// ranking superlative in THREE closed, general forms, never a phrase fitted
// to one team, question, or dataset:
//
//  1. an UNCONDITIONAL word (cohortSuperlativeUnconditionalTermsList) --
//     best/worst/most/least read as a ranking claim in virtually any
//     surrounding text about a cited cohort member, so these reject
//     wherever they appear.
//  2. a POSITIONAL word (cohortSuperlativePositionalTermsList) --
//     top/bottom/first/last/leads/trails/leading/trailing -- but ONLY when
//     a cohort noun or rank word (cohortNounOrRankWords) also appears in
//     the SAME SENTENCE. A bare positional word in ordinary prose ("covers
//     the last 30 days", "top-level of the org chart", "first turn in the
//     rotation", "the bottom line") is not a ranking claim, and rejecting
//     it anyway burns a re-synthesis draw over benign phrasing.
//  3. a GENERAL -est/-iest SUPERLATIVE CONSTRUCTION
//     (isCohortSuperlativeEstSuffixWord) -- strongest/weakest/highest/
//     lowest and every other adjective this domain's own vocabulary forms
//     the same way (riskiest, unhealthiest, neediest, readiest, ...) --
//     under the SAME same-sentence cohort-noun requirement as a positional
//     word, since an ordinary English word that happens to end in
//     "-est"/"-iest" (test, rest, interest, guest, forest, honest, latest,
//     ...) is excepted by cohortSuperlativeEstSuffixExceptions and never
//     read as a superlative at all.
//
// Two more constructions no single word can express are checked separately
// against the whole text, not sentence-scoped: cohortSuperlativeComparativeToGroupPattern
// ("more <X> than any/all/every other", the comparative-to-the-group
// position "most" would otherwise express) and cohortSuperlativeOrdinalPositionPattern
// ("#1"/"number one", the first-place position "first" would otherwise
// express).
func requireNoSuperlativeClaimOverUnrankableMember(driver DriverJudgment, cohort *Cohort) error {
	if cohort == nil {
		return nil
	}
	citesUnrankable := false
	for _, subject := range driver.AffectedSubjects {
		for _, member := range cohort.Members {
			if member.Subject.CanonicalID != subject.CanonicalID || member.Subject.Kind != subject.Kind {
				continue
			}
			if member.Outcome == CohortOutcomeInsufficientEvidence || member.Outcome == CohortOutcomeNotApplicable {
				citesUnrankable = true
			}
		}
	}
	if !citesUnrankable {
		return nil
	}
	text := strings.ToLower(driver.Title + ". " + driver.Summary)
	for _, sentence := range sentenceWords(text) {
		hasCohortNoun := false
		for _, word := range sentence {
			if cohortNounOrRankWords[word] {
				hasCohortNoun = true
				break
			}
		}
		for _, word := range sentence {
			if cohortSuperlativeUnconditionalTerms[word] {
				return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
					"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
			}
			if !hasCohortNoun {
				continue
			}
			if cohortSuperlativePositionalTerms[word] || isCohortSuperlativeEstSuffixWord(word) {
				return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
					"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
			}
		}
	}
	if cohortSuperlativeComparativeToGroupPattern.MatchString(text) || cohortSuperlativeOrdinalPositionPattern.MatchString(text) {
		return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
			"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
	}
	return nil
}

// cohortSuperlativeUnconditionalTermsList are whole-word matched with NO
// same-sentence scoping requirement: best/worst (the AnswerEmphasis-sourced
// pair, frame_vocab.go's EmphasisPositiveOutliers "the strong end" /
// EmphasisNegativeOutliers "the weak end") and most/least (the general
// English superlative construction for a multi-syllable adjective, e.g.
// "most pressured", "least ready" -- exactly the shape this cohort's own
// attention-domain adjectives take).
var cohortSuperlativeUnconditionalTermsList = []string{"best", "worst", "most", "least"}

// cohortSuperlativePositionalTermsList are ordinal/comparative-position
// words that read as a ranking claim only inside a sentence that also names
// a cohort noun or rank word (cohortNounOrRankWords) -- see
// requireNoSuperlativeClaimOverUnrankableMember's own doc comment for the
// false-positive shapes this same-sentence scoping exists to admit.
var cohortSuperlativePositionalTermsList = []string{"top", "bottom", "first", "last", "leads", "trails", "leading", "trailing"}

// CohortSuperlativeJudgmentTerms is the union of the two lists above, in
// order -- exported so genkitruntime's synthesis prompt can render this
// SAME list as the words it tells the model never to use there, one
// constant read by both the guard and the prompt so they cannot list
// different words. The general -est/-iest superlative construction
// (isCohortSuperlativeEstSuffixWord) is not a list and has no place in a
// rendered word list; the prompt states that construction as prose instead.
var CohortSuperlativeJudgmentTerms = append(
	append([]string{}, cohortSuperlativeUnconditionalTermsList...),
	cohortSuperlativePositionalTermsList...,
)

var cohortSuperlativeUnconditionalTerms = wordSet(cohortSuperlativeUnconditionalTermsList)
var cohortSuperlativePositionalTerms = wordSet(cohortSuperlativePositionalTermsList)

// cohortSuperlativeEstSuffixExceptions is the CLOSED set of ordinary
// English words that end in "est"/"iest" but are never themselves a
// superlative claim -- isCohortSuperlativeEstSuffixWord's general
// CONSTRUCTION rule would otherwise flag every one of them.
var cohortSuperlativeEstSuffixExceptions = wordSet([]string{
	"test", "rest", "request", "interest", "guest", "west", "chest",
	"forest", "harvest", "invest", "digest", "manifest", "contest",
	"protest", "arrest", "honest", "modest", "earnest", "suggest", "latest",
})

// isCohortSuperlativeEstSuffixWord reports whether word (already lowercased)
// is a general English -est/-iest superlative form: at least 5 letters,
// ending in "est", and not one of the closed exceptions above. A
// CONSTRUCTION rule, not a word list -- this domain's own adjectives (risk,
// health, readiness, need, ...) form their superlatives the same way
// strong/weak/high/low do ("riskiest", "unhealthiest", "neediest",
// "readiest"), and a fixed word list can never anticipate every one.
func isCohortSuperlativeEstSuffixWord(word string) bool {
	if len(word) < 5 || !strings.HasSuffix(word, "est") {
		return false
	}
	return !cohortSuperlativeEstSuffixExceptions[word]
}

// cohortNounOrRankWords is the closed set of nouns a positional or
// -est-suffixed term must share a sentence with to read as a ranking claim
// about THIS cohort, rather than an ordinary English sentence that happens
// to use an ordinary word.
var cohortNounOrRankWords = wordSet([]string{
	"team", "teams",
	"member", "members",
	"project", "projects",
	"repository", "repositories",
	"rank", "ranks", "ranked", "ranking",
	"place", "places", "placed",
	"position", "positions", "positioned",
	"cohort",
})

// cohortSuperlativeComparativeToGroupPattern matches "more <up to 6 words>
// than any/all/every other", case-insensitive -- the comparative-to-the-group
// construction "most" would otherwise express (e.g. "more pressured than any
// other team", "more critically behind than every other project"). A closed
// grammatical pattern, not a phrase fitted to one dataset: it never matches
// on subject/adjective content, only on the surrounding English structure.
var cohortSuperlativeComparativeToGroupPattern = regexp.MustCompile(`(?i)\bmore\b(?:\s+\S+){0,6}?\s+than\s+(?:any|all|every)\s+other\b`)

// cohortSuperlativeOrdinalPositionPattern matches "#1" or "number one",
// case-insensitive -- the SAME first-place position "first" already covers
// as a word, spelled as a rank number instead.
var cohortSuperlativeOrdinalPositionPattern = regexp.MustCompile(`(?i)(?:#\s?1\b|\bnumber\s+one\b)`)

// wordSet builds a lookup set from a word list, for the several closed
// vocabularies above that are consulted by membership rather than iterated
// in order.
func wordSet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

// sentenceWords splits text (already lowercased) into sentences on ".",
// "!", "?", and newlines, and each sentence into whole words using the SAME
// word-byte definition containsWord/isWordByte use elsewhere in this file --
// so a hyphenated compound like "top-level" splits into "top" and "level"
// as two separate words, neither of which is a cohort noun, rather than
// surviving as one token a positional-term check could never match anyway.
func sentenceWords(text string) [][]string {
	var sentences [][]string
	var current []string
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			current = append(current, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(text); i++ {
		b := text[i]
		switch {
		case b == '.' || b == '!' || b == '?' || b == '\n':
			flushWord()
			if len(current) > 0 {
				sentences = append(sentences, current)
				current = nil
			}
		case isWordByte(b):
			word.WriteByte(b)
		default:
			flushWord()
		}
	}
	flushWord()
	if len(current) > 0 {
		sentences = append(sentences, current)
	}
	return sentences
}

// isWordByte reports whether b is part of an identifier-shaped word --
// sentenceWords' own tokenizer boundary.
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
