package contextfabric

import (
	"bytes"
	"testing"
)

// RunRequirementOutcomeTransitionScenarioForTest is a test-only export: it
// drives one named scenario through Engine.Investigate with the PRODUCTION slog
// JSON handler, so the external eventspec certification pin (package
// contextfabric_test, which can import eventspec/certify without an import
// cycle) judges the real emitted bytes. It calls the same fixtures the internal
// pins use and re-implements nothing.
func RunRequirementOutcomeTransitionScenarioForTest(t *testing.T, scenario string) (log []byte, requestID string) {
	t.Helper()
	var buf *bytes.Buffer
	switch scenario {
	case "class_b":
		_, buf, requestID = runReconciliationLogged(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, 0)
	case "narrowed_count":
		_, buf, requestID = runReconciliationLogged(t, countingCohort(SubjectTeam, 5), InvestigationComplete, nil, 2)
	case "served_count":
		_, buf, requestID = runReconciliationLogged(t, countingCohort(SubjectTeam, 3), InvestigationComplete, nil, 0)
	case "state_subject_kind_unsupported":
		_, buf, requestID = runNarrowedStateLogged(t, registryDeriver{})
	case "state_no_declaring_producer":
		_, buf, requestID = runNarrowedStateLogged(t, registryDeriver{capabilities: teamStatusWithoutStateCapabilities()})
	default:
		t.Fatalf("unknown requirement outcome transition scenario %q", scenario)
	}
	return buf.Bytes(), requestID
}
