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
	Emitted int
	Skipped int
	Missing []FactKind
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

func projectStatusDrivers(interpretation InterpretedQuestion, committed []SubjectRef, facts []CanonicalFact, existingDrivers []DriverJudgment, existingClaims int, allocation ItemAllocation) ([]DriverJudgment, []ClaimedFact, ProjectStatusDriverEvent) {
	var event ProjectStatusDriverEvent
	if len(committed) != 1 || committed[0].Kind != SubjectProject || !projectStatusIntent(interpretation) {
		return nil, nil, event
	}
	project := committed[0]
	allowed := contractsv1.ContextFabricDriversMaxCount - len(existingDrivers)
	if claimRoom := contractsv1.ContextFabricClaimedFactsMaxCount - existingClaims; claimRoom < allowed {
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
	var drivers []DriverJudgment
	var claims []ClaimedFact
	for _, observation := range projectStatusObservations {
		var fact *CanonicalFact
		for index := range facts {
			candidate := &facts[index]
			if candidate.Kind == observation.kind && candidate.Subject.Kind == project.Kind && candidate.Subject.CanonicalID == project.CanonicalID &&
				candidate.SourceState == SourceAvailable {
				fact = candidate
				break
			}
		}
		if fact == nil || len(fact.EvidenceRefIDs) == 0 {
			event.Missing = append(event.Missing, observation.kind)
			continue
		}
		title, ok := observation.title(fact.Fields)
		claimValue, hasValue := fact.Fields[observation.field]
		if !ok || !hasValue || claimValue.Null {
			event.Missing = append(event.Missing, observation.kind)
			continue
		}
		if len(drivers) >= allowed {
			event.Skipped++
			continue
		}
		driverID := observation.id
		if _, clash := taken[driverID]; clash {
			event.Skipped++
			continue
		}
		taken[driverID] = struct{}{}
		claimID := projectStatusClaimID(project, observation)
		claims = append(claims, ClaimedFact{ClaimID: claimID, Kind: observation.kind, Subject: project, Field: observation.field, Value: convertFactValueScalar(claimValue)})
		drivers = append(drivers, DriverJudgment{
			DriverID: driverID, Standing: DriverPrincipal, Category: observation.category,
			Title: title, Summary: title + ".",
			AffectedSubjects: []SubjectRef{project}, EvidenceRefIDs: fact.EvidenceRefIDs, ClaimedFactIDs: []string{claimID},
			Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.95, Current: true,
		})
	}
	event.Emitted = len(drivers)
	return drivers, claims, event
}

// projectStatusDriverCandidates are the drivers shown to a client that writes
// its own answer. They hold no claim id and use the narrative category, so a
// path that serves a candidate verbatim serves a driver that validates by
// itself; the minted driver, with its claim, only exists after synthesis.
func projectStatusDriverCandidates(drivers []DriverJudgment) []DriverJudgment {
	candidates := make([]DriverJudgment, 0, len(drivers))
	for _, driver := range drivers {
		driver.DriverID += "-candidate"
		driver.Category = string(contractsv1.ContextFabricDriverCategoryNarrative)
		driver.ClaimedFactIDs = nil
		candidates = append(candidates, driver)
	}
	return candidates
}
