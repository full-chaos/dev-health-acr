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

// CohortSuperlativeJudgmentTerms is the CLOSED, fixed set of ranking
// superlative/ordinal-position terms requireNoSuperlativeClaimOverUnrankableMember
// refuses over an unrankable member. Exported so genkitruntime's synthesis
// prompt can render this SAME list as the terms it tells the model never to
// use there -- one constant read by both the guard and the prompt, so they
// cannot list different words. Not an invented pattern-match: strongest/
// weakest and best/worst are the exact words this codebase's own
// AnswerEmphasis vocabulary already names a ranking's two ends with
// (frame_vocab.go: EmphasisPositiveOutliers "the strong end",
// EmphasisNegativeOutliers "the weak end"); highest/lowest, top/bottom, and
// first/last are their ordinary ordinal-position synonyms -- the system's
// own existing ranking vocabulary, never a phrase fitted to one team,
// question, or dataset.
var CohortSuperlativeJudgmentTerms = []string{
	"strongest", "weakest",
	"best", "worst",
	"highest", "lowest",
	"top", "bottom",
	"first", "last",
}

// requireNoSuperlativeClaimOverUnrankableMember is the structural backstop
// SynthesisDraft.ValidateAgainst calls for every model-authored driver: a
// driver whose affected_subjects cite a cohort member the ranking
// formula could NOT score (Outcome insufficient_evidence or not_applicable)
// must never use a ranking superlative (any term in
// CohortSuperlativeJudgmentTerms, in any case, matched as a whole word) in
// its own title or summary. The prompt
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
	for _, term := range CohortSuperlativeJudgmentTerms {
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
