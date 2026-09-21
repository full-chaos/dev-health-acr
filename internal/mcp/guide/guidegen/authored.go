package guidegen

// Authored text. Every map is keyed by a registry member. Build refuses to
// run when a registry member has no entry here, and when an entry here names
// no registry member, so the text cannot drift from the tables.

type familyText struct {
	Meaning string
	Example string
}

var familyTexts = map[string]familyText{
	"subject_investigation": {
		Meaning: "One named subject: its current state and the drivers behind it.",
		Example: "What is the status of the payments project, and why is it slipping?",
	},
	"discovered_cohort_ranking": {
		Meaning: "Many subjects of one kind, discovered by ACR and ranked, each with drivers.",
		Example: "Which teams need attention, and why?",
	},
	"scoped_cohort_status": {
		Meaning: "Many subjects of one kind, inside one named parent of a different kind.",
		Example: "What is the status of each project owned by the platform team?",
	},
	"grouped_cohort_status": {
		Meaning: "Many subjects partitioned by a grouping kind, with drivers per group.",
		Example: "Group the repositories by team and explain what drives each group.",
	},
	"explicit_comparison": {
		Meaning: "Two or more named subjects, compared on the same measures over the same window.",
		Example: "Compare the web team and the api team over the last 90 days.",
	},
	"trend": {
		Meaning: "How one measure moved over time for one subject.",
		Example: "How has cycle time moved for the payments project?",
	},
	"investment_allocation": {
		Meaning: "Where effort went, as a breakdown by investment theme.",
		Example: "Where did the platform team's effort go last quarter?",
	},
	"unclassified": {
		Meaning: "Fallback when the question shape cannot be established. ACR refuses to guess. It is not an error and not a question type to ask for.",
		Example: "How are things going?",
	},
}

var subjectAxisTexts = map[string]string{
	"none":            "no subject established",
	"one":             "one named subject",
	"many_named":      "several named subjects",
	"many_discovered": "many subjects that ACR discovers",
	"many_scoped":     "many subjects inside one named parent",
	"many_grouped":    "many subjects partitioned by a grouping kind",
}

var dimensionTexts = map[string]string{
	"execution_completion":        "execution and completion of work",
	"delivery_flow":               "delivery flow",
	"reliability_and_release":     "reliability and release",
	"review_and_ci_pressure":      "review and CI pressure",
	"code_ownership_risk":         "code ownership risk",
	"cognitive_workload_pressure": "cognitive workload pressure",
	"investment_balance":          "investment balance",
	"dependencies_and_blockers":   "dependencies and blockers",
	"data_trust":                  "data trust",
}

var subjectKindTexts = map[string]string{
	"organization":        "The whole organization; the root scope.",
	"team":                "A team. Team answers need synced project/repository ownership.",
	"project":             "A project that groups work items.",
	"repository":          "A source repository.",
	"work_item":           "One tracked issue or ticket.",
	"pull_request":        "One pull request or merge request.",
	"deployment":          "One deployment.",
	"incident":            "One operational incident.",
	"document":            "One document.",
	"decision":            "One recorded decision.",
	"episode":             "One agent-run evidence record. It is not durable truth.",
	"metric":              "One named metric.",
	"pull_request_review": "One review on a pull request.",
	"ci_pipeline_run":     "One CI pipeline run.",
	"work_item_ref":       "A stub for a linked work item that has no synced record. Never answerable.",
}

// grammarExamples pins one example question per registry pattern. Build
// tests execute each example through the real grammar (BindHandles) and
// require the named pattern to bind the named value.
type grammarExample struct {
	Question string
	Value    string
	Note     string
}

var grammarExamples = map[string]grammarExample{
	"pull_request_number": {
		Question: "How is PR 532 doing?",
		Value:    "532",
		Note:     "Also matches `pull request #532`.",
	},
	"work_item_ticket_key": {
		Question: "What is blocking CHAOS-123?",
		Value:    "CHAOS-123",
		Note:     "The registry knows only the `CHAOS-` key prefix today. An organization's own ticket-key prefix is org-specific data and does not bind through this pattern.",
	},
	"ci_run_id": {
		Question: "Why did run 18234567 fail?",
		Value:    "18234567",
		Note:     "Needs a keyword (`run`, `pipeline`, `CI run`, `CI pipeline`, `pipeline run`) and 4 or more digits.",
	},
}

var windowTexts = map[string]string{
	"trailing_30d":  "the last 30 days",
	"trailing_90d":  "the last 90 days",
	"trailing_365d": "the last 365 days",
	"all_time":      "no bound; send `start` and `end` absent",
}

var statusTexts = map[string]string{
	"complete":               "The investigation is complete. Still read `limitations` and coverage.",
	"partial":                "Some canonical or graph coverage was unavailable. The answer states what it could establish.",
	"degraded":               "Coverage was limited. Read `limitations`.",
	"clarification_required": "ACR needs one more input. Follow `structure_needs`; see `acr://guide/conversation`.",
	"no_match":               "ACR could not commit an answer as asked. Causes differ: no subject matched, the offered choices can no longer be redeemed, or a time bound could not be honored. Read `limitations` and the answer text, then rephrase with a named subject, handle, kind, or window.",
}

var renderKindTexts = map[string]string{
	"series":   "one or more named series",
	"table":    "a table",
	"quadrant": "a two-measure landscape",
	"treemap":  "a part-to-whole breakdown",
	"sunburst": "a hierarchical part-to-whole breakdown",
	"sankey":   "a flow between categories",
	"burndown": "remaining scope over time",
	"forecast": "a completion forecast",
}
