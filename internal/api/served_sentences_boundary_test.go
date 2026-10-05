package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The server writes some answer sentences itself: the period total with the
// days it rests on, and the count of a member set. They are stored on the
// result's deterministic answer. These tests read what an MCP client receives
// -- the structured JSON and the markdown of the tool call -- from the real
// engine behind the real hosted route and the real MCP server. None builds
// the sentence it asserts on, and none stops at the engine's own value.

var servedSentencesNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

type servedSentencesScenario struct {
	frame      *contextfabric.QuestionFrame
	resolution contextfabric.SubjectResolution
	bases      contextfabric.CommitBasisSet
	cohort     *contextfabric.Cohort
	facts      servedSentencesFacts
	window     contractsv1.ContextFabricRelativeWindowID
	// modelFails makes the answer-writing model call fail: the answer is then
	// served without any model text.
	modelFails bool
}

type servedSentencesFacts struct {
	capabilities []contextfabric.FactCapability
	facts        []contextfabric.CanonicalFact
	read         contextfabric.FactReadSubjects
}

func (r servedSentencesFacts) Capabilities() []contextfabric.FactCapability { return r.capabilities }

func (r servedSentencesFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{
		Facts: r.facts, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[contextfabric.FactKind]string{}, Watermarks: map[contextfabric.FactKind]string{},
		ReadSubjects: r.read,
	}, nil
}

type servedSentencesInterpreter struct {
	frame *contextfabric.QuestionFrame
}

func (i servedSentencesInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "count",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{},
	}, contextfabric.QuestionFamilyOutcome{
		Frame: i.frame, FrameObligations: i.frame.Obligations,
		Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel,
		WinningSampleIndex: 0, WinningSample: contextfabric.FamilySample{},
	}, nil
}

type servedSentencesGraph struct {
	resolution contextfabric.SubjectResolution
	bases      contextfabric.CommitBasisSet
	cohort     *contextfabric.Cohort
}

func (g servedSentencesGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "served-sentences-key", Epoch: 0}, nil
}

func (g servedSentencesGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return g.resolution, contextfabric.StructureOfferMaterial{}, g.bases, nil, nil
}

func (g servedSentencesGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{
		Cohort: g.cohort, Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

func (g servedSentencesGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range outcomes {
		outcomes[index] = contextfabric.StoredSubjectAdmitted
	}
	return outcomes, nil
}

// servedSentencesModel writes the answer with no claim at all, or fails the way
// prod's did (an output the service cannot read, served as a degraded answer): the
// model's output is never what carries the server's sentences.
type servedSentencesModel struct{ fails bool }

func (m servedSentencesModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

func (m servedSentencesModel) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	if m.fails {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelOutput
	}
	return contextfabric.SynthesisDraft{
		Status: contextfabric.InvestigationPartial, DirectJudgment: "The counted activity appears below the whole period.", CurrentState: "Coverage appears to be partial.",
		StrongestPressures: []string{}, Drivers: []contextfabric.DriverJudgment{}, RemainingWork: []contextfabric.Finding{},
		ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, ClaimedFacts: []contextfabric.ClaimedFact{},
		DeterministicAnswer: "model prose placeholder", Warnings: []string{},
	}, clientRouteReceipt(contextfabric.ModelOperationSynthesize), nil
}

type servedSentencesRig struct {
	boot  *acrmcp.Bootstrap
	store *memoryinvestigation.Store
}

func newServedSentencesRig(t *testing.T, scenario servedSentencesScenario) *servedSentencesRig {
	t.Helper()
	store := memoryinvestigation.NewStore()
	model := servedSentencesModel{fails: scenario.modelFails}
	synthesizer := contextfabric.RuntimeAnswerSynthesizer{
		Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "served-sentences", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
	}
	capabilities := scenario.facts.capabilities
	nextID := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  servedSentencesInterpreter{frame: scenario.frame},
		Graph:        servedSentencesGraph{resolution: scenario.resolution, bases: scenario.bases, cohort: scenario.cohort},
		Facts:        scenario.facts,
		Synthesizer:  synthesizer,
		Results:      store,
		Requirements: servedSentencesDeriver{capabilities: capabilities},
	}, contextfabric.EngineOptions{
		ServiceVersion: "served-sentences",
		Now:            func() time.Time { return servedSentencesNow },
		NewResultID: func() string {
			nextID++
			return fmt.Sprintf("result_served_sent%02d", nextID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, &bytes.Buffer{})
	app.config.RequestTimeout = time.Minute
	server := httptest.NewTLSServer(app.InstrumentedHandler(app.Handler()))
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("real MCP bootstrap: %v", err)
	}
	assertAdvertised(t, boot.Capabilities.EnabledTools, "investigate_question", "investigation_result")
	return &servedSentencesRig{boot: boot, store: store}
}

type servedSentencesDeriver struct {
	capabilities []contextfabric.FactCapability
}

func (d servedSentencesDeriver) DeriveRequirements(frame contextfabric.QuestionFrame) []contextfabric.DerivedRequirement {
	return contextfabric.DeriveRequirements(frame, contextfabric.GenerateObligationSeed(d.capabilities), d.capabilities)
}

// servedAnswer is what a client of one tool call receives.
type servedAnswer struct {
	structured json.RawMessage
	markdown   string
}

func callRealMCPTool(t *testing.T, boot *acrmcp.Bootstrap, name string, arguments any) servedAnswer {
	t.Helper()
	ctx := context.Background()
	server := acrmcp.NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "served-sentences-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	defer clientSession.Close()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	called, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: json.RawMessage(encoded)})
	if err != nil {
		t.Fatalf("%s call: %v", name, err)
	}
	if called.IsError {
		t.Fatalf("%s reported an error: %s", name, mustRawJSON(t, called.Content))
	}
	var text strings.Builder
	for _, content := range called.Content {
		if block, ok := content.(*mcpsdk.TextContent); ok {
			text.WriteString(block.Text)
		}
	}
	return servedAnswer{structured: mustRawJSON(t, called.StructuredContent), markdown: text.String()}
}

// ask puts the question the way a client does: one tool call, an evidence
// window by its relative id.
func (r *servedSentencesRig) ask(t *testing.T, question string, window contractsv1.ContextFabricRelativeWindowID) servedAnswer {
	t.Helper()
	return callRealMCPTool(t, r.boot, "investigate_question", contractsv1.MCPInvestigateQuestionRequest{
		Question:       question,
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: window},
	})
}

// field reads a dotted path out of the structured payload the client receives.
func (a servedAnswer) field(t *testing.T, path ...string) any {
	t.Helper()
	var node any
	if err := json.Unmarshal(a.structured, &node); err != nil {
		t.Fatal(err)
	}
	for _, key := range path {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = object[key]
	}
	return node
}

func (a servedAnswer) servedText(t *testing.T) string {
	t.Helper()
	text, _ := a.field(t, "structured", "deterministic_answer").(string)
	return text
}

func servedSentencesDailyColumns() []contextfabric.FactColumnDeclaration {
	return []contextfabric.FactColumnDeclaration{
		{Name: "day", Type: contextfabric.FactFieldString},
		{Name: "commits_count", Type: contextfabric.FactFieldInteger, Unit: "count", Additivity: contextfabric.FactAdditive},
		{Name: "prs_merged", Type: contextfabric.FactFieldInteger, Unit: "count", Additivity: contextfabric.FactAdditive},
		{Name: "bus_factor", Type: contextfabric.FactFieldInteger, Unit: "count", Additivity: contextfabric.FactNonAdditive},
	}
}

func servedSentencesCapabilities() []contextfabric.FactCapability {
	return []contextfabric.FactCapability{
		{Kind: contextfabric.FactMetrics, Fields: []contextfabric.FactFieldDeclaration{
			{Name: "daily_metrics", Type: contextfabric.FactFieldTable, DailySeries: true, Columns: servedSentencesDailyColumns()},
		}},
		{Kind: contextfabric.FactHealth, Fields: []contextfabric.FactFieldDeclaration{
			{Name: "daily_health", Type: contextfabric.FactFieldTable, DailySeries: true, Columns: []contextfabric.FactColumnDeclaration{
				{Name: "day", Type: contextfabric.FactFieldString},
				{Name: "compounding_risk", Type: contextfabric.FactFieldNumber, Additivity: contextfabric.FactNonAdditive},
			}},
		}},
	}
}

var servedSentencesRepository = contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:NAMED_ONE", Label: "named one"}

func servedSentencesCandidate(subject contextfabric.SubjectRef) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_named_one", Subject: subject, State: contextfabric.ResolutionProposed,
		MatchedTerms: []string{"a"}, MatchReasons: []string{"Repository/project alias matched."}, Confidence: 1,
		EvidenceRefIDs: []string{"evidence_identity_1234"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias, contextfabric.MatchLexical},
	}
}

func servedSentencesCohort(kind contextfabric.SubjectKind, size int) *contextfabric.Cohort {
	members := make([]contextfabric.CohortMember, 0, size)
	for index := 0; index < size; index++ {
		members = append(members, contextfabric.CohortMember{
			Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: fmt.Sprintf("%s:COUNTED_%c", kind, 'A'+index), Label: fmt.Sprintf("Counted %c", 'A'+index)},
			Rank:    index + 1, InclusionReasons: []string{"matched"},
		})
	}
	return &contextfabric.Cohort{Kind: kind, Rationale: "scope census match", Members: members, Complete: true}
}

func servedSentencesCountFrame(memberKind contextfabric.SubjectKind) *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: memberKind},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

// repositoryCommitsScenario is the prod question: how many commits did one
// named repository have over a trailing window.
func repositoryCommitsScenario(facts ...contextfabric.CanonicalFact) servedSentencesScenario {
	return servedSentencesScenario{
		frame:      servedSentencesCountFrame(contextfabric.SubjectRepository),
		resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{servedSentencesRepository}, Candidates: []contextfabric.SubjectCandidate{servedSentencesCandidate(servedSentencesRepository)}},
		bases:      contextfabric.CommitBasisSet{},
		cohort:     servedSentencesCohort(contextfabric.SubjectRepository, 5),
		facts:      servedSentencesFacts{capabilities: servedSentencesCapabilities(), facts: facts},
		window:     contractsv1.ContextFabricRelativeWindowTrailing30D,
	}
}

type servedDayRow struct {
	day     string
	commits int64
	merged  int64
}

func servedMetricsFact(rows []servedDayRow) contextfabric.CanonicalFact {
	valueRows := make([]contextfabric.FactValueRow, 0, len(rows))
	for _, row := range rows {
		valueRows = append(valueRows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			"day":           contextfabric.StringFactValue(row.day),
			"commits_count": contextfabric.IntegerFactValue(row.commits),
			"prs_merged":    contextfabric.IntegerFactValue(row.merged),
			"bus_factor":    contextfabric.IntegerFactValue(3),
		}})
	}
	return contextfabric.CanonicalFact{
		Kind: contextfabric.FactMetrics, Subject: servedSentencesRepository, EvidenceRefIDs: []string{"evidence_1"},
		SourceState: contextfabric.SourceAvailable, Source: "devhealthfacts.metrics", SourceVersion: "v1",
		Fields: map[string]contextfabric.FactValue{"daily_metrics": contextfabric.TableFactValue(contextfabric.FactTable{
			Shape: contextfabric.FactTableTimeSeries, Key: []string{"day"},
			Measures: []string{"commits_count", "prs_merged", "bus_factor"},
			Rows:     valueRows,
		})},
	}
}

// completedDays lists, independently of the production arithmetic, the n most
// recent completed UTC days before the day the reported window ends in.
func completedDays(t *testing.T, answer servedAnswer, n int) []string {
	t.Helper()
	endText, _ := answer.field(t, "structured", "effective_evidence_window", "end").(string)
	end, err := time.Parse(time.RFC3339Nano, endText)
	if err != nil {
		t.Fatalf("answer reports no window end: %q", endText)
	}
	today := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	var days []string
	for i := n; i >= 1; i-- {
		days = append(days, today.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return days
}

// assertServed requires the sentence in the JSON and in the markdown the client
// reads, labelled as computed by the server and not as model text.
func assertServed(t *testing.T, answer servedAnswer, want ...string) {
	t.Helper()
	served := answer.servedText(t)
	if served == "" {
		t.Fatalf("the answer carries no structured.deterministic_answer; the client cannot read the server's sentences. structured = %s", answer.structured)
	}
	for _, sentence := range want {
		if !strings.Contains(served, sentence) {
			t.Errorf("structured.deterministic_answer = %q, want it to contain %q", served, sentence)
		}
		if !strings.Contains(answer.markdown, sentence) {
			t.Errorf("the markdown the client reads lacks %q:\n%s", sentence, answer.markdown)
		}
	}
	if !strings.Contains(answer.markdown, "computed by the server from stored facts, not written by a model") {
		t.Errorf("the markdown does not label the server-computed sentences:\n%s", answer.markdown)
	}
}

func TestAClientReadsTheThirtyDayTotalOfOneRepositoryInTheMCPAnswer(t *testing.T) {
	probe := newServedSentencesRig(t, repositoryCommitsScenario()).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	period := completedDays(t, probe, 30)
	var rows []servedDayRow
	var want int64
	for i, day := range period {
		row := servedDayRow{day: day, commits: int64(3 + i%5), merged: 1}
		rows = append(rows, row)
		want += row.commits
	}
	if len(period) != 30 {
		t.Fatalf("the last 30 days lists %d days", len(period))
	}
	// A row for today's partial day exists and is not in the total.
	rows = append(rows, servedDayRow{day: "2026-10-04", commits: 1000, merged: 1})
	rig := newServedSentencesRig(t, repositoryCommitsScenario(servedMetricsFact(rows)))
	answer := rig.ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	assertServed(t, answer,
		fmt.Sprintf("Total of commits count over the period: %d, summed from %d of %d days", want, len(period), len(period)),
		fmt.Sprintf("The period is the %d most recent completed UTC days, %s to %s; today's partial day is not in the total.", len(period), period[0], period[len(period)-1]),
	)
	if strings.Contains(answer.servedText(t), "Partial total") {
		t.Errorf("a full period is stated as partial: %q", answer.servedText(t))
	}
}

func TestAClientReadsAPartialTotalWithTheNamedMissingDays(t *testing.T) {
	probe := newServedSentencesRig(t, repositoryCommitsScenario()).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	period := completedDays(t, probe, 30)
	missing := map[int]bool{4: true, 11: true}
	var rows []servedDayRow
	var want int64
	for i, day := range period {
		if missing[i] {
			continue
		}
		row := servedDayRow{day: day, commits: int64(7 + i), merged: 1}
		rows = append(rows, row)
		want += row.commits
	}
	rig := newServedSentencesRig(t, repositoryCommitsScenario(servedMetricsFact(rows)))
	answer := rig.ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	assertServed(t, answer,
		fmt.Sprintf("Partial total of commits count over the period: %d, summed from the %d of %d days that have a stored row; no row is stored for %s, %s", want, len(period)-2, len(period), period[4], period[11]),
		"those days are not counted as zero",
	)
}

func TestAClientReadsWhyANinetyDayPeriodHasNoTotal(t *testing.T) {
	scenario := repositoryCommitsScenario()
	scenario.window = contractsv1.ContextFabricRelativeWindowTrailing90D
	probe := newServedSentencesRig(t, scenario).ask(t, "how many commits did repository named one have in the last 90 days", contractsv1.ContextFabricRelativeWindowTrailing90D)
	period := completedDays(t, probe, 90)
	var rows []servedDayRow
	for _, day := range period {
		rows = append(rows, servedDayRow{day: day, commits: 2, merged: 1})
	}
	scenario = repositoryCommitsScenario(servedMetricsFact(rows))
	answer := newServedSentencesRig(t, scenario).ask(t, "how many commits did repository named one have in the last 90 days", contractsv1.ContextFabricRelativeWindowTrailing90D)
	assertServed(t, answer, fmt.Sprintf("The period is longer than the %d daily rows one read returns, so no metrics total is stated for it.", contextfabric.MaxFactValueRows))
	if strings.Contains(answer.servedText(t), "Total of commits") {
		t.Errorf("a total is stated over a period one read cannot cover: %q", answer.servedText(t))
	}
}

func TestAClientReadsNoTotalSentenceForANonAdditiveSeries(t *testing.T) {
	probe := newServedSentencesRig(t, repositoryCommitsScenario()).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	var rows []contextfabric.FactValueRow
	for _, day := range completedDays(t, probe, 30) {
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{"day": contextfabric.StringFactValue(day), "compounding_risk": contextfabric.NumberFactValue(0.4)}})
	}
	health := contextfabric.CanonicalFact{
		Kind: contextfabric.FactHealth, Subject: servedSentencesRepository, EvidenceRefIDs: []string{"evidence_1"},
		SourceState: contextfabric.SourceAvailable, Source: "devhealthfacts.health", SourceVersion: "v1",
		Fields: map[string]contextfabric.FactValue{"daily_health": contextfabric.TableFactValue(contextfabric.FactTable{
			Shape: contextfabric.FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"compounding_risk"}, Rows: rows,
		})},
	}
	answer := newServedSentencesRig(t, repositoryCommitsScenario(health)).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	if text := answer.servedText(t); strings.Contains(text, "otal of") || strings.Contains(text, "whole days") {
		t.Errorf("a non-additive series produced a total sentence: %q", text)
	}
}

func TestAClientReadsTheCountSentenceOfAMemberSet(t *testing.T) {
	scenario := servedSentencesScenario{
		frame:      servedSentencesCountFrame(contextfabric.SubjectTeam),
		resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{servedSentencesRepository}, Candidates: []contextfabric.SubjectCandidate{servedSentencesCandidate(servedSentencesRepository)}},
		bases:      contextfabric.CommitBasisSet{},
		cohort:     servedSentencesCohort(contextfabric.SubjectTeam, 3),
		facts:      servedSentencesFacts{capabilities: servedSentencesCapabilities()},
		window:     contractsv1.ContextFabricRelativeWindowTrailing30D,
	}
	scenario.bases.Record(servedSentencesRepository, contextfabric.CommitBasisCallerCanonicalID)
	answer := newServedSentencesRig(t, scenario).ask(t, "how many teams own repository named one", contractsv1.ContextFabricRelativeWindowTrailing30D)
	assertServed(t, answer, "Counted 3 teams.")
}

func TestAClientStillReadsTheTotalWhenTheAnswerModelFails(t *testing.T) {
	probe := newServedSentencesRig(t, repositoryCommitsScenario()).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	period := completedDays(t, probe, 30)
	var rows []servedDayRow
	var want int64
	for i, day := range period {
		row := servedDayRow{day: day, commits: int64(2 + i%4), merged: 1}
		rows = append(rows, row)
		want += row.commits
	}
	scenario := repositoryCommitsScenario(servedMetricsFact(rows))
	scenario.modelFails = true
	answer := newServedSentencesRig(t, scenario).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	if claims, _ := answer.field(t, "structured", "key_facts").([]any); len(claims) != 0 {
		t.Fatalf("the failed model call still produced claims: %v", claims)
	}
	assertServed(t, answer, fmt.Sprintf("Total of commits count over the period: %d, summed from %d of %d days", want, len(period), len(period)))
}

// The same sentences reach a client that reads the stored result back, in the
// markdown as well as the structured result.
func TestAClientReadsTheTotalInTheStoredResultAndItsMarkdown(t *testing.T) {
	probe := newServedSentencesRig(t, repositoryCommitsScenario()).ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	period := completedDays(t, probe, 30)
	var rows []servedDayRow
	var want int64
	for i, day := range period {
		row := servedDayRow{day: day, commits: int64(5 + i%3), merged: 1}
		rows = append(rows, row)
		want += row.commits
	}
	rig := newServedSentencesRig(t, repositoryCommitsScenario(servedMetricsFact(rows)))
	answered := rig.ask(t, "how many commits did repository named one have in the last 30 days", contractsv1.ContextFabricRelativeWindowTrailing30D)
	resultID, _ := answered.field(t, "structured", "result_id").(string)
	stored := callRealMCPTool(t, rig.boot, "investigation_result", contractsv1.MCPInvestigationResultRequest{ResultID: resultID})
	sentence := fmt.Sprintf("Total of commits count over the period: %d, summed from %d of %d days", want, len(period), len(period))
	if text, _ := stored.field(t, "structured", "deterministic_answer").(string); !strings.Contains(text, sentence) {
		t.Errorf("investigation_result structured.deterministic_answer = %q, want %q", text, sentence)
	}
	if !strings.Contains(stored.markdown, sentence) {
		t.Errorf("investigation_result markdown lacks %q:\n%s", sentence, stored.markdown)
	}
}
