package devhealthfacts

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts/internal/opschangefailure"
)

// opsChangeFailureDigest pins the bytes of the vendored ops rule. A change to
// the vendored file must be a re-vendor from the ops commit named in its
// header, and must pass the executed comparison below.
const opsChangeFailureDigest = "c5e771bd24236e19457f4ebc7a46a9a863753cc01a96c2bab7b6df6c0ebeecd0"

func TestVendoredOpsChangeFailureRuleIsPinned(t *testing.T) {
	raw, err := os.ReadFile("internal/opschangefailure/changefailure.go")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != opsChangeFailureDigest {
		t.Fatalf("vendored ops change failure rule digest %s, pinned %s: re-vendor from ops and re-pin deliberately", got, opsChangeFailureDigest)
	}
}

// The rule is executed against the ops function over a grid of counts: the
// state, the value and the lowest link tier must agree on every point, so a
// drift of either side cannot pass.
func TestChangeFailureRuleAgreesWithTheOpsRuleOnEveryCount(t *testing.T) {
	values := []int64{0, 1, 2, 4}
	points := 0
	for _, stored := range []int64{0, 3} {
		for _, deployments := range values {
			for _, failedNative := range values {
				for _, failedHeuristic := range values {
					for _, direct := range values {
						for _, via := range values {
							mine := evaluateChangeFailure(changeFailureCounts{
								storedRows: stored, deployments: deployments, failedNative: failedNative,
								failedHeuristic: failedHeuristic, incidentsDirect: direct, incidentsViaDeployment: via,
							})
							theirs := opschangefailure.Evaluate(opschangefailure.View{
								Counts: opschangefailure.Counts{
									Deployments: uint64(deployments), FailedNative: uint64(failedNative), FailedHeuristic: uint64(failedHeuristic),
									IncidentsDirect: uint64(direct), IncidentsViaDeployment: uint64(via),
								},
								StoredRows: uint64(stored),
							})
							if mine.state != string(theirs.State) || mine.linkTier != theirs.LinkTier || mine.measured != (theirs.Value != nil) || (theirs.Value != nil && mine.value != *theirs.Value) {
								t.Fatalf("stored=%d dep=%d native=%d heur=%d direct=%d via=%d: acr %+v, ops %+v", stored, deployments, failedNative, failedHeuristic, direct, via, mine, theirs)
							}
							points++
						}
					}
				}
			}
		}
	}
	if points != 2*4*4*4*4*4 {
		t.Fatalf("compared %d points", points)
	}
}

// The known defects the rule exists to prevent, each asserted on its own so a
// comparator change cannot lose them: a deployment with no incident evidence is
// unknown (not 0), no deployment is not applicable (not 0), a stored 0 is a
// measured 0, native and heuristic both count, the window is the sum of counts.
func TestChangeFailureRuleKeepsTheStatesApart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       changeFailureCounts
		state    string
		measured bool
		value    float64
		tier     string
	}{
		{"no stored row", changeFailureCounts{}, "", false, 0, ""},
		{"incidents without a deployment", changeFailureCounts{storedRows: 1, incidentsDirect: 2}, changeFailureStateNotApplicable, false, 0, ""},
		{"deployments without incident evidence", changeFailureCounts{storedRows: 1, deployments: 4}, changeFailureStateUnknown, false, 0, ""},
		{"failed deployments without incident evidence", changeFailureCounts{storedRows: 1, deployments: 4, failedNative: 1}, changeFailureStateUnknown, false, 0, ""},
		{"measured zero", changeFailureCounts{storedRows: 1, deployments: 4, incidentsDirect: 1}, changeFailureStateMeasured, true, 0, ""},
		{"via-deployment incident, heuristic tier", changeFailureCounts{storedRows: 1, deployments: 4, failedHeuristic: 1, incidentsViaDeployment: 1}, changeFailureStateMeasured, true, 0.25, changeFailureTierHeuristic},
		{"native and heuristic both count", changeFailureCounts{storedRows: 2, deployments: 4, failedNative: 1, failedHeuristic: 1, incidentsDirect: 1}, changeFailureStateMeasured, true, 0.5, changeFailureTierHeuristic},
		{"native only", changeFailureCounts{storedRows: 1, deployments: 4, failedNative: 1, incidentsDirect: 1}, changeFailureStateMeasured, true, 0.25, changeFailureTierNative},
	} {
		got := evaluateChangeFailure(tc.in)
		if got.state != tc.state || got.measured != tc.measured || got.value != tc.value || got.linkTier != tc.tier {
			t.Errorf("%s: got %+v, want state %s measured %v value %v tier %q", tc.name, got, tc.state, tc.measured, tc.value, tc.tier)
		}
	}
}

// Counts with no stored row behind them (an ungrouped aggregate over nothing)
// serve no field at all, whatever the sums say.
func TestSetChangeFailureServesNothingForCountsWithNoStoredRow(t *testing.T) {
	fields := map[string]contextfabric.FactValue{}
	setChangeFailure(fields, &changeFailureCounts{deployments: 4, failedNative: 1, incidentsDirect: 1})
	if len(fields) != 0 {
		t.Fatalf("fields served with no stored row: %v", fields)
	}
}
