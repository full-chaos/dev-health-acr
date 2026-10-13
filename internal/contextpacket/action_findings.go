package contextpacket

import (
	"context"
	"sort"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	ActionUnavailableNoSource   = "no_stored_recommendation_source"
	ActionUnavailableNoOwner    = "owning_team_unknown"
	ActionUnavailableNoGrant    = "owning_team_not_readable"
	ActionUnavailableReadFailed = "stored_findings_read_failed"
	ActionUnavailableNoneFired  = "no_fired_finding_for_owning_team"
	actionSourceVersion         = "operational_deficiencies.v1"
)

// ActionFinding is one stored, rule-fired finding of an owning team. It is
// read, never generated: the rule engine that fired it lives in the producer.
type ActionFinding struct {
	RuleID, Severity, Title, SuccessCriterion string
}

// TeamActionFindings groups one owning team's stored findings with the
// evidence ref of the team row they are about.
type TeamActionFindings struct {
	TeamID        string
	EvidenceRefID string
	ObservedAt    time.Time
	Findings      []ActionFinding
}

// ActionFindingsResult carries the findings served for a repository, or the
// closed reason none are.
type ActionFindingsResult struct {
	Teams  []TeamActionFindings
	Reason string
}

// ActionFindingsSource reads the stored findings of the team(s) that own a
// repository. Implementations decide the caller's grant to read them.
type ActionFindingsSource interface {
	ActionFindings(ctx context.Context, principal storage.Principal, repoID string) (ActionFindingsResult, error)
}

var findingSeverityRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "warning": 2, "low": 1, "info": 0}

func actionFindingEvidence(team TeamActionFindings, observedFallback time.Time) (contractsv1.EvidenceRef, bool) {
	if len(team.Findings) == 0 || team.EvidenceRefID == "" {
		return contractsv1.EvidenceRef{}, false
	}
	findings := append([]ActionFinding(nil), team.Findings...)
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := findingSeverityRank[strings.ToLower(findings[i].Severity)], findingSeverityRank[strings.ToLower(findings[j].Severity)]
		if ri != rj {
			return ri > rj
		}
		return findings[i].RuleID < findings[j].RuleID
	})
	parts := make([]string, 0, 3)
	for index, finding := range findings {
		if index == 3 {
			break
		}
		text := strings.TrimSpace(finding.Title)
		if text == "" {
			text = finding.RuleID
		}
		if criterion := strings.TrimSpace(finding.SuccessCriterion); criterion != "" {
			text += " (stated success criterion: " + criterion + ")"
		}
		parts = append(parts, text)
	}
	observed := team.ObservedAt
	if observed.IsZero() {
		observed = observedFallback
	}
	label := truncateRunes("Stored finding for the owning team: "+parts[0], 500)
	return contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: team.EvidenceRefID,
		Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: "recommendation", EntityID: team.TeamID, DisplayLabel: label},
		Provenance: "native", Confidence: 1, Citation: truncateRunes("Operations rules fired for the owning team; this suggests: "+strings.Join(parts, "; "), 1900),
		ObservedAt: observed, Availability: contractsv1.EvidenceAvailable, SourceVersion: actionSourceVersion,
	}, true
}
