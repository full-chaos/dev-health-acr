package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type projectStatusObservation struct {
	kind     FactKind
	category string
	field    string
	id       string
	title    func(fields map[string]FactValue) (string, bool)
}

func factInteger(fields map[string]FactValue, name string) (int64, bool) {
	value, ok := fields[name]
	if !ok || value.Integer == nil {
		return 0, false
	}
	return *value.Integer, true
}

func factString(fields map[string]FactValue, name string) (string, bool) {
	value, ok := fields[name]
	if !ok || value.String == nil || *value.String == "" {
		return "", false
	}
	return *value.String, true
}

func itemsNoun(count int64) string {
	if count == 1 {
		return "item"
	}
	return "items"
}

// projectStatusObservations name each number for what it measures and give the
// day it was observed. A number with no day is not served: its observation
// reports no_value.
//
// The two backlog_size fields are different measures. The workload fact's is
// the capacity forecast's backlog: the items in progress when ops last
// forecast the project (capacity_forecasts, from wip_count_end_of_day). The
// readiness fact's is the open items of the day (estimate_coverage_metrics_daily:
// created and not completed or closed by the end of that day).
//
// The order is the answer's order: the quick answer first, the open items of
// the day, then the details (the forecast's in-progress count, the estimate
// coverage, the risk severity).
var projectStatusObservations = []projectStatusObservation{
	{
		kind: FactReadiness, category: "readiness", field: "backlog_size", id: "project-status-0-open-items",
		title: func(fields map[string]FactValue) (string, bool) {
			open, okOpen := factInteger(fields, "backlog_size")
			day, okDay := factString(fields, "day")
			if !okOpen || !okDay {
				return "", false
			}
			return fmt.Sprintf("Open items: %d (%s)", open, day), true
		},
	},
	{
		kind: FactWorkload, category: "workload", field: "backlog_size", id: "project-status-1-workload",
		title: func(fields map[string]FactValue) (string, bool) {
			backlog, okBacklog := factInteger(fields, "backlog_size")
			day, okDay := factString(fields, "day")
			if !okBacklog || !okDay {
				return "", false
			}
			title := fmt.Sprintf("Forecast backlog: %d %s in progress (capacity forecast of %s", backlog, itemsNoun(backlog), day)
			if run, ok := factString(fields, "latest_run_day"); ok && day < run {
				title += ", not refreshed by the forecast run of " + run
			}
			return title + ")", true
		},
	},
	{
		kind: FactReadiness, category: "readiness", field: "estimated_count", id: "project-status-2-readiness",
		title: func(fields map[string]FactValue) (string, bool) {
			estimated, okEstimated := factInteger(fields, "estimated_count")
			open, okOpen := factInteger(fields, "backlog_size")
			day, okDay := factString(fields, "day")
			if !okEstimated || !okOpen || !okDay {
				return "", false
			}
			return fmt.Sprintf("Estimate coverage: %d of %d open %s estimated (%s)", estimated, open, itemsNoun(open), day), true
		},
	},
	{
		kind: FactHealth, category: "health", field: "severity", id: "project-status-3-health",
		title: func(fields map[string]FactValue) (string, bool) {
			severity, ok := factString(fields, "severity")
			if !ok {
				return "", false
			}
			if asOf, dated := factString(fields, "severity_as_of"); dated {
				return "Risk severity: " + severity + " (as of " + asOf + ")", true
			}
			return "Risk severity: " + severity, true
		},
	},
}

type ProjectStatusDriverEvent struct {
	Emitted        int
	SkippedBudget  int
	SkippedIDClash int
	// RowsAttached counts the fact rows (both tables) the status claims
	// carry. RowsShared counts the status claims that carry none because
	// another claim of the answer already carries the rows of the same fact.
	// RowsCut counts the status claims whose rows were cut at the claimed
	// fact row bound.
	RowsAttached  int
	RowsShared    int
	RowsCut       int
	Missing       []FactKind
	MissingStates []string
	Partial       []FactKind
	PartialStates []SourceState
}

func projectStatusUsableState(state SourceState) bool {
	return state == SourceAvailable || state == SourceTruncated || state == SourceStale
}

func projectStatusStateLabel(state SourceState) string {
	if label, cause := driverSourceStateCause(state); label != "" {
		return " (" + label + ": " + cause + ")."
	}
	return "."
}

func projectStatusIntent(interpretation InterpretedQuestion) bool {
	for _, requirement := range interpretation.FactRequirements {
		if requirement.Kind == FactStatus {
			return true
		}
	}
	return false
}

func projectStatusClaimID(subject SubjectRef, observation projectStatusObservation) string {
	digest := sha256.Sum256([]byte(subject.CanonicalID + "\x00" + string(observation.kind) + "\x00" + observation.field))
	return "claim_project_status_" + hex.EncodeToString(digest[:])[:32]
}

func projectStatusDrivers(interpretation InterpretedQuestion, committed []SubjectRef, facts []CanonicalFact, coverage Coverage, existingDrivers []DriverJudgment, existingClaims []ClaimedFact, allocation ItemAllocation) ([]DriverJudgment, []ClaimedFact, *ProjectStatusDriverEvent) {
	if len(committed) != 1 || committed[0].Kind != SubjectProject || !projectStatusIntent(interpretation) {
		return nil, nil, nil
	}
	event := &ProjectStatusDriverEvent{}
	project := committed[0]
	allowed := contractsv1.ContextFabricDriversMaxCount - len(existingDrivers)
	if claimRoom := contractsv1.ContextFabricClaimedFactsMaxCount - len(existingClaims); claimRoom < allowed {
		allowed = claimRoom
	}
	if allocation.InForce() {
		if grant := allocation.NarrationGrant / 2; grant < allowed {
			allowed = grant
		}
	}
	taken := make(map[string]struct{}, len(existingDrivers))
	for _, driver := range existingDrivers {
		taken[driver.DriverID] = struct{}{}
	}
	takenClaims := make(map[string]struct{}, len(existingClaims))
	rowsCarried := make(map[string]bool, len(existingClaims))
	for _, claim := range existingClaims {
		takenClaims[claim.ClaimID] = struct{}{}
		if claim.FactID != "" && (len(claim.Rows) > 0 || len(claim.TimeSeriesRows) > 0) {
			rowsCarried[claim.FactID] = true
		}
	}
	var drivers []DriverJudgment
	var claims []ClaimedFact
	for _, observation := range projectStatusObservations {
		var fact, named *CanonicalFact
		for index := range facts {
			candidate := &facts[index]
			if candidate.Kind != observation.kind || candidate.Subject.Kind != project.Kind || candidate.Subject.CanonicalID != project.CanonicalID {
				continue
			}
			if named == nil {
				named = candidate
			}
			if projectStatusUsableState(candidate.SourceState) {
				fact = candidate
				break
			}
		}
		// Two observations read the readiness fact: a kind is named missing
		// or partial once, by the first observation that finds it so.
		missing := func(state string) {
			if containsFactKind(event.Missing, observation.kind) {
				return
			}
			event.Missing = append(event.Missing, observation.kind)
			event.MissingStates = append(event.MissingStates, state)
		}
		if fact == nil {
			if named == nil {
				missing(projectStatusCoverageState(coverage, observation.kind))
			} else {
				missing(string(named.SourceState))
			}
			continue
		}
		if len(fact.EvidenceRefIDs) == 0 {
			missing(projectStatusStateNoEvidence)
			continue
		}
		title, ok := observation.title(fact.Fields)
		claimValue, hasValue := fact.Fields[observation.field]
		if !ok || !hasValue || claimValue.Null {
			missing(projectStatusStateNoValue)
			continue
		}
		if len(drivers) >= allowed {
			event.SkippedBudget++
			continue
		}
		driverID := observation.id
		claimID := projectStatusClaimID(project, observation)
		_, driverClash := taken[driverID]
		_, claimClash := takenClaims[claimID]
		if driverClash || claimClash {
			event.SkippedIDClash++
			continue
		}
		taken[driverID] = struct{}{}
		summary := title + projectStatusStateLabel(fact.SourceState)
		var sourceState SourceState
		if fact.SourceState != SourceAvailable {
			sourceState = fact.SourceState
			if !containsFactKind(event.Partial, observation.kind) {
				event.Partial = append(event.Partial, observation.kind)
				event.PartialStates = append(event.PartialStates, fact.SourceState)
			}
		}
		// The claim names the one fact it restates and carries that fact's
		// rows, so its number traces to the dated rows it was read from. When
		// another claim of the answer already carries the rows of that fact,
		// the fact id names them and they are not served twice.
		claim := ClaimedFact{ClaimID: claimID, Kind: observation.kind, Subject: project, Field: observation.field, Value: convertFactValueScalar(claimValue), FactID: fact.ID}
		if fact.ID != "" && rowsCarried[fact.ID] {
			event.RowsShared++
		} else if attached, rows, _, cut := attachCanonicalRows([]ClaimedFact{claim}, []CanonicalFact{*fact}); len(attached) == 1 {
			claim = attached[0]
			if fact.ID != "" && (len(claim.Rows) > 0 || len(claim.TimeSeriesRows) > 0) {
				rowsCarried[fact.ID] = true
			}
			event.RowsAttached += rows + len(claim.TimeSeriesRows)
			if cut {
				event.RowsCut++
			}
		}
		claims = append(claims, claim)
		drivers = append(drivers, DriverJudgment{
			DriverID: driverID, Standing: DriverPrincipal, Category: observation.category,
			Title: title, Summary: summary,
			AffectedSubjects: []SubjectRef{project}, EvidenceRefIDs: fact.EvidenceRefIDs, ClaimedFactIDs: []string{claimID},
			Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.95, SourceState: sourceState, Current: true,
		})
	}
	event.Emitted = len(drivers)
	return drivers, claims, event
}

// projectStatusDriverCandidates are the drivers shown to a client that writes
// its own answer: the same observation with its true category and the fact's
// evidence refs, and no claim id. They exist only on the synthesis input copy
// of the graph, never on a served or stored result.
func projectStatusDriverCandidates(drivers []DriverJudgment) []DriverJudgment {
	candidates := make([]DriverJudgment, 0, len(drivers))
	for _, driver := range drivers {
		driver.DriverID += "-candidate"
		driver.ClaimedFactIDs = nil
		candidates = append(candidates, driver)
	}
	return candidates
}

func projectStatusMissingLimitations(event *ProjectStatusDriverEvent) []string {
	if event == nil || len(event.Missing) == 0 {
		return nil
	}
	sentences := make([]string, 0, len(event.Missing))
	for index, kind := range event.Missing {
		sentences = append(sentences, contractsv1.ContextFabricProjectStatusMissingLimitation(string(kind), event.MissingStates[index]))
	}
	return sentences
}

// The three missing states a project status driver can report that are not a
// source state: the fact held no evidence, the fact held no value, and the
// coverage held no observation of the kind.
const (
	projectStatusStateNoEvidence = "no_evidence"
	projectStatusStateNoValue    = "no_value"
	projectStatusStateNotRead    = "not_read"
)

// ProjectStatusDriverKindVocabulary is the closed set of fact kinds a project
// status driver event names as missing or partial: the kinds the observations read.
func ProjectStatusDriverKindVocabulary() []string {
	out := make([]string, 0, len(projectStatusObservations))
	for _, observation := range projectStatusObservations {
		if !slices.Contains(out, string(observation.kind)) {
			out = append(out, string(observation.kind))
		}
	}
	return out
}

func containsFactKind(kinds []FactKind, kind FactKind) bool {
	return slices.Contains(kinds, kind)
}

// ProjectStatusDriverMissingStateVocabulary is the closed set of states a
// missing project status kind reports: a source state, or one of the three
// states above.
func ProjectStatusDriverMissingStateVocabulary() []string {
	return []string{
		string(SourceAvailable), string(SourceStale), string(SourceUnavailable), string(SourceUnconfigured), string(SourceUnauthorized),
		string(SourceNoData), string(SourceTruncated), string(SourceConflicted), string(SourceNotApplicable), string(SourcePruned),
		projectStatusStateNoEvidence, projectStatusStateNoValue, projectStatusStateNotRead,
	}
}

// ProjectStatusDriverPartialStateVocabulary is the closed set of states a
// partial project status kind reports: a usable state that is not available.
func ProjectStatusDriverPartialStateVocabulary() []string {
	return []string{string(SourceStale), string(SourceTruncated)}
}

func projectStatusCoverageState(coverage Coverage, kind FactKind) string {
	source := "canonical_fact:" + string(kind)
	observed := ""
	for _, observation := range coverage.Sources {
		if observation.Source != source || observation.State == "" {
			continue
		}
		if observation.State != SourceAvailable {
			return string(observation.State)
		}
		observed = string(observation.State)
	}
	if observed != "" {
		return observed
	}
	return projectStatusStateNotRead
}
