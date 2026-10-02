package genkitruntime

import (
	"fmt"
	"strings"
)

// The goal, emphasis, dimension, judgment-kind, clarification and follow-up
// sections of interpretationSystemPrompt. Each is its own constant or table
// so a later rule family adds a section beside it; prompts.go splices them in.

// interpretationGoalCueRules states which goals a question's wording takes.
// The temporal consequences of these goals live in interpretationTemporalRules.
const interpretationGoalCueRules = `Goal cues:
- An open "what changed", "how is X different" or "has X improved", asked with no "why" and naming no measure to follow over a span, is assess_state. When the question names one measure and a span, the movement cue below wins.
- explain_change needs an explicit "why" about a movement that the question, or the turn it refers to, states (slowed, slower than last month, rose, fell, gone up, dropped).
- A "why" about a present condition (stuck, blocked, behind, slow, struggling) is explain_drivers. When a ranking and a "why" share one condition (a ranking by "furthest behind" that also asks why), the ranked condition is a state: rank_or_survey and explain_drivers.
- A ranking by how much something improved or dropped compared with before, with no "why": rank_or_survey.
- The state of each member of a set, with no order word (an "each" question): assess_state, not rank_or_survey.
- A question that asks for an effort split ("where did X's time go", "how was X's effort split", "support versus features"): allocate_investment. The trend of an effort split over a window: allocate_investment and describe_trend. A "why" about an effort split (what causes the split): explain_drivers alone, without allocate_investment; the split is given, not asked. A follow-up that adds a comparison after an effort-split question: compare alone, without allocate_investment.
- How one named measure (a duration, a rate or a count) moved over a stated span or over time is describe_trend. A count that is the measure being trended does not add count_or_aggregate. A second goal joins in these two cases only: the trend of one measure for two or more named subjects shown together ("side by side", "next to") takes describe_trend and compare; the trend of an effort split takes allocate_investment and describe_trend. An open "what changed", or a comparison of two points ("since last month", "has X improved"), stays assess_state.`

// interpretationJudgmentKindCues replaces the older judgment-kind sentence:
// the cues say which basis a ranking or comparison has.
const interpretationJudgmentKindCues = `requested_judgment_kind cues. Emit it only when the current question itself ranks or compares subjects, by these cues:
- attention: needs attention or support, "look at first", "worry about first"; struggling, "hardest time", "worst shape"; pressure, load, strain, burden.
- performance: performed or performing best or worst, "best on", "worst on", "doing better", productive; an output measure: shipped, throughput, delivery pace, speed or flow, faster, turnaround, resolved, commits. A descriptive comparison ("side by side", "next to") of an output measure is also performance.
- A cue word alone never emits it. A question that does not rank or compare subjects leaves it unset, also when it names a cue word: a question that asks only for a state, a driver (a "why"), a trend, a count or the state of each member. A question that picks subjects by a cue condition ("which teams are struggling") counts as a ranking by that condition. A follow-up emits it only when its own words rank or compare subjects by a cue; it never keeps the kind of an earlier turn.
- Omit it for every other basis: risk, failures, failed or failing checks, flakiness, incident or rollback counts, health, blocked, ahead or behind, slipped, "waiting longest". Omit it for a basis that only an earlier turn names. Omission is always safer than a guess: a wrong pick can make an honest answer read as a refusal. This never changes the fact_requirements you choose or how you word requested_judgment itself.`

// interpretationEmphasisWords are the word lists behind question_frame.emphasis,
// as data so the test reads the same table the prompt renders.
var interpretationEmphasisWords = []struct {
	Emphasis string
	Words    string
}{
	{"negative_outliers", `worst, most at risk, most strained, struggling the most, slowest, heaviest load, most incidents or failures, most behind, needs attention first`},
	{"positive_outliers", `best, fastest, healthiest, most productive, resolved the most, most commits, deployed the most`},
}

var interpretationEmphasisRules = func() string {
	var b strings.Builder
	b.WriteString("question_frame.emphasis is OPTIONAL, a list from this closed set: %s. It says which ends of a ranking the answer must speak to and never adds new evidence. A ranking that asks for one end names that end:")
	for _, e := range interpretationEmphasisWords {
		fmt.Fprintf(&b, "\n- %s for a word like: %s.", e.Emphasis, e.Words)
	}
	b.WriteString("\n- Both values when both ends are asked (\"who is doing best and who is struggling\").\n- No emphasis for a plain filter (which members are in a condition, with no end word) or for \"each\".")
	return b.String()
}()

// interpretationDimensionWords is the closed word list per dimension, as data.
// A word not listed here emits nothing.
var interpretationDimensionWords = []struct {
	Dimension string
	Words     string
}{
	{"execution_completion", `completion is named ("actually done", "delivered", "finished"). The verb "closed" on work items ("closed last week") is not a completion word: it emits no dimension`},
	{"delivery_flow", `flow, pace, speed, fast, faster, fastest, slow, slower, slowest, slowing, cycle time, throughput`},
	{"reliability_and_release", `reliability or release is named, or the question asks whether a subject is ready to release ("ready to ship", "ready for its next release", "go live", "ship today"). Not for readiness for other work, not for deployment frequency or incident counts alone, not for the reliability of data`},
	{"review_and_ci_pressure", `review, CI, checks or builds, for their timing, load, pressure or failures. Not for the state of a review (a pull request that is approved, has changes requested, or has no review yet)`},
	{"cognitive_workload_pressure", `workload, on-call, burden, overload, or load on a team or the organization. Not repository load`},
	{"investment_balance", `the split, share or balance of effort across kinds of work (features, maintenance, support, tech debt, unplanned work). Not for an effort split across subjects (projects, teams); that question still takes the goal allocate_investment`},
	{"dependencies_and_blockers", `"stuck", "holding up", "blocked by", a bare "blocked" or "blocking", "holding X back", or dependencies named`},
	{"data_trust", `the trust or reliability of data is named`},
	{"code_ownership_risk", `code ownership risk is named`},
}

var interpretationDimensionRules = func() string {
	var b strings.Builder
	b.WriteString("question_frame.dimensions is OPTIONAL, a list from this closed set: %s. Naming a dimension only ever ADDS to what the answer covers; it never narrows it, so do not emit one to focus the answer. Only the words listed here emit a dimension. A synonym never does (\"overwhelmed\", \"capacity\", \"turnaround\", \"on track to finish\", \"closed\", \"bottleneck\", \"moving smoothly\"). A listed word always emits its dimension unless it is excluded here.")
	for _, d := range interpretationDimensionWords {
		fmt.Fprintf(&b, "\n- %s: %s.", d.Dimension, d.Words)
	}
	b.WriteString("\n- \"review load\" on a team: review_and_ci_pressure and cognitive_workload_pressure. \"on-call load\": cognitive_workload_pressure only, never reliability_and_release.")
	return b.String()
}()

// interpretationClarificationRule bounds when a question needs a clarification.
const interpretationClarificationRule = `Clarification: a bare "why?" with no referent in the question or in its conversation turns sets clarification_needed true, with a clarification_reason that asks the user to be more specific; topic suggestions are optional. Otherwise use clarification only when materially different authorized subjects or timeframes remain plausible and proceeding would make the answer unreliable.`

// interpretationFollowUpRules states what a follow-up carries from the prior turn.
const interpretationFollowUpRules = `Follow-ups in a conversation:
- A follow-up that refers back to a subject restates that subject as the first term, copied from the text of the turn that names it. Use nothing that the request does not carry.
- A dimension that only an earlier turn names is not emitted, with one exception: a follow-up that keeps the prior turn's goal and facts and changes only the subject, adds a comparison subject, or changes only the window keeps the prior turn's dimensions. A window-only follow-up also keeps the prior turn's emphasis. A follow-up that changes the goal does not carry the dimension.
- A "why?" follow-up takes the fact kinds of the topic that its referent turn names.
- A follow-up about the same set as the prior turn restates that set by its definition: the same anchor_terms, member_kind and member_qualifier.`
