package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12460. A budget lever's outcome row names no requirement on purpose: the
// cut runs across requirements, and naming one would be a wrong attribution. That
// row can decide the state, and the correction took its outcome with no
// requirement, which the contract refuses: the caller lost the whole answer to a
// 500. The status correction stands; only the deciding pair goes.

// blankRowWriter is one way the real engine appends a row with no requirement.
type blankRowWriter struct {
	name           string
	shape          cf.InvestigationShape
	cohort         bool
	claims         int // claims about the first subject; 0 is one per fact read
	facts          int
	rowsPerClaim   int
	paths          int
	drivers        bool
	maxBytes       int64
	wantCause      v1.ContextFabricBudgetOverrun
	wantLimitation string
}

// The site named is the appendLeverOutcomeRow call that wrote the row.
func blankRowWriters() []blankRowWriter {
	return []blankRowWriter{
		// budget_trim.go:123: the single-subject item trim.
		{name: "budget_trim", shape: cf.ShapeSingleSubject, claims: 31, maxBytes: 65536, wantCause: v1.ContextFabricBudgetOverrunItems, wantLimitation: v1.ContextFabricBudgetTrimClaimedFactsLimitation},
		// claim_depth_narrowing.go:188: the cohort item trim.
		{name: "claim_depth", shape: cf.ShapeDiscoveredCohort, cohort: true, claims: 120, maxBytes: 65536, wantCause: v1.ContextFabricBudgetOverrunItems, wantLimitation: "This answer shows "},
		// path_drop.go:178, the row at :193: the byte lever on paths.
		{name: "path_drop", shape: cf.ShapeDiscoveredCohort, cohort: true, facts: 8, paths: 25, drivers: true, maxBytes: 45000, wantCause: v1.ContextFabricBudgetOverrunBytes, wantLimitation: "paths"},
		// fact_row_truncation.go:402, the row at :378: the byte lever on rows.
		{name: "fact_row_truncation", shape: cf.ShapeDiscoveredCohort, cohort: true, facts: 26, rowsPerClaim: 30, maxBytes: 65536, wantCause: v1.ContextFabricBudgetOverrunBytes, wantLimitation: "table rows"},
	}
}

type blankRowInterpreter struct{ shape cf.InvestigationShape }

func (i blankRowInterpreter) Interpret(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.QuestionFamilyOutcome, error) {
	return cf.InterpretedQuestion{
		Shape: i.shape, RequestedJudgment: "status", TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent},
		FactRequirements: []cf.FactRequirement{{Kind: cf.FactStatus}},
	}, cf.QuestionFamilyOutcome{Family: cf.QuestionFamilyUnclassified, Source: cf.QuestionFamilySourceNone}, nil
}

type blankRowGraph struct {
	surfaceGraph
	cohort *cf.Cohort
	paths  []cf.RelationshipPath
}

func (g blankRowGraph) DiscoverContext(context.Context, storage.Principal, cf.GraphDiscoveryRequest) (cf.GraphContext, error) {
	return cf.GraphContext{
		Cohort: g.cohort, Paths: g.paths, DriverCandidates: []cf.DriverJudgment{}, FactRequirements: []cf.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type blankRowSynthesizer struct {
	writer blankRowWriter
	draft  cf.InvestigationStatus
	teams  []cf.SubjectRef
}

func blankRowClaimRows(fact, count int) []v1.ContextFabricClaimedFactRow {
	rows := make([]v1.ContextFabricClaimedFactRow, 0, count)
	for day := range count {
		date := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day).Format("2006-01-02")
		tally := int64(fact*100 + day)
		state := "open_items_awaiting_review"
		rows = append(rows, v1.ContextFabricClaimedFactRow{Fields: map[string]v1.ContextFabricScalarValue{"day": {String: &date}, "count": {Integer: &tally}, "state": {String: &state}}})
	}
	return rows
}

func (s blankRowSynthesizer) Synthesize(_ context.Context, _ storage.Principal, input cf.SynthesisInput) (cf.InvestigationResult, error) {
	green := "green"
	claims := []cf.ClaimedFact{}
	if s.writer.claims > 0 {
		for index := range s.writer.claims {
			claims = append(claims, cf.ClaimedFact{ClaimID: fmt.Sprintf("claim_%03d", index), Kind: cf.FactStatus, Subject: s.teams[index%len(s.teams)], Field: "status", Value: cf.ScalarValue{String: &green}})
		}
	} else {
		for index, fact := range input.Facts.Facts {
			claim := cf.ClaimedFact{ClaimID: fmt.Sprintf("claim_%03d", index), Kind: fact.Kind, Subject: fact.Subject, Field: "status", Value: cf.ScalarValue{String: &green}}
			if s.writer.rowsPerClaim > 0 {
				claim.Rows = blankRowClaimRows(index, s.writer.rowsPerClaim)
				claim.Table = &v1.ContextFabricClaimedFactTable{Field: "status", Shape: v1.ContextFabricFactTableShapeTimeSeries, Key: []string{"day"}, Measures: []string{"count"}, Observations: []string{"state"}}
			}
			claims = append(claims, claim)
		}
	}
	drivers := []cf.DriverJudgment{}
	if s.writer.drivers {
		for index := range 4 {
			drivers = append(drivers, cf.DriverJudgment{
				DriverID: fmt.Sprintf("driver_blank_%02d", index), Standing: cf.DriverPrincipal, Category: "relationship",
				Title: "Ownership relationship", Summary: "The team appears to own active repositories.", AffectedSubjects: []cf.SubjectRef{s.teams[index%len(s.teams)]},
				PathIDs: []string{blankRowPathID(index * 2), blankRowPathID(index*2 + 1)}, EvidenceRefIDs: []string{fmt.Sprintf("evidence_driver_%02d", index)},
				Derivation: cf.DerivationCanonicalStructured, EpistemicStatus: cf.EpistemicObserved, Confidence: 0.8, Current: true,
			})
		}
	}
	return cf.InvestigationResult{
		Status: s.draft, DirectJudgment: "Two teams lean toward needing attention.", CurrentState: "Review queues appear to be growing.",
		StrongestPressures: []string{}, Drivers: drivers, RemainingWork: []cf.Finding{}, ReadinessGaps: []cf.Finding{},
		Paths: slices.Clone(input.Graph.Paths), Conflicts: []cf.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
		Coverage:            cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Two teams appear to need attention, based on available context.", Warnings: []string{},
		Versions: cf.VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
	}, nil
}

func blankRowPathID(index int) string { return fmt.Sprintf("path_blank_%04d", index) }

func blankRowPaths(teams []cf.SubjectRef, count int) []cf.RelationshipPath {
	paths := []cf.RelationshipPath{}
	for index := range count {
		team := teams[index%len(teams)]
		repository := cf.SubjectRef{Kind: cf.SubjectRepository, CanonicalID: fmt.Sprintf("repo_%02d", index), Label: fmt.Sprintf("full-chaos/repo-%02d", index)}
		evidence := fmt.Sprintf("evidence_path_%02d", index)
		paths = append(paths, cf.RelationshipPath{
			PathID: blankRowPathID(index), Nodes: []cf.SubjectRef{team, repository},
			WhyRelevant: fmt.Sprintf("path %02d: ", index) + strings.Repeat("the team owns this repository and its recent work ", 36),
			Edges: []cf.RelationshipEdge{{
				Type: v1.ContextFabricRelationshipRelatedTo, From: team, To: repository,
				Derivation: cf.DerivationRuleInferred, EpistemicStatus: cf.EpistemicInferred, EvidenceRefIDs: []string{evidence},
			}},
			EvidenceRefIDs: []string{evidence},
		})
	}
	return paths
}

// blankRowApp is the real engine behind the real routes, for the principal a web
// or MCP user holds. Both flags reach the engine and the read by id, as
// cmd/acr-api wires them.
type blankRowApp struct {
	app   *App
	token string
	store *memoryinvestigation.Store
	logs  *lockedBuffer
}

func newBlankRowApp(t *testing.T, writer blankRowWriter, draft cf.InvestigationStatus, authority, symmetric bool, store *memoryinvestigation.Store) *blankRowApp {
	t.Helper()
	teams := []cf.SubjectRef{}
	members := []cf.CohortMember{}
	for rank, id := range []string{"team_platform", "team_payments"} {
		team := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: id, Label: id}
		teams = append(teams, team)
		members = append(members, cf.CohortMember{Subject: team, Rank: rank + 1, InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."}})
	}
	graph := blankRowGraph{paths: blankRowPaths(teams, writer.paths)}
	if writer.cohort {
		graph.cohort = &cf.Cohort{Kind: cf.SubjectTeam, Members: members, Rationale: "chaos-12460", Complete: true}
		graph.surfaceGraph = surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{}}}
	} else {
		graph.surfaceGraph = surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{teams[0]}}}
		teams = teams[:1]
	}
	observed := time.Unix(100, 0).UTC()
	green := "green"
	facts := []cf.CanonicalFact{}
	for index := range writer.facts {
		facts = append(facts, cf.CanonicalFact{
			Kind: cf.FactStatus, Subject: cf.SubjectRef{Kind: cf.SubjectRepository, CanonicalID: fmt.Sprintf("repo_%02d", index), Label: fmt.Sprintf("repo_%02d", index)},
			Fields: map[string]cf.FactValue{"status": {String: &green}}, ObservedAt: &observed, EvidenceRefIDs: []string{fmt.Sprintf("evidence_%02d", index)},
			SourceState: cf.SourceAvailable, Source: "ops", SourceVersion: "ops-v1",
		})
	}
	if store == nil {
		store = memoryinvestigation.NewStore()
	}
	logs := &lockedBuffer{}
	engine, err := cf.NewEngine(cf.EngineDependencies{
		Interpreter: blankRowInterpreter{shape: writer.shape}, Graph: graph,
		Facts: liveFactReader{bundle: cf.CanonicalFactBundle{
			Facts: facts, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}},
			Version: "ops-v1", Versions: map[cf.FactKind]string{}, Watermarks: map[cf.FactKind]string{},
		}},
		Synthesizer: blankRowSynthesizer{writer: writer, draft: draft, teams: teams}, Results: store,
		Telemetry: cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(logs, nil))),
	}, cf.EngineOptions{
		ServiceVersion: "acr-test", MaxItems: 30, MaxSerializedBytes: writer.maxBytes, SynthesisDeadlineReserve: time.Second,
		ServerCompletenessAuthorityEnabled: authority, ServerCompletenessAuthoritySymmetricEnabled: symmetric,
		Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_blank_requirement" },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 1 << 20, MaxBytes: 1 << 24}, logs)
	app.config.ServerCompletenessAuthorityEnabled, app.config.ServerCompletenessAuthoritySymmetricEnabled = authority, symmetric
	app.config.ContextFabricVocabularyLabelsEnabled = true
	app.config.MaxSerializedBytes, app.config.MaxItems = v1.ContextFabricSerializedBytesMax, 500
	serveAsNarrowedUser(t, app)
	return &blankRowApp{app: app, token: token, store: store, logs: logs}
}

func (r *blankRowApp) create(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	request := surfaceRequest("request_blank_requirement")
	request.Options.MaxSerializedBytes = 65536
	return serveJSON(t, r.app, r.token, request)
}

// decodeValid reads a served answer through the contract every consumer holds it
// to: a document the validator refuses is a failure at any status.
func decodeValid(t *testing.T, recorder *httptest.ResponseRecorder) cf.InvestigationResult {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %.400s", recorder.Code, recorder.Body.String())
	}
	var served cf.InvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	if err := cf.ValidateStoredResult(served); err != nil {
		t.Fatalf("the served answer fails its own contract: %v", err)
	}
	return served
}

// assertLeverRowDecides proves the row under test is the deciding row: the only
// row of the document, with an outcome and no requirement, and that the writer
// named is the one that ran.
func assertLeverRowDecides(t *testing.T, served cf.InvestigationResult, writer blankRowWriter) {
	t.Helper()
	if len(served.Completeness.Outcomes) != 1 {
		t.Fatalf("rows = %+v, want the lever's row alone: another row would decide", served.Completeness.Outcomes)
	}
	row := served.Completeness.Outcomes[0]
	if row.Requirement != "" || row.Outcome != v1.ContextFabricRequirementNarrowed || row.CauseOverrun != writer.wantCause {
		t.Fatalf("row = %+v, want a narrowed row with no requirement, caused by %q", row, writer.wantCause)
	}
	if !slices.ContainsFunc(served.Limitations, func(limitation string) bool { return strings.Contains(limitation, writer.wantLimitation) }) {
		t.Fatalf("limitations %q do not hold %q: this is not the writer under test", served.Limitations, writer.wantLimitation)
	}
}

// Each writer, each draft, each flag state: the real engine through the route.
func TestABudgetLeverRowLeavesTheCorrectionWithNoDecidingPair(t *testing.T) {
	for _, writer := range blankRowWriters() {
		for _, draft := range []cf.InvestigationStatus{cf.InvestigationDegraded, cf.InvestigationComplete, cf.InvestigationPartial} {
			for _, flags := range []struct {
				name                 string
				authority, symmetric bool
			}{{"both_on", true, true}, {"symmetric_only", false, true}, {"authority_only", true, false}, {"both_off", false, false}} {
				t.Run(fmt.Sprintf("%s/%s/%s", writer.name, draft, flags.name), func(t *testing.T) {
					rig := newBlankRowApp(t, writer, draft, flags.authority, flags.symmetric, nil)
					served := decodeValid(t, rig.create(t))
					assertLeverRowDecides(t, served, writer)

					// Every lever lowers a complete draft itself, and the row
					// derives partial, so only a degraded draft is left to correct.
					corrected := draft == cf.InvestigationDegraded && flags.symmetric
					wantStatus := cf.InvestigationPartial
					if draft == cf.InvestigationDegraded && !flags.symmetric {
						wantStatus = cf.InvestigationDegraded
					}
					if served.Status != wantStatus {
						t.Fatalf("status = %q, want %q", served.Status, wantStatus)
					}
					if !corrected {
						if served.StatusCorrection != nil {
							t.Fatalf("correction = %+v, want none", served.StatusCorrection)
						}
						return
					}
					want := v1.ContextFabricStatusCorrection{ReplacedStatus: cf.InvestigationDegraded, Direction: v1.ContextFabricStatusCorrectionDegradedToPartial}
					if served.StatusCorrection == nil || *served.StatusCorrection != want {
						t.Fatalf("correction = %+v, want %+v: it stands, with no deciding pair", served.StatusCorrection, want)
					}
					if served.Completeness.StatusCorrection == nil || *served.Completeness.StatusCorrection != want {
						t.Fatalf("mirror = %+v, want %+v", served.Completeness.StatusCorrection, want)
					}
					if got, wantNote := served.VocabularyLabels[v1.ContextFabricStatusCorrectionNoteKey], "The service raised this answer from Degraded answer to Partial answer."; got != wantNote {
						t.Fatalf("note = %q, want the sentence with no clause: %q", got, wantNote)
					}
					// The saved row is the corrected one, and it passed the store's validator.
					stored, err := rig.store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, served.ResultID)
					if err != nil {
						t.Fatal(err)
					}
					if stored.Result.StatusCorrection == nil || *stored.Result.StatusCorrection != want {
						t.Fatalf("stored correction = %+v", stored.Result.StatusCorrection)
					}
				})
			}
		}
	}
}

// No lever, no blank row, nothing to correct, in every flag state.
func TestWithNoLeverNothingIsCorrected(t *testing.T) {
	writer := blankRowWriter{name: "no_lever", shape: cf.ShapeSingleSubject, claims: 2, maxBytes: 65536}
	for _, draft := range []cf.InvestigationStatus{cf.InvestigationDegraded, cf.InvestigationComplete, cf.InvestigationPartial} {
		for _, flags := range [][2]bool{{true, true}, {false, true}, {true, false}, {false, false}} {
			t.Run(fmt.Sprintf("%s/authority=%v/symmetric=%v", draft, flags[0], flags[1]), func(t *testing.T) {
				served := decodeValid(t, newBlankRowApp(t, writer, draft, flags[0], flags[1], nil).create(t))
				if served.Status != draft || served.StatusCorrection != nil {
					t.Fatalf("status %q correction %+v, want %q and none", served.Status, served.StatusCorrection, draft)
				}
			})
		}
	}
}

// The operator keeps what decided: the Info line is an account of the decision,
// not the contract's pair, so it states the blank requirement and the outcome.
func TestTheAuthorityLineKeepsTheOutcomeOfABlankDecidingRow(t *testing.T) {
	writer := blankRowWriters()[0]
	rig := newBlankRowApp(t, writer, cf.InvestigationDegraded, true, true, nil)
	decodeValid(t, rig.create(t))
	found := false
	for _, line := range strings.Split(rig.logs.String(), "\n") {
		if !strings.Contains(line, `"msg":"context fabric completeness authority"`) {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatal(err)
		}
		found = true
		if fields["deciding_outcome"] != string(v1.ContextFabricRequirementNarrowed) || fields["deciding_requirement"] != "" {
			t.Fatalf("line states requirement %q outcome %q", fields["deciding_requirement"], fields["deciding_outcome"])
		}
		if fields["status_correction_disclosed"] != true {
			t.Fatalf("line does not disclose the correction: %s", line)
		}
	}
	if !found {
		t.Fatalf("no authority line:\n%s", rig.logs.String())
	}
}

// A row saved with the symmetric flag off and read with it on is corrected at the
// door. The by-id route does not re-validate a stored document, so the bad pair
// was served to the caller, whose validator refuses it, and not caught here.
func TestAStoredRowCorrectedOnReadCarriesNoDecidingPair(t *testing.T) {
	for _, writer := range blankRowWriters() {
		t.Run(writer.name, func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			saved := decodeValid(t, newBlankRowApp(t, writer, cf.InvestigationDegraded, false, false, store).create(t))
			if saved.Status != cf.InvestigationDegraded || saved.StatusCorrection != nil {
				t.Fatalf("saved status %q correction %+v", saved.Status, saved.StatusCorrection)
			}
			reader := newBlankRowApp(t, writer, cf.InvestigationDegraded, false, true, store)
			recorder := httptest.NewRecorder()
			reader.app.Handler().ServeHTTP(recorder, investigationResultRequest(t, reader.token, saved.ResultID))
			served := decodeValid(t, recorder)
			want := v1.ContextFabricStatusCorrection{ReplacedStatus: cf.InvestigationDegraded, Direction: v1.ContextFabricStatusCorrectionDegradedToPartial}
			if served.Status != cf.InvestigationPartial || served.StatusCorrection == nil || *served.StatusCorrection != want {
				t.Fatalf("status %q correction %+v, want partial and %+v", served.Status, served.StatusCorrection, want)
			}
		})
	}
}
