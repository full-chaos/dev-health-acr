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
//  2. requireNoSuperlativeClaimOverUnrankableMember is a BOUNDED, PRECISION-
//     TUNED lexical backstop behind (1), the prompt, and the interpreter's
//     own judgment-kind pick: a model-authored driver may never use a
//     ranking superlative about a member the formula could not score at
//     all. Its false positives cost a served answer (a bounded
//     re-synthesis, then a fail-closed refusal), so every rule it applies
//     requires the CONSTRUCTION -- a superlative form adjacent to, or
//     sharing a sentence with, a cohort or rank noun -- never a bare word
//     anywhere in the text.

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
// ranking superlative in several closed, general forms, never a phrase
// fitted to one team, question, or dataset. Every form below requires the
// CONSTRUCTION -- a superlative adjacent to, or sharing a sentence with, a
// cohort or rank noun (cohortNounOrRankWords) -- so ordinary prose that
// merely contains one of these words or forms is never flagged:
//
//  1. an UNCONDITIONAL word (cohortSuperlativeUnconditionalTermsList) --
//     best/worst read as a ranking claim wherever they appear about a
//     cited member; no false-positive class has been found for these two.
//  2. a POSITIONAL word (cohortSuperlativePositionalTermsList) --
//     top/bottom/first/last/leads/trails/leading/trailing -- when a cohort
//     noun or rank word also appears in the SAME SENTENCE.
//  3. "most"/"least" (cohortMostLeastAdjacencyPattern) -- ONLY when
//     followed, within at most two filler words, by a cohort noun or rank
//     word ("the most pressured team", "least ready project"). A bare
//     quantifier ("most of its incidents", "at least 3", "least-recently-
//     used") is never a claim -- being unconditional, as these words used
//     to be, rejected exactly that quantifier use.
//  4. a general -est/-iest SUPERLATIVE CONSTRUCTION
//     (cohortEstAdjacencyPattern) -- strongest/weakest/highest/lowest and
//     every other adjective this domain's own vocabulary forms the same
//     way (riskiest, unhealthiest, neediest, readiest, ...) -- but ONLY
//     when the word is immediately preceded by "the" or followed, within
//     two filler words, by a cohort noun. A free-floating -est/-iest word
//     anywhere in the sentence is NOT enough: ordinary English words that
//     happen to end this way (test, priest, attest, tempest, bequest,
//     conquest, inquest, honest, latest, ...) are common, and
//     cohortSuperlativeEstSuffixExceptions is the closed backstop for the
//     ones that can still reach the adjacency form.
//
// Two more constructions no single word can express are checked against
// the whole text: cohortSuperlativeComparativeToGroupPattern ("more <X>
// than any/all/every other") and cohortSuperlativeOrdinalPositionPattern
// ("#1"/"number one").
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
	text := driver.Title + ". " + driver.Summary
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
			if cohortSuperlativePositionalTerms[word] {
				return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
					"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
			}
		}
	}
	if cohortMostLeastAdjacencyPattern.MatchString(text) {
		return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
			"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
	}
	if textHasEstSuffixSuperlative(text) {
		return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
			"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
	}
	if cohortSuperlativeComparativeToGroupPattern.MatchString(text) || cohortSuperlativeOrdinalPositionPattern.MatchString(text) {
		return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
			"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
	}
	return nil
}

// cohortSuperlativeUnconditionalTermsList are whole-word matched with NO
// same-sentence scoping requirement: best/worst, the AnswerEmphasis-sourced
// pair (frame_vocab.go's EmphasisPositiveOutliers "the strong end" /
// EmphasisNegativeOutliers "the weak end").
var cohortSuperlativeUnconditionalTermsList = []string{"best", "worst"}

// cohortSuperlativePositionalTermsList are ordinal/comparative-position
// words that read as a ranking claim only inside a sentence that also names
// a cohort noun or rank word (cohortNounOrRankWords).
var cohortSuperlativePositionalTermsList = []string{"top", "bottom", "first", "last", "leads", "trails", "leading", "trailing"}

// CohortSuperlativeJudgmentTerms is the union of the two lists above, in
// order -- exported so genkitruntime's synthesis prompt can render this
// SAME list as the words it tells the model never to use there, one
// constant read by both the guard and the prompt so they cannot list
// different words. "most"/"least" and the general -est/-iest superlative
// construction are adjacency patterns, not a list, and have no place in a
// rendered word list; the prompt states both as prose instead.
var CohortSuperlativeJudgmentTerms = append(
	append([]string{}, cohortSuperlativeUnconditionalTermsList...),
	cohortSuperlativePositionalTermsList...,
)

var cohortSuperlativeUnconditionalTerms = wordSet(cohortSuperlativeUnconditionalTermsList)
var cohortSuperlativePositionalTerms = wordSet(cohortSuperlativePositionalTermsList)

// cohortSuperlativeEstSuffixExceptions is the CLOSED set of ordinary
// English words that end in "est"/"iest" but are never themselves a
// superlative claim -- the backstop for cohortEstAdjacencyPattern's own
// candidates, since an ordinary word can still land in "the <word>" or
// "<word> <cohort noun>" shape (e.g. "the priest", "attest team").
var cohortSuperlativeEstSuffixExceptions = wordSet([]string{
	"test", "rest", "request", "interest", "guest", "west", "chest",
	"forest", "harvest", "invest", "digest", "manifest", "contest",
	"protest", "arrest", "honest", "modest", "earnest", "suggest", "latest",
	"attest", "priest", "tempest", "bequest", "conquest", "inquest", "quest",
})

// cohortNounOrRankWordList is the closed list of nouns a positional,
// most/least, or -est-suffixed term must sit near to read as a ranking
// claim about THIS cohort, rather than an ordinary English sentence that
// happens to use an ordinary word.
var cohortNounOrRankWordList = []string{
	"team", "teams",
	"member", "members",
	"project", "projects",
	"repository", "repositories", "repo", "repos",
	"rank", "ranks", "ranked", "ranking",
	"place", "places", "placed",
	"position", "positions", "positioned",
	"cohort", "group", "peers",
}

var cohortNounOrRankWords = wordSet(cohortNounOrRankWordList)

// cohortNounOrRankWordPattern is cohortNounOrRankWordList rendered as one
// alternation, for the two adjacency regexes below -- built from the SAME
// list so the regex and the token-set check can never name different
// nouns.
var cohortNounOrRankWordPattern = strings.Join(cohortNounOrRankWordList, "|")

// cohortMostLeastAdjacencyPattern matches "most"/"least" followed, within
// at most two filler words, by a cohort noun or rank word -- "the most
// pressured team", "least ready project" -- and never a bare quantifier
// ("most of its incidents", "at least 3", "least-recently-used") that
// happens to share a sentence with an unrelated cohort noun elsewhere.
var cohortMostLeastAdjacencyPattern = regexp.MustCompile(`(?i)\b(?:most|least)\b(?:\s+\S+){0,2}\s+(?:` + cohortNounOrRankWordPattern + `)\b`)

// cohortEstAdjacencyPattern matches an -est/-iest CANDIDATE word (the
// regex's own minimum, a 2-letter-or-longer stem before the suffix, is
// deliberately loose -- textHasEstSuffixSuperlative applies the REAL
// 5-letter-total floor after extraction, since the total word length, not
// the stem alone, is what the guard's own contract promises) either
// immediately preceded by "the" or followed, within two filler words, by a
// cohort noun or rank word -- textHasEstSuffixSuperlative then filters
// each candidate through cohortSuperlativeEstSuffixExceptions. Adjacency,
// not "anywhere in the sentence": a free-floating -est word (a sentence
// that separately mentions a cohort noun somewhere else) is not a ranking
// claim.
var cohortEstAdjacencyPattern = regexp.MustCompile(`(?i)\bthe\s+([a-z]{2,}(?:est|iest))\b|\b([a-z]{2,}(?:est|iest))\b(?:\s+\S+){0,2}\s+(?:` + cohortNounOrRankWordPattern + `)\b`)

// textHasEstSuffixSuperlative reports whether text contains an -est/-iest
// superlative (at least 5 letters, the same floor the guard's own doc
// comment promises) in one of the two adjacency shapes
// cohortEstAdjacencyPattern matches, filtering every candidate through the
// closed exception list.
func textHasEstSuffixSuperlative(text string) bool {
	for _, match := range cohortEstAdjacencyPattern.FindAllStringSubmatch(text, -1) {
		word := match[1]
		if word == "" {
			word = match[2]
		}
		word = strings.ToLower(word)
		if len(word) < 5 {
			continue
		}
		if !cohortSuperlativeEstSuffixExceptions[word] {
			return true
		}
	}
	return false
}

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

// cohortSentenceAbbreviations is the closed list of abbreviations
// sentenceWords must not split a sentence after, even though each one ends
// in a period immediately followed by whitespace and an uppercase letter
// (its own usual look of a sentence boundary): "Team A vs. Team B", "Acme
// Inc. reported delays", "the riskiest, e.g. Team Foo, needs review". A
// plain list, not a wordSet: "e.g"/"i.e" contain an internal period, so
// endsWithSentenceAbbreviation matches by SUBSTRING lookback in the raw
// text, never by the already-tokenized word (sentenceWords' own tokenizer
// would otherwise see "e.g" as the two separate words "e" and "g").
var cohortSentenceAbbreviations = []string{"e.g", "i.e", "etc", "vs", "inc"}

// endsWithSentenceAbbreviation reports whether text, immediately before the
// period at periodIndex, ends with one of cohortSentenceAbbreviations as a
// whole word/phrase (never as a mid-word substring, e.g. never matching
// "vs" inside some other word).
func endsWithSentenceAbbreviation(text string, periodIndex int) bool {
	lower := strings.ToLower(text[:periodIndex])
	for _, abbr := range cohortSentenceAbbreviations {
		if !strings.HasSuffix(lower, abbr) {
			continue
		}
		start := len(lower) - len(abbr)
		if start == 0 || !isWordByte(lower[start-1]) {
			return true
		}
	}
	return false
}

// wordSet builds a lowercased lookup set from a word list, for the several
// closed vocabularies above that are consulted by membership rather than
// iterated in order.
func wordSet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[strings.ToLower(w)] = true
	}
	return set
}

// sentenceWords splits text into sentences and each sentence into whole,
// lowercased words using the SAME word-byte definition isWordByte uses
// elsewhere in this file (so a hyphenated compound like "top-level" splits
// into "top" and "level" as two separate words, neither of which is a
// cohort noun). A sentence ends at "." "!" or "?" ONLY when followed by
// whitespace and an uppercase letter, UNLESS the word just completed is a
// known abbreviation (cohortSentenceAbbreviations) -- and at a BLANK line
// (two or more newlines, whitespace-only lines counting as blank). A
// single newline is ordinary whitespace, not a sentence boundary: a
// bullet or wrapped line ("the riskiest\n- team") must not split a real
// claim across two "sentences" this guard would then check separately.
func sentenceWords(text string) [][]string {
	var sentences [][]string
	var current []string
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			current = append(current, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	endSentence := func() {
		flushWord()
		if len(current) > 0 {
			sentences = append(sentences, current)
			current = nil
		}
	}
	i := 0
	for i < len(text) {
		b := text[i]
		switch {
		case b == '\n':
			flushWord()
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r') {
				j++
			}
			if j < len(text) && text[j] == '\n' {
				endSentence()
				i = j
				continue
			}
			// A single newline is ordinary whitespace -- fall through.
		case b == '.' || b == '!' || b == '?':
			flushWord()
			j := i + 1
			sawSpace := false
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				sawSpace = true
				j++
			}
			nextIsUpper := j < len(text) && text[j] >= 'A' && text[j] <= 'Z'
			if sawSpace && nextIsUpper && !endsWithSentenceAbbreviation(text, i) {
				endSentence()
			}
		case isWordByte(b):
			word.WriteByte(b)
		default:
			flushWord()
		}
		i++
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
