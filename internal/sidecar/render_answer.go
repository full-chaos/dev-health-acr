package sidecar

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	// Before the judgment, never after it. A historical answer whose axis
	// appears below the prose is an answer the reader has already read as
	// current -- and the "Current state" heading further down actively
	// asserts that it is. The label goes where the axis is known first.
	for _, line := range temporalLines(projection.Temporal) {
		b.writeLine(line)
	}
	if projection.CoveragePartial {
		b.writeLine("- Coverage: partial")
	}
	if projection.ProjectionBudget.Truncated {
		b.writeLine("- This answer is shortened; see the omitted counts below.")
	}

	if len(projection.CommittedSubjects) > 0 {
		b.writeLine("")
		if !b.writeLine("## Subjects") {
			return b.finishWithTruncation()
		}
		// The answer must be able to say STRUCTURALLY what it is about.
		// Without this the reader had only the model's prose to identify
		// the subject, which is the rely-on-prose pattern this surface
		// rejects everywhere else (CHAOS-3746 round 8).
		for _, subject := range projection.CommittedSubjects {
			if !b.writeLine(fmt.Sprintf("- %s `%s`", safeInline(string(subject.Kind)), untrustedInline(subject.Label))) {
				return b.finishWithTruncation()
			}
		}
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

	if len(projection.StrongestPressures) > 0 {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Strongest pressures (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		for _, pressure := range projection.StrongestPressures {
			if !b.writeLines(untrustedBlock("pressure", pressure)) {
				return b.finishWithTruncation()
			}
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
				safeInline(string(candidate.Subject.Kind)), untrustedInline(candidate.Subject.Label),
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
				untrustedInline(driver.Title), safeInline(string(driver.Standing)), safeInline(driver.Category))
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
			// AffectedSubjects (CHAOS-4398 PR3, wired PR3b): which cohort
			// member(s) this driver judgment ties back to -- kind/canonical_id
			// are closed-vocabulary/opaque-id (inline-escaped, same
			// discipline the Cohort member rows below already use); Label is
			// source-derived text (untrusted-marked).
			if len(driver.AffectedSubjects) > 0 {
				subjects := make([]string, len(driver.AffectedSubjects))
				for i, subject := range driver.AffectedSubjects {
					subjects[i] = fmt.Sprintf("%s `%s` (%s)", safeInline(string(subject.Kind)), safeInline(subject.CanonicalID), untrustedInline(subject.Label))
				}
				if !b.writeLine("- Affected: " + strings.Join(subjects, ", ")) {
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
		if !b.writeLines(untrustedBlock("cohort_rationale", projection.Cohort.Rationale)) {
			return b.finishWithTruncation()
		}
		for _, member := range projection.Cohort.Members {
			line := fmt.Sprintf("%s. %s `%s`", strconv.Itoa(member.Rank),
				safeInline(string(member.Subject.Kind)), untrustedInline(member.Subject.Label))
			if !b.writeLine(line) {
				return b.finishWithTruncation()
			}
			for _, reason := range member.InclusionReasons {
				if !b.writeLine("   - " + untrustedInline(reason)) {
					return b.finishWithTruncation()
				}
			}
		}
		// RankingTable (CHAOS-4398 PR3, wired PR3b): the Rows-panel view,
		// one line per RankingComputed member in AttentionRank order.
		// Every field value is untrusted-marked regardless of its scalar
		// variant -- the SAME conservative "generic scalar-bag, mark the
		// whole leaf" treatment key_facts[].rows[].fields{}.string already
		// gets (this row type is the identical
		// ContextFabricClaimedFactRow shape). Field names themselves are
		// fixed Go string literals (rankingTableFieldOrder), never wire
		// data, so they render bare.
		if len(projection.Cohort.RankingTable) > 0 {
			b.writeLine("")
			if !b.writeLine(fmt.Sprintf("## Rows (%s)", untrustedDataHeader)) {
				return b.finishWithTruncation()
			}
			for _, row := range projection.Cohort.RankingTable {
				if !b.writeLine(rankingTableRowLine(row)) {
					return b.finishWithTruncation()
				}
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
				untrustedInline(fact.Subject.Label), untrustedInline(fact.Field),
				untrustedInline(scalarValueText(fact.Value)), safeInline(string(fact.Kind)))
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
				line += " (" + untrustedInline(entry.Reason) + ")"
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

	// Warnings render alongside limitations. Dropping them entirely meant
	// an answer could carry a warning the reader never saw (codex round-4
	// F5) -- the opposite of what a warning is for.
	if len(projection.Warnings) > 0 {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Warnings (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		for _, warning := range projection.Warnings {
			if !b.writeLines(untrustedBlock("warning", warning)) {
				return b.finishWithTruncation()
			}
		}
	}

	// CHAOS-3900 W2 / CHAOS-3972 P3 (design brief §2.3: "structure_needs...
	// plus a bounded rendered_markdown section flagged untrusted, matching
	// the existing response discipline"). Structural values (member/kind
	// enums, receipt/option ids, pattern ids) are inline-escaped; every
	// server-rendered label, the handle value span, and the source column
	// name are untrusted-marked (same conservative leaf-name treatment
	// MCPInvestigateQuestionUntrustedFields itself applies).
	if we := projection.EffectiveEvidenceWindow; we != nil {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("- Evidence window: %s (%s)", safeInline(windowBoundsText(we)), safeInline(string(we.Provenance)))) {
			return b.finishWithTruncation()
		}
	}
	if needs := projection.StructureNeeds; needs != nil {
		// CHAOS-4118 (team-lead ruling 2026-08-22): windowConfirmationRequiredResult
		// (contextfabric/window.go) composes StructureNeeds' window member
		// (Missing=[window], WindowOptions) and the legacy WindowClarification
		// field in lockstep, from the identical option set -- rendering both
		// would show every window option twice, under two different
		// headings. The legacy "## Window options" rendering below stays
		// canonical and byte-identical to pre-CHAOS-4118 behavior; this block
		// skips the window member entirely -- both its Missing entry and its
		// WindowOptions -- whenever WindowClarification is present, rather
		// than deciding here which surface should ultimately own rendering
		// (CHAOS-4121 follow-up). Skipping loses nothing: it is the SAME
		// data, and every non-window member renders exactly as before.
		skipWindow := projection.WindowClarification != nil
		var missing []contractsv1.ContextFabricStructureNeedKind
		for _, member := range needs.Missing {
			if skipWindow && member == contractsv1.ContextFabricStructureNeedWindow {
				continue
			}
			missing = append(missing, member)
		}
		windowOptions := needs.WindowOptions
		if skipWindow {
			windowOptions = nil
		}
		if len(missing) > 0 || len(needs.KindOptions) > 0 || len(needs.AnchorOptions) > 0 || len(needs.HandleOptions) > 0 || len(needs.CandidateOptions) > 0 || len(windowOptions) > 0 {
			b.writeLine("")
			if !b.writeLine("## Structure needed") {
				return b.finishWithTruncation()
			}
			for _, member := range missing {
				if !b.writeLine("- Missing: " + safeInline(string(member))) {
					return b.finishWithTruncation()
				}
			}
			for _, opt := range needs.KindOptions {
				line := fmt.Sprintf("- Kind option `%s` (receipt `%s`): %s%s", safeInline(string(opt.Kind)), safeInline(opt.ReceiptID), untrustedInline(opt.Label), phrasingSuffix(opt.Phrasing))
				if !b.writeLine(line) {
					return b.finishWithTruncation()
				}
			}
			for _, opt := range needs.AnchorOptions {
				line := fmt.Sprintf("- Anchor option `%s` (receipt `%s`): %s%s", safeInline(string(opt.Kind)), safeInline(opt.ReceiptID), untrustedInline(opt.Label), phrasingSuffix(opt.Phrasing))
				if !b.writeLine(line) {
					return b.finishWithTruncation()
				}
			}
			for _, opt := range needs.HandleOptions {
				line := fmt.Sprintf("- Handle option `%s`/`%s` (receipt `%s`): %s = %s (source %s)%s",
					safeInline(string(opt.Kind)), safeInline(opt.PatternID), safeInline(opt.ReceiptID),
					untrustedInline(opt.Label), untrustedInline(opt.Value), untrustedInline(opt.SourceColumn), phrasingSuffix(opt.Phrasing))
				if !b.writeLine(line) {
					return b.finishWithTruncation()
				}
			}
			// CHAOS-4012: CandidateOptions is StructureNeeds' 5th member's
			// own offer list -- same untrustedInline(opt.Label) discipline
			// as every option list above, and the SAME "kind + canonical_id"
			// shape AnchorOptions uses (CandidateOption is AnchorOption's
			// field shape minus MatchedTermHash), never a
			// MatchedTermHash line since this member has none.
			for _, opt := range needs.CandidateOptions {
				line := fmt.Sprintf("- Candidate option `%s`/`%s` (receipt `%s`): %s%s", safeInline(string(opt.Kind)), safeInline(opt.CanonicalID), safeInline(opt.ReceiptID), untrustedInline(opt.Label), phrasingSuffix(opt.Phrasing))
				if !b.writeLine(line) {
					return b.finishWithTruncation()
				}
			}
			for _, opt := range windowOptions {
				line := fmt.Sprintf("- Window option (receipt `%s`): %s", safeInline(opt.ReceiptID), untrustedInline(opt.Label))
				if !b.writeLine(line) {
					return b.finishWithTruncation()
				}
			}
		}
	}
	if wc := projection.WindowClarification; wc != nil {
		b.writeLine("")
		if !b.writeLine("## Window options") {
			return b.finishWithTruncation()
		}
		for _, opt := range wc.Options {
			line := fmt.Sprintf("- (receipt `%s`): %s", safeInline(opt.ReceiptID), untrustedInline(opt.Label))
			if !b.writeLine(line) {
				return b.finishWithTruncation()
			}
		}
	}
	if len(projection.ConfirmedStructure) > 0 {
		b.writeLine("")
		if !b.writeLine("## Structure confirmed") {
			return b.finishWithTruncation()
		}
		for _, entry := range projection.ConfirmedStructure {
			line := fmt.Sprintf("- %s = %s (%s, %s)", safeInline(string(entry.Member)), untrustedInline(entry.AppliedValue),
				safeInline(string(entry.Source)), safeInline(string(entry.Provenance)))
			if !b.writeLine(line) {
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
	add(budget.LimitationsOmitted, "limitations")
	add(budget.WarningsOmitted, "warnings")
	add(budget.CoverageOmitted, "coverage entries")
	add(budget.ReasonsOmitted, "reasons")
	add(budget.ValuesClamped, "shortened values")
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

// rankingTableFieldOrder is the FIXED, deterministic key order
// rankingTableRowLine reads a Rows-panel row in -- ContextFabricClaimedFactRow.Fields
// is a map (Go iteration order is random), and answerprojection's own
// buildRankingTable (ranking_table.go) always writes exactly this key set
// (rankingTableTopDrivers=2, so only driver_1_*/driver_2_* ever appear;
// *_threshold_label is present only when that driver claimed one).
// Rendering in a fixed order, not map order, keeps this view byte-stable
// across identical inputs.
var rankingTableFieldOrder = []string{
	"team_canonical_id", "team_label", "attention_rank", "outcome", "score", "window",
	"driver_1_signal", "driver_1_value", "driver_1_weight_contributed", "driver_1_threshold_label",
	"driver_2_signal", "driver_2_value", "driver_2_weight_contributed", "driver_2_threshold_label",
}

// rankingTableRowLine renders one Rows-panel row as a single compact line.
// See the "## Rows" call site's own comment for why every field is
// untrusted-marked regardless of its scalar variant.
func rankingTableRowLine(row contractsv1.ContextFabricClaimedFactRow) string {
	parts := make([]string, 0, len(rankingTableFieldOrder))
	for _, key := range rankingTableFieldOrder {
		value, ok := row.Fields[key]
		if !ok {
			continue
		}
		parts = append(parts, safeInline(key)+"="+untrustedInline(scalarValueText(value)))
	}
	return "- " + strings.Join(parts, " ")
}

// temporalLines renders CHAOS-3781's temporal label as header lines, or
// nothing at all when the answer is about current state.
//
// Every value here is structural -- a closed-vocabulary axis, a closed
// grain, and RFC 3339 instants the service composed itself -- so these are
// inline-escaped rather than wrapped as untrusted data. No model authors
// any part of this label: what time an answer covers is a fact about which
// reads ran (see ContextFabricTemporalLabel's doc comment).
//
// Both the requested and the effective time are shown when they differ.
// Showing only the effective one would silently answer a different question
// than the caller asked; showing only the requested one would repeat the
// H6 defect, claiming coverage the sources could not give.
func temporalLines(label *contractsv1.ContextFabricTemporalLabel) []string {
	if label == nil {
		return nil
	}
	lines := []string{
		fmt.Sprintf("- Answers for: %s, %s", safeInline(string(label.Effective.Axis)), safeInline(describeTimeContext(label.Effective))),
	}
	if requested := describeTimeContext(label.Requested); requested != describeTimeContext(label.Effective) {
		lines = append(lines, fmt.Sprintf("- Requested: %s", safeInline(requested)))
	}
	lines = append(lines, fmt.Sprintf("- Time grain: %s", safeInline(string(label.Grain))))
	if !label.CoverageComplete {
		// Stated as a fact, not a hedge: at least one source told the
		// service it cannot speak for this time, and its own limitation
		// is listed further down.
		lines = append(lines, "- At least one source could not answer for this time; see the limitations.")
	}
	return lines
}

// windowBoundsText renders an effective evidence window's own bounds
// structurally (CHAOS-3900 W2) -- RelativeID/Start/End are all
// server-computed, closed-vocabulary or RFC3339 instants, never model- or
// source-derived text, so this is inline-escaped like describeTimeContext
// rather than wrapped as untrusted data.
func windowBoundsText(w *contractsv1.ContextFabricEffectiveEvidenceWindow) string {
	if w.RelativeID == contractsv1.ContextFabricRelativeWindowAllTime {
		return "all time"
	}
	if w.Start == nil || w.End == nil {
		return "unstated window"
	}
	return w.Start.UTC().Format(time.RFC3339) + " to " + w.End.UTC().Format(time.RFC3339)
}

// phrasingSuffix (CHAOS-4171 PR2) renders an option's optional Phrasing
// alongside its structural Label, when phrasing was applied -- empty when
// it was not (fail-open to structure never removes the Label line above,
// this only ever ADDS to it). Phrasing is genuine model output, wrapped in
// the SAME untrustedInline discipline as every other model- or
// source-derived value on this line.
func phrasingSuffix(phrasing string) string {
	if phrasing == "" {
		return ""
	}
	return fmt.Sprintf(" (phrasing: %s)", untrustedInline(phrasing))
}

// describeTimeContext renders the instants of one time context. The axis
// decides which fields exist, and the contract already validated that
// pairing, so a missing pointer here means the caller passed something the
// contract would have rejected -- reported as such rather than rendered as
// a plausible-looking blank.
func describeTimeContext(timeContext contractsv1.ContextFabricTimeContext) string {
	switch timeContext.Axis {
	case contractsv1.ContextFabricTemporalRange:
		if timeContext.Start == nil || timeContext.End == nil {
			return "unstated range"
		}
		return timeContext.Start.UTC().Format(time.RFC3339) + " to " + timeContext.End.UTC().Format(time.RFC3339)
	case contractsv1.ContextFabricTemporalValidTime, contractsv1.ContextFabricTemporalObservedTime:
		if timeContext.AsOf == nil {
			return "unstated instant"
		}
		return "as of " + timeContext.AsOf.UTC().Format(time.RFC3339)
	default:
		return "current"
	}
}
