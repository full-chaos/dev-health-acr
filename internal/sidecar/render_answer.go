package sidecar

import (
	"fmt"
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RenderAnswerProjectionMarkdown renders a bounded answer projection as
// markdown for a display-oriented MCP client (CHAOS-3746).
//
// Every model-authored or source-derived string -- the judgment, driver
// prose, cohort rationale, limitations -- is wrapped as UNTRUSTED DATA. An
// investigation answer is composed partly from approved documents, issue
// text, and episodes, which are data and never instructions; a rendering
// that presented them as ordinary prose would be the exact injection path
// the domain rules forbid.
//
// Structural values (status, standing, counts, identifiers) are inline
// escaped instead, because they come from closed vocabularies and bounded
// numbers rather than free text.
//
// This rendering is a VIEW. It never adds, reorders, or reinterprets
// anything the projection did not already assert: the projection is the
// only thing allowed to decide what an answer says.
func RenderAnswerProjectionMarkdown(projection contractsv1.ContextFabricAnswerProjection, maxBytes int) (markdown string, truncated bool) {
	if maxBytes <= 0 {
		return "", false
	}
	b := newBoundedBuilder(maxBytes)

	b.writeLine("# Answer")
	b.writeLine(fmt.Sprintf("- Status: %s", safeInline(string(projection.Status))))
	b.writeLine(fmt.Sprintf("- Result ID: %s", safeInline(projection.ResultID)))
	if projection.CoveragePartial {
		b.writeLine("- Coverage: partial")
	}
	if projection.ProjectionBudget.Truncated {
		b.writeLine("- This answer is shortened; see the omitted counts below.")
	}

	if projection.DirectJudgment != "" {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Judgment (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if !b.writeLines(untrustedBlock("direct_judgment", projection.DirectJudgment)) {
			return b.finishWithTruncation()
		}
	}
	if projection.CurrentState != "" {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Current state (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if !b.writeLines(untrustedBlock("current_state", projection.CurrentState)) {
			return b.finishWithTruncation()
		}
	}

	// A clarification request is the answer when the engine could not
	// settle the subject, so it renders before the drivers rather than as
	// a footnote a reader might miss.
	if projection.Clarification != nil {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Clarification needed (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if projection.Clarification.Prompt != "" {
			if !b.writeLines(untrustedBlock("clarification_prompt", projection.Clarification.Prompt)) {
				return b.finishWithTruncation()
			}
		}
		for _, candidate := range projection.Clarification.Candidates {
			line := fmt.Sprintf("- %s `%s` (receipt `%s`, confidence %.2f)",
				safeInline(string(candidate.Subject.Kind)), safeInline(candidate.Subject.Label),
				safeInline(candidate.ReceiptID), candidate.Confidence)
			if !b.writeLine(line) {
				return b.finishWithTruncation()
			}
		}
	}

	if len(projection.PrincipalDrivers) > 0 {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Drivers (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		for _, driver := range projection.PrincipalDrivers {
			header := fmt.Sprintf("### %s [%s / %s]",
				safeInline(driver.Title), safeInline(string(driver.Standing)), safeInline(driver.Category))
			if !b.writeLine(header) {
				return b.finishWithTruncation()
			}
			if !b.writeLines(untrustedBlock("driver_summary", driver.Summary)) {
				return b.finishWithTruncation()
			}
			if driver.Qualification != "" {
				if !b.writeLines(untrustedBlock("driver_qualification", driver.Qualification)) {
					return b.finishWithTruncation()
				}
			}
			if len(driver.EvidenceRefIDs) > 0 {
				if !b.writeLine("- Evidence: " + safeInline(strings.Join(driver.EvidenceRefIDs, ", "))) {
					return b.finishWithTruncation()
				}
			}
		}
	}

	if projection.Cohort != nil {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Cohort (%s of %s shown)",
			strconv.Itoa(len(projection.Cohort.Members)), strconv.Itoa(projection.Cohort.Total))) {
			return b.finishWithTruncation()
		}
		for _, member := range projection.Cohort.Members {
			line := fmt.Sprintf("%s. %s `%s`", strconv.Itoa(member.Rank),
				safeInline(string(member.Subject.Kind)), safeInline(member.Subject.Label))
			if !b.writeLine(line) {
				return b.finishWithTruncation()
			}
		}
	}

	if len(projection.KeyFacts) > 0 {
		b.writeLine("")
		if !b.writeLine("## Canonical facts") {
			return b.finishWithTruncation()
		}
		for _, fact := range projection.KeyFacts {
			line := fmt.Sprintf("- %s.%s = %s (`%s`)",
				safeInline(fact.Subject.Label), safeInline(fact.Field),
				safeInline(scalarValueText(fact.Value)), safeInline(string(fact.Kind)))
			if !b.writeLine(line) {
				return b.finishWithTruncation()
			}
		}
	}

	// Coverage and limitations render even when the answer is long. A
	// reader who cannot see that a source was missing cannot judge what
	// the answer is worth.
	if len(projection.CoverageSummary) > 0 {
		b.writeLine("")
		if !b.writeLine("## Source coverage") {
			return b.finishWithTruncation()
		}
		for _, entry := range projection.CoverageSummary {
			line := fmt.Sprintf("- %s: %s", safeInline(entry.Source), safeInline(string(entry.State)))
			if entry.Reason != "" {
				line += " (" + safeInline(entry.Reason) + ")"
			}
			if !b.writeLine(line) {
				return b.finishWithTruncation()
			}
		}
	}
	if len(projection.Limitations) > 0 {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Limitations (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		for _, limitation := range projection.Limitations {
			if !b.writeLines(untrustedBlock("limitation", limitation)) {
				return b.finishWithTruncation()
			}
		}
	}

	if omitted := omittedSummary(projection.ProjectionBudget); omitted != "" {
		b.writeLine("")
		if !b.writeLine("## Omitted from this answer") {
			return b.finishWithTruncation()
		}
		if !b.writeLine(omitted) {
			return b.finishWithTruncation()
		}
		if !b.writeLine(fmt.Sprintf("Fetch the full result with investigation_result and result_id `%s`.", safeInline(projection.ResultID))) {
			return b.finishWithTruncation()
		}
	}

	return b.finishWithTruncation()
}

// RenderInvestigationResultMarkdown renders the full canonical result as a
// short header plus its judgment. It stays deliberately brief: a caller
// reaching for the full result wants the STRUCTURED payload, and a
// thorough markdown re-rendering of a large result would consume the byte
// budget that payload needs.
func RenderInvestigationResultMarkdown(result contractsv1.ContextFabricInvestigationResult, maxBytes int) (markdown string, truncated bool) {
	if maxBytes <= 0 {
		return "", false
	}
	b := newBoundedBuilder(maxBytes)

	b.writeLine("# Investigation result")
	b.writeLine(fmt.Sprintf("- Status: %s", safeInline(string(result.Status))))
	b.writeLine(fmt.Sprintf("- Result ID: %s", safeInline(result.ResultID)))
	b.writeLine(fmt.Sprintf("- Drivers: %s", strconv.Itoa(len(result.Drivers))))
	b.writeLine(fmt.Sprintf("- Claimed facts: %s", strconv.Itoa(len(result.ClaimedFacts))))
	if result.Coverage.Partial {
		b.writeLine("- Coverage: partial")
	}
	if result.DirectJudgment != "" {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Judgment (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if !b.writeLines(untrustedBlock("direct_judgment", result.DirectJudgment)) {
			return b.finishWithTruncation()
		}
	}
	b.writeLine("")
	b.writeLine("The structured payload carries the full detail.")
	return b.finishWithTruncation()
}

// omittedSummary describes what the projection dropped, in the caller's
// terms. It returns "" when nothing was dropped, so an untruncated answer
// carries no misleading footer.
func omittedSummary(budget contractsv1.ContextFabricProjectionBudget) string {
	parts := make([]string, 0, 7)
	add := func(count int, noun string) {
		if count > 0 {
			parts = append(parts, strconv.Itoa(count)+" "+noun)
		}
	}
	add(budget.DriversOmitted, "drivers")
	add(budget.WithheldDriversOmitted, "withheld drivers")
	add(budget.CohortMembersOmitted, "cohort members")
	add(budget.FactsOmitted, "canonical facts")
	add(budget.CandidatesOmitted, "clarification candidates")
	add(budget.EvidenceRefsOmitted, "evidence references")
	if budget.FullResultOmitted {
		parts = append(parts, "the full canonical result (it exceeded the byte budget)")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Omitted: " + strings.Join(parts, ", ") + "."
}

// scalarValueText renders a claimed fact value for display. An absent value
// prints as "unknown" rather than an empty string, so a reader can tell a
// missing value from a blank one.
func scalarValueText(value contractsv1.ContextFabricScalarValue) string {
	switch {
	case value.String != nil:
		return *value.String
	case value.Integer != nil:
		return strconv.FormatInt(*value.Integer, 10)
	case value.Number != nil:
		return strconv.FormatFloat(*value.Number, 'f', -1, 64)
	case value.Boolean != nil:
		return strconv.FormatBool(*value.Boolean)
	case value.Null:
		return "null"
	default:
		return "unknown"
	}
}
