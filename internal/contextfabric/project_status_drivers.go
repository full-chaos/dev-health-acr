package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

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

var projectStatusObservations = []projectStatusObservation{
	{
		kind: FactWorkload, category: "workload", field: "backlog_size", id: "project-status-1-workload",
		title: func(fields map[string]FactValue) (string, bool) {
			backlog, ok := factInteger(fields, "backlog_size")
			return fmt.Sprintf("Backlog: %d items", backlog), ok
		},
	},
	{
		kind: FactReadiness, category: "readiness", field: "estimated_count", id: "project-status-2-readiness",
		title: func(fields map[string]FactValue) (string, bool) {
			estimated, okEstimated := factInteger(fields, "estimated_count")
			backlog, okBacklog := factInteger(fields, "backlog_size")
			return fmt.Sprintf("Estimate coverage: %d of %d items estimated", estimated, backlog), okEstimated && okBacklog
		},
	},
	{
		kind: FactHealth, category: "health", field: "severity", id: "project-status-3-health",
		title: func(fields map[string]FactValue) (string, bool) {
			value, ok := fields["severity"]
			if !ok || value.String == nil || *value.String == "" {
				return "", false
			}
			return "Risk severity: " + *value.String, true
		},
	},
}

type ProjectStatusDriverEvent struct {
	Emitted        int
	SkippedBudget  int
	SkippedIDClash int
	Missing        []FactKind
	MissingStates  []string
	Partial        []FactKind
	PartialStates  []SourceState
}

func projectStatusUsableState(state SourceState) bool {
	return state == SourceAvailable || state == SourceTruncated || state == SourceStale
}

func projectStatusStateLabel(state SourceState) string {
	switch state {
	case SourceTruncated:
		return " (partial: the source read was truncated)."
	case SourceStale:
		return " (stale: the source read is out of date)."
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

func projectStatusDrivers(interpretation InterpretedQuestion, committed []SubjectRef, facts []CanonicalFact, existingDrivers []DriverJudgment, existingClaims []ClaimedFact, allocation ItemAllocation) ([]DriverJudgment, []ClaimedFact, *ProjectStatusDriverEvent) {
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
	for _, claim := range existingClaims {
		takenClaims[claim.ClaimID] = struct{}{}
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
		missing := func(state string) {
			event.Missing = append(event.Missing, observation.kind)
			event.MissingStates = append(event.MissingStates, state)
		}
		if fact == nil {
			if named == nil {
				missing("not_read")
			} else {
				missing(string(named.SourceState))
			}
			continue
		}
		if len(fact.EvidenceRefIDs) == 0 {
			missing("no_evidence")
			continue
		}
		title, ok := observation.title(fact.Fields)
		claimValue, hasValue := fact.Fields[observation.field]
		if !ok || !hasValue || claimValue.Null {
			missing("no_value")
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
			event.Partial = append(event.Partial, observation.kind)
			event.PartialStates = append(event.PartialStates, fact.SourceState)
		}
		claims = append(claims, ClaimedFact{ClaimID: claimID, Kind: observation.kind, Subject: project, Field: observation.field, Value: convertFactValueScalar(claimValue)})
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
