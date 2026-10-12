package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const teamBlockersField = "blocked_work_items_current"

type teamBlockersObservation struct {
	team    SubjectRef
	fact    *CanonicalFact
	count   int64
	partial bool
}

type TeamBlockersDriverResult struct {
	Drivers    []DriverJudgment
	Claims     []ClaimedFact
	Limitation string
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
	if projectReached, ok := factInteger(observation.fact.Fields, "project_blocked_work_items_current"); ok && projectReached > 0 {
		title += fmt.Sprintf(", %d of them reached through project-linked repositories", projectReached)
		if basis, has := observation.fact.Fields["repository_link_basis"]; has && basis.String != nil && *basis.String != "" {
			title += " (weakest link tier: " + *basis.String + ")"
		}
	}
	return title
}

func teamBlockersObservations(cohort *Cohort, facts []CanonicalFact) []teamBlockersObservation {
	if cohort == nil || cohort.Kind != SubjectTeam {
		return nil
	}
	var observations []teamBlockersObservation
	for _, member := range cohort.Members {
		if member.Subject.Kind != SubjectTeam {
			continue
		}
		for index := range facts {
			fact := &facts[index]
			if fact.Kind != FactBlockers || fact.Subject.Kind != SubjectTeam || fact.Subject.CanonicalID != member.Subject.CanonicalID ||
				len(fact.EvidenceRefIDs) == 0 || (fact.SourceState != SourceAvailable && fact.SourceState != SourceTruncated) {
				continue
			}
			count, ok := factInteger(fact.Fields, teamBlockersField)
			if !ok || count <= 0 {
				break
			}
			observations = append(observations, teamBlockersObservation{team: member.Subject, fact: fact, count: count, partial: fact.SourceState == SourceTruncated})
			break
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

func teamBlockersDrivers(interpretation InterpretedQuestion, cohort *Cohort, facts []CanonicalFact, existingDrivers []DriverJudgment, existingClaims []ClaimedFact, allocation ItemAllocation) TeamBlockersDriverResult {
	if !teamBlockersIntent(interpretation) {
		return TeamBlockersDriverResult{}
	}
	all := teamBlockersObservations(cohort, facts)
	observations := all[:0:0]
	partial := 0
	for _, observation := range all {
		if observation.partial {
			partial++
			continue
		}
		observations = append(observations, observation)
	}
	var result TeamBlockersDriverResult
	if partial > 0 {
		result.Limitation = fmt.Sprintf("Blocked work items for %d teams come from a partial read and are not listed as a count.", partial)
	}
	if len(observations) == 0 {
		return result
	}
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
	for _, observation := range observations {
		if len(result.Drivers) >= allowed {
			break
		}
		driverID, claimID := teamBlockersDriverID(observation.team), teamBlockersClaimID(observation.team)
		if _, clash := taken[driverID]; clash {
			continue
		}
		if _, clash := takenClaims[claimID]; clash {
			continue
		}
		taken[driverID] = struct{}{}
		title := teamBlockersTitle(observation)
		driver := DriverJudgment{
			DriverID: driverID, Standing: DriverContributing, Category: "blockers",
			Title: title, Summary: title + ".",
			AffectedSubjects: []SubjectRef{observation.team}, EvidenceRefIDs: observation.fact.EvidenceRefIDs,
			Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.95, Current: true,
		}
		value := observation.fact.Fields[teamBlockersField]
		result.Claims = append(result.Claims, ClaimedFact{ClaimID: claimID, Kind: FactBlockers, Subject: observation.team, Field: teamBlockersField, Value: convertFactValueScalar(value)})
		driver.ClaimedFactIDs = []string{claimID}
		result.Drivers = append(result.Drivers, driver)
	}
	if len(result.Drivers) < len(observations) {
		result.Limitation = strings.TrimSpace(result.Limitation + fmt.Sprintf(" Blocked work items are listed for %d of %d teams with blocked work; the teams with the most blocked work come first.", len(result.Drivers), len(observations)))
	}
	return result
}

func teamBlockersDriverCandidates(drivers []DriverJudgment) []DriverJudgment {
	return projectStatusDriverCandidates(drivers)
}
