package sidecar

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RenderAnswerProjectionMarkdown renders a bounded answer projection as
// markdown for a display-oriented MCP client.
//
// The text opens with ONE untrusted-data notice. Every model-authored or
// source-derived string -- the judgment, driver prose, labels, cohort
// rationale, limitations -- appears only inside a fenced block (one block per
// section), never as plain markdown. An investigation answer is composed
// partly from approved documents, issue text, and episodes, which are data
// and never instructions. Headings and labels the server writes itself are
// plain text.
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
	b.writeLine(untrustedNoticeLine)
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

	// section writes a blank line, a plain server-written heading, then one
	// fenced block holding every hosted line of the section.
	section := func(heading string, lines []string) bool {
		b.writeLine("")
		if !b.writeLine(heading) {
			return false
		}
		return b.writeBlock(lines)
	}

	if len(projection.CommittedSubjects) > 0 {
		lines := make([]string, 0, len(projection.CommittedSubjects))
		for _, subject := range projection.CommittedSubjects {
			lines = append(lines, fmt.Sprintf("- %s %s", hostedInline(kindWords(subject.Kind)), hostedInline(subject.Label)))
		}
		if !section("## Subjects", lines) {
			return b.finishWithTruncation()
		}
	}

	if len(projection.InactiveHints) > 0 {
		lines := make([]string, 0, len(projection.InactiveHints))
		for _, hint := range projection.InactiveHints {
			line := fmt.Sprintf("- team %s is inactive; no data was read for it", hostedInline(hint.Subject.Label))
			if hint.ActiveCanonicalID != "" {
				line += fmt.Sprintf("; an active team of the same name is %s", hostedInline(hint.ActiveCanonicalID))
			}
			lines = append(lines, line)
		}
		if !section("## Inactive teams", lines) {
			return b.finishWithTruncation()
		}
	}

	if projection.DirectJudgment != "" {
		if !section("## Judgment", []string{projection.DirectJudgment}) {
			return b.finishWithTruncation()
		}
	}
	if projection.CurrentState != "" {
		if !section("## Current state", []string{projection.CurrentState}) {
			return b.finishWithTruncation()
		}
	}
	if projection.DeterministicAnswer != "" {
		if !section("## Server-computed answer ("+serverComputedNote+")", []string{projection.DeterministicAnswer}) {
			return b.finishWithTruncation()
		}
	}

	if len(projection.StrongestPressures) > 0 {
		if !section("## Strongest pressures", joinBlankSeparated(projection.StrongestPressures)) {
			return b.finishWithTruncation()
		}
	}

	// A clarification request is the answer when the engine could not
	// settle the subject, so it renders before the drivers rather than as
	// a footnote a reader might miss.
	if projection.Clarification != nil {
		var lines []string
		if projection.Clarification.Prompt != "" {
			lines = append(lines, projection.Clarification.Prompt)
		}
		for _, candidate := range projection.Clarification.Candidates {
			line := fmt.Sprintf("- %s %s (receipt %s, confidence %.2f)",
				hostedInline(kindWords(candidate.Subject.Kind)), hostedInline(candidate.Subject.Label),
				hostedInline(candidate.ReceiptID), candidate.Confidence)
			if candidate.Provider != "" {
				line += fmt.Sprintf(" provider %s", hostedInline(candidate.Provider))
			}
			lines = append(lines, line)
		}
		if !section("## Clarification needed", lines) {
			return b.finishWithTruncation()
		}
	}

	if len(projection.PrincipalDrivers) > 0 {
		var lines []string
		for i, driver := range projection.PrincipalDrivers {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, fmt.Sprintf("%d. %s [%s / %s]", i+1,
				hostedInline(driver.Title), hostedInline(strings.ReplaceAll(string(driver.Standing), "_", " ")), hostedInline(strings.ReplaceAll(driver.Category, "_", " "))))
			lines = append(lines, driver.Summary)
			if driver.Qualification != "" {
				lines = append(lines, driver.Qualification)
			}
			if len(driver.EvidenceRefIDs) > 0 {
				lines = append(lines, "- Evidence: "+hostedInline(strings.Join(driver.EvidenceRefIDs, ", ")))
			}
			// AffectedSubjects: which cohort member(s) this driver
			// judgment ties back to.
			if len(driver.AffectedSubjects) > 0 {
				subjects := make([]string, len(driver.AffectedSubjects))
				for j, subject := range driver.AffectedSubjects {
					subjects[j] = fmt.Sprintf("%s %s (%s)", hostedInline(string(subject.Kind)), hostedInline(subject.CanonicalID), hostedInline(subject.Label))
				}
				lines = append(lines, "- Affected: "+strings.Join(subjects, ", "))
			}
		}
		if !section("## Drivers", lines) {
			return b.finishWithTruncation()
		}
	}

	displayedFacts := make(map[string]struct{})
	if projection.Cohort != nil {
		size := strconv.Itoa(projection.Cohort.Total)
		if projection.Cohort.Population > projection.Cohort.Total || projection.Cohort.PopulationLowerBound {
			size = strconv.Itoa(max(projection.Cohort.Population, projection.Cohort.Total))
			if projection.Cohort.PopulationLowerBound {
				size = "at least " + size
			}
		}
		lines := []string{projection.Cohort.Rationale}
		plainWorkItems := projection.Cohort.Kind == contractsv1.ContextFabricSubjectWorkItem
		for _, member := range projection.Cohort.Members {
			if member.RankingComputed {
				plainWorkItems = false
			}
		}
		heading := fmt.Sprintf("## Cohort (%s of %s shown)", strconv.Itoa(len(projection.Cohort.Members)), size)
		for _, member := range projection.Cohort.Members {
			if plainWorkItems {
				values := []string{}
				for _, fact := range projection.KeyFacts {
					if fact.Subject.Kind != member.Subject.Kind || fact.Subject.CanonicalID != member.Subject.CanonicalID {
						continue
					}
					if fact.Kind == contractsv1.ContextFabricFactStatus {
						if text, shown := scalarFieldText(fact.Field, fact.Value); shown {
							values = append(values, hostedInline(text))
						}
						displayedFacts[fact.ClaimID] = struct{}{}
					}
					// The source title is already the member label. Do not print
					// the identical work/title scalar as a second repeated line.
					if fact.Kind == contractsv1.ContextFabricFactWork && fact.Field == "title" && fact.Value.String != nil && *fact.Value.String == member.Subject.Label {
						displayedFacts[fact.ClaimID] = struct{}{}
					}
				}
				status := "No status evidence in this answer"
				if len(values) > 0 {
					status = strings.Join(values, "; ")
				}
				lines = append(lines, "- Work item: "+hostedInline(member.Subject.Label)+" — Status: "+status)
				continue
			}
			lines = append(lines, fmt.Sprintf("%s. %s %s", strconv.Itoa(member.Rank),
				hostedInline(kindWords(member.Subject.Kind)), hostedInline(member.Subject.Label)))
			for _, reason := range member.InclusionReasons {
				lines = append(lines, "   - "+hostedInline(reason))
			}
		}
		b.writeLine("")
		if !b.writeLine(heading) {
			return b.finishWithTruncation()
		}
		if plainWorkItems && !b.writeLine("### Work item / Status") {
			return b.finishWithTruncation()
		}
		if !b.writeBlock(lines) {
			return b.finishWithTruncation()
		}
		// RankingTable: the Rows-panel view, one line per RankingComputed
		// member in AttentionRank order. Field names are fixed Go string
		// literals (rankingTableFieldOrder), never wire data; every value
		// is hosted text.
		if len(projection.Cohort.RankingTable) > 0 {
			rows := make([]string, 0, len(projection.Cohort.RankingTable))
			for _, row := range projection.Cohort.RankingTable {
				rows = append(rows, rankingTableRowLine(row))
			}
			if !section("## Rows", rows) {
				return b.finishWithTruncation()
			}
		}
	}

	if len(projection.KeyFacts) > 0 {
		var lines []string
		for _, fact := range projection.KeyFacts {
			if _, shown := displayedFacts[fact.ClaimID]; shown {
				continue
			}
			if basis := answerprojection.WorkItemCountQualification(projection, fact); basis != "absent" {
				line := "- Recorded work-item count: " + strconv.FormatInt(*fact.Value.Integer, 10) + ". This view does not establish whether the population count is exact."
				if basis == "floor" {
					line = "- Work-item count: at least " + strconv.FormatInt(*fact.Value.Integer, 10) + "."
				}
				lines = append(lines, line)
				continue
			}
			display, shown := scalarFieldText(fact.Field, fact.Value)
			if !shown {
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: %s (%s, %s)",
				hostedInline(contractsv1.ContextFabricFactFieldNoun(fact.Field)), hostedInline(display),
				hostedInline(fact.Subject.Label), hostedInline(strings.ReplaceAll(string(fact.Kind), "_", " "))))
		}
		if !section("## Canonical facts", lines) {
			return b.finishWithTruncation()
		}
	}

	// Coverage and limitations render even when the answer is long. A
	// reader who cannot see that a source was missing cannot judge what
	// the answer is worth.
	if len(projection.CoverageSummary) > 0 {
		lines := make([]string, 0, len(projection.CoverageSummary))
		for _, entry := range projection.CoverageSummary {
			line := fmt.Sprintf("- %s: %s", hostedInline(entry.Source), hostedInline(string(entry.State)))
			if entry.Reason != "" {
				line += " (" + hostedInline(entry.Reason) + ")"
			}
			lines = append(lines, line)
		}
		if !section("## Source coverage", lines) {
			return b.finishWithTruncation()
		}
	}
	if len(projection.Limitations) > 0 {
		if !section("## Limitations", joinBlankSeparated(projection.Limitations)) {
			return b.finishWithTruncation()
		}
	}

	// Warnings render alongside limitations: an answer must never carry a
	// warning the reader did not see.
	if len(projection.Warnings) > 0 {
		if !section("## Warnings", joinBlankSeparated(projection.Warnings)) {
			return b.finishWithTruncation()
		}
	}

	// Structural values (member/kind enums, receipt/option ids, pattern ids)
	// and every server-rendered label, handle value span and source column
	// name share one fenced block per section.
	if we := projection.EffectiveEvidenceWindow; we != nil {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("- Evidence window: %s (%s)", safeInline(windowBoundsText(we)), safeInline(string(we.Provenance)))) {
			return b.finishWithTruncation()
		}
	}
	if needs := projection.StructureNeeds; needs != nil {
		// windowConfirmationRequiredResult composes StructureNeeds' window
		// member (Missing=[window], WindowOptions) and the legacy
		// WindowClarification field in lockstep, from the identical option
		// set -- rendering both would show every window option twice. The
		// "## Window options" section below stays canonical; this block
		// skips the window member whenever WindowClarification is present.
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
			var lines []string
			for _, member := range missing {
				lines = append(lines, "- Missing: "+hostedInline(string(member)))
			}
			for _, opt := range needs.KindOptions {
				lines = append(lines, fmt.Sprintf("- Kind option %s (receipt %s): %s%s", hostedInline(string(opt.Kind)), hostedInline(opt.ReceiptID), hostedInline(opt.Label), phrasingSuffix(opt.Phrasing)))
			}
			for _, opt := range needs.AnchorOptions {
				lines = append(lines, fmt.Sprintf("- Anchor option %s (receipt %s): %s%s", hostedInline(string(opt.Kind)), hostedInline(opt.ReceiptID), hostedInline(opt.Label), phrasingSuffix(opt.Phrasing)))
			}
			for _, opt := range needs.HandleOptions {
				lines = append(lines, fmt.Sprintf("- Handle option %s/%s (receipt %s): %s = %s (source %s)%s",
					hostedInline(string(opt.Kind)), hostedInline(opt.PatternID), hostedInline(opt.ReceiptID),
					hostedInline(opt.Label), hostedInline(opt.Value), hostedInline(opt.SourceColumn), phrasingSuffix(opt.Phrasing)))
			}
			for _, opt := range needs.CandidateOptions {
				lines = append(lines, fmt.Sprintf("- Candidate option %s/%s (receipt %s): %s%s", hostedInline(string(opt.Kind)), hostedInline(opt.CanonicalID), hostedInline(opt.ReceiptID), hostedInline(opt.Label), phrasingSuffix(opt.Phrasing)))
			}
			for _, opt := range windowOptions {
				lines = append(lines, fmt.Sprintf("- Window option (receipt %s): %s", hostedInline(opt.ReceiptID), hostedInline(opt.Label)))
			}
			if !section("## Structure needed", lines) {
				return b.finishWithTruncation()
			}
		}
	}
	if wc := projection.WindowClarification; wc != nil {
		lines := make([]string, 0, len(wc.Options))
		for _, opt := range wc.Options {
			lines = append(lines, fmt.Sprintf("- (receipt %s): %s", hostedInline(opt.ReceiptID), hostedInline(opt.Label)))
		}
		if !section("## Window options", lines) {
			return b.finishWithTruncation()
		}
	}
	if len(projection.ConfirmedStructure) > 0 {
		lines := make([]string, 0, len(projection.ConfirmedStructure))
		for _, entry := range projection.ConfirmedStructure {
			lines = append(lines, fmt.Sprintf("- %s = %s (%s, %s)", hostedInline(string(entry.Member)), hostedInline(entry.AppliedValue),
				hostedInline(string(entry.Source)), hostedInline(string(entry.Provenance))))
		}
		if !section("## Structure confirmed", lines) {
			return b.finishWithTruncation()
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

// serverComputedNote says what the deterministic answer is: text the service
// composed from stored facts, as opposed to the model's own wording.
const serverComputedNote = "computed by the server from stored facts, not written by a model"

// untrustedNoticeLine is the single notice a rendering carries. Hosted text
// appears only inside the fenced blocks below it.
const untrustedNoticeLine = "> **" + untrustedDataHeader + ".** Hosted text appears only inside fenced blocks."

// hostedInline makes one hosted (or structural) value safe to put on a
// single line inside a fenced block: line breaks collapse and control/bidi
// runes are escaped, so a value can neither start a new line nor end the
// fence (the fence is longer than any backtick run in the block).
func hostedInline(value string) string {
	return sanitizeInlineContent(value)
}

// joinBlankSeparated places items one after another inside one block, with a
// blank line between them.
func joinBlankSeparated(items []string) []string {
	out := make([]string, 0, len(items)*2)
	for i, item := range items {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, item)
	}
	return out
}

// writeBlock writes lines as one blockquoted fenced block. The fence is
// longer than any backtick run in the content. Lines are added while they
// fit; the closing fence is always written, and room for the truncation
// notice stays reserved, so a fence is never left open. It reports false
// when anything was dropped.
func (b *boundedBuilder) writeBlock(lines []string) bool {
	if b.truncated {
		return false
	}
	sanitized := sanitizeUntrustedContent(strings.Join(lines, "\n"))
	fence := fenceFor(sanitized)
	open := "> " + fence + "\n"
	reserve := len(open) + len(truncationNotice)
	if b.buf.Len()+len(open)+reserve > b.maxBytes {
		b.truncated = true
		return false
	}
	b.buf.WriteString(open)
	complete := true
	for _, line := range strings.Split(sanitized, "\n") {
		piece := "> " + line + "\n"
		if b.buf.Len()+len(piece)+reserve > b.maxBytes {
			b.truncated = true
			complete = false
			break
		}
		b.buf.WriteString(piece)
	}
	b.buf.WriteString(open)
	return complete
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
	b.writeLine(untrustedNoticeLine)
	b.writeLine(fmt.Sprintf("- Status: %s", safeInline(string(result.Status))))
	b.writeLine(fmt.Sprintf("- Result ID: %s", safeInline(result.ResultID)))
	b.writeLine(fmt.Sprintf("- Drivers: %s", strconv.Itoa(len(result.Drivers))))
	b.writeLine(fmt.Sprintf("- Claimed facts: %s", strconv.Itoa(len(result.ClaimedFacts))))
	if result.Coverage.Partial {
		b.writeLine("- Coverage: partial")
	}
	if result.DirectJudgment != "" {
		b.writeLine("")
		if !b.writeLine("## Judgment") {
			return b.finishWithTruncation()
		}
		if !b.writeBlock([]string{result.DirectJudgment}) {
			return b.finishWithTruncation()
		}
	}
	if result.DeterministicAnswer != "" {
		b.writeLine("")
		if !b.writeLine("## Server-computed answer (" + serverComputedNote + ")") {
			return b.finishWithTruncation()
		}
		if !b.writeBlock([]string{result.DeterministicAnswer}) {
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
		return contractsv1.ContextFabricDisplayNumber(*value.Number)
	case value.Boolean != nil:
		return strconv.FormatBool(*value.Boolean)
	case value.Null:
		return "null"
	default:
		return "unknown"
	}
}

// kindWords renders a subject kind as words: the display noun, or the token
// with underscores as spaces for a kind outside the vocabulary.
func kindWords(kind contractsv1.ContextFabricSubjectKind) string {
	if noun := contractsv1.ContextFabricSubjectKindNoun(kind, 1); noun != "" {
		return noun
	}
	return strings.ReplaceAll(string(kind), "_", " ")
}

// scalarFieldText renders a claimed fact value of a named field for display:
// a token reads as words, and shown is false for a field that stays out of
// sentences.
func scalarFieldText(field string, value contractsv1.ContextFabricScalarValue) (text string, shown bool) {
	if value.String != nil {
		if strings.HasSuffix(field, "_signal") {
			if name, ok := contractsv1.ContextFabricCohortSignalNoun(*value.String); ok {
				return name, true
			}
		}
		return contractsv1.ContextFabricDisplayString(field, *value.String)
	}
	return scalarValueText(value), true
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
		display, shown := scalarFieldText(key, value)
		if !shown {
			continue
		}
		parts = append(parts, contractsv1.ContextFabricFactFieldNoun(key)+": "+hostedInline(display))
	}
	var extra []string
	for key := range row.Fields {
		if !slices.Contains(rankingTableFieldOrder, key) {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		display, shown := scalarFieldText(key, row.Fields[key])
		if !shown {
			continue
		}
		parts = append(parts, hostedInline(contractsv1.ContextFabricFactFieldNoun(key))+": "+hostedInline(display))
	}
	return "- " + strings.Join(parts, "; ")
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
// the same fenced-block discipline as every other model- or
// source-derived value.
func phrasingSuffix(phrasing string) string {
	if phrasing == "" {
		return ""
	}
	return fmt.Sprintf(" (phrasing: %s)", hostedInline(phrasing))
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
