package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const teamBlockersField = "blocked_work_items_current"

type teamBlockersObservation struct {
	team    SubjectRef
	fact    *CanonicalFact
	count   int64
	partial bool
}

type TeamBlockersDriverEvent struct {
	Emitted          int
	TruncatedServed  int
	SkippedMissing   int
	SkippedZero      int
	SkippedBudget    int
	SkippedIDClash   int
	SkippedStale     int
	SkippedDuplicate int
}

type TeamBlockersDriverResult struct {
	Drivers    []DriverJudgment
	Claims     []ClaimedFact
	Limitation string
	Event      *TeamBlockersDriverEvent
}

func teamBlockersIntent(interpretation InterpretedQuestion) bool {
	for _, requirement := range interpretation.FactRequirements {
		if requirement.Kind == FactBlockers {
			return true
		}
	}
	return false
}

func teamBlockersDriverID(team SubjectRef) string {
	digest := sha256.Sum256([]byte(team.CanonicalID))
	return "team-blockers-" + hex.EncodeToString(digest[:])[:16]
}

func teamBlockersClaimID(team SubjectRef) string {
	digest := sha256.Sum256([]byte(team.CanonicalID + "\x00" + string(FactBlockers) + "\x00" + teamBlockersField))
	return "claim_team_blockers_" + hex.EncodeToString(digest[:])[:32]
}

func teamBlockersTitle(observation teamBlockersObservation) string {
	title := fmt.Sprintf("Blocked work items: %d", observation.count)
	if observation.partial {
		title = fmt.Sprintf("At least %d blocked work items (partial read)", observation.count)
	}
	if projectReached, ok := factInteger(observation.fact.Fields, "project_blocked_work_items_current"); ok && projectReached > 0 && !observation.partial {
		title += fmt.Sprintf(", %d of them reached through project-linked repositories", projectReached)
		if basis, has := observation.fact.Fields["repository_link_basis"]; has && basis.String != nil && *basis.String != "" {
			title += " (weakest link tier over its counted links: " + *basis.String + ")"
		}
	}
	return title
}

func teamBlockersObservations(cohort *Cohort, facts []CanonicalFact, event *TeamBlockersDriverEvent) []teamBlockersObservation {
	if cohort == nil || cohort.Kind != SubjectTeam {
		return nil
	}
	var observations []teamBlockersObservation
	for _, member := range cohort.Members {
		if member.Subject.Kind != SubjectTeam {
			continue
		}
		found, stale := false, false
		for index := range facts {
			fact := &facts[index]
			if fact.Kind != FactBlockers || fact.Subject.Kind != SubjectTeam || fact.Subject.CanonicalID != member.Subject.CanonicalID {
				continue
			}
			if fact.SourceState == SourceStale {
				stale = true
				continue
			}
			if len(fact.EvidenceRefIDs) == 0 || (fact.SourceState != SourceAvailable && fact.SourceState != SourceTruncated) {
				continue
			}
			found = true
			count, ok := factInteger(fact.Fields, teamBlockersField)
			switch {
			case !ok:
				event.SkippedMissing++
			case count <= 0:
				event.SkippedZero++
			default:
				observations = append(observations, teamBlockersObservation{team: member.Subject, fact: fact, count: count, partial: fact.SourceState == SourceTruncated})
			}
			break
		}
		switch {
		case found:
		case stale:
			event.SkippedStale++
		default:
			event.SkippedMissing++
		}
	}
	sort.SliceStable(observations, func(i, j int) bool {
		if observations[i].count != observations[j].count {
			return observations[i].count > observations[j].count
		}
		return observations[i].team.Label < observations[j].team.Label
	})
	return observations
}

func teamBlockersDrivers(interpretation InterpretedQuestion, cohort *Cohort, facts []CanonicalFact, existingDrivers []DriverJudgment, existingClaims []ClaimedFact, allocation ItemAllocation, narrationItemsSpent int) TeamBlockersDriverResult {
	if !teamBlockersIntent(interpretation) || cohort == nil || cohort.Kind != SubjectTeam {
		return TeamBlockersDriverResult{}
	}
	event := &TeamBlockersDriverEvent{}
	result := TeamBlockersDriverResult{Event: event}
	observations := teamBlockersObservations(cohort, facts, event)
	if len(observations) == 0 {
		return result
	}
	allowed := contractsv1.ContextFabricDriversMaxCount - len(existingDrivers)
	if claimRoom := contractsv1.ContextFabricClaimedFactsMaxCount - len(existingClaims); claimRoom < allowed {
		allowed = claimRoom
	}
	if allocation.InForce() {
		if grant := (allocation.NarrationGrant - narrationItemsSpent) / 2; grant < allowed {
			allowed = grant
		}
		if allowed < 0 {
			allowed = 0
		}
	}
	taken := make(map[string]struct{}, len(existingDrivers))
	covered := make(map[SubjectRef]struct{})
	for _, driver := range existingDrivers {
		taken[driver.DriverID] = struct{}{}
		if driver.Category == "blockers" {
			for _, subject := range driver.AffectedSubjects {
				covered[SubjectRef{Kind: subject.Kind, CanonicalID: subject.CanonicalID}] = struct{}{}
			}
		}
	}
	takenClaims := make(map[string]struct{}, len(existingClaims))
	for _, claim := range existingClaims {
		takenClaims[claim.ClaimID] = struct{}{}
	}
	for rank, observation := range observations {
		if len(result.Drivers) >= allowed {
			event.SkippedBudget++
			continue
		}
		if _, done := covered[SubjectRef{Kind: observation.team.Kind, CanonicalID: observation.team.CanonicalID}]; done {
			event.SkippedDuplicate++
			continue
		}
		driverID, claimID := teamBlockersDriverID(observation.team), teamBlockersClaimID(observation.team)
		_, driverClash := taken[driverID]
		_, claimClash := takenClaims[claimID]
		if driverClash || claimClash {
			event.SkippedIDClash++
			continue
		}
		taken[driverID] = struct{}{}
		title := teamBlockersTitle(observation)
		driver := DriverJudgment{
			DriverID: driverID, Standing: DriverContributing, Category: "blockers",
			Rank: rank + 1, Title: title, Summary: title + ".",
			AffectedSubjects: []SubjectRef{observation.team}, EvidenceRefIDs: observation.fact.EvidenceRefIDs,
			Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.95, Current: true,
		}
		if observation.partial {
			driver.Qualification = "The read was cut short, so the real count may be higher."
			event.TruncatedServed++
		}
		value := observation.fact.Fields[teamBlockersField]
		result.Claims = append(result.Claims, ClaimedFact{ClaimID: claimID, Kind: FactBlockers, Subject: observation.team, Field: teamBlockersField, Value: convertFactValueScalar(value)})
		driver.ClaimedFactIDs = []string{claimID}
		result.Drivers = append(result.Drivers, driver)
	}
	event.Emitted = len(result.Drivers)
	if event.SkippedBudget > 0 {
		result.Limitation = fmt.Sprintf("Blocked work items are listed for %d of %d teams with blocked work; the teams with the most blocked work come first.", len(result.Drivers)+event.SkippedDuplicate, len(observations))
	}
	return result
}

func teamBlockersDriverCandidates(drivers []DriverJudgment) []DriverJudgment {
	return projectStatusDriverCandidates(drivers)
}
