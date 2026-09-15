package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// captureRetainedRankingFromProduction executes the real ranking and memory
// codec. The historical snapshots were captured with this same mechanism at
// fc75581e377aaef8f7807a37f819272998b484df; current controls execute it afresh.
type retainedRankingCaptureMode struct{ capped, zeroSignal bool }

func captureRetainedRankingFromProduction(t *testing.T, haveFacts bool, modes ...retainedRankingCaptureMode) cf.StoredInvestigationResult {
	mode := retainedRankingCaptureMode{}
	if len(modes) > 0 {
		mode = modes[0]
	}
	t.Helper()
	members := []cf.CohortMember{}
	bundle := cf.CanonicalFactBundle{Facts: []cf.CanonicalFact{}, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}
	for _, kind := range []cf.FactKind{cf.FactInvestment, cf.FactHealth, cf.FactOperationalDeficiencies, cf.FactReadiness, cf.FactWorkload} {
		bundle.Coverage.Sources = append(bundle.Coverage.Sources, cf.SourceObservation{Source: "canonical_fact:" + string(kind), State: cf.SourceAvailable})
	}
	for i := 1; i <= 2; i++ {
		subject := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: fmt.Sprintf("team:surface%d", i), Label: fmt.Sprintf("Team %d", i)}
		members = append(members, cf.CohortMember{Subject: subject, Rank: i, InclusionReasons: []string{"bounded cohort"}})
		if !haveFacts {
			continue
		}
		investment := map[string]cf.FactValue{cf.FactFieldThemeQualityBugfix: cf.NumberFactValue(0.05)}
		for _, theme := range []string{cf.ThemeFeatureDelivery, cf.ThemeOperational, cf.ThemeMaintenance, cf.ThemeQuality, cf.ThemeRisk} {
			investment[cf.FactFieldTheme(theme)] = cf.NumberFactValue(0.2)
		}
		for kind, fields := range map[cf.FactKind]map[string]cf.FactValue{
			cf.FactInvestment: investment, cf.FactHealth: {"severity": cf.StringFactValue("low")}, cf.FactOperationalDeficiencies: {"severity": cf.StringFactValue("high")}, cf.FactReadiness: {"estimate_coverage_ratio": cf.NumberFactValue(0.9)}, cf.FactWorkload: {"forecast_p50_days": cf.IntegerFactValue(8)},
		} {
			bundle.Facts = append(bundle.Facts, cf.CanonicalFact{Kind: kind, Subject: subject, Fields: fields})
		}
	}
	// A pruned deficiency batch has no observed available-zero signal.
	if mode.zeroSignal {
		for i := range bundle.Coverage.Sources {
			if bundle.Coverage.Sources[i].Source == "canonical_fact:operational_deficiencies" {
				bundle.Coverage.Sources[i].State = cf.SourcePruned
			}
		}
	}
	cohort := &cf.Cohort{Kind: cf.SubjectTeam, Members: members, Rationale: "bounded team cohort", Complete: true}
	graph := rankingSurfaceGraph{surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{members[0].Subject}}}, cohort: cohort}
	registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(nil), cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := rankingSurfaceStore{memoryinvestigation.NewStore()}
	next := 0
	trace := &bytes.Buffer{}
	var graphReader cf.GraphReader = graph
	if mode.capped {
		graphReader = retainedRankingCappedGraph{rankingSurfaceGraph: graph}
	}
	deps := cf.EngineDependencies{Telemetry: cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(trace, nil))), Interpreter: rankingSurfaceInterpreter{}, Graph: graphReader, Facts: liveFactReader{bundle: bundle}, Synthesizer: rankingSurfaceSynthesizer{}, Results: store, Requirements: registry}
	options := cf.EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() }, NewResultID: func() string { next++; return fmt.Sprintf("result_ranking_surface_%02d", next) }}
	engine, err := cf.NewEngine(deps, options)
	if err != nil {
		t.Fatal(err)
	}

	request := surfaceRequest("request_retained_producer")
	request.Question = "Rank the team cohort"
	if mode.capped {
		request.Options.MaxCohortMembers = 2
	}
	principal := storage.Principal{OrgID: callerOrgID}
	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SemanticStateRead != cf.SemanticStateReadAvailable {
		t.Fatalf("semantic state=%v", stored.SemanticStateRead)
	}
	if err := stored.Result.ValidateStored(); err != nil {
		t.Fatal(err)
	}
	if stored.Result.Cohort == nil || len(stored.Result.Cohort.Members) != 2 || !stored.Result.Cohort.Members[0].RankingComputed {
		t.Fatalf("real rank did not produce both retained members: %+v", stored.Result.Cohort)
	}

	return stored
}
func TestCaptureRetainedRankingFromProduction(t *testing.T) {
	for _, haveFacts := range []bool{true, false} {
		t.Run(fmt.Sprintf("facts=%v", haveFacts), func(t *testing.T) {
			stored := captureRetainedRankingFromProduction(t, haveFacts)
			rows := 0
			for _, row := range stored.Result.Completeness.Outcomes {
				if row.Obligation == "ranking" && row.Stage == v1.ContextFabricOutcomeStageAssembledResult {
					rows++
				}
			}
			data, err := json.MarshalIndent(stored, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("ranking-facts-%v.json", haveFacts)
			if dir := os.Getenv("CF_RETAINED_CAPTURE_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			} else if rows != 1 {
				t.Fatalf("current producer assembled ranking rows=%d; want1", rows)
			}
			t.Logf("capture=%s sha256=%x bytes=%d assembled_rows=%d", name, sha256.Sum256(data), len(data), rows)
		})
	}
}

// This adapter invokes the actual discovery admission/cap producer during the
// engine's DiscoverContext call; it does not hand-author stored scope flags.
type retainedRankingCappedGraph struct{ rankingSurfaceGraph }

func (g retainedRankingCappedGraph) DiscoverContext(_ context.Context, p storage.Principal, d cf.GraphDiscoveryRequest) (cf.GraphContext, error) {
	nodes := []graphrank.CandidateNode{}
	for i := 1; i <= 3; i++ {
		nodes = append(nodes, graphrank.CandidateNode{Attributes: map[string]any{"subject_kind": "team", "canonical_id": fmt.Sprintf("team:surface%d", i), "label": fmt.Sprintf("Team %d", i)}})
	}
	cohort, _, _, _, _, population := graphrank.DiscoveredCohort(p, d, nodes, false, func(cf.SubjectRef) bool { return false })
	return cf.GraphContext{Cohort: cohort, CohortPopulation: population, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}}, nil
}
func TestCaptureRetainedRankingCappedProduction(t *testing.T) {
	stored := captureRetainedRankingFromProduction(t, true, retainedRankingCaptureMode{capped: true})
	if stored.Result.Cohort.Complete || !stored.Result.Cohort.Truncated {
		t.Fatal("actual discovery cap did not record scope loss")
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("CF_RETAINED_CAPTURE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "ranking-capped.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("capture=ranking-capped.json sha256=%x bytes=%d retained=%d complete=%v truncated=%v", sha256.Sum256(data), len(data), len(stored.Result.Cohort.Members), stored.Result.Cohort.Complete, stored.Result.Cohort.Truncated)
}
