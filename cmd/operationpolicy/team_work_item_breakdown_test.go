package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-13001: the work-item team breakdown goes to ops with useInvestment false; the investment entries
// keep forcing true. A TEAM x CYCLE_TIME_HOURS call must never reach the investment (work-unit) basis.

func TestTeamWorkItemBreakdownForcesTheWorkItemBasis(t *testing.T) {
	declared := declaredPolicy()
	decl, ok := declared.Served["teamWorkItemBreakdown"]
	if !ok {
		t.Fatal("teamWorkItemBreakdown is not declared")
	}
	use := decl.Variables["batch.useInvestment"]
	if use.Source != directread.SourceForced || string(use.ForcedValue) != "false" {
		t.Fatalf("batch.useInvestment = %+v, want forced false (the work-item basis)", use)
	}
	for _, name := range []string{"investmentBreakdown", "investmentBreakdownTeam", "investmentBreakdownRepository", "investmentFull"} {
		other, present := declared.Served[name]
		if !present {
			continue
		}
		got := other.Variables["batch.useInvestment"]
		if got.Source != directread.SourceForced || string(got.ForcedValue) != "true" {
			t.Errorf("%s: batch.useInvestment = %+v, want forced true: one operation name serves one basis", name, got)
		}
	}
	if dims := decl.Variables["batch.breakdowns[*].dimension"].AllowedValues; len(dims) != 1 || dims[0] != "TEAM" {
		t.Fatalf("dimension allowed = %v, want only TEAM", dims)
	}
	if ms := decl.Variables["batch.breakdowns[*].measure"].AllowedValues; len(ms) != 1 || ms[0] != "CYCLE_TIME_HOURS" {
		t.Fatalf("measure allowed = %v, want only CYCLE_TIME_HOURS", ms)
	}
}

func TestTheGeneratedArtifactServesTeamWorkItemBreakdownToTheUnrestrictedCallerOnly(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join(repoRoot, artifactPath))
	if err != nil {
		t.Fatal(err)
	}
	op, ok := decodeArtifact(t, committed)["teamWorkItemBreakdown"]
	if !ok {
		t.Fatal("the committed artifact has no teamWorkItemBreakdown")
	}
	if !op.Scope(directread.CallerUnrestricted).Served || op.Scope(directread.CallerRestricted).Served {
		t.Fatalf("scopes = %+v, want unrestricted served and restricted refused", op.Scopes)
	}
	rule := ruleAt(t, op, "batch.useInvestment")
	if rule.Source != directread.SourceForced || string(rule.ForcedValue) != "false" {
		t.Fatalf("batch.useInvestment rule = %+v, want forced false", rule)
	}
	var forced any
	if err := json.Unmarshal(rule.ForcedValue, &forced); err != nil || forced != false {
		t.Fatalf("forced value = %s, want false", rule.ForcedValue)
	}
	// CHAOS-13018: a JSON leaf with an output exception passes any key that is not a person-name token, so
	// the evidence-quality JSON outputs (computed only on the investment path) are withheld on this basis.
	for _, path := range []string{"analytics.evidenceQualityDistribution", "analytics.evidenceQualityStats.bandCounts"} {
		if op.OutputAllowed(path) {
			t.Errorf("%s is an allowed output of teamWorkItemBreakdown: a JSON leaf here can carry a person name", path)
		}
		withheld := false
		for _, w := range op.WithheldOutputs {
			withheld = withheld || w.Path == path
		}
		if !withheld {
			t.Errorf("%s is not listed as withheld", path)
		}
	}
	if _, excepted := declaredPolicy().Served["teamWorkItemBreakdown"].OutputExceptions["analytics.evidenceQualityDistribution"]; excepted {
		t.Error("the declaration excepts the evidence-quality JSON output")
	}
}
