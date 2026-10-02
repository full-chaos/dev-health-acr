package genkitruntime

// The fact-kind sections of interpretationSystemPrompt. Each section is its
// own constant so a later rule family adds a section beside these instead of
// rewriting them; prompts.go splices them in with %s.

// interpretationFactKindGlossary states what each kind in the closed set
// holds, which subject kinds it serves, and what it is not. Kind names read
// by their everyday meaning pick the wrong kind, so every trap is explicit.
const interpretationFactKindGlossary = `Fact kinds: what each holds, the subject kinds it serves, and what it is not.
- identity: a repository's id, name and provider, or a work item's id and title (repository, work_item). Not the identity of a team, a project or a person.
- membership: the organization that a repository belongs to, or the repository that a work item belongs to (repository, work_item). Not team membership and not project membership.
- status: the status column of one work item: backlog, todo, in progress, done, canceled or unknown (work_item). Not completion.
- actual_completion: whether and when a work item was completed; for a project, the count and ratio of its completed work items, with canceled items left out (work_item, project). Not deployment completion.
- work: the title of one work item and nothing else (work_item). Not its type, priority, size or description.
- blockers: the other work items that block a work item; for a team, its blocked-item counts (work_item, team). Only "blocks" links, not every dependency.
- required_children: every other dependency that a work item requires (work_item). Not only parent-to-child links.
- pull_requests: the state of one pull request, a merged one reading merged, not closed; for a team, its pull request counts (pull_request, team). Not a review state, CI state or timing, and not a repository fact.
- reviews: the state of one review (pull_request_review). Not the pull request and not review speed.
- continuous_integration: the status of one CI run; for a repository, the latest day's pipeline count, success rate, duration and queue time (ci_pipeline_run, repository). Not deployment status and not a series.
- deployments: the status and environment of one deployment; for a repository, the latest day's deployment count, failed count, deploy time and lead time, which is also its release-readiness signal; for a team, its deployment counts (deployment, repository, team). Not readiness.
- incidents: the status and severity of one incident; for a team, its incident counts (incident, team). Not a repository fact.
- metrics: precomputed delivery metrics. For a repository: daily commits, merged pull requests, pull request cycle time, change failure rate, time to restore, bus factor and code ownership concentration. For a team or a project: commit counts and after-hours and weekend commit ratios only (repository, team, project).
- health: a compounding-risk score and its severity band (repository, team, project). Not a general health verdict.
- workload: a team's capacity forecast: throughput, backlog size and forecast days (team, project). Not a person's workload and not a load or work-in-progress count.
- investment: the share of work in each of the five canonical themes (team, project, repository). Not effort, spend or time tracking of people.
- readiness: backlog estimate coverage, that is how much of the backlog has an estimate (team, project). Not release, ship or delivery readiness.
- operational_deficiencies: the rules already marked as fired for a team, each with severity, title and rationale (team). Not a diagnosis, and not a score.
- source_health: the outcome of the data-sync jobs per provider (organization). Not the health of a team, a repository or code.
- evidence: holds nothing and has no provider. It is not the evidence references that other facts carry. It is in the closed set only so the vocabulary stays complete: never list it.
- flow: delivery-flow signals. For a team or a project: items started and completed, work in progress and its age, cycle and lead time, bug share and story points completed. For a repository: pull request pickup and review timing (team, project, repository). Not flow efficiency.
- landscape: aggregated statistics of a team's work areas: churn, cycle time and work in progress against throughput (team, project). Not a per-person list and not a ranking.
A team or project question also reaches the work-item kinds (status, work, actual_completion, blockers, required_children, identity, membership) through its work items, and pull_requests and reviews through its activity. A team also has its own rollups of blockers, pull_requests, incidents and deployments. continuous_integration and source_health are not reached from a team or a project; operational_deficiencies is not reached from a project.
These lines say what a kind means. They do not decide which kinds to list: list the kinds that the question's words name by the fact_requirements rules, also when a kind is not served for that kind of subject.`

// interpretationFactRequirementRules states how the question's words select
// the kinds to list. It replaces the earlier instruction to infer the
// families that may be needed.
const interpretationFactRequirementRules = `fact_requirements rules: list the kinds that the question's words name, and no others. Never add a kind only because it might explain a cause (metrics, operational_deficiencies, deployments and so on) when no word names it. Kinds come from the topic, not from the population noun. Keep a kind the words name even when it may not be served for that kind of subject. Never list evidence. The one fixed exception is the burden composite below, which is listed whole.
- Meanings: status is the work-item status column and is not completion (completion is actual_completion). readiness is backlog estimate coverage, not release readiness. membership is repository containment, not team membership. reviews is the state of one review, never review time; review timing is flow. work is the work-item title only and is never a stand-in for another kind.
- A count, or an attribute that no kind holds (an assignee, a creation date), gets no kind for it. A counted noun that is itself a kind ("how many incidents", "how many deployments", "how many pull requests") takes that kind. A counted noun that is not a kind (repositories in the organization) takes none.
- A condition tied to one activity takes that activity's kinds: "review load" is pull_requests, reviews.
- "stuck", "holding up", "blocked by", a bare "blocked" or "blocking", "holding X back": blockers. A blocking word together with a state word about work items ("stuck in a state", "blocked and in progress"): status and blockers. "stuck in review": blockers, pull_requests, reviews.
- An unqualified burden condition on a team, a project, a repository or the organization ("struggling", "overburdened", "strained", "under pressure", "needs attention", "needs support", "hardest time", "worst shape"), and a general state question with no topic word ("how is X doing", "how is X going", "operating condition"): the burden composite, exactly flow, health, incidents, investment, workload.
- "needs attention" on work items or pull requests: blockers, status.
- "ready to release" and the noun form "release readiness": actual_completion, deployments. Never readiness.
- A closing verb on work items within a window ("closed last week"): actual_completion.
- "on track", "on schedule", "behind", "delayed", "miss targets": actual_completion, status.
- "on-call load", "operational load": incidents. The word "load" never names workload.
- "code ownership risk" named: metrics. "maintenance attention": investment. "security risk": health. "build health": continuous_integration. "release reliability": deployments, incidents. A repository "failure rate": continuous_integration.
- An output measure with no kind word ("most productive", "performed best on delivery"): flow.
- A share of a named subject's members: actual_completion for completion, readiness for estimate coverage.
- A "why?" follow-up takes the fact kinds of the topic that its referent turn names.`
