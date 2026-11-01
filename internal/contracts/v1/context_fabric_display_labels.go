package v1

import (
	"fmt"
	"strings"
)

// CHAOS-4690 item 4: deterministic display labels, shipped WITH the
// vocabulary. These registries are the contract-owned source of every
// terse label the engine stamps onto chips and coverage details, so a new
// vocabulary value cannot ship unlabeled ("unmapped" is unrepresentable):
// totality tests in context_fabric_display_labels_test.go fail CI when any
// member of a closed vocabulary here lacks a label.
//
// These are NOT the banned phrasing-table shape: they live in the model
// layer's own repo, beside the vocabulary they label, are
// totality-enforced, and carry terse identity/quantity phrases — the
// deterministic fail-closed floor. Sentences remain synthesis's job
// (ContextFabricCoverageDetail.Phrasing). Ticket item 4 names this
// mechanism verbatim; the settled design's sol review cleared it against
// the binding language principle (r2 dimension-1: "deterministic registry
// labels satisfy it").

// contextFabricFactKindLabels labels every member of the closed fact-kind
// vocabulary (contextFabricFactKinds).
var contextFabricFactKindLabels = map[ContextFabricFactKind]string{
	ContextFabricFactIdentity:                "identity",
	ContextFabricFactMembership:              "membership",
	ContextFabricFactStatus:                  "status",
	ContextFabricFactActualCompletion:        "completion",
	ContextFabricFactWork:                    "work",
	ContextFabricFactBlockers:                "blockers",
	ContextFabricFactRequiredChildren:        "required children",
	ContextFabricFactPullRequests:            "pull request",
	ContextFabricFactReviews:                 "review",
	ContextFabricFactContinuousIntegration:   "CI",
	ContextFabricFactDeployments:             "deployment",
	ContextFabricFactIncidents:               "incident",
	ContextFabricFactMetrics:                 "metrics",
	ContextFabricFactHealth:                  "health",
	ContextFabricFactWorkload:                "workload",
	ContextFabricFactInvestment:              "investment",
	ContextFabricFactReadiness:               "readiness",
	ContextFabricFactOperationalDeficiencies: "operational deficiency",
	ContextFabricFactSourceHealth:            "source health",
	ContextFabricFactEvidence:                "evidence",
	ContextFabricFactFlow:                    "flow",
	ContextFabricFactLandscape:               "landscape",
	ContextFabricFactSecurityAlerts:          "security alerts",
	ContextFabricFactCommitment:              "commitment",
	ContextFabricFactFileHotspots:            "file hotspots",
}

// contextFabricSourceStateLabels labels every member of the closed source
// state vocabulary. The words are ruled per row (CHAOS-12413): "no data" stays,
// because an empty read is only one of its three paths, so it must not read
// as zero.
var contextFabricSourceStateLabels = map[ContextFabricSourceState]string{
	ContextFabricSourceAvailable:     "available",
	ContextFabricSourceStale:         "may be out of date",
	ContextFabricSourceUnavailable:   "unavailable",
	ContextFabricSourceUnconfigured:  "not set up",
	ContextFabricSourceUnauthorized:  "not readable with your access",
	ContextFabricSourceNoData:        "no data",
	ContextFabricSourceTruncated:     "partly included",
	ContextFabricSourceConflicted:    "sources disagree",
	ContextFabricSourceNotApplicable: "not applicable",
	ContextFabricSourcePruned:        "not needed for this question",
}

// contextFabricEvidenceEntityLabels labels every member of the closed
// ContextFabricEvidenceEntityType vocabulary (CHAOS-4698) -- the producers
// in internal/contextpacket/source_queries.go and internal/contextfabric
// that mint an acr:v1:<entity-type>:<id> ref. TOTAL over that vocabulary,
// enforced by TestEvidenceEntityLabelsAreTotal: a new member cannot ship
// without joining this map in the SAME change, mirroring
// contextFabricFactKindLabels. A ref segment OUTSIDE this vocabulary can
// still appear at read time (a legacy stored row an acr producer did not
// mint) -- ContextFabricEvidenceRefLabel keeps parsing by raw string for
// that case and falls back to the generic "Evidence" label, countable via
// RecordEvidenceLabelFallback (count only, never the segment).
var contextFabricEvidenceEntityLabels = map[ContextFabricEvidenceEntityType]string{
	ContextFabricEvidenceEntityCommit:             "Commit",
	ContextFabricEvidenceEntityRepository:         "Repository",
	ContextFabricEvidenceEntityWorkItem:           "Work item",
	ContextFabricEvidenceEntityWorkItemDependency: "Work item dependency",
	ContextFabricEvidenceEntityCommitFile:         "Commit file",
	ContextFabricEvidenceEntityPullRequest:        "Pull request",
	ContextFabricEvidenceEntityReview:             "Review",
	ContextFabricEvidenceEntityCI:                 "CI run",
	ContextFabricEvidenceEntityGraph:              "Relationship",
	ContextFabricEvidenceEntityAIRun:              "AI workflow run",
	ContextFabricEvidenceEntityAIArtifact:         "AI artifact",
	ContextFabricEvidenceEntityReviewOutcome:      "Review outcome",
	ContextFabricEvidenceEntityDeployment:         "Deployment",
	ContextFabricEvidenceEntityIncident:           "Incident",
	ContextFabricEvidenceEntityDeploymentIncident: "Deployment/incident link",
	ContextFabricEvidenceEntityHotspot:            "File hotspot",
	ContextFabricEvidenceEntityComplexity:         "File complexity",
	ContextFabricEvidenceEntityTeam:               "Team",
	ContextFabricEvidenceEntityProject:            "Project",
	ContextFabricEvidenceEntityOrganization:       "Organization",
	// The next four were already minted by devhealthsource producers
	// (episodes.go, tables.go's work-item-hierarchy edges,
	// teams_projects_edges.go's project-team/work-item-team edges) but had
	// NO registry entry -- they rendered the generic "Evidence" label and
	// counted as a fallback on every read. CHAOS-4698 requiring the typed
	// enum surfaced the gap; closing it is this change's actual taxonomy fix.
	ContextFabricEvidenceEntityEpisode:           "Episode",
	ContextFabricEvidenceEntityWorkItemHierarchy: "Work item hierarchy",
	ContextFabricEvidenceEntityProjectTeam:       "Project/team link",
	ContextFabricEvidenceEntityWorkItemTeam:      "Work item/team link",
	// The ".v2" successors (CHAOS-7252) name the same things as the kinds
	// they replace, so they read the same.
	ContextFabricEvidenceEntityWorkItemDependencyV2: "Work item dependency",
	ContextFabricEvidenceEntityDeploymentIncidentV2: "Deployment/incident link",
	ContextFabricEvidenceEntityWorkItemHierarchyV2:  "Work item hierarchy",
	ContextFabricEvidenceEntityProjectTeamV2:        "Project/team link",
	ContextFabricEvidenceEntityWorkItemTeamV2:       "Work item/team link",
	ContextFabricEvidenceEntityReviewV2:             "Review",
}

// ContextFabricVocabulary names one served vocabulary: the prefix of a
// vocabulary_labels key (CHAOS-12413). The list is the shortest set a reader
// actually sees outside the Details fold; a raw token that stays in the fold
// gets no entry, so its label cannot drift from a word nobody reads.
//
// Not served, on purpose:
//   - completeness.outcomes[].requirement is not a vocabulary. It is a
//     composite coordinate, obligation/role/subject, so it takes the generic
//     path.
//   - obligation, the cause and refinement fields, plan narrowing, driver
//     category, epistemic status and derivation stay in the fold.
//   - completeness.state and data_completeness are never shown, and
//     outcomes[].impact goes to the fold with its table (CHAOS-12245,
//     CHAOS-12416). The reader sees the answer status, whose rule reads the
//     completeness state (ContextFabricAnswerStatusLabel).
//   - a derived claim's field is free text, not a declared name, so it has no
//     row and takes the generic path.
type ContextFabricVocabulary string

const (
	ContextFabricVocabularyAnswerStatus            ContextFabricVocabulary = "answer_status"
	ContextFabricVocabularyRequirementOutcome      ContextFabricVocabulary = "requirement_outcome"
	ContextFabricVocabularyCandidateState          ContextFabricVocabulary = "candidate_state"
	ContextFabricVocabularyPriorReceiptDisposition ContextFabricVocabulary = "prior_receipt_disposition"
	ContextFabricVocabularyStructureDisposition    ContextFabricVocabulary = "structure_disposition"
	ContextFabricVocabularySubjectKind             ContextFabricVocabulary = "subject_kind"
	// ContextFabricVocabularySubjectKindPlural serves the plural beside the
	// singular, so a consumer writes "Ranked repositories" without a
	// pluralizer of its own.
	ContextFabricVocabularySubjectKindPlural ContextFabricVocabulary = "subject_kind_plural"
	ContextFabricVocabularyMemberOutcome     ContextFabricVocabulary = "member_outcome"
	ContextFabricVocabularyRankingSignal     ContextFabricVocabulary = "ranking_signal"
	ContextFabricVocabularyThresholdLabel    ContextFabricVocabulary = "threshold_label"
	ContextFabricVocabularyDriverWindow      ContextFabricVocabulary = "driver_window"
	ContextFabricVocabularyDriverStanding    ContextFabricVocabulary = "driver_standing"
	ContextFabricVocabularyFactKind          ContextFabricVocabulary = "fact_kind"
	// ContextFabricVocabularyFactField serves the words of declared fact fields
	// and table columns (CHAOS-12418), so a claimed fact reads with no help from
	// the prose.
	ContextFabricVocabularyFactField        ContextFabricVocabulary = "fact_field"
	ContextFabricVocabularyWindowRelativeID ContextFabricVocabulary = "window_relative_id"
	ContextFabricVocabularyWindowProvenance ContextFabricVocabulary = "window_provenance"
)

var contextFabricVocabularyNames = [...]ContextFabricVocabulary{
	ContextFabricVocabularyAnswerStatus,
	ContextFabricVocabularyRequirementOutcome,
	ContextFabricVocabularyCandidateState,
	ContextFabricVocabularyPriorReceiptDisposition,
	ContextFabricVocabularyStructureDisposition,
	ContextFabricVocabularySubjectKind,
	ContextFabricVocabularySubjectKindPlural,
	ContextFabricVocabularyMemberOutcome,
	ContextFabricVocabularyRankingSignal,
	ContextFabricVocabularyThresholdLabel,
	ContextFabricVocabularyDriverWindow,
	ContextFabricVocabularyDriverStanding,
	ContextFabricVocabularyFactKind,
	ContextFabricVocabularyFactField,
	ContextFabricVocabularyWindowRelativeID,
	ContextFabricVocabularyWindowProvenance,
}

// ContextFabricVocabularyNames returns the served vocabularies in published
// order.
func ContextFabricVocabularyNames() [len(contextFabricVocabularyNames)]ContextFabricVocabulary {
	return contextFabricVocabularyNames
}

// contextFabricFactKindLabelsBeyondTheFactVocabulary holds the kinds a finding
// or a claimed fact publishes that are not canonical fact kinds: the count the
// server computed, and the finding kinds with no fact of their own.
var contextFabricFactKindLabelsBeyondTheFactVocabulary = map[string]string{
	string(ContextFabricFactCardinality): "member count",
	"operational_deficiency":             "operational deficiency",
	"relationship":                       "relationship",
	"narrative":                          "narrative",
}

// contextFabricVocabularyFactKindLabels is the fact kind table a chip reads:
// the registry's own word, so a chip and a coverage sentence never disagree.
func contextFabricVocabularyFactKindLabels() map[string]string {
	labels := make(map[string]string, len(contextFabricFactKindLabels)+len(contextFabricFactKindLabelsBeyondTheFactVocabulary))
	for kind, label := range contextFabricFactKindLabels {
		labels[string(kind)] = label
	}
	for kind, label := range contextFabricFactKindLabelsBeyondTheFactVocabulary {
		labels[kind] = label
	}
	return labels
}

// contextFabricVocabularyLabels is the label of every value a reader sees
// outside the fold. Plain, service-authored words, never model text. Totality
// runs both ways in chaos12415_vocabulary_labels_test.go: a new value cannot
// ship unlabeled, and a label cannot outlive its value.
var contextFabricVocabularyLabels = map[ContextFabricVocabulary]map[string]string{
	// Each says "answer", so it cannot be read as a project status or as work
	// completion (North Star check 3). A served word for `complete` also reads
	// the completeness state: ContextFabricAnswerStatusLabel holds that rule.
	ContextFabricVocabularyAnswerStatus: {
		string(ContextFabricInvestigationComplete):              "Full answer",
		string(ContextFabricInvestigationPartial):               "Partial answer",
		string(ContextFabricInvestigationDegraded):              "Degraded answer",
		string(ContextFabricInvestigationClarificationRequired): "Answer needs your confirmation",
		string(ContextFabricInvestigationNoMatch):               "No answer",
	},
	ContextFabricVocabularyRequirementOutcome: {
		string(ContextFabricRequirementSatisfied):     "met",
		string(ContextFabricRequirementNarrowed):      "narrowed",
		string(ContextFabricRequirementUnavailable):   "unavailable",
		string(ContextFabricRequirementNotApplicable): "not applicable",
		string(ContextFabricRequirementNotAttempted):  "not attempted",
	},
	ContextFabricVocabularyCandidateState: {
		string(ContextFabricResolutionCommitted):  "selected",
		string(ContextFabricResolutionProposed):   "suggested",
		string(ContextFabricResolutionAmbiguous):  "more than one match",
		string(ContextFabricResolutionUnresolved): "no match",
	},
	ContextFabricVocabularyPriorReceiptDisposition: {
		string(ContextFabricPriorSubjectReceiptApplied):                "used",
		string(ContextFabricPriorSubjectReceiptSkippedUnloadable):      "could not be loaded",
		string(ContextFabricPriorSubjectReceiptSkippedNoMatch):         "no longer matches",
		string(ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch): "out of date",
		string(ContextFabricPriorSubjectReceiptSkippedFailedReauth):    "not readable with your access",
	},
	ContextFabricVocabularyStructureDisposition: {
		string(ContextFabricStructureDispositionApplied):            "used",
		string(ContextFabricStructureDispositionVetoedUnresolved):   "could not be matched",
		string(ContextFabricStructureDispositionVetoedConflict):     "conflicts with this question",
		string(ContextFabricStructureDispositionVetoedStale):        "out of date",
		string(ContextFabricStructureDispositionSupersededByCaller): "replaced by your request",
		string(ContextFabricStructureDispositionNotEvaluated):       "not checked",
	},
	ContextFabricVocabularySubjectKind: {
		string(ContextFabricSubjectOrganization):      "organization",
		string(ContextFabricSubjectTeam):              "team",
		string(ContextFabricSubjectProject):           "project",
		string(ContextFabricSubjectRepository):        "repository",
		string(ContextFabricSubjectWorkItem):          "work item",
		string(ContextFabricSubjectPullRequest):       "pull request",
		string(ContextFabricSubjectDeployment):        "deployment",
		string(ContextFabricSubjectIncident):          "incident",
		string(ContextFabricSubjectDocument):          "document",
		string(ContextFabricSubjectDecision):          "decision",
		string(ContextFabricSubjectEpisode):           "episode",
		string(ContextFabricSubjectMetric):            "metric",
		string(ContextFabricSubjectPullRequestReview): "pull request review",
		string(ContextFabricSubjectCIRun):             "CI run",
		string(ContextFabricSubjectWorkItemRef):       "work item reference",
	},
	// devhealthschema:not-a-production-replica display words for a subject kind; they spell table names and read no table
	ContextFabricVocabularySubjectKindPlural: {
		string(ContextFabricSubjectOrganization):      "organizations",
		string(ContextFabricSubjectTeam):              "teams",
		string(ContextFabricSubjectProject):           "projects",
		string(ContextFabricSubjectRepository):        "repositories",
		string(ContextFabricSubjectWorkItem):          "work items",
		string(ContextFabricSubjectPullRequest):       "pull requests",
		string(ContextFabricSubjectDeployment):        "deployments",
		string(ContextFabricSubjectIncident):          "incidents",
		string(ContextFabricSubjectDocument):          "documents",
		string(ContextFabricSubjectDecision):          "decisions",
		string(ContextFabricSubjectEpisode):           "episodes",
		string(ContextFabricSubjectMetric):            "metrics",
		string(ContextFabricSubjectPullRequestReview): "pull request reviews",
		string(ContextFabricSubjectCIRun):             "CI runs",
		string(ContextFabricSubjectWorkItemRef):       "work item references",
	},
	ContextFabricVocabularyMemberOutcome: {
		string(ContextFabricCohortOutcomeQualified):            "qualified",
		string(ContextFabricCohortOutcomeProvisional):          "provisional",
		string(ContextFabricCohortOutcomeInsufficientEvidence): "not enough evidence",
		string(ContextFabricCohortOutcomeNotApplicable):        "not applicable",
	},
	ContextFabricVocabularyRankingSignal: contextFabricCohortSignalNouns,
	ContextFabricVocabularyThresholdLabel: {
		"investment_mix.reactive_share_high":          "reactive share is high",
		"investment_mix.deliberate_share_low":         "deliberate share is low",
		"investment_mix.mix_concentrated":             "work mix is concentrated",
		"investment_mix.mix_shift_toward_operational": "mix moved toward operational work",
		"investment_mix.mix_shift_toward_feature":     "mix moved toward feature work",
		"investment_mix.mix_shift_other":              "mix shifted",
	},
	ContextFabricVocabularyDriverWindow: {
		string(ContextFabricCohortMemberDriverWindowCurrent):        "current period",
		string(ContextFabricCohortMemberDriverWindowCurrentVsPrior): "current vs prior period",
	},
	ContextFabricVocabularyDriverStanding: {
		string(ContextFabricDriverPrincipal):    "main driver",
		string(ContextFabricDriverContributing): "contributing",
		string(ContextFabricDriverSymptom):      "symptom",
		string(ContextFabricDriverContext):      "context",
		string(ContextFabricDriverWithheld):     "withheld",
	},
	ContextFabricVocabularyFactKind:  contextFabricVocabularyFactKindLabels(),
	ContextFabricVocabularyFactField: contextFabricFactFieldLabels,
	ContextFabricVocabularyWindowRelativeID: {
		string(ContextFabricRelativeWindowTrailing30D):  "last 30 days",
		string(ContextFabricRelativeWindowTrailing90D):  "last 90 days",
		string(ContextFabricRelativeWindowTrailing365D): "last 365 days",
		string(ContextFabricRelativeWindowAllTime):      "all time",
	},
	ContextFabricVocabularyWindowProvenance: {
		string(ContextFabricWindowInferredDefault):        "default window",
		string(ContextFabricWindowQuestionStated):         "stated in the question",
		string(ContextFabricWindowClarificationConfirmed): "confirmed by you",
		string(ContextFabricWindowServerDefault):          "the window the reads used",
	},
}

// ContextFabricVocabularyLabelKey is the vocabulary_labels key of one value.
func ContextFabricVocabularyLabelKey(vocabulary ContextFabricVocabulary, value string) string {
	return string(vocabulary) + ":" + value
}

// ContextFabricVocabularyLabel returns the served label of one value; ok is
// false for a value with no row, which a consumer renders by its generic path.
func ContextFabricVocabularyLabel(vocabulary ContextFabricVocabulary, value string) (string, bool) {
	label, ok := contextFabricVocabularyLabels[vocabulary][value]
	return label, ok
}

// ContextFabricVocabularyLabelsTier names how much of vocabulary_labels a served
// document carries (CHAOS-12416). Closed: the shed line states it.
//
// The tiers rank what a reader loses. The status words go last, because with no
// map a partial answer reads a neutral pill, and the correction note is gone, so
// the pill says less than the limitation below it. The fact field words go
// first (CHAOS-12418): they are the largest table and the only one a fold can
// stand in for, so a field word never costs a chip its word.
type ContextFabricVocabularyLabelsTier string

const (
	// ContextFabricVocabularyLabelsOff: the flag is off, so nothing was shed.
	ContextFabricVocabularyLabelsOff ContextFabricVocabularyLabelsTier = "off"
	// ContextFabricVocabularyLabelsFull: the whole map.
	ContextFabricVocabularyLabelsFull ContextFabricVocabularyLabelsTier = "full"
	// ContextFabricVocabularyLabelsNoFactFields: every word but fact_field:*.
	ContextFabricVocabularyLabelsNoFactFields ContextFabricVocabularyLabelsTier = "no_fact_fields"
	// ContextFabricVocabularyLabelsStatusOnly: answer_status:* and the note.
	ContextFabricVocabularyLabelsStatusOnly ContextFabricVocabularyLabelsTier = "status_only"
	// ContextFabricVocabularyLabelsNone: no map fits.
	ContextFabricVocabularyLabelsNone ContextFabricVocabularyLabelsTier = "none"
)

// ContextFabricVocabularyLabelsTierVocabulary returns the closed set.
func ContextFabricVocabularyLabelsTierVocabulary() [5]ContextFabricVocabularyLabelsTier {
	return [5]ContextFabricVocabularyLabelsTier{
		ContextFabricVocabularyLabelsOff, ContextFabricVocabularyLabelsFull,
		ContextFabricVocabularyLabelsNoFactFields, ContextFabricVocabularyLabelsStatusOnly,
		ContextFabricVocabularyLabelsNone,
	}
}

// Shed reports that a bound left words out of the served document.
func (t ContextFabricVocabularyLabelsTier) Shed() bool {
	return t == ContextFabricVocabularyLabelsNoFactFields || t == ContextFabricVocabularyLabelsStatusOnly || t == ContextFabricVocabularyLabelsNone
}

// ContextFabricVocabularyLabelsWithoutFactFields keeps the words of the
// no_fact_fields tier. It returns nil when no word is left, so an empty map never
// stands for a tier.
func ContextFabricVocabularyLabelsWithoutFactFields(labels map[string]string) map[string]string {
	var kept map[string]string
	for key, label := range labels {
		if vocabulary, _, _ := strings.Cut(key, ":"); vocabulary == string(ContextFabricVocabularyFactField) {
			continue
		}
		if kept == nil {
			kept = map[string]string{}
		}
		kept[key] = label
	}
	return kept
}

// ContextFabricStatusVocabularyLabels keeps the words of the status_only tier:
// answer_status:* and the correction note. It returns nil when neither is held,
// so an empty map never stands for a tier.
func ContextFabricStatusVocabularyLabels(labels map[string]string) map[string]string {
	var kept map[string]string
	for key, label := range labels {
		vocabulary, _, _ := strings.Cut(key, ":")
		if vocabulary != string(ContextFabricVocabularyAnswerStatus) && key != ContextFabricStatusCorrectionNoteKey {
			continue
		}
		if kept == nil {
			kept = map[string]string{}
		}
		kept[key] = label
	}
	return kept
}

// The two answer status words that are not a table row: a `complete` status
// whose completeness does not back the table's "Full answer" (CHAOS-12245).
const (
	contextFabricAnswerNotFullyCoveredLabel = "Answer, not fully covered"
	contextFabricAnswerPlainLabel           = "Answer"
)

// ContextFabricAnswerStatusLabel is the served word of an answer status, the
// ONE function behind answer_status:<status> and both statuses of the
// correction note, so the pill and the note cannot disagree (CHAOS-12245).
//
// "Full answer" needs a complete status AND complete completeness. A complete
// status beside partial or degraded completeness reads "Answer, not fully
// covered". With no derived completeness it reads a plain "Answer", which
// claims neither a full answer nor a gap. Any other status reads its registry
// word. ok is false for a status with no row, and the raw state is never shown.
func ContextFabricAnswerStatusLabel(status ContextFabricInvestigationStatus, completeness ContextFabricAnswerCompletenessState) (string, bool) {
	label, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyAnswerStatus, string(status))
	if status != ContextFabricInvestigationComplete {
		return label, ok
	}
	switch completeness {
	case ContextFabricAnswerCompletenessComplete:
		return label, true
	case ContextFabricAnswerCompletenessPartial, ContextFabricAnswerCompletenessDegraded:
		return contextFabricAnswerNotFullyCoveredLabel, true
	default:
		return contextFabricAnswerPlainLabel, true
	}
}

// ContextFabricStatusCorrectionNoteKey is the vocabulary_labels key of the
// correction note (CHAOS-12416). It is one sentence the service composes for
// one result, not a table row, so it is not a member of
// ContextFabricVocabularyNames.
const ContextFabricStatusCorrectionNoteKey = "status_correction:note"

// contextFabricStatusCorrectionSpans names what each direction moved. Only
// degraded_to_partial raises a status. A table, so no direction is parsed.
var contextFabricStatusCorrectionSpans = map[ContextFabricStatusCorrectionDirection]struct {
	verb     string
	from, to ContextFabricInvestigationStatus
}{
	ContextFabricStatusCorrectionCompleteToPartial:  {"lowered", ContextFabricInvestigationComplete, ContextFabricInvestigationPartial},
	ContextFabricStatusCorrectionCompleteToDegraded: {"lowered", ContextFabricInvestigationComplete, ContextFabricInvestigationDegraded},
	ContextFabricStatusCorrectionPartialToDegraded:  {"lowered", ContextFabricInvestigationPartial, ContextFabricInvestigationDegraded},
	ContextFabricStatusCorrectionDegradedToPartial:  {"raised", ContextFabricInvestigationDegraded, ContextFabricInvestigationPartial},
}

// contextFabricCorrectionObligationWords names each answer obligation in the
// correction note. Each is singular, because the outcome words say "was".
var contextFabricCorrectionObligationWords = map[string]string{
	"state":                               "status",
	"completion":                          "completion",
	"readiness":                           "readiness",
	"health":                              "health",
	"principal_drivers":                   "driver detail",
	"ranking":                             "ranking",
	"remaining_work":                      "remaining work",
	"evidence":                            "evidence",
	ContextFabricAnswerObligationCoverage: "coverage",
	ContextFabricAnswerObligationCount:    "the count",
	"allocation_breakdown":                "the allocation breakdown",
	"trend_series":                        "the trend",
	"period_delta":                        "the change between periods",
}

// contextFabricCorrectionOutcomeWords says what became of the obligation.
var contextFabricCorrectionOutcomeWords = map[ContextFabricPlanRequirementOutcome]string{
	ContextFabricRequirementSatisfied:     "was read in full",
	ContextFabricRequirementNarrowed:      "was read only in part",
	ContextFabricRequirementUnavailable:   "was not available",
	ContextFabricRequirementNotApplicable: "does not apply",
	ContextFabricRequirementNotAttempted:  "was not read",
}

// ContextFabricStatusCorrectionNote composes the whole correction sentence for
// one result, in the service's own words (CHAOS-12413 ruling 2). Each status
// reads ContextFabricAnswerStatusLabel, so the sentence never holds a raw
// status word. An obligation or outcome with no word drops that clause, and a
// direction outside the vocabulary reads "changed" between the correction's
// own two statuses. ok is false when a status has no word, and the consumer
// then shows no note: a raw token is never the fallback.
func ContextFabricStatusCorrectionNote(correction ContextFabricStatusCorrection, served ContextFabricInvestigationStatus, completeness ContextFabricAnswerCompletenessState) (string, bool) {
	span, known := contextFabricStatusCorrectionSpans[correction.Direction]
	if !known {
		span.verb, span.from, span.to = "changed", correction.ReplacedStatus, served
	}
	from, fromKnown := ContextFabricAnswerStatusLabel(span.from, completeness)
	to, toKnown := ContextFabricAnswerStatusLabel(span.to, completeness)
	if !fromKnown || !toKnown {
		return "", false
	}
	note := "The service " + span.verb + " this answer from " + from + " to " + to
	obligation, _, _ := strings.Cut(correction.DecidingRequirement, "/")
	what, whatKnown := contextFabricCorrectionObligationWords[obligation]
	outcome, outcomeKnown := contextFabricCorrectionOutcomeWords[correction.DecidingOutcome]
	if whatKnown && outcomeKnown {
		note += ": " + what + " " + outcome
	}
	return note + ".", true
}

// humanizeVocabularyToken is the deterministic transform for a token from
// one of acr's OWN vocabularies whose display needs no bespoke label
// (underscores to spaces). Never applied to free text.
func humanizeVocabularyToken(token string) string {
	return strings.ReplaceAll(strings.TrimSpace(token), "_", " ")
}

// ContextFabricFactKindLabel returns the display label for a fact kind,
// falling back to the deterministic token transform for a value outside the
// registry (unreachable for a validated result; the totality test keeps the
// registry complete).
func ContextFabricFactKindLabel(kind ContextFabricFactKind) string {
	if label, ok := contextFabricFactKindLabels[kind]; ok {
		return label
	}
	return humanizeVocabularyToken(string(kind))
}

// The reasons a fact kind is withheld from a project-only caller
// (CHAOS-12330). Each is a template over the kind's label: the one place the
// sentence is written.
const (
	projectScopeWithheldTeamRollup   = "%s facts are built from owning teams, which a project grant does not cover"
	projectScopeWithheldMixedPlacing = "%s facts are placed by repository or span the organization, which a project grant does not cover"
	projectScopeWithheldOrganization = "%s facts are organization-wide figures, which a project grant does not cover"
	projectScopeWithheldPullRequests = "%s facts need pull requests, which are not in a project grant yet"
	projectScopeWithheldMembership   = "%s facts need exact project membership, which is not proven yet"
	projectScopeWithheldRepositories = "%s facts belong to repositories, which a project grant does not cover"
)

// contextFabricProjectScopeWithheldFactReasons names why each fact kind is
// withheld from a project-only caller. A kind served to that caller one day
// loses its row here and its branch in the registry together.
var contextFabricProjectScopeWithheldFactReasons = map[ContextFabricFactKind]string{
	ContextFabricFactHealth:                  projectScopeWithheldTeamRollup,
	ContextFabricFactFlow:                    projectScopeWithheldTeamRollup,
	ContextFabricFactLandscape:               projectScopeWithheldTeamRollup,
	ContextFabricFactMetrics:                 projectScopeWithheldTeamRollup,
	ContextFabricFactReadiness:               projectScopeWithheldTeamRollup,
	ContextFabricFactWorkload:                projectScopeWithheldTeamRollup,
	ContextFabricFactOperationalDeficiencies: projectScopeWithheldTeamRollup,
	ContextFabricFactInvestment:              projectScopeWithheldMixedPlacing,
	ContextFabricFactSourceHealth:            projectScopeWithheldOrganization,
	ContextFabricFactPullRequests:            projectScopeWithheldPullRequests,
	ContextFabricFactReviews:                 projectScopeWithheldPullRequests,
	ContextFabricFactIdentity:                projectScopeWithheldMembership,
	ContextFabricFactStatus:                  projectScopeWithheldMembership,
	ContextFabricFactWork:                    projectScopeWithheldMembership,
	ContextFabricFactActualCompletion:        projectScopeWithheldMembership,
	ContextFabricFactBlockers:                projectScopeWithheldMembership,
	ContextFabricFactRequiredChildren:        projectScopeWithheldMembership,
	ContextFabricFactCommitment:              projectScopeWithheldMembership,
	ContextFabricFactMembership:              projectScopeWithheldRepositories,
	ContextFabricFactContinuousIntegration:   projectScopeWithheldRepositories,
	ContextFabricFactDeployments:             projectScopeWithheldRepositories,
	ContextFabricFactIncidents:               projectScopeWithheldRepositories,
	ContextFabricFactSecurityAlerts:          projectScopeWithheldRepositories,
	ContextFabricFactFileHotspots:            projectScopeWithheldRepositories,
	ContextFabricFactEvidence:                projectScopeWithheldRepositories,
}

// ContextFabricRequestedProjectNarrowingUnavailableLimitation is the sentence
// of a project-only request that names projects: the engine stops before any
// read, and no answer is written over the whole grant (CHAOS-12324 narrows).
const ContextFabricRequestedProjectNarrowingUnavailableLimitation = "Requested project narrowing not available yet"

// ContextFabricProjectScopeWithheldLimitation is the sentence of an answer a
// project grant could only partly cover: the details beside it say what was
// left out and why.
const ContextFabricProjectScopeWithheldLimitation = "A project grant does not cover part of what was asked, so this answer leaves it out"

// ContextFabricProjectScopeWithheldFactLabel is the label, and the reason, of
// a fact kind withheld from a project-only caller. A kind with no row reads
// the sentence that assumes least.
func ContextFabricProjectScopeWithheldFactLabel(kind ContextFabricFactKind) string {
	reason, ok := contextFabricProjectScopeWithheldFactReasons[kind]
	if !ok {
		reason = projectScopeWithheldRepositories
	}
	return fmt.Sprintf(reason, kindClause(ContextFabricFactKindLabel(kind)))
}

// ContextFabricProjectScopeWithheldKindLabel is the label of a subject kind a
// project grant does not reach. It states the grant class and never that a
// subject of the kind exists. A project detail is the refused narrowing: a
// project node of the grant is admitted, so nothing else names that kind.
func ContextFabricProjectScopeWithheldKindLabel(kind ContextFabricSubjectKind) string {
	switch kind {
	case ContextFabricSubjectProject:
		return ContextFabricRequestedProjectNarrowingUnavailableLimitation
	case ContextFabricSubjectTeam:
		return "Teams are not in a project grant yet"
	case ContextFabricSubjectPullRequest:
		return "Pull requests are not in a project grant yet"
	case ContextFabricSubjectRepository:
		return "Repositories are not in a project grant"
	default:
		return kindClause(humanizeVocabularyToken(string(kind))) + " subjects are not in a project grant"
	}
}

// ContextFabricSourceStateLabel returns the display label for a source
// state.
func ContextFabricSourceStateLabel(state ContextFabricSourceState) string {
	if label, ok := contextFabricSourceStateLabels[state]; ok {
		return label
	}
	return humanizeVocabularyToken(string(state))
}

// ContextFabricSourceObservationLabel returns the display label for a
// coverage source name ("canonical_fact:blockers" → "Canonical facts —
// blockers"). Unknown shapes get the generic "Source" — the deterministic
// fail-readable floor, shipped with the vocabulary instead of guessed at a
// consumer.
func ContextFabricSourceObservationLabel(source string) string {
	switch source {
	case "context-fabric:graph":
		return "Relationship graph"
	case "context-fabric:graph-validity-windows":
		return "Relationship graph — undated elements"
	}
	if kind, ok := strings.CutPrefix(source, "canonical_fact:"); ok {
		if validFactKind(ContextFabricFactKind(kind)) {
			return "Canonical facts — " + ContextFabricFactKindLabel(ContextFabricFactKind(kind))
		}
		return "Canonical facts"
	}
	if capability, ok := strings.CutPrefix(source, "dev-health-ops:"); ok {
		token := humanizeVocabularyToken(capability)
		if token == "" {
			return "Dev Health"
		}
		return "Dev Health — " + token
	}
	return "Source"
}

// ContextFabricEvidenceRefLabel returns the display label for one
// acr:v1:<entity-type>:<id...> evidence ref, plus whether the entity-type
// segment was in the known registry (false = the generic floor was used;
// the caller counts that via telemetry, never the segment itself).
//
// The label is CLAMPED to the label bound (terra r2 P1, EXECUTED): the ref
// id itself may legally run to ContextFabricEvidenceRefIDMaxLength (256)
// runes, so an unclamped "<noun>: <id>" concatenation could exceed the
// 160-rune label bound -- and because the engine stamps this label for
// every closure member immediately before write-path validation, the
// oversized label would fail the WHOLE investigation (ErrInvalidResult →
// 500) on a perfectly valid evidence ref. Truncating a chip label is
// strictly better than losing the answer that carries it -- the full id
// stays one fold away in the raw evidence_ref_ids the consumer already
// renders in Details.
func ContextFabricEvidenceRefLabel(refID string) (string, bool) {
	parts := strings.Split(refID, ":")
	if len(parts) < 4 || parts[0] != "acr" || parts[1] != "v1" {
		return "Evidence", false
	}
	entityType := parts[2]
	id := strings.Join(parts[3:], ":")
	label, known := contextFabricEvidenceEntityLabels[ContextFabricEvidenceEntityType(entityType)]
	if !known {
		if id == "" {
			return "Evidence", false
		}
		return clampLabel("Evidence: " + id), false
	}
	if id == "" {
		return label, true
	}
	if EncodedEvidenceEntityType(ContextFabricEvidenceEntityType(entityType)) {
		id = decodedEvidenceID(id)
	}
	return clampLabel(label + ": " + id), true
}

// decodedEvidenceID renders an encoded (".v2") id for display: each
// ':'-separated segment unescaped, rejoined with ':', so the label reads as
// the unescaped ids did before CHAOS-7252. Display only: a label is never
// parsed or compared. It mirrors identity.DecodeSegment ("%3A" -> ':' first,
// then "%25" -> '%'); TestDecodedEvidenceIDMatchesTheIdentityCodec pins the
// two against each other, since contracts/v1 imports nothing internal.
func decodedEvidenceID(id string) string {
	segments := strings.Split(id, ":")
	for index, segment := range segments {
		segment = strings.ReplaceAll(segment, "%3A", ":")
		segments[index] = strings.ReplaceAll(segment, "%25", "%")
	}
	return strings.Join(segments, ":")
}

// countPhrase renders a detail's structured Count for a label — the ONLY
// place a quantity is put into words on this surface (the settled design's
// digit rule: synthesis Phrasing may carry no digits; the deterministic
// Label states the number).
func countPhrase(count *int, singular, plural string) string {
	if count == nil {
		return "some " + plural
	}
	if *count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", *count, plural)
}

// coverageDetailIntOrZero reads a pointer field for label composition. Only
// ContextFabricCoverageDetailKindCensusTruncated's field rule requires
// Declared/Served together, so a nil here is unreachable on a validated
// detail -- zero is the safe floor for an unvalidated caller (this
// function's own caller, ComposeCoverageDetailLabel, runs BEFORE Validate).
func coverageDetailIntOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// ComposeCoverageDetailLabel composes the deterministic terse label for one
// coverage detail from its own structured fields. Pure; the engine stamps
// its result into ContextFabricCoverageDetail.Label before validation.
func ComposeCoverageDetailLabel(d ContextFabricCoverageDetail) string {
	kind := ContextFabricFactKindLabel(d.FactKind)
	var label string
	switch d.Code {
	case ContextFabricCoverageDetailFactUnconfigured:
		label = "No source is configured for " + kind + " facts"
	case ContextFabricCoverageDetailFactNoDeclaringProducer:
		label = "No source declares " + kind + " facts for this subject"
	case ContextFabricCoverageDetailFactTableShapeUndeclared:
		label = "The source for " + kind + " facts does not declare the shape this needs"
	case ContextFabricCoverageDetailFactScopeUnexpanded:
		label = kindClause(kind) + " facts reachable only through scope expansion were not reached"
	case ContextFabricCoverageDetailFactReadFailed:
		label = kindClause(kind) + " facts could not be read"
	case ContextFabricCoverageDetailFactProviderReported:
		label = providerReportedLabel(kind, d.SourceState)
	case ContextFabricCoverageDetailFactPruned:
		label = kindClause(kind) + " facts do not apply to what was asked"
	case ContextFabricCoverageDetailFactNarrowed:
		label = countPhrase(d.Count, "subject was", "subjects were") + " skipped for " + kind + " facts"
	case ContextFabricCoverageDetailGraphEndpointLookupFailed:
		label = countPhrase(d.Count, "relationship link", "relationship links") + " could not be resolved"
	case ContextFabricCoverageDetailGraphExactNameCandidatesTruncated:
		label = "More exact-name matches existed than could be shown"
	case ContextFabricCoverageDetailGraphCohortDeniedByAuthorization:
		label = countPhrase(d.Count, "group member", "group members") + " excluded by authorization"
	case ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked:
		label = "No issue of this project links a pull request, so its deployments could not be reached (" + countPhrase(d.Count, "issue", "issues") + " checked)"
	case ContextFabricCoverageDetailWorkItemRepositoryUnlinked:
		label = "No pull request of this repository links an issue, so its work items could not be reached (" + countPhrase(d.Count, "pull request", "pull requests") + " checked)"
	case ContextFabricCoverageDetailGraphNoMemberFound:
		label = "No " + humanizeVocabularyToken(string(d.Kind)) + " was found under the named subject, so none are listed"
	case ContextFabricCoverageDetailGraphWalkCutBeforeMember:
		label = "The read was cut before it reached any " + humanizeVocabularyToken(string(d.Kind)) + ", so some may exist that were not read"
	case ContextFabricCoverageDetailGraphHopWalkTruncated:
		label = "The read of linked subjects was cut at its limit, so some may exist that were not read"
	case ContextFabricCoverageDetailGraphTeamNarrowingUndecidedPaths:
		label = countPhrase(d.Count, "linked subject was left out because its", "linked subjects were left out because their") + " team ownership could not be decided"
	case ContextFabricCoverageDetailComputedPopulationAbsent:
		label = "No member set could be produced for this, so it was not computed"
	case ContextFabricCoverageDetailGraphUnknownRelationshipType:
		label = countPhrase(d.Count, "relationship edge", "relationship edges") + " of an unrecognized type dropped"
	case ContextFabricCoverageDetailGraphValidityUnbounded:
		label = countPhrase(d.Count, "undated element", "undated elements") + " included at the requested time"
	case ContextFabricCoverageDetailGraphAnswerPullRequestsOnExplicitTextLinks:
		// These two count pull requests IN THE ANSWER (cohort members and edge
		// endpoints). The walk line's walk_crossed_pull_requests_* counts the
		// pull requests a walk crossed: a different number, never shown here.
		label = countPhrase(d.Count, "pull request", "pull requests") + " tied to the requested project only by a text reference"
	case ContextFabricCoverageDetailGraphAnswerPullRequestsOnHeuristicLinks:
		label = countPhrase(d.Count, "pull request", "pull requests") + " tied to the requested project only by an inferred link"
	case ContextFabricCoverageDetailReuseAuxiliaryRefsStripped:
		label = countPhrase(d.Count, "supporting item is", "supporting items are") + " no longer visible to you and were removed"
	case ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt:
		// Says the answer STOPPED, not that a read failed. A reader who is
		// told "could not be read" would go looking for a broken source;
		// nothing was read, so there is nothing to look at.
		// No count phrasing: the field rule for this code allows no count,
		// because nothing was read and there is nothing to count.
		label = "The answer ended before this was looked at"
	case ContextFabricCoverageDetailPopulationTruncated:
		// Says the NUMBER is a floor, not that anything failed. A reader
		// told "could not be read" would go looking for a broken source;
		// everything here was read successfully and the answer simply did
		// not reach the end of the set.
		// No count phrasing: the only count available is the size of what
		// WAS found, which the outcome row already states as `served`, and
		// the number this label is about is the one nothing measured.
		label = "The full set could not be listed, so this is at least this many"
	case ContextFabricCoverageDetailReadPopulationUnverified:
		// Says WHO the answer was owed to is unknown -- not that a source
		// failed and not that a set was cut short. Both of those send a
		// reader somewhere useless: there is no broken provider to check and
		// no larger set to page through. What is missing is the list of
		// subjects this was supposed to cover, so the phrasing names that
		// and nothing else.
		// No count phrasing: the row carries 0/0 precisely because nothing
		// was counted, and a number here would imply one was.
		label = "The set this covers could not be identified"
	case ContextFabricCoverageDetailRequirementReadNotPlanned:
		// Says the answer NEVER LOOKED, not that looking failed. Every other
		// phrasing in this switch would send a reader to check a source, and
		// there is no source to check: the turn planned no fact that could
		// serve this cell, so nothing was attempted and nothing is broken.
		// "was not looked at" rather than "could not be read" for exactly
		// that reason.
		// No count phrasing: nothing was read, so there is nothing to count
		// -- the same rule as `answer_terminated_before_attempt` above, for
		// the same reason.
		label = "This was not looked at for this question"
	case ContextFabricCoverageDetailRequirementNotEvaluable:
		// Says the answer did not EVALUATE this, not that a source failed:
		// there is no broken provider to check. No count phrasing: the field
		// rule for this code allows none.
		label = "This was considered but could not be evaluated"
	case ContextFabricCoverageDetailFactReadOriginState:
		// Names the POPULATION the read was for, then its state, in the
		// state registry's own words. No count and no "could not": the row
		// is a disclosure of one read's outcome, and the folded source's own
		// detail is what says whether that outcome degraded the answer.
		label = kindClause(kind) + " facts, read for each " + humanizeVocabularyToken(string(d.OriginKind)) + ": " + ContextFabricSourceStateLabel(d.SourceState)
	case ContextFabricCoverageDetailKindCensusTruncated:
		// Names the KIND the census counted and both numbers, phrased as a
		// floor rather than a total -- the same "at least" reading
		// `population_truncated`'s own label uses, for the same reason: the
		// number this label states is one nothing measured past.
		label = fmt.Sprintf("At least %d %s found; %d included", coverageDetailIntOrZero(d.Declared), humanizeVocabularyToken(string(d.Kind)), coverageDetailIntOrZero(d.Served))
	case ContextFabricCoverageDetailSynthesisInputBounded:
		label = fmt.Sprintf("%d of %d %s facts read were given to answer synthesis", coverageDetailIntOrZero(d.Served), coverageDetailIntOrZero(d.Declared), kind)
	case ContextFabricCoverageDetailFactScoreWithheld:
		label = fmt.Sprintf(ContextFabricFactScoreWithheldLabel, kindClause(kind))
	case ContextFabricCoverageDetailProjectScopeWithheld:
		// Says the caller's GRANT stops here, not that a source failed and
		// not that nothing exists: nothing was read, so there is nothing to
		// check and no zero to report.
		if d.Kind != "" {
			label = ContextFabricProjectScopeWithheldKindLabel(d.Kind)
		} else {
			label = ContextFabricProjectScopeWithheldFactLabel(d.FactKind)
		}
	case ContextFabricCoverageDetailSynthesisModelFailed:
		label = "The model call that writes the answer failed (" + humanizeVocabularyToken(string(d.FailureClass)) + "); " + humanizeVocabularyToken(string(d.RetryHint))
	case ContextFabricCoverageDetailSynthesisNotRun:
		switch d.FailureClass {
		case ContextFabricSynthesisFailureDeadline:
			label = "Facts only; no second synthesis was started because the time budget was spent; " + humanizeVocabularyToken(string(d.RetryHint))
		case ContextFabricSynthesisFailureRowsOverBudget:
			label = "Facts only; the written answer was over the response budget because of the rows it carried; " + humanizeVocabularyToken(string(d.RetryHint))
		case ContextFabricSynthesisFailureRetryOverBudget:
			label = "Facts only; a narrower retry also ran and did not fit the response budget; " + humanizeVocabularyToken(string(d.RetryHint))
		default:
			label = "Facts only; the written answer was over the response budget and could not be narrowed; " + humanizeVocabularyToken(string(d.RetryHint))
		}
	default:
		label = "Coverage was limited"
	}
	if d.Narrowed && d.Code != ContextFabricCoverageDetailFactNarrowed {
		label += "; some subjects were skipped"
	}
	return clampLabel(label)
}

func kindClause(kindLabel string) string {
	if kindLabel == "" {
		return "Some"
	}
	return strings.ToUpper(kindLabel[:1]) + kindLabel[1:]
}

func providerReportedLabel(kind string, state ContextFabricSourceState) string {
	switch state {
	case ContextFabricSourceStale:
		return kindClause(kind) + " facts may be out of date"
	case ContextFabricSourceTruncated:
		return "Only part of the " + kind + " facts could be included"
	case ContextFabricSourceNoData:
		return "No " + kind + " facts were found"
	case ContextFabricSourceNotApplicable:
		return kindClause(kind) + " facts do not apply here"
	case ContextFabricSourceUnauthorized:
		return kindClause(kind) + " facts are not authorized for this account"
	case ContextFabricSourceConflicted:
		return kindClause(kind) + " facts held conflicting data"
	default:
		return kindClause(kind) + " facts were limited"
	}
}

func clampLabel(label string) string {
	runes := []rune(label)
	if len(runes) <= ContextFabricCoverageDetailLabelMaxLength {
		return label
	}
	return strings.TrimSpace(string(runes[:ContextFabricCoverageDetailLabelMaxLength]))
}

// ContextFabricFactScoreWithheldLabel is the label of a fact_score_withheld detail: the fact kind, capitalized, then the
// reason. The one place the sentence is written.
const ContextFabricFactScoreWithheldLabel = "%s scores withheld: their drivers were not served"
