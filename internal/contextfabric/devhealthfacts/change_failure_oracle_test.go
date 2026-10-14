package devhealthfacts

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/google/uuid"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts/internal/opschangefailure"
)

// opsChangeFailureDigest pins the bytes of the vendored ops rule. A change to
// the vendored files must be a re-vendor from the ops commit named in their
// header (internal/opschangefailure/vendor.sh), and must pass the executed
// comparison below.
var opsVendoredDigests = map[string]string{
	"internal/opschangefailure/changefailure.go":    "af1756cec4e31a9be3fb7903fa7ce6f8bd612a0c22df597a1e8f0c8fb5e8c09e",
	"internal/deploymentstatus/deploymentstatus.go": "3879ba2b16d97118772603bc1fefcdab864fdc40924bcd8b5dc561c0e2464aae",
}

func TestVendoredOpsChangeFailureRuleIsPinned(t *testing.T) {
	for path, want := range opsVendoredDigests {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("vendored %s digest %s, pinned %s: re-vendor from ops and re-pin deliberately", path, got, want)
		}
	}
}

// The rule is executed against the ops function over a grid of counts: the
// state, the value, the lowest link tier, the failed total and the evidence
// basis must agree on every point, so a drift of either side cannot pass.
func TestChangeFailureRuleAgreesWithTheOpsRuleOnEveryCount(t *testing.T) {
	values := []int64{0, 1, 2, 4}
	statusValues := []int64{0, 1, 3}
	points := 0
	for _, stored := range []int64{0, 3} {
		for _, deployments := range values {
			for _, failedNative := range values {
				for _, failedHeuristic := range values {
					for _, direct := range values {
						for _, via := range values {
							for _, failedStatus := range statusValues {
								for _, known := range statusValues {
									mine := evaluateChangeFailure(changeFailureCounts{
										storedRows: stored, deployments: deployments, failedNative: failedNative,
										failedHeuristic: failedHeuristic, incidentsDirect: direct, incidentsViaDeployment: via,
										failedStatus: failedStatus, statusKnown: known, statusColumns: true,
									})
									ops := opschangefailure.Counts{
										Deployments: uint64(deployments), FailedNative: uint64(failedNative), FailedHeuristic: uint64(failedHeuristic),
										IncidentsDirect: uint64(direct), IncidentsViaDeployment: uint64(via),
										FailedStatus: uint64(failedStatus), StatusKnown: uint64(known),
									}
									theirs := opschangefailure.Evaluate(opschangefailure.View{Counts: ops, StoredRows: uint64(stored)})
									if mine.state != string(theirs.State) || mine.linkTier != theirs.LinkTier || mine.basis != theirs.Basis ||
										mine.measured != (theirs.Value != nil) || (theirs.Value != nil && mine.value != *theirs.Value) {
										t.Fatalf("stored=%d dep=%d native=%d heur=%d direct=%d via=%d status=%d known=%d: acr %+v, ops %+v",
											stored, deployments, failedNative, failedHeuristic, direct, via, failedStatus, known, mine, theirs)
									}
									if mine.measured == (mine.reason != "") {
										t.Fatalf("reason %q with measured=%v: a reason is served exactly when no rate is", mine.reason, mine.measured)
									}
									points++
								}
							}
						}
					}
				}
			}
		}
	}
	if points != 2*4*4*4*4*4*3*3 {
		t.Fatalf("compared %d points", points)
	}
}

// The real producer feeds the rule: CountDay of the ops function turns
// statuses, incidents and links into the stored counts, and acr's rule over
// those counts must name what ops names, for the cases the decision is about.
func TestChangeFailureRuleAgreesWithTheOpsRuleOnProducedCounts(t *testing.T) {
	repo := uuid.MustParse("00000000-0000-0000-0000-0000000000f1")
	dep := func(id, status string) opschangefailure.Deployment {
		return opschangefailure.Deployment{RepoID: repo, DeploymentID: id, Status: status}
	}
	tie := []opschangefailure.IncidentTie{{RepoID: repo, IncidentID: "i1"}}
	link := []opschangefailure.Link{{RepoID: repo, DeploymentID: "d1", IncidentID: "i1", Source: opschangefailure.TierNative}}
	for _, tc := range []struct {
		name        string
		deployments []opschangefailure.Deployment
		ties        []opschangefailure.IncidentTie
		links       []opschangefailure.Link
		state       string
		basis       string
		value       float64
	}{
		{"all success is a measured zero", []opschangefailure.Deployment{dep("d1", "success"), dep("d2", "success")}, nil, nil, "measured", "status", 0},
		{"a failed status counts without an incident", []opschangefailure.Deployment{dep("d1", "failure"), dep("d2", "success")}, nil, nil, "measured", "status", 0.5},
		{"canceled is neither failed nor a deployment", []opschangefailure.Deployment{dep("d1", "canceled"), dep("d2", "success")}, nil, nil, "measured", "status", 0},
		{"only canceled has no deployment", []opschangefailure.Deployment{dep("d1", "cancelled")}, nil, nil, "not_applicable_no_deployments", "", 0},
		{"no status and no incident is unknown", []opschangefailure.Deployment{dep("d1", ""), dep("d2", "queued")}, nil, nil, "unknown_no_incident_evidence", "", 0},
		{"a failed deployment with a link counts once", []opschangefailure.Deployment{dep("d1", "failed"), dep("d2", "success")}, tie, link, "measured", "both", 0.5},
		{"incident evidence alone", []opschangefailure.Deployment{dep("d1", "")}, tie, link, "measured", "incident", 1},
	} {
		counts := opschangefailure.CountDay(tc.deployments, tc.ties, tc.links)[repo]
		theirs := opschangefailure.Evaluate(opschangefailure.View{Counts: counts, StoredRows: 1})
		mine := evaluateChangeFailure(changeFailureCounts{
			storedRows: 1, deployments: int64(counts.Deployments), failedNative: int64(counts.FailedNative), failedHeuristic: int64(counts.FailedHeuristic),
			incidentsDirect: int64(counts.IncidentsDirect), incidentsViaDeployment: int64(counts.IncidentsViaDeployment),
			failedStatus: int64(counts.FailedStatus), statusKnown: int64(counts.StatusKnown), statusColumns: true,
		})
		if mine.state != tc.state || string(theirs.State) != tc.state || mine.basis != tc.basis || theirs.Basis != tc.basis {
			t.Errorf("%s: acr %+v ops %+v, want state %s basis %q", tc.name, mine, theirs, tc.state, tc.basis)
		}
		if tc.state == "measured" && (mine.value != tc.value || *theirs.Value != tc.value) {
			t.Errorf("%s: value acr %v ops %v, want %v", tc.name, mine.value, *theirs.Value, tc.value)
		}
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
		{"all success by status is a measured zero", changeFailureCounts{storedRows: 1, deployments: 4, statusKnown: 4, statusColumns: true}, changeFailureStateMeasured, true, 0, ""},
		{"failed by status alone has no link tier", changeFailureCounts{storedRows: 1, deployments: 4, statusKnown: 4, failedStatus: 1, statusColumns: true}, changeFailureStateMeasured, true, 0.25, ""},
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
	setChangeFailure(fields, &changeFailureCounts{deployments: 4, failedNative: 1, incidentsDirect: 1}, true)
	if len(fields) != 0 {
		t.Fatalf("fields served with no stored row: %v", fields)
	}
}
