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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
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

func boundedLabelsApp(t *testing.T, results cf.InvestigationResultStore, answer cf.InvestigationResult) (*App, string, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
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

// tierLine is the shed line's closed value, as the JSON handler writes it.
func tierLine(tier v1.ContextFabricVocabularyLabelsTier) string {
	return `"tier":"` + string(tier) + `"`
}

func statusWordsOf(t *testing.T, labels map[string]string) map[string]string {
	t.Helper()
	kept := v1.ContextFabricStatusVocabularyLabels(labels)
	if len(kept) == 0 || len(kept) == len(labels) {
		t.Fatalf("the full map must hold status words and others, or this proves nothing: %v", labels)
	}
	return kept
}

// A stored result fitted near the ceiling still serves within it: the ceiling is
// a byte exact line, each tier goes in turn, and a label never refuses it.
func TestAStoredResultNearTheCeilingServesWithinIt(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seeded := seedResult3355(t, store, callerOrgID, threeRollupProjectStatusResult("result_labels_bound01"))
	app, token, logs := boundedLabelsApp(t, store, seeded)

	labeled := readByID(t, app, token, seeded.ResultID, "")
	if labeled.Code != http.StatusOK {
		t.Fatalf("unbounded read: %d %s", labeled.Code, labeled.Body.String())
	}
	full := decodeLabeled(t, labeled.Body.Bytes()).VocabularyLabels
	keep := statusWordsOf(t, full)

	app.config.MaxSerializedBytes = labeled.Body.Len()
	if exact := readByID(t, app, token, seeded.ResultID, ""); exact.Code != http.StatusOK || !maps.Equal(decodeLabeled(t, exact.Body.Bytes()).VocabularyLabels, full) {
		t.Fatalf("a document exactly at the ceiling lost words: %d %s", exact.Code, exact.Body.String())
	}
	if strings.Contains(logs.String(), shedLine) {
		t.Fatalf("nothing was shed, yet the line fired:\n%s", logs.String())
	}

	app.config.MaxSerializedBytes = labeled.Body.Len() - 1
	tiered := readByID(t, app, token, seeded.ResultID, "")
	if tiered.Code != http.StatusOK || tiered.Body.Len() > app.config.MaxSerializedBytes || !maps.Equal(decodeLabeled(t, tiered.Body.Bytes()).VocabularyLabels, keep) {
		t.Fatalf("one byte short of the map: %d, %d bytes of %d: %s", tiered.Code, tiered.Body.Len(), app.config.MaxSerializedBytes, tiered.Body.String())
	}
	if !strings.Contains(logs.String(), shedLine) || !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsStatusOnly)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}

	app.config.MaxSerializedBytes = tiered.Body.Len() - 1
	shed := readByID(t, app, token, seeded.ResultID, "")
	if shed.Code != http.StatusOK || shed.Body.Len() > app.config.MaxSerializedBytes || strings.Contains(shed.Body.String(), "vocabulary_labels") {
		t.Fatalf("one byte short of the status words: %d, %d bytes of %d: %s", shed.Code, shed.Body.Len(), app.config.MaxSerializedBytes, shed.Body.String())
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsNone)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}

	// The refusal stands where it always stood, and for the document's own bytes.
	app.config.MaxSerializedBytes = shed.Body.Len() - 1
	if refused := readByID(t, app, token, seeded.ResultID, ""); refused.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the ceiling with no words: %d %s", refused.Code, refused.Body.String())
	}
}

// By id, the projection is measured with its words, which shed by tier before
// the route refuses it, while the canonical document stands far over the ceiling.
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
	logs.Reset()

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

	full := decodeProjection(t, projected.Body.Bytes()).VocabularyLabels
	app.config.MaxSerializedBytes = projected.Body.Len() - 1
	tiered := readByID(t, app, token, row.ResultID, "view=projection")
	if tiered.Code != http.StatusOK || tiered.Body.Len() > app.config.MaxSerializedBytes {
		t.Fatalf("projection one byte over: %d, %d bytes of %d", tiered.Code, tiered.Body.Len(), app.config.MaxSerializedBytes)
	}
	if got := decodeProjection(t, tiered.Body.Bytes()).VocabularyLabels; !maps.Equal(got, statusWordsOf(t, full)) {
		t.Fatalf("the status tier holds %v", got)
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsStatusOnly)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}

	app.config.MaxSerializedBytes = tiered.Body.Len() - 1
	shed := readByID(t, app, token, row.ResultID, "view=projection")
	if shed.Code != http.StatusOK || strings.Contains(shed.Body.String(), "vocabulary_labels") {
		t.Fatalf("one byte short of the status words: %d %s", shed.Code, shed.Body.String())
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsNone)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}
}

func decodeProjection(t *testing.T, body []byte) v1.ContextFabricAnswerProjection {
	t.Helper()
	var projection v1.ContextFabricAnswerProjection
	if err := json.Unmarshal(body, &projection); err != nil {
		t.Fatal(err)
	}
	if projection.SchemaVersion != v1.ContextFabricAnswerProjectionSchema {
		t.Fatalf("schema = %q, want the projection: %s", projection.SchemaVersion, body)
	}
	return projection
}

// A created answer that fits the caller's bound only with fewer words stays the
// canonical answer. Measuring the map into the disposition would turn it into a
// projection, and that is a label costing the reader the document.
func TestACreatedAnswerThatFitsOnlyWithFewerWordsStaysCanonical(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound03")
	app, token, logs := boundedLabelsApp(t, memoryinvestigation.NewStore(), answer)
	body := investigationRequestBody()
	body.Options.MaxSerializedBytes = v1.ContextFabricSerializedBytesMax

	labeled := serveJSON(t, app, token, body)
	if labeled.Code != http.StatusOK || !strings.Contains(labeled.Body.String(), "vocabulary_labels") {
		t.Fatalf("unbounded create: %d %s", labeled.Code, labeled.Body.String())
	}
	full := decodeLabeled(t, labeled.Body.Bytes()).VocabularyLabels

	body.Options.MaxSerializedBytes = labeled.Body.Len() - 1
	tiered := serveJSON(t, app, token, body)
	if tiered.Code != http.StatusOK || tiered.Body.Len() > body.Options.MaxSerializedBytes {
		t.Fatalf("create one byte short: %d, %d bytes of %d", tiered.Code, tiered.Body.Len(), body.Options.MaxSerializedBytes)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(tiered.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if _, canonical := keys["claimed_facts"]; !canonical {
		t.Fatalf("the answer became a projection for a label: %s", tiered.Body.String())
	}
	if got := decodeLabeled(t, tiered.Body.Bytes()).VocabularyLabels; !maps.Equal(got, statusWordsOf(t, full)) {
		t.Fatalf("the status tier holds %v", got)
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsStatusOnly)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}

	body.Options.MaxSerializedBytes = tiered.Body.Len() - 1
	shed := serveJSON(t, app, token, body)
	if shed.Code != http.StatusOK || shed.Body.Len() > body.Options.MaxSerializedBytes || strings.Contains(shed.Body.String(), "vocabulary_labels") {
		t.Fatalf("create one byte short of the status words: %d, %d bytes of %d", shed.Code, shed.Body.Len(), body.Options.MaxSerializedBytes)
	}
	if err := json.Unmarshal(shed.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if _, canonical := keys["claimed_facts"]; !canonical {
		t.Fatalf("the answer became a projection for a label: %s", shed.Body.String())
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsNone)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}
}

// Over the caller's bound the projection serves, and it carries the words.
func TestAProjectionOverTheCallerBoundCarriesTheWords(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound04")
	app, token, logs := boundedLabelsApp(t, memoryinvestigation.NewStore(), answer)
	labeled, _ := cf.ServeVocabularyLabels(answer, true, 0)
	bounded, _, projectedBytes, _, _, fits := app.projectToCallerOption(labeled, answer, v1.ContextFabricInvestigationOptions{MaxDrivers: 10, MaxCohortMembers: 50, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20})
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
	projection := decodeProjection(t, recorder.Body.Bytes())
	if len(projection.VocabularyLabels) != len(labeled.VocabularyLabels) {
		t.Fatalf("the projection carries %v, want the keys of %v", projection.VocabularyLabels, labeled.VocabularyLabels)
	}
	for key := range labeled.VocabularyLabels {
		if _, held := projection.VocabularyLabels[key]; !held {
			t.Fatalf("the projection lost %q", key)
		}
	}
	assertProjectionStatusWord(t, projection)
	if strings.Contains(logs.String(), shedLine) {
		t.Fatalf("the words were served, yet the line fired:\n%s", logs.String())
	}
}

// assertProjectionStatusWord: the pill of a projected answer reads THIS
// document's completeness, which a cut lowers.
func assertProjectionStatusWord(t *testing.T, projection v1.ContextFabricAnswerProjection) {
	t.Helper()
	want, ok := v1.ContextFabricAnswerStatusLabel(projection.Status, projection.Completeness.State)
	if !ok {
		t.Fatalf("status %q has no word", projection.Status)
	}
	if got := projection.VocabularyLabels["answer_status:"+string(projection.Status)]; got != want {
		t.Fatalf("status %q at completeness %q reads %q, want %q", projection.Status, projection.Completeness.State, got, want)
	}
}

// The projection's words go before its rows: at each tier's byte the counts are
// the ones the full map had.
func TestAProjectionShedsItsWordsBeforeItsRows(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound05")
	labeled, tier := cf.ServeVocabularyLabels(answer, true, 0)
	if tier != v1.ContextFabricVocabularyLabelsFull || labeled.VocabularyLabels == nil {
		t.Fatalf("the fixture has no words: %v", labeled.VocabularyLabels)
	}
	app := &App{}
	options := v1.ContextFabricInvestigationOptions{MaxDrivers: 10, MaxCohortMembers: 50, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20}
	whole, _, wholeBytes, _, wholeTier, fits := app.projectToCallerOption(labeled, answer, options)
	if !fits || whole.VocabularyLabels == nil || wholeTier != v1.ContextFabricVocabularyLabelsFull {
		t.Fatalf("the projection must fit with its words first: fits=%v tier=%q map=%v", fits, wholeTier, whole.VocabularyLabels)
	}
	rowsIntact := func(t *testing.T, got v1.ContextFabricAnswerProjection, budget answerprojection.Budget) {
		t.Helper()
		if budget.MaxDrivers != options.MaxDrivers || budget.MaxEvidenceRefs != options.MaxEvidenceRefs || budget.MaxCohortMembers != options.MaxCohortMembers {
			t.Fatalf("the rows were cut for a label: %+v", budget)
		}
		if len(got.PrincipalDrivers) != len(whole.PrincipalDrivers) || len(got.KeyFacts) != len(whole.KeyFacts) {
			t.Fatalf("rows changed: drivers %d/%d facts %d/%d", len(got.PrincipalDrivers), len(whole.PrincipalDrivers), len(got.KeyFacts), len(whole.KeyFacts))
		}
	}

	options.MaxSerializedBytes = int(wholeBytes) - 1
	status, _, statusBytes, budget, statusTier, fits := app.projectToCallerOption(labeled, answer, options)
	if !fits || statusBytes > wholeBytes-1 || statusTier != v1.ContextFabricVocabularyLabelsStatusOnly {
		t.Fatalf("one byte short: fits=%v %d bytes tier %q", fits, statusBytes, statusTier)
	}
	if !maps.Equal(status.VocabularyLabels, statusWordsOf(t, whole.VocabularyLabels)) {
		t.Fatalf("the status tier holds %v", status.VocabularyLabels)
	}
	rowsIntact(t, status, budget)

	options.MaxSerializedBytes = int(statusBytes) - 1
	none, _, noneBytes, budget, noneTier, fits := app.projectToCallerOption(labeled, answer, options)
	if !fits || noneBytes > statusBytes-1 || noneTier != v1.ContextFabricVocabularyLabelsNone || none.VocabularyLabels != nil {
		t.Fatalf("one byte short of the status words: fits=%v %d bytes tier %q map %v", fits, noneBytes, noneTier, none.VocabularyLabels)
	}
	rowsIntact(t, none, budget)
}

// A projection with no status word has no status tier, and one with no map at
// all reports no shed.
func TestAProjectionsTierNamesWhatItHolds(t *testing.T) {
	noStatus := v1.ContextFabricAnswerProjection{VocabularyLabels: map[string]string{"subject_kind:team": "team"}}
	if _, _, _, tier, err := fitProjectionWords(noStatus, 1); err != nil || tier != v1.ContextFabricVocabularyLabelsNone {
		t.Fatalf("no status word: tier %q (%v), want none", tier, err)
	}
	if _, _, _, tier, err := fitProjectionWords(v1.ContextFabricAnswerProjection{}, 1); err != nil || tier != v1.ContextFabricVocabularyLabelsOff {
		t.Fatalf("no map: tier %q (%v), want off", tier, err)
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
	statusOnly, _ := cf.ServeVocabularyLabels(answer, true, 0)
	statusOnly.VocabularyLabels = v1.ContextFabricStatusVocabularyLabels(labeled.VocabularyLabels)
	for name, row := range map[string]struct {
		caller int64
		tier   v1.ContextFabricVocabularyLabelsTier
	}{
		"exactly the envelope with its words": {envelope(labeled), v1.ContextFabricVocabularyLabelsFull},
		"one byte short":                      {envelope(labeled) - 1, v1.ContextFabricVocabularyLabelsStatusOnly},
		"one byte short of the status words":  {envelope(statusOnly) - 1, v1.ContextFabricVocabularyLabelsNone},
	} {
		t.Run(name, func(t *testing.T) {
			served, tier := cf.ServeVocabularyLabels(answer, true, app.investigationWordsBound(answer, row.caller, input))
			if tier != row.tier {
				t.Fatalf("tier %q, want %q", tier, row.tier)
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
		"fitProjectionWords": true, "unstampedProjection": true, "investigationWordsBound": true, "MarshalContextFabricResponse": true,
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

// A cut answer on the real engine: a ranked team cohort with no facts and more
// claimed facts than the item ceiling, trimmed and disclosed by
// budgetTrimDisclose, then served by the real routes to the narrowed user. What a
// reader has left once the map is shed is the point of the tiers: the status
// words and limitations[].

const (
	cutAnswerMaxItems = 30
	cutAnswerClaims   = 120
)

type cutAnswerSynthesizer struct{ members []cf.SubjectRef }

func (s cutAnswerSynthesizer) Synthesize(context.Context, storage.Principal, cf.SynthesisInput) (cf.InvestigationResult, error) {
	// The prose is what a projection keeps, and a caller's bound has a floor, so
	// it is long enough to put the projection above that floor.
	prose := strings.TrimSpace(strings.Repeat("Pressure appears moderate. ", 120))
	green := "green"
	claims := make([]cf.ClaimedFact, 0, cutAnswerClaims)
	for index := range cutAnswerClaims {
		claims = append(claims, cf.ClaimedFact{ClaimID: fmt.Sprintf("claim_%02d", index), Kind: cf.FactHealth, Subject: s.members[index%len(s.members)], Field: "severity", Value: cf.ScalarValue{String: &green}})
	}
	return cf.InvestigationResult{
		Status: cf.InvestigationComplete, DirectJudgment: prose, CurrentState: prose,
		StrongestPressures: []string{}, Drivers: []cf.DriverJudgment{}, RemainingWork: []cf.Finding{}, ReadinessGaps: []cf.Finding{},
		Paths: []cf.RelationshipPath{}, Conflicts: []cf.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
		Coverage:            cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: prose, Warnings: []string{},
		Versions: cf.VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
	}, nil
}

type cutAnswerRig struct {
	app   *App
	token string
	logs  *lockedBuffer
	store *memoryinvestigation.Store
}

// newCutAnswerRig serves the cut answer. The trim lowers it to partial. With no
// facts its ranking row is unavailable, so authority corrects it to degraded,
// and that is what puts a correction note on the answer.
func newCutAnswerRig(t *testing.T, enabled, authority bool) *cutAnswerRig {
	t.Helper()
	members := []cf.CohortMember{}
	subjects := []cf.SubjectRef{}
	for i := 1; i <= 2; i++ {
		subject := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: fmt.Sprintf("team:cut%d", i), Label: fmt.Sprintf("Team %d", i)}
		members = append(members, cf.CohortMember{Subject: subject, Rank: i, InclusionReasons: []string{"bounded cohort"}})
		subjects = append(subjects, subject)
	}
	store := memoryinvestigation.NewStore()
	registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(nil), cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	next := 0
	engine, err := cf.NewEngine(cf.EngineDependencies{
		Interpreter: rankingSurfaceInterpreter{},
		Graph: rankingSurfaceGraph{
			surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{subjects[0]}}},
			cohort:       &cf.Cohort{Kind: cf.SubjectTeam, Members: members, Rationale: "bounded team cohort", Complete: true},
		},
		Facts: liveFactReader{bundle: cf.CanonicalFactBundle{
			Facts: []cf.CanonicalFact{}, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}},
			Version: "ops-v1", Versions: map[cf.FactKind]string{}, Watermarks: map[cf.FactKind]string{},
		}},
		Synthesizer: cutAnswerSynthesizer{members: subjects}, Results: store, Requirements: registry,
	}, cf.EngineOptions{
		ServiceVersion: "acr-test", MaxItems: cutAnswerMaxItems, MaxSerializedBytes: 65536, SynthesisDeadlineReserve: time.Second,
		ServerCompletenessAuthorityEnabled: authority, ServerCompletenessAuthoritySymmetricEnabled: authority,
		Now: func() time.Time { return time.Unix(700, 0).UTC() }, NewResultID: func() string {
			next++
			return fmt.Sprintf("result_cut_answer_%04d", next)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	app, token := newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 1 << 20, MaxBytes: 1 << 24}, logs)
	app.config.ContextFabricVocabularyLabelsEnabled = enabled
	app.config.ServerCompletenessAuthorityEnabled, app.config.ServerCompletenessAuthoritySymmetricEnabled = authority, authority
	app.config.MaxSerializedBytes = v1.ContextFabricSerializedBytesMax
	app.config.MaxItems = 500
	serveAsNarrowedUser(t, app)
	return &cutAnswerRig{app: app, token: token, logs: logs, store: store}
}

// overBoundDoor serves the real cut answer as the engine stored it, at a create
// door with a bound below it. The engine fits a fresh answer under the caller's
// bound, so the door reaches its projection only for a document handed over
// larger than that bound, and a stub investigator is the one way to hand it over.
// The document is the engine's; only the hand-over is stubbed.
func (r *cutAnswerRig) overBoundDoor(t *testing.T, answer cf.InvestigationResult, enabled bool) func(int) (*httptest.ResponseRecorder, *lockedBuffer) {
	t.Helper()
	stored, err := r.store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, answer.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Result.VocabularyLabels != nil {
		t.Fatalf("the stored document carries the map: %v", stored.Result.VocabularyLabels)
	}
	app, token, logs := boundedLabelsApp(t, memoryinvestigation.NewStore(), stored.Result)
	app.config.ContextFabricVocabularyLabelsEnabled = enabled
	serveAsNarrowedUser(t, app)
	return func(bound int) (*httptest.ResponseRecorder, *lockedBuffer) {
		t.Helper()
		request := surfaceRequest("request_cut_answer")
		request.Options.MaxSerializedBytes = bound
		return serveJSON(t, app, token, request), logs
	}
}

func (r *cutAnswerRig) post(t *testing.T, bound int) *httptest.ResponseRecorder {
	t.Helper()
	request := surfaceRequest("request_cut_answer")
	request.Options.MaxSerializedBytes = bound
	return serveJSON(t, r.app, r.token, request)
}

// whole is the cut answer with nothing bounding the door. It proves the fixture:
// a real trim, disclosed, and a map holding words beyond the status tier.
func (r *cutAnswerRig) whole(t *testing.T) (*httptest.ResponseRecorder, cf.InvestigationResult) {
	t.Helper()
	recorder := r.post(t, v1.ContextFabricSerializedBytesMax)
	if recorder.Code != http.StatusOK {
		t.Fatalf("the cut answer: %d %s\n%s", recorder.Code, recorder.Body.String(), r.logs.String())
	}
	answer := decodeLabeled(t, recorder.Body.Bytes())
	if cutSentence(t, answer.Limitations) == "" || len(answer.ClaimedFacts) >= cutAnswerClaims {
		t.Fatalf("no real trim: %d claims, limitations %q", len(answer.ClaimedFacts), answer.Limitations)
	}
	return recorder, answer
}

// cutSentence is the limitation that tells the reader this answer was cut.
func cutSentence(t *testing.T, limitations []string) string {
	t.Helper()
	index := slices.IndexFunc(limitations, func(limitation string) bool { return strings.HasPrefix(limitation, "This answer shows ") })
	if index < 0 {
		t.Fatalf("limitations %q do not say the answer was cut", limitations)
	}
	return limitations[index]
}

// bareBytes is what the answer measures with no map.
func bareBytes(t *testing.T, answer cf.InvestigationResult) int {
	t.Helper()
	answer.VocabularyLabels = nil
	_, size, err := marshalContextFabricResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	return int(size)
}

// (a) One byte short of the full map, on a real cut and corrected answer: the
// status word, the note and the limitation are all in the body, and the document
// stays canonical.
func TestACutAnswerKeepsItsStatusWordsNoteAndLimitation(t *testing.T) {
	rig := newCutAnswerRig(t, true, true)
	whole, answer := rig.whole(t)
	if answer.StatusCorrection == nil || answer.VocabularyLabels[v1.ContextFabricStatusCorrectionNoteKey] == "" {
		t.Fatalf("no correction note on this answer, so this proves nothing: status %q %+v", answer.Status, answer.StatusCorrection)
	}
	keep := statusWordsOf(t, answer.VocabularyLabels)

	bound := whole.Body.Len() - 1
	recorder := rig.post(t, bound)
	if recorder.Code != http.StatusOK || recorder.Body.Len() > bound {
		t.Fatalf("one byte short: %d, %d bytes of %d", recorder.Code, recorder.Body.Len(), bound)
	}
	served := decodeLabeled(t, recorder.Body.Bytes())
	if !maps.Equal(served.VocabularyLabels, keep) {
		t.Fatalf("the served map is %v, want %v", served.VocabularyLabels, keep)
	}
	if got := cutSentence(t, served.Limitations); got != cutSentence(t, answer.Limitations) {
		t.Fatalf("the body says %q", got)
	}
	if served.VocabularyLabels[v1.ContextFabricStatusCorrectionNoteKey] == "" {
		t.Fatal("the note is gone")
	}
	if len(served.ClaimedFacts) == 0 {
		t.Fatal("the document stopped being canonical")
	}
	if !strings.Contains(rig.logs.String(), tierLine(v1.ContextFabricVocabularyLabelsStatusOnly)) {
		t.Fatalf("the shed did not name its tier:\n%s", rig.logs.String())
	}
}

// (b) A trimmed answer's status is partial, and with no map its pill reads the
// neutral word. The status tier keeps the partial word.
func TestATrimmedAnswersPillReadsThePartialWord(t *testing.T) {
	rig := newCutAnswerRig(t, true, false)
	whole, answer := rig.whole(t)
	if answer.Status != v1.ContextFabricInvestigationPartial || answer.StatusCorrection != nil {
		t.Fatalf("status %q correction %+v: the trim itself must be what lowered it", answer.Status, answer.StatusCorrection)
	}
	statusWordsOf(t, answer.VocabularyLabels)
	recorder := rig.post(t, whole.Body.Len()-1)
	if recorder.Code != http.StatusOK {
		t.Fatalf("one byte short: %d", recorder.Code)
	}
	if got := decodeLabeled(t, recorder.Body.Bytes()).VocabularyLabels["answer_status:partial"]; got != "Partial answer" {
		t.Fatalf("the pill reads %q, want the partial word", got)
	}
}

// (c) Over the caller's bound the document is the projection, with its full
// result left out, and at one byte short of its full map it keeps the same
// words, each read against its own completeness.
func TestACutProjectionKeepsItsStatusWordsNoteAndLimitation(t *testing.T) {
	rig := newCutAnswerRig(t, true, true)
	_, answer := rig.whole(t)
	door := rig.overBoundDoor(t, answer, true)
	first, _ := door(bareBytes(t, answer) - 1)
	if first.Code != http.StatusOK {
		t.Fatalf("over the caller's bound: %d %s", first.Code, first.Body.String())
	}
	full := decodeProjection(t, first.Body.Bytes())
	if !full.ProjectionBudget.FullResultOmitted || full.VocabularyLabels[v1.ContextFabricStatusCorrectionNoteKey] == "" {
		t.Fatalf("omitted=%v map=%v: this was not the path under test", full.ProjectionBudget.FullResultOmitted, full.VocabularyLabels)
	}
	assertProjectionStatusWord(t, full)
	keep := statusWordsOf(t, full.VocabularyLabels)

	bound := first.Body.Len() - 1
	recorder, logs := door(bound)
	if recorder.Code != http.StatusOK || recorder.Body.Len() > bound {
		t.Fatalf("one byte short: %d, %d bytes of %d", recorder.Code, recorder.Body.Len(), bound)
	}
	projected := decodeProjection(t, recorder.Body.Bytes())
	if !projected.ProjectionBudget.FullResultOmitted {
		t.Fatal("the full result is not declared left out")
	}
	if !maps.Equal(projected.VocabularyLabels, keep) {
		t.Fatalf("the served map is %v, want %v", projected.VocabularyLabels, keep)
	}
	assertProjectionStatusWord(t, projected)
	if got := cutSentence(t, projected.Limitations); got != cutSentence(t, answer.Limitations) {
		t.Fatalf("the projection says %q", got)
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsStatusOnly)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}
}

// A complete status over complete completeness reads "Full answer" on the
// canonical document, but the create door's projection leaves the full result
// out and lowers its state, so its pill must not say so. The new word is longer,
// so at the projection's exact size the body still fits: the bytes are measured
// after the word is set.
func TestAProjectionThatLeftTheFullResultOutNeverReadsFullAnswer(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound07")
	answer.Status = v1.ContextFabricInvestigationComplete
	answer.Completeness.State = v1.ContextFabricAnswerCompletenessComplete
	answer.Completeness.TerminalStatus = v1.ContextFabricInvestigationComplete
	labeled, _ := cf.ServeVocabularyLabels(answer, true, 0)
	if labeled.VocabularyLabels["answer_status:complete"] != "Full answer" {
		t.Fatalf("the canonical word is %q: this proves nothing", labeled.VocabularyLabels["answer_status:complete"])
	}
	app, token, _ := boundedLabelsApp(t, memoryinvestigation.NewStore(), answer)
	_, canonicalBytes, err := marshalContextFabricResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	body := investigationRequestBody()
	body.Options.MaxSerializedBytes = int(canonicalBytes) - 1
	first := serveJSON(t, app, token, body)
	if first.Code != http.StatusOK {
		t.Fatalf("over the caller's bound: %d %s", first.Code, first.Body.String())
	}
	body.Options.MaxSerializedBytes = first.Body.Len()
	recorder := serveJSON(t, app, token, body)
	if recorder.Code != http.StatusOK || recorder.Body.Len() > body.Options.MaxSerializedBytes {
		t.Fatalf("at its own size: %d, %d bytes of %d", recorder.Code, recorder.Body.Len(), body.Options.MaxSerializedBytes)
	}
	projection := decodeProjection(t, recorder.Body.Bytes())
	if !projection.ProjectionBudget.FullResultOmitted || projection.Completeness.State == v1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("omitted=%v state=%q: this proves nothing", projection.ProjectionBudget.FullResultOmitted, projection.Completeness.State)
	}
	if got := projection.VocabularyLabels["answer_status:complete"]; got != "Answer, not fully covered" {
		t.Fatalf("the pill reads %q", got)
	}
}

// A stored row whose sources hold no words of today: the stamps are what the
// serve adds, and they are the last words to go.
func withUnstampedSources(row cf.InvestigationResult) cf.InvestigationResult {
	row.Coverage.Sources = nil
	for index := range 12 {
		row.Coverage.Sources = append(row.Coverage.Sources, v1.ContextFabricSourceObservation{
			Source: fmt.Sprintf("canonical_fact:source_%02d", index), State: v1.ContextFabricSourceTruncated, Reason: "the read was cut", StateLabel: "limited",
		})
	}
	return row
}

// Only the stamps cross the ceiling. By id, each tier goes in turn, and the last
// one projects the stored row again, so a stamp never refuses the answer. Both
// sizes matter: a large row's canonical document is over the ceiling and stood
// unstamped already, and a small row's fits it stamped, because its projection is
// the larger document.
func TestAStampedProjectionByIDIsNotRefusedForItsStamps(t *testing.T) {
	const stampedWord, storedWord = `"state_label":"partly included"`, `"state_label":"limited"`
	small := validContextFabricInvestigationResult()
	small.ResultID = "result_labels_bound11"
	for name, row := range map[string]cf.InvestigationResult{
		"canonical over the ceiling": withUnstampedSources(threeRollupProjectStatusResult("result_labels_bound08")),
		"canonical within it":        withUnstampedSources(small),
	} {
		t.Run(name, func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			seedResult3355(t, store, callerOrgID, row)
			app, token, logs := boundedLabelsApp(t, store, row)
			read := func(ceiling int, query string) *httptest.ResponseRecorder {
				t.Helper()
				app.config.MaxSerializedBytes = ceiling
				return readByID(t, app, token, row.ResultID, query)
			}

			stamped := read(labelsCallerBytes, "view=projection")
			if stamped.Code != http.StatusOK || !strings.Contains(stamped.Body.String(), stampedWord) {
				t.Fatalf("unbounded: %d %.400s", stamped.Code, stamped.Body.String())
			}
			for strings.Contains(stamped.Body.String(), "vocabulary_labels") {
				stamped = read(stamped.Body.Len()-1, "view=projection")
			}
			if stamped.Code != http.StatusOK || !strings.Contains(stamped.Body.String(), stampedWord) {
				t.Fatalf("no map keeps the stamps: %d %.400s", stamped.Code, stamped.Body.String())
			}
			logs.Reset()

			// The exact size of the stamped document: nothing more goes.
			if exact := read(stamped.Body.Len(), "view=projection"); exact.Code != http.StatusOK || !strings.Contains(exact.Body.String(), stampedWord) {
				t.Fatalf("a stamped projection exactly at the ceiling lost its stamps: %d", exact.Code)
			}

			ceiling := stamped.Body.Len() - 1
			canonical := read(labelsCallerBytes, "")
			if within := canonical.Body.Len() <= ceiling; within != (name == "canonical within it") {
				t.Fatalf("canonical %d bytes against a %d ceiling: this is not the row under test", canonical.Body.Len(), ceiling)
			}
			unstamped := read(ceiling, "view=projection")
			if unstamped.Code != http.StatusOK || unstamped.Body.Len() > ceiling {
				t.Fatalf("one byte short of the stamps: %d, %d bytes of %d: %.300s", unstamped.Code, unstamped.Body.Len(), ceiling, unstamped.Body.String())
			}
			if strings.Contains(unstamped.Body.String(), stampedWord) || !strings.Contains(unstamped.Body.String(), storedWord) || strings.Contains(unstamped.Body.String(), "vocabulary_labels") {
				t.Fatalf("this is not the stored row's own projection: %.600s", unstamped.Body.String())
			}
			if decodeProjection(t, unstamped.Body.Bytes()).ProjectionBudget.FullResultOmitted {
				t.Fatal("a read by id left no full result out")
			}

			// The refusal stands where it always stood, for the row's own bytes.
			if refused := read(unstamped.Body.Len()-1, "view=projection"); refused.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("over the ceiling with no words: %d", refused.Code)
			}
		})
	}
}

// The create door's projection: the stamps go before a row does, and the document
// still declares the full result left out.
func TestAStampedProjectionCutsNoRowForItsStamps(t *testing.T) {
	answer := withUnstampedSources(threeRollupProjectStatusResult("result_labels_bound09"))
	labeled, _ := cf.ServeVocabularyLabels(answer, true, 0)
	app := &App{}
	options := v1.ContextFabricInvestigationOptions{MaxDrivers: 10, MaxCohortMembers: 50, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20}
	whole, _, _, _, _, fits := app.projectToCallerOption(labeled, answer, options)
	if !fits || whole.VocabularyLabels == nil {
		t.Fatalf("the projection must fit with its words first: %v", fits)
	}
	noMap := answerprojection.ShedVocabularyLabels(whole, v1.ContextFabricVocabularyLabelsNone)
	_, noMapBytes, err := marshalContextFabricResponse(noMap)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxSerializedBytes = int(noMapBytes) - 1
	cut, encoded, cutBytes, budget, tier, fits := app.projectToCallerOption(labeled, answer, options)
	if !fits || cutBytes > noMapBytes-1 || tier != v1.ContextFabricVocabularyLabelsNone {
		t.Fatalf("one byte short of the stamps: fits=%v %d bytes tier %q", fits, cutBytes, tier)
	}
	if budget.MaxDrivers != options.MaxDrivers || budget.MaxEvidenceRefs != options.MaxEvidenceRefs || budget.MaxCohortMembers != options.MaxCohortMembers {
		t.Fatalf("the rows were cut for a stamp: %+v", budget)
	}
	if !cut.ProjectionBudget.FullResultOmitted || !strings.Contains(string(encoded), `"state_label":"limited"`) {
		t.Fatalf("omitted=%v: this is not the answer's own projection", cut.ProjectionBudget.FullResultOmitted)
	}

	// Below the answer's own projection the rows do go, as they always did, and
	// nothing is served over the bound.
	options.MaxSerializedBytes = int(cutBytes) - 1
	_, _, smallerBytes, cutBudget, _, fits := app.projectToCallerOption(labeled, answer, options)
	if fits && smallerBytes > int64(options.MaxSerializedBytes) {
		t.Fatalf("served %d bytes over a %d bound", smallerBytes, options.MaxSerializedBytes)
	}
	if cutBudget.MaxEvidenceRefs == options.MaxEvidenceRefs {
		t.Fatalf("no row went below the answer's own projection: %+v", cutBudget)
	}
}

// The last tier carries no map, whatever the answer held.
func TestTheUnstampedProjectionCarriesNoMap(t *testing.T) {
	answer := threeRollupProjectStatusResult("result_labels_bound10")
	answer.VocabularyLabels = map[string]string{"answer_status:complete": "a word from elsewhere"}
	projection, _, _, ok := unstampedProjection(answer, answerprojection.Budget{}, false)
	if !ok || projection.VocabularyLabels != nil {
		t.Fatalf("ok=%v map=%v", ok, projection.VocabularyLabels)
	}
}

// The same on the read by id: no full result is left out there, and the tier is
// the projection's own.
func TestACutProjectionByIDKeepsItsStatusWordsNoteAndLimitation(t *testing.T) {
	rig := newCutAnswerRig(t, true, true)
	_, answer := rig.whole(t)
	first := readByID(t, rig.app, rig.token, answer.ResultID, "view=projection")
	if first.Code != http.StatusOK {
		t.Fatalf("projection by id: %d %s", first.Code, first.Body.String())
	}
	full := decodeProjection(t, first.Body.Bytes())
	keep := statusWordsOf(t, full.VocabularyLabels)
	if keep[v1.ContextFabricStatusCorrectionNoteKey] == "" {
		t.Fatalf("no note on the projection, so this proves nothing: %v", keep)
	}
	rig.app.config.MaxSerializedBytes = first.Body.Len() - 1
	recorder := readByID(t, rig.app, rig.token, answer.ResultID, "view=projection")
	if recorder.Code != http.StatusOK || recorder.Body.Len() > rig.app.config.MaxSerializedBytes {
		t.Fatalf("one byte short: %d, %d bytes", recorder.Code, recorder.Body.Len())
	}
	projected := decodeProjection(t, recorder.Body.Bytes())
	if !maps.Equal(projected.VocabularyLabels, keep) {
		t.Fatalf("the served map is %v, want %v", projected.VocabularyLabels, keep)
	}
	if got := cutSentence(t, projected.Limitations); got != cutSentence(t, answer.Limitations) {
		t.Fatalf("the projection says %q", got)
	}
}

// (d) A bound too small for the status tier serves no map, and the answer is the
// one a reader gets with no words at all, on both documents.
func TestABoundTooSmallForTheStatusWordsLeavesTheAnswerUnharmed(t *testing.T) {
	plain := newCutAnswerRig(t, false, true)
	plainWhole, plainAnswer := plain.whole(t)
	if plainAnswer.VocabularyLabels != nil {
		t.Fatalf("off served a map: %v", plainAnswer.VocabularyLabels)
	}
	plainSentence := cutSentence(t, plainAnswer.Limitations)
	assertUnharmed := func(t *testing.T, label string, recorder *httptest.ResponseRecorder, wantBytes int) {
		t.Helper()
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "vocabulary_labels") || recorder.Body.Len() != wantBytes {
			t.Fatalf("%s: %d, %d bytes, want the %d of the same answer with no words: %s", label, recorder.Code, recorder.Body.Len(), wantBytes, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), plainSentence) {
			t.Fatalf("%s: the body lost its limitation", label)
		}
	}

	rig := newCutAnswerRig(t, true, true)
	canonical := rig.post(t, plainWhole.Body.Len())
	assertUnharmed(t, "canonical", canonical, plainWhole.Body.Len())
	served := decodeLabeled(t, canonical.Body.Bytes())
	if served.Status != plainAnswer.Status || len(served.ClaimedFacts) != len(plainAnswer.ClaimedFacts) || served.DirectJudgment != plainAnswer.DirectJudgment {
		t.Fatalf("the answer changed: status %q/%q claims %d/%d", served.Status, plainAnswer.Status, len(served.ClaimedFacts), len(plainAnswer.ClaimedFacts))
	}
	if !strings.Contains(rig.logs.String(), tierLine(v1.ContextFabricVocabularyLabelsNone)) {
		t.Fatalf("the shed did not name its tier:\n%s", rig.logs.String())
	}

	plainProjection, _ := plain.overBoundDoor(t, plainAnswer, false)(plainWhole.Body.Len() - 1)
	if plainProjection.Code != http.StatusOK || decodeProjection(t, plainProjection.Body.Bytes()).VocabularyLabels != nil {
		t.Fatalf("plain projection: %d %s", plainProjection.Code, plainProjection.Body.String())
	}
	projected, logs := rig.overBoundDoor(t, plainAnswer, true)(plainProjection.Body.Len())
	assertUnharmed(t, "projection", projected, plainProjection.Body.Len())
	if !decodeProjection(t, projected.Body.Bytes()).ProjectionBudget.FullResultOmitted {
		t.Fatal("the full result is not declared left out")
	}
	if !strings.Contains(logs.String(), tierLine(v1.ContextFabricVocabularyLabelsNone)) {
		t.Fatalf("the shed did not name its tier:\n%s", logs.String())
	}
}
