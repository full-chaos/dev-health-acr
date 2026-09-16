package contextfabric

import "strings"

// CHAOS-5774: a cohort ranking's Score/AttentionRank/RankingBasis/Drivers
// state an ATTENTION measure (adverse pressure -- operational deficiencies,
// readiness gaps, workload pressure, health risk, investment-mix
// concentration), never a performance, productivity, capability, or quality
// judgment. Two independent, general (never dataset- or question-specific)
// mechanisms close the gap an executed-answer observation found between
// that measure and a served "strongest/weakest performer" claim:
//
//  1. requestedJudgmentImpliesPerformanceJudgment + applyCohortJudgmentMismatch
//     compute, server-side and deterministically, whether the investigation's
//     OWN requested_judgment asked for a judgment the cohort's ScoreMeaning
//     does not support, and mint that as data (Cohort.JudgmentMismatch) --
//     seen by both the synthesis prompt (so the model can decline honestly)
//     and any consumer of the served result.
//  2. requireNoSuperlativeClaimOverUnrankableMember is the structural
//     backstop: a model-authored driver may never use a ranking superlative
//     about a member the formula could not score at all.

// cohortPerformanceJudgmentTerms is the CLOSED, fixed substring vocabulary
// requestedJudgmentImpliesPerformanceJudgment matches against. It mirrors
// frame_shape.go's own established pattern of classifying free-text
// RequestedJudgment by substring match (see that file's doc comment on the
// cohort-kind match) -- applied here to a different question (does this
// judgment ask for PERFORMANCE) rather than to which kind of subject is
// being asked about. Fixed and general: these are the ordinary English words
// for the requested-judgment CLASS this rule cares about, never a phrase
// tuned to one team, question, or corpus row.
var cohortPerformanceJudgmentTerms = []string{
	"performance",
	"performing",
	"productivity",
	"productive",
}

// requestedJudgmentImpliesPerformanceJudgment reports whether judgment (the
// interpreter's own free-text InterpretedQuestion.RequestedJudgment) asks
// for a performance/productivity comparison -- the one judgment class this
// cohort ranking formula can never support (its formula measures adverse
// pressure only; see cohort_ranking.go's weight* constants).
func requestedJudgmentImpliesPerformanceJudgment(judgment string) bool {
	lower := strings.ToLower(judgment)
	for _, term := range cohortPerformanceJudgmentTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// scoreMeaningSupportsPerformanceJudgment reports whether meaning itself IS
// a performance measure. Fail-closed: an unrecognized or future meaning is
// never assumed to satisfy a performance judgment, so a later
// ContextFabricCohortScoreMeaning member added for some OTHER non-
// performance measure (e.g. a quality or complexity score) does not
// silently stop this rule from firing just because it is not "attention".
func scoreMeaningSupportsPerformanceJudgment(meaning CohortScoreMeaning) bool {
	return false
}

// applyCohortJudgmentMismatch mints Cohort.JudgmentMismatch in place,
// deterministically, BEFORE synthesis runs -- so it rides in the
// model's own input payload (the model does not have to infer the mismatch
// itself from requested_judgment text) and, via the existing
// result.Cohort = graphContext.Cohort assignment, in the served answer too.
//
// A no-op whenever cohort is nil or unranked (ScoreMeaning empty): there is
// nothing to mismatch a judgment against.
func applyCohortJudgmentMismatch(cohort *Cohort, requestedJudgment string) {
	if cohort == nil || cohort.ScoreMeaning == "" {
		return
	}
	cohort.JudgmentMismatch = requestedJudgmentImpliesPerformanceJudgment(requestedJudgment) &&
		!scoreMeaningSupportsPerformanceJudgment(cohort.ScoreMeaning)
}

// cohortSuperlativeJudgmentTerms is the CLOSED, fixed set of ranking
// superlatives requireNoSuperlativeClaimOverUnrankableMember refuses over an
// unrankable member. Not an invented pattern-match: these are the exact
// words this codebase's own AnswerEmphasis vocabulary already names a
// ranking's two ends with (frame_vocab.go: EmphasisPositiveOutliers "the
// strong end", EmphasisNegativeOutliers "the weak end") plus their ordinary
// English synonyms, so the list is the system's own existing ranking
// vocabulary, never a phrase fitted to one team, question, or dataset.
var cohortSuperlativeJudgmentTerms = []string{
	"strongest",
	"weakest",
	"best",
	"worst",
}

// requireNoSuperlativeClaimOverUnrankableMember is the structural backstop
// SynthesisDraft.ValidateAgainst calls for every model-authored driver: a
// driver whose affected_subjects cite a cohort member the ranking
// formula could NOT score (Outcome insufficient_evidence or not_applicable)
// must never use a ranking superlative (best/worst/strongest/weakest, or any
// case/word-boundary variant) in its own title or summary. The prompt
// (genkitruntime's synthesisSystemPrompt) states this rule too, but a
// deterministic guard applies even if a future prompt regresses -- the same
// "guard, not the prompt, is what actually enforces this" discipline
// phrasingSystemPrompt's own doc comment already states for a sibling
// bounded call.
//
// A member with a real Score (qualified/provisional) is UNAFFECTED: this
// rule is about a member the formula never scored at all, not about
// disagreeing with a superlative over a member that DOES have a supported
// attention position.
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
	text := strings.ToLower(driver.Title + " " + driver.Summary)
	for _, term := range cohortSuperlativeJudgmentTerms {
		if containsWord(text, term) {
			return rejectSynthesis(RejectionReasonDriverSuperlativeOverUnrankableMember,
				"driver uses a ranking superlative about a cohort member the ranking formula could not score (outcome insufficient_evidence/not_applicable)")
		}
	}
	return nil
}

// containsWord reports whether term appears in text (already lowercased) as
// a whole word -- not merely a substring -- so this guard never trips over
// an unrelated word that happens to contain one of the fixed superlative
// terms as a substring.
func containsWord(text, term string) bool {
	idx := 0
	for {
		pos := strings.Index(text[idx:], term)
		if pos < 0 {
			return false
		}
		start := idx + pos
		end := start + len(term)
		beforeOK := start == 0 || !isWordByte(text[start-1])
		afterOK := end == len(text) || !isWordByte(text[end])
		if beforeOK && afterOK {
			return true
		}
		idx = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
