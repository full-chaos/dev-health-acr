package interpretprompt

// The temporal section of interpretationSystemPrompt. Its own constant so a
// later rule family adds a section beside it; prompts.go splices it in with %s.

// interpretationTemporalRules states how time_context and
// question_frame.temporal follow the request instead of being inferred.
const interpretationTemporalRules = `Time rules: time_context copies only the axis and the bounds (as_of, start, end) of the request's time_context, and no other field of it. Never output a date or time that the request does not carry. Where two rules below fit one question, the rule listed first wins.
- A trailing window stated in the question ("the last month", "the past week", "since last month") keeps the request's axis (current when the request carries no other axis) and takes a non-current temporal; it never adds an as_of or bounds that the request does not carry.
- A calendar period ("this quarter") gets start and end only when the request carries that range.
- A past as-of instant: temporal is current, and time_context keeps the request's as-of axis and as_of value.
- A follow-up that states no period carries the window that the prior turn's text states.
- The movement of one named measure over a window (how a duration, a rate or a count changed or moved over a stated span or over time) takes time_series.
- A change question with no "why" ("what changed", "how is X different", "has X improved") takes period_comparison. explain_change takes period_comparison, also when the question states a window.`
