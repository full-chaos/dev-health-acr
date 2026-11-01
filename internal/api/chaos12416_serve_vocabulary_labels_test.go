package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12416. The words of a closed vocabulary reach every read of an answer,
// by every door, for the principal a web or MCP user holds: a user-class
// credential after NarrowRepositoryScopes. Nothing stores them.

const labelsCallerBytes = 1 << 20

// serveAsNarrowedUser sends the app's calls through the real access read as a
// user-class credential.
func serveAsNarrowedUser(t *testing.T, app *App) {
	t.Helper()
	reader := &fakePrincipalAccess{}
	reader.set(grantedAccess(), nil)
	app.runtime.PrincipalAccess = reader
	// A user-class create is refused below the wait budget's minimum.
	app.config.RequestTimeout = time.Minute
	owner, _, _ := strings.Cut(hostedTestRepository, "/")
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{owner + "/*"})
}

// storedRowReuseGate hands back the whole stored row, so the stored-result gate
// reads the narrowed user's own grant from it. surfaceReuseGate serves only the
// document, which that gate refuses for a restricted caller.
type storedRowReuseGate struct{ row cf.StoredInvestigationResult }

func (g storedRowReuseGate) FindReusable(context.Context, storage.Principal, cf.ReuseKey) (cf.StoredInvestigationResult, bool, cf.ReuseMissReason, error) {
	return g.row, true, "", nil
}

// labelsRig is the ranking surface's real engine behind the real routes.
type labelsRig struct {
	enabled bool
	store   rankingSurfaceStore
	deps    cf.EngineDependencies
	options cf.EngineOptions
	server  *httptest.Server
	token   string
}

func newLabelsRig(t *testing.T, enabled bool) *labelsRig {
	t.Helper()
	members := []cf.CohortMember{}
	bundle := cf.CanonicalFactBundle{Facts: []cf.CanonicalFact{}, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-labels"}
	for _, kind := range []cf.FactKind{cf.FactInvestment, cf.FactHealth, cf.FactOperationalDeficiencies, cf.FactReadiness, cf.FactWorkload} {
		bundle.Coverage.Sources = append(bundle.Coverage.Sources, cf.SourceObservation{Source: "canonical_fact:" + string(kind), State: cf.SourceAvailable})
	}
	for i := 1; i <= 2; i++ {
		subject := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: fmt.Sprintf("team:surface%d", i), Label: fmt.Sprintf("Team %d", i)}
		members = append(members, cf.CohortMember{Subject: subject, Rank: i, InclusionReasons: []string{"bounded cohort"}})
		investment := map[string]cf.FactValue{cf.FactFieldThemeQualityBugfix: cf.NumberFactValue(0.05)}
		for _, theme := range []string{cf.ThemeFeatureDelivery, cf.ThemeOperational, cf.ThemeMaintenance, cf.ThemeQuality, cf.ThemeRisk} {
			investment[cf.FactFieldTheme(theme)] = cf.NumberFactValue(0.2)
		}
		for kind, fields := range map[cf.FactKind]map[string]cf.FactValue{
			cf.FactInvestment: investment, cf.FactHealth: {"severity": cf.StringFactValue("low")}, cf.FactOperationalDeficiencies: {"severity": cf.StringFactValue("high")},
		} {
			bundle.Facts = append(bundle.Facts, cf.CanonicalFact{Kind: kind, Subject: subject, Fields: fields})
		}
	}
	cohort := &cf.Cohort{Kind: cf.SubjectTeam, Members: members, Rationale: "bounded team cohort", Complete: true}
	registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(nil), cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := rankingSurfaceStore{memoryinvestigation.NewStore()}
	next := 0
	rig := &labelsRig{enabled: enabled, store: store}
	rig.deps = cf.EngineDependencies{
		Telemetry: cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))), Interpreter: rankingSurfaceInterpreter{},
		Graph: rankingSurfaceGraph{surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{members[0].Subject}}}, cohort: cohort},
		Facts: liveFactReader{bundle: bundle}, Synthesizer: rankingSurfaceSynthesizer{}, Results: store, Requirements: registry,
	}
	rig.options = cf.EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() }, NewResultID: func() string {
		next++
		return fmt.Sprintf("result_labels_surface_%02d", next)
	}}
	rig.server, rig.token = rig.serve(t, rig.deps)
	return rig
}

func (r *labelsRig) serve(t *testing.T, deps cf.EngineDependencies) (*httptest.Server, string) {
	t.Helper()
	engine, err := cf.NewEngine(deps, r.options)
	if err != nil {
		t.Fatal(err)
	}
	app, token := newParityHostedApp(t, engine, r.store)
	app.config.ContextFabricVocabularyLabelsEnabled = r.enabled
	serveAsNarrowedUser(t, app)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	return server, token
}

// create posts the question through the real route and returns the raw body.
func (r *labelsRig) create(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	request := surfaceRequest("request_labels_surface")
	request.Question = "Rank the team cohort"
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	post, err := http.NewRequest(http.MethodPost, server.URL+ContextFabricInvestigationsPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Authorization", "Bearer "+r.token)
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("X-ACR-Client-Version", "1.2.5")
	response, err := server.Client().Do(post)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create: %d %s", response.StatusCode, body.String())
	}
	return body.Bytes()
}

func decodeLabeled(t *testing.T, body []byte) cf.InvestigationResult {
	t.Helper()
	var result cf.InvestigationResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// assertAnswerWords states what a served ranking answer must carry. The status
// word comes from the one rule, read against this answer's own completeness.
func assertAnswerWords(t *testing.T, label string, labels map[string]string, status v1.ContextFabricInvestigationStatus, state v1.ContextFabricAnswerCompletenessState) {
	t.Helper()
	want, ok := v1.ContextFabricAnswerStatusLabel(status, state)
	if !ok {
		t.Fatalf("%s: status %q has no word", label, status)
	}
	if got := labels[v1.ContextFabricVocabularyLabelKey(v1.ContextFabricVocabularyAnswerStatus, string(status))]; got != want {
		t.Errorf("%s: status word = %q, want %q (map %v)", label, got, want, labels)
	}
	if labels["subject_kind:team"] != "team" || labels["subject_kind_plural:team"] != "teams" {
		t.Errorf("%s: the cohort kind has no words: %v", label, labels)
	}
}

// Fresh, reused, by id, projected, over MCP: the real engine, the real routes,
// and the narrowed user's principal. The stored row carries no word throughout.
func TestEveryReadOfAnAnswerCarriesItsWords(t *testing.T) {
	rig := newLabelsRig(t, true)
	principal := storage.Principal{OrgID: callerOrgID}

	fresh := decodeLabeled(t, rig.create(t, rig.server))
	assertAnswerWords(t, "fresh", fresh.VocabularyLabels, fresh.Status, fresh.Completeness.State)
	if fresh.Reused {
		t.Fatal("the first turn must be fresh")
	}

	stored, err := rig.store.Get(context.Background(), principal, fresh.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Result.VocabularyLabels != nil {
		t.Fatalf("the stored document carries the map: %v", stored.Result.VocabularyLabels)
	}

	byID := getRealAPIResult(t, rig.server, rig.token, fresh.ResultID)
	if !maps.Equal(byID.VocabularyLabels, fresh.VocabularyLabels) {
		t.Errorf("by id map %v, fresh map %v", byID.VocabularyLabels, fresh.VocabularyLabels)
	}

	projected := getRealAPIProjection(t, rig.server, rig.token, fresh.ResultID, 10, 2, 100)
	if !maps.Equal(projected.VocabularyLabels, fresh.VocabularyLabels) {
		t.Errorf("projection map %v, fresh map %v", projected.VocabularyLabels, fresh.VocabularyLabels)
	}

	configureSidecarEnvironment(t, rig.server, rig.token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if mcpResult := callRealMCPInvestigationResult(t, boot, fresh.ResultID); !maps.Equal(mcpResult.VocabularyLabels, fresh.VocabularyLabels) {
		t.Errorf("MCP result map %v, fresh map %v", mcpResult.VocabularyLabels, fresh.VocabularyLabels)
	}

	reuse := rig.deps
	reuse.ReuseGate = storedRowReuseGate{row: stored}
	reusedServer, _ := rig.serve(t, reuse)
	reused := decodeLabeled(t, rig.create(t, reusedServer))
	if !reused.Reused {
		t.Fatal("the real reuse path did not run")
	}
	if !maps.Equal(reused.VocabularyLabels, fresh.VocabularyLabels) {
		t.Errorf("reused map %v, fresh map %v", reused.VocabularyLabels, fresh.VocabularyLabels)
	}

	after, err := rig.store.Get(context.Background(), principal, fresh.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Result.VocabularyLabels != nil {
		t.Fatalf("a read wrote the map to the stored document: %v", after.Result.VocabularyLabels)
	}
}

// The event stream ends on the JSON body, byte for byte, so it carries the map.
func TestTheEventStreamCarriesTheWords(t *testing.T) {
	investigator := investigatorOf(answering(t, "context_fabric_investigation_result.v1.json", nil))
	jsonApp, jsonToken, _ := newStreamTestApp(t, investigator)
	streamApp, streamToken, _ := newStreamTestApp(t, investigator)
	jsonApp.config.ContextFabricVocabularyLabelsEnabled, streamApp.config.ContextFabricVocabularyLabelsEnabled = true, true
	body := investigationRequestBody()
	answer := serveJSON(t, jsonApp, jsonToken, body)
	stream := serveStream(t, streamApp, streamToken, body, nil)
	terminal := stream.events[len(stream.events)-1]
	if terminal.Type != v1.ContextFabricInvestigationEventResult || !bytes.Equal(terminal.Result, answer.Body.Bytes()) {
		t.Fatalf("the result event is not the JSON body:\nstream %s\njson   %s", terminal.Result, answer.Body.Bytes())
	}
	if labels := decodeLabeled(t, terminal.Result).VocabularyLabels; len(labels) == 0 {
		t.Fatal("the stream's result carries no map")
	}
}

// A row stored before this change gets its words on read, in both flag states
// for the sources, with no rewrite and no migration.
func TestAStoredRowGetsItsWordsWithNoRewrite(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_labels_legacy1"
			row.Coverage.Sources = []v1.ContextFabricSourceObservation{{Source: "canonical_fact:blockers", State: v1.ContextFabricSourceTruncated, Reason: "the read was cut", StateLabel: "limited"}}
			seedResult3355(t, store, callerOrgID, row)
			before, err := store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, row.ResultID)
			if err != nil {
				t.Fatal(err)
			}
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				return cf.InvestigationResult{}, nil
			}), store)
			app.config.ContextFabricVocabularyLabelsEnabled = enabled
			serveAsNarrowedUser(t, app)

			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, row.ResultID))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
			}
			served := decodeLabeled(t, recorder.Body.Bytes())
			if got := served.Coverage.Sources[0]; got.Label != "Canonical facts — blockers" || got.StateLabel != "partly included" {
				t.Errorf("source reads %q / %q", got.Label, got.StateLabel)
			}
			if enabled {
				assertStatusWord(t, served)
			} else if strings.Contains(recorder.Body.String(), "vocabulary_labels") {
				t.Errorf("off served the key: %s", recorder.Body.String())
			}

			after, err := store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, row.ResultID)
			if err != nil {
				t.Fatal(err)
			}
			beforeEncoded, _ := json.Marshal(before.Result)
			afterEncoded, _ := json.Marshal(after.Result)
			if !bytes.Equal(beforeEncoded, afterEncoded) {
				t.Fatalf("the read rewrote the row:\nbefore %s\n after %s", beforeEncoded, afterEncoded)
			}
		})
	}
}

func assertStatusWord(t *testing.T, result cf.InvestigationResult) {
	t.Helper()
	want, ok := v1.ContextFabricAnswerStatusLabel(result.Status, result.Completeness.State)
	if !ok || result.VocabularyLabels["answer_status:"+string(result.Status)] != want {
		t.Fatalf("status word = %q, want %q: %v", result.VocabularyLabels["answer_status:"+string(result.Status)], want, result.VocabularyLabels)
	}
}

// Off: no door serves the key, for the real engine's own answer.
func TestOffNoDoorServesTheKey(t *testing.T) {
	rig := newLabelsRig(t, false)
	created := rig.create(t, rig.server)
	if strings.Contains(string(created), "vocabulary_labels") {
		t.Fatalf("create served the key: %s", created)
	}
	fresh := decodeLabeled(t, created)
	for _, query := range []string{"", "?view=projection"} {
		request, err := http.NewRequest(http.MethodGet, rig.server.URL+ContextFabricInvestigationsPath+"/"+fresh.ResultID+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+rig.token)
		request.Header.Set("X-ACR-Client-Version", "1.2.5")
		response, err := rig.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		if _, err := body.ReadFrom(response.Body); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("read %q: %d %s", query, response.StatusCode, body.String())
		}
		if strings.Contains(body.String(), "vocabulary_labels") {
			t.Fatalf("read %q served the key: %s", query, body.String())
		}
	}
}

func boundedLabelsApp(t *testing.T, results cf.InvestigationResultStore, answer cf.InvestigationResult) (*App, string, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	app, token := newParityHostedAppWithLogs(t, investigatorFunc(func(_ context.Context, _ storage.Principal, request cf.InvestigationRequest) (cf.InvestigationResult, error) {
		answer.RequestID = request.RequestID
		return answer, nil
	}), results, limits.ResourceBudget{MaxItems: 500, MaxTokens: 1 << 20, MaxBytes: 1 << 24}, logs)
	app.config.ContextFabricVocabularyLabelsEnabled = true
	app.config.MaxSerializedBytes = labelsCallerBytes
	app.config.MaxItems = 500
	return app, token, logs
}

func readByID(t *testing.T, app *App, token, resultID, query string) *httptest.ResponseRecorder {
	t.Helper()
	request := investigationResultRequest(t, token, resultID)
	request.URL.RawQuery = query
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

const shedLine = "context fabric vocabulary labels shed"

// A stored result fitted near the ceiling still serves within it: the ceiling
// is a byte exact line, the map goes first, and a label never refuses it.
func TestAStoredResultNearTheCeilingServesWithinIt(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seeded := seedResult(t, store, callerOrgID, "result_labels_bound01")
	app, token, logs := boundedLabelsApp(t, store, seeded)

	labeled := readByID(t, app, token, seeded.ResultID, "")
	if labeled.Code != http.StatusOK || !strings.Contains(labeled.Body.String(), "vocabulary_labels") {
		t.Fatalf("unbounded read: %d %s", labeled.Code, labeled.Body.String())
	}

	app.config.MaxSerializedBytes = labeled.Body.Len()
	if exact := readByID(t, app, token, seeded.ResultID, ""); exact.Code != http.StatusOK || !strings.Contains(exact.Body.String(), "vocabulary_labels") {
		t.Fatalf("a document exactly at the ceiling lost its map: %d %s", exact.Code, exact.Body.String())
	}
	if strings.Contains(logs.String(), shedLine) {
		t.Fatalf("nothing was shed, yet the line fired:\n%s", logs.String())
	}

	app.config.MaxSerializedBytes = labeled.Body.Len() - 1
	shed := readByID(t, app, token, seeded.ResultID, "")
	if shed.Code != http.StatusOK || shed.Body.Len() > app.config.MaxSerializedBytes || strings.Contains(shed.Body.String(), "vocabulary_labels") {
		t.Fatalf("one byte short of the map: %d, %d bytes of %d: %s", shed.Code, shed.Body.Len(), app.config.MaxSerializedBytes, shed.Body.String())
	}
	if !strings.Contains(logs.String(), shedLine) {
		t.Fatalf("the shed was silent:\n%s", logs.String())
	}

	// The refusal stands where it always stood, and for the document's own bytes.
	app.config.MaxSerializedBytes = shed.Body.Len() - 1
	if refused := readByID(t, app, token, seeded.ResultID, ""); refused.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the ceiling with no words: %d %s", refused.Code, refused.Body.String())
	}
}

// By id, the projection is measured with its words, which are shed before the
// route refuses it, while the canonical document stands far over the ceiling.
func TestAProjectionByIDShedsItsWordsBeforeARefusal(t *testing.T) {
	store := memoryinvestigation.NewStore()
	row := threeRollupProjectStatusResult("result_labels_bound02")
	seedResult3355(t, store, callerOrgID, row)
	app, token, logs := boundedLabelsApp(t, store, row)

	projected := readByID(t, app, token, row.ResultID, "view=projection")
	canonical := readByID(t, app, token, row.ResultID, "")
	if projected.Code != http.StatusOK || !strings.Contains(projected.Body.String(), "vocabulary_labels") {
		t.Fatalf("unbounded projection: %d %s", projected.Code, projected.Body.String())
	}
	if canonical.Body.Len() <= projected.Body.Len() {
		t.Fatalf("the canonical document must be the larger one: %d %d", canonical.Body.Len(), projected.Body.Len())
	}

	// The canonical document is over this ceiling and the projection is not:
	// nothing the reader receives lost a word, so nothing says it did.
	app.config.MaxSerializedBytes = projected.Body.Len()
	exact := readByID(t, app, token, row.ResultID, "view=projection")
	if exact.Code != http.StatusOK || !strings.Contains(exact.Body.String(), "vocabulary_labels") {
		t.Fatalf("a projection exactly at the ceiling lost its map: %d", exact.Code)
	}
	if strings.Contains(logs.String(), shedLine) {
		t.Fatalf("the projection kept its words, yet the line fired:\n%s", logs.String())
	}

	app.config.MaxSerializedBytes = projected.Body.Len() - 1
	shed := readByID(t, app, token, row.ResultID, "view=projection")
	if shed.Code != http.StatusOK || shed.Body.Len() > app.config.MaxSerializedBytes || strings.Contains(shed.Body.String(), "vocabulary_labels") {
		t.Fatalf("projection one byte over: %d, %d bytes of %d", shed.Code, shed.Body.Len(), app.config.MaxSerializedBytes)
	}
	if !strings.Contains(logs.String(), shedLine) {
		t.Fatalf("the shed was silent:\n%s", logs.String())
	}
}

// A created answer that fits the caller's bound only with no words stays the
// canonical answer. Measuring the map into the disposition would turn it into a
// projection, and that is a label costing the reader the document.
func TestACreatedAnswerThatFitsOnlyWithNoWordsStaysCanonical(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound03")
	app, token, logs := boundedLabelsApp(t, memoryinvestigation.NewStore(), answer)
	body := investigationRequestBody()
	body.Options.MaxSerializedBytes = v1.ContextFabricSerializedBytesMax

	labeled := serveJSON(t, app, token, body)
	if labeled.Code != http.StatusOK || !strings.Contains(labeled.Body.String(), "vocabulary_labels") {
		t.Fatalf("unbounded create: %d %s", labeled.Code, labeled.Body.String())
	}
	body.Options.MaxSerializedBytes = labeled.Body.Len() - 1
	shed := serveJSON(t, app, token, body)
	if shed.Code != http.StatusOK || shed.Body.Len() > body.Options.MaxSerializedBytes {
		t.Fatalf("create one byte short: %d, %d bytes of %d", shed.Code, shed.Body.Len(), body.Options.MaxSerializedBytes)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(shed.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if _, canonical := keys["claimed_facts"]; !canonical {
		t.Fatalf("the answer became a projection for a label: %s", shed.Body.String())
	}
	if _, held := keys["vocabulary_labels"]; held {
		t.Fatal("the document over its bound still holds the map")
	}
	if !strings.Contains(logs.String(), shedLine) {
		t.Fatalf("the shed was silent:\n%s", logs.String())
	}
}

// Over the caller's bound the projection serves, and it carries the words.
func TestAProjectionOverTheCallerBoundCarriesTheWords(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound04")
	app, token, logs := boundedLabelsApp(t, memoryinvestigation.NewStore(), answer)
	labeled, _ := cf.ServeVocabularyLabels(answer, true, 0)
	bounded, _, projectedBytes, _, fits := app.projectToCallerOption(labeled, v1.ContextFabricInvestigationOptions{MaxDrivers: 10, MaxCohortMembers: 50, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20})
	_, canonicalBytes, err := marshalContextFabricResponse(labeled)
	if err != nil {
		t.Fatal(err)
	}
	if !fits || bounded.VocabularyLabels == nil || projectedBytes >= canonicalBytes {
		t.Fatalf("the projection must be the smaller document and hold the words: %d of %d", projectedBytes, canonicalBytes)
	}
	body := investigationRequestBody()
	body.Options.MaxSerializedBytes = int(projectedBytes+canonicalBytes) / 2
	recorder := serveJSON(t, app, token, body)
	if recorder.Code != http.StatusOK || recorder.Body.Len() > body.Options.MaxSerializedBytes {
		t.Fatalf("create: %d, %d bytes: %s", recorder.Code, recorder.Body.Len(), recorder.Body.String())
	}
	var projection v1.ContextFabricAnswerProjection
	if err := json.Unmarshal(recorder.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.SchemaVersion != v1.ContextFabricAnswerProjectionSchema {
		t.Fatalf("schema = %q, want the projection", projection.SchemaVersion)
	}
	if !maps.Equal(projection.VocabularyLabels, labeled.VocabularyLabels) {
		t.Fatalf("the projection carries %v, want %v", projection.VocabularyLabels, labeled.VocabularyLabels)
	}
	if strings.Contains(logs.String(), shedLine) {
		t.Fatalf("the words were served, yet the line fired:\n%s", logs.String())
	}
}

// The projection's words go before its rows: at one byte short of the projected
// map the counts are the ones the map had.
func TestAProjectionShedsItsWordsBeforeItsRows(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound05")
	labeled, shed := cf.ServeVocabularyLabels(answer, true, 0)
	if shed || labeled.VocabularyLabels == nil {
		t.Fatalf("the fixture has no words: %v", labeled.VocabularyLabels)
	}
	app := &App{}
	options := v1.ContextFabricInvestigationOptions{MaxDrivers: 10, MaxCohortMembers: 50, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20}
	whole, _, wholeBytes, _, fits := app.projectToCallerOption(labeled, options)
	if !fits || whole.VocabularyLabels == nil {
		t.Fatalf("the projection must fit with its words first: fits=%v map=%v", fits, whole.VocabularyLabels)
	}
	options.MaxSerializedBytes = int(wholeBytes) - 1
	cut, _, cutBytes, budget, fits := app.projectToCallerOption(labeled, options)
	if !fits || cutBytes > wholeBytes-1 {
		t.Fatalf("one byte short: fits=%v %d bytes", fits, cutBytes)
	}
	if cut.VocabularyLabels != nil {
		t.Fatalf("the map survived: %v", cut.VocabularyLabels)
	}
	if budget.MaxDrivers != options.MaxDrivers || budget.MaxEvidenceRefs != options.MaxEvidenceRefs || budget.MaxCohortMembers != options.MaxCohortMembers {
		t.Fatalf("the rows were cut for a label: %+v", budget)
	}
	if len(cut.PrincipalDrivers) != len(whole.PrincipalDrivers) || len(cut.KeyFacts) != len(whole.KeyFacts) {
		t.Fatalf("rows changed: drivers %d/%d facts %d/%d", len(cut.PrincipalDrivers), len(whole.PrincipalDrivers), len(cut.KeyFacts), len(whole.KeyFacts))
	}
}

// A client synthesis turn is held to the bound with its input in the body, and
// that turn has no projection to fall to, so a map that overran the body would
// refuse the answer. The real route, on a real client turn, over the budgets a
// caller can send: the words never change the outcome, and each body stays
// within its budget.
//
// NOT pinned here: the overrun itself. On this fixture the engine's own input
// cut leaves about 1 KB under the budget, more than this map, so the scan does
// not reach it. TestTheWordsBoundLeavesRoomForTheSynthesisInput pins that bound
// to the byte.
func TestAClientTurnIsNeverRefusedForAWord(t *testing.T) {
	fixture := manyFactsFixture(40, 600)
	post := func(enabled bool, budget int) *httptest.ResponseRecorder {
		t.Helper()
		rig := newClientRouteRig(t, fixture)
		rig.app.config.ContextFabricVocabularyLabelsEnabled = enabled
		serveAsNarrowedUser(t, rig.app)
		return rig.post(t, v1.ContextFabricSynthesisModeClient, func(request *v1.ContextFabricInvestigationRequest) {
			request.Options.MaxSerializedBytes = budget
		})
	}
	whole := post(false, v1.ContextFabricSerializedBytesMax)
	if decodeEnvelope(t, whole).SynthesisInput == nil || whole.Body.Len() <= v1.ContextFabricSerializedBytesMin {
		t.Fatalf("no synthesis input in a %d byte body: this was not the turn under test", whole.Body.Len())
	}
	served, carried := 0, 0
	for budget := v1.ContextFabricSerializedBytesMin; budget <= whole.Body.Len()+1<<12; budget += 97 {
		plain := post(false, budget)
		labeled := post(true, budget)
		if labeled.Code != plain.Code {
			t.Fatalf("budget %d: answered %d with words, %d with none: %s", budget, labeled.Code, plain.Code, labeled.Body.String())
		}
		if labeled.Code != http.StatusOK {
			continue
		}
		served++
		if labeled.Body.Len() > budget {
			t.Fatalf("budget %d: %d body bytes", budget, labeled.Body.Len())
		}
		if decodeEnvelope(t, labeled).VocabularyLabels != nil {
			carried++
		}
	}
	if served == 0 || carried == 0 {
		t.Fatalf("%d budgets served, %d carried the map: this measured nothing", served, carried)
	}
}

// The bound leaves room for the envelope, to the byte.
func TestTheWordsBoundLeavesRoomForTheSynthesisInput(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound06")
	input := &v1.ContextFabricSynthesisInput{Input: json.RawMessage(`{"canonical_facts":[]}`)}
	envelope := func(document cf.InvestigationResult) int64 {
		t.Helper()
		_, size, err := marshalContextFabricResponse(v1.ContextFabricInvestigationResponse{ContextFabricInvestigationResult: document, SynthesisInput: input})
		if err != nil {
			t.Fatal(err)
		}
		return size
	}
	labeled, _ := cf.ServeVocabularyLabels(answer, true, 0)
	app := &App{config: AppConfig{MaxSerializedBytes: v1.ContextFabricSerializedBytesMax}}
	for name, row := range map[string]struct {
		caller  int64
		carries bool
	}{
		"exactly the envelope with its words": {envelope(labeled), true},
		"one byte short":                      {envelope(labeled) - 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			served, shed := cf.ServeVocabularyLabels(answer, true, app.investigationWordsBound(answer, row.caller, input))
			if (served.VocabularyLabels != nil) != row.carries || shed == row.carries {
				t.Fatalf("map=%v shed=%v, want carries=%v", served.VocabularyLabels != nil, shed, row.carries)
			}
			if got := envelope(served); got > row.caller {
				t.Fatalf("envelope %d over the caller's %d", got, row.caller)
			}
		})
	}
	if got := (&App{config: AppConfig{MaxSerializedBytes: 100}}).investigationWordsBound(answer, 1000, nil); got != 100 {
		t.Errorf("the service ceiling did not bind: %d", got)
	}
	if got := (&App{config: AppConfig{MaxSerializedBytes: 1000}}).investigationWordsBound(answer, 100, nil); got != 100 {
		t.Errorf("the caller's bound did not bind: %d", got)
	}
	if got := (&App{config: AppConfig{MaxSerializedBytes: 1000}}).investigationWordsBound(answer, 0, nil); got != 1000 {
		t.Errorf("no caller bound must leave the ceiling: %d", got)
	}
	if got := (&App{config: AppConfig{MaxSerializedBytes: 1}}).investigationWordsBound(answer, 0, input); got != 1 {
		t.Errorf("a bound below the envelope must stay positive, or it measures nothing: %d", got)
	}
}

// A new way to encode an answer is a new door. Each function that encodes one
// is named here, and each door composes the words: a door that skips them
// serves a stored answer with no map and no error.
func TestEveryDoorThatEncodesAnAnswerComposesItsWords(t *testing.T) {
	doors := []string{"serveContextFabricInvestigationResult", "serveStoredInvestigationResultOn"}
	encoders := map[string]bool{
		"serveContextFabricInvestigationResult": true, "serveStoredInvestigationResultOn": true,
		"projectToCallerOption": true, "investigationWordsBound": true, "MarshalContextFabricResponse": true,
	}
	files, err := filepath.Glob(filepath.Join(repopath.Path(t, "internal", "api"), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	encoding, composing := map[string]bool{}, map[string]bool{}
	read := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, declaration := range parsed.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction {
				continue
			}
			ast.Inspect(function, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				switch callee := call.Fun.(type) {
				case *ast.Ident:
					if callee.Name == "marshalContextFabricResponse" {
						encoding[function.Name.Name] = true
					}
				case *ast.SelectorExpr:
					if callee.Sel.Name == "ServeVocabularyLabels" {
						composing[function.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if read < 20 {
		t.Fatalf("read only %d files: this measured almost nothing", read)
	}
	for name := range encoding {
		if !encoders[name] {
			t.Errorf("%s encodes an answer and is not a known door", name)
		}
	}
	for _, door := range doors {
		if !encoding[door] {
			t.Errorf("%s no longer encodes an answer: this scan moved", door)
		}
		if !composing[door] {
			t.Errorf("%s encodes an answer with no words", door)
		}
	}
	names := make([]string, 0, len(encoding))
	for name := range encoding {
		names = append(names, name)
	}
	slices.Sort(names)
	t.Logf("encoders: %v", names)
}
