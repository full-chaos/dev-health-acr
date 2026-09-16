package contextfabric

// CHAOS-5774: a cohort ranking's Score/AttentionRank/RankingBasis/Drivers
// state an ATTENTION measure (adverse pressure -- operational deficiencies,
// readiness gaps, workload pressure, health risk, investment-mix
// concentration), never a performance, productivity, capability, or quality
// judgment. applyCohortJudgmentMismatch computes, server-side and
// deterministically, whether the interpreter's own closed-vocabulary
// RequestedJudgmentKind asks for a judgment the cohort's ScoreMeaning does
// not support, and mints that as data (Cohort.JudgmentMismatch) -- seen by
// both the synthesis prompt (so the model can decline honestly) and any
// consumer of the served result. The judgment's KIND is the INTERPRETER's
// classification (a closed enum picked alongside its own free-text
// RequestedJudgment), never re-derived downstream by pattern-matching that
// free text: a keyword scan over free text is fitted to wordings the same
// way a fabricated metric is fitted to a dataset, and the judgment's basis
// is the interpreter's job to name, not a downstream guess.

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
