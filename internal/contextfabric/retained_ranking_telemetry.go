package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

const RetainedRankingAccountingLogMessage = "context fabric retained ranking accounting"

// RetainedRankingAccountingEvent reports recorded member qualification, not a
// new RankCohort execution or an estimate of the original population. Missing
// or contradictory qualification is counted explicitly. ExistingRow takes
// precedence over RowAdded, including when old qualification is unavailable.
type RetainedRankingAccountingEvent struct {
	Requirement           string
	SubjectKind           SubjectKind
	ExistingRow           bool
	QualificationRecorded bool
	RowAdded              bool
	AssembledOutcome      contractsv1.ContextFabricPlanRequirementOutcome
	RetainedMembers       int
	Qualified             int
	Provisional           int
	InsufficientEvidence  int
	NotApplicable         int
	UnrecordedMembers     int
	Index                 int
	Total                 int
}

func RetainedRankingAccountingLogArgs(event RetainedRankingAccountingEvent, orgID string) []any {
	outcome := string(event.AssembledOutcome)
	if outcome == "" {
		outcome = "none"
	}
	return []any{
		"org_id", SanitizeLogAttr(orgID),
		"requirement", SanitizeLogAttr(event.Requirement),
		"subject_kind", SanitizeLogAttr(string(event.SubjectKind)),
		"existing_row", event.ExistingRow,
		"qualification_recorded", event.QualificationRecorded,
		"row_added", event.RowAdded,
		"assembled_outcome", SanitizeLogAttr(outcome),
		"retained_members", event.RetainedMembers,
		"qualified", event.Qualified,
		"provisional", event.Provisional,
		"insufficient_evidence", event.InsufficientEvidence,
		"not_applicable", event.NotApplicable,
		"unrecorded_members", event.UnrecordedMembers,
		"index", event.Index, "total", event.Total,
	}
}
