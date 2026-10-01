package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamAtTheCapItems is how many work items, pull requests and reviews the
// team owns: more than the scope expansion admits for one kind.
const teamAtTheCapItems = 250

// seedTeamAtTheExpansionCap gives the fixture team more work items, pull
// requests and reviews than one expansion admits, with one blocker per work
// item.
func seedTeamAtTheExpansionCap(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, at time.Time) {
	t.Helper()
	for _, statement := range devhealthschema.DDL("work_item_dependencies") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	stamp := at.Format("2006-01-02 15:04:05")
	var workItems, attributions, dependencies, pullRequests, reviews []string
	for index := 0; index < teamAtTheCapItems; index++ {
		id := fmt.Sprintf("PLAT-%04d", index)
		blocker := fmt.Sprintf("PLAT-%04d", (index+1)%teamAtTheCapItems)
		title := "Carry the delivery signal for work item " + id + " through the weekly review and the release checklist"
		number := 100 + index
		workItems = append(workItems, fmt.Sprintf("('%s', '%s', '%s', '%s', 'open', 'https://tracker.example.test/issue/%s', '', '', '%s')", id, chaos4099ZeroRepositoryID, chaos4099OrgID, title, id, stamp))
		attributions = append(attributions, fmt.Sprintf("('%s', '%s', '%s', '%s', 'native_team', 1, 'high', '%s')", chaos4099OrgID, chaos4099ZeroRepositoryID, id, chaos4101TeamID, stamp))
		dependencies = append(dependencies, fmt.Sprintf("('%s', '%s', 'blocks', 'Blocks', '%s', '%s')", blocker, id, stamp, chaos4099OrgID))
		pullRequests = append(pullRequests, fmt.Sprintf("('%s', '%s', %d, 'Change %d', 'open', '%s', '%s', NULL, NULL, 'feat/change-%d', '')", chaos4101RepoAID, chaos4099OrgID, number, number, stamp, stamp, number))
		reviews = append(reviews, fmt.Sprintf("('review-%d', '%s', '%s', %d, 'approved', '%s')", number, chaos4101RepoAID, chaos4099OrgID, number, stamp))
	}
	for label, statement := range map[string]string{
		"work items":    `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES ` + strings.Join(workItems, ", "),
		"attributions":  `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES ` + strings.Join(attributions, ", "),
		"dependencies":  `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ` + strings.Join(dependencies, ", "),
		"pull requests": `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES ` + strings.Join(pullRequests, ", "),
		"reviews":       `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at) VALUES ` + strings.Join(reviews, ", "),
	} {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
}

// recordedSynthesisModel is an OpenAI-compatible endpoint that answers every
// chat completion with one synthesis draft and keeps the input each carried.
type recordedSynthesisModel struct {
	mu     sync.Mutex
	inputs []string
	url    string
}

func newRecordedSynthesisModel(t *testing.T) *recordedSynthesisModel {
	t.Helper()
	model := &recordedSynthesisModel{}
	const draft = `{"status":"complete","direct_judgment":"The Platform team has open work.","current_state":"Open work.","strongest_pressures":[],"drivers":[],"remaining_work":[],"readiness_gaps":[],"conflicts":[],"limitations":[],"evidence_ref_ids":[],"claimed_facts":[],"deterministic_answer":"The Platform team has open work.","warnings":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read model request: %v", err)
		}
		var decoded struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		for _, message := range decoded.Messages {
			if message.Role != "user" {
				continue
			}
			var text string
			if err := json.Unmarshal(message.Content, &text); err != nil {
				var parts []struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(message.Content, &parts); err != nil {
					t.Errorf("decode the user message: %v", err)
				}
				for _, part := range parts {
					text += part.Text
				}
			}
			model.mu.Lock()
			model.inputs = append(model.inputs, text)
			model.mu.Unlock()
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"id":"chatcmpl-test","object":"chat.completion","created":1760000000,"model":"gpt-5-nano","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":41,"completion_tokens":17,"total_tokens":58}}`, draft)
	}))
	t.Cleanup(server.Close)
	model.url = server.URL + "/v1/"
	return model
}

func (m *recordedSynthesisModel) given() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.inputs...)
}

// The production chain end to end against a real ClickHouse: the real scope
// expander admits its cap for each of the six kinds a team question expands,
// the real producers answer a fact for each target, and the facts are larger
// than the model input bound. The real synthesizer over the production model
// runtime still answers: the model is given the same part of every kind, and
// the answer is partial and says why.
func TestATeamAtTheExpansionCapIsSynthesizedWithinTheModelInputBound(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)
	seedTeamAtTheExpansionCap(t, ctx, direct, at)

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
		ScopeExpander: devhealthfacts.NewScopeExpander(query), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}}
	kinds := []contextfabric.FactKind{
		contextfabric.FactIdentity, contextfabric.FactActualCompletion, contextfabric.FactWork,
		contextfabric.FactBlockers, contextfabric.FactPullRequests, contextfabric.FactReviews,
	}
	requirements := make([]contextfabric.FactRequirement, 0, len(kinds))
	for _, kind := range kinds {
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind})
	}
	bundle, err := registry.ReadFacts(ctx, principal, contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Requirements: requirements,
	})
	if err != nil {
		t.Fatalf("ReadFacts error = %v", err)
	}
	read := map[contextfabric.FactKind]int{}
	for _, fact := range bundle.Facts {
		read[fact.Kind]++
	}
	const expansionCap = 200
	for _, kind := range kinds {
		if read[kind] != expansionCap {
			t.Fatalf("facts read = %v, want %d of every kind: the expansion cap", read, expansionCap)
		}
	}

	input := contextfabric.SynthesisInput{
		Request: contextfabric.InvestigationRequest{RequestID: "request_team_cap_0001", Question: "How is the Platform team doing?"},
		Graph: contextfabric.GraphContext{Resolution: contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{chaos4101TeamSubject()},
		}},
		Facts: bundle,
	}
	unbounded, err := genkitruntime.BuildSynthesisPrompt(input, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(unbounded) <= genkitruntime.DefaultExchangeMaxInputBytes {
		t.Fatalf("the unbounded synthesis input is %d bytes, within the bound of %d: this fixture no longer reaches the bound", len(unbounded), genkitruntime.DefaultExchangeMaxInputBytes)
	}

	model := newRecordedSynthesisModel(t)
	runtime, err := modelprovider.New(ctx, modelprovider.Config{
		Provider: modelprovider.DefaultProvider, BaseURL: model.url, Model: modelprovider.DefaultModel,
		APIKey: "sk-configured", Timeout: 10 * time.Second, MaxAttempts: 1, MaxTransportRetries: 0, AllowInsecureBaseURL: true,
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := contextfabric.RuntimeAnswerSynthesizer{Runtime: runtime, Telemetry: contextfabric.NewSlogEngineTelemetry(logger)}.Synthesize(ctx, principal, input)

	if err != nil {
		t.Fatalf("Synthesize error = %q, want the answer served", err)
	}
	inputs := model.given()
	if len(inputs) == 0 {
		t.Fatal("the model was not called")
	}
	if len(inputs[0]) > genkitruntime.DefaultExchangeMaxInputBytes {
		t.Fatalf("the model was given %d bytes, bound %d", len(inputs[0]), genkitruntime.DefaultExchangeMaxInputBytes)
	}
	var given struct {
		Facts []struct {
			Kind contextfabric.FactKind `json:"kind"`
		} `json:"canonical_facts"`
	}
	if err := json.Unmarshal([]byte(inputs[0]), &given); err != nil {
		t.Fatalf("the model input is not the synthesis input: %v", err)
	}
	perKind := map[contextfabric.FactKind]int{}
	for _, fact := range given.Facts {
		perKind[fact.Kind]++
	}
	share := perKind[contextfabric.FactIdentity]
	if share == 0 || share >= expansionCap {
		t.Fatalf("facts given = %v, want a part of the %d read for every kind", perKind, expansionCap)
	}
	for _, kind := range kinds {
		if perKind[kind] != share {
			t.Fatalf("facts given = %v, want %d of every kind: the same share", perKind, share)
		}
	}
	if result.Status != contextfabric.InvestigationPartial || !result.Coverage.Partial {
		t.Fatalf("status = %q coverage.partial = %v, want a partial answer: the model answered complete from part of the facts", result.Status, result.Coverage.Partial)
	}
	disclosed := false
	for _, limitation := range result.Limitations {
		disclosed = disclosed || limitation == contractsv1.ContextFabricSynthesisInputBoundedLimitation
	}
	if !disclosed {
		t.Fatalf("limitations = %q, want the bounded-input disclosure", result.Limitations)
	}
	var bound map[string]any
	for _, line := range strings.Split(logs.String(), "\n") {
		entry := map[string]any{}
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "context fabric synthesis input bounded" {
			bound = entry
		}
	}
	if bound["outcome"] != "fitted" || bound["input_bytes"] != float64(len(unbounded)) || bound["facts_read"] != float64(len(bundle.Facts)) ||
		bound["facts_given"] != float64(len(given.Facts)) || bound["kinds_given"] != float64(len(kinds)) || bound["kinds_bounded"] != float64(len(kinds)) {
		t.Fatalf("bound line = %v, want outcome=fitted input_bytes=%d facts_read=%d facts_given=%d kinds_given=%d kinds_bounded=%d", bound, len(unbounded), len(bundle.Facts), len(given.Facts), len(kinds), len(kinds))
	}
}

// The same bounded team input twice, once with nothing to rank on and once
// with the late-read facts named by the question's requirements. Counts kept
// per kind are the same; the named facts are kept only by relevance.
func TestABoundedTeamInputKeepsTheFactsTheQuestionNamesNotTheFirstRead(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)
	seedTeamAtTheExpansionCap(t, ctx, direct, at)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
		ScopeExpander: devhealthfacts.NewScopeExpander(query), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}}
	kinds := []contextfabric.FactKind{
		contextfabric.FactIdentity, contextfabric.FactActualCompletion, contextfabric.FactWork,
		contextfabric.FactBlockers, contextfabric.FactPullRequests, contextfabric.FactReviews,
	}
	requirements := make([]contextfabric.FactRequirement, 0, len(kinds))
	for _, kind := range kinds {
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind})
	}
	bundle, err := registry.ReadFacts(ctx, principal, contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Requirements: requirements,
	})
	if err != nil {
		t.Fatalf("ReadFacts error = %v", err)
	}
	const namedPerKind = 5
	named := map[contextfabric.FactKind][]contextfabric.SubjectRef{}
	for _, fact := range bundle.Facts {
		named[fact.Kind] = append(named[fact.Kind], fact.Subject)
	}
	var namedRequirements []contextfabric.FactRequirement
	namedIDs := map[string]struct{}{}
	namedFacts := 0
	for _, kind := range kinds {
		late := named[kind][len(named[kind])-namedPerKind:]
		namedRequirements = append(namedRequirements, contextfabric.FactRequirement{Kind: kind, Subjects: late})
		for _, subject := range late {
			namedIDs[string(kind)+"/"+subject.CanonicalID] = struct{}{}
			namedFacts++
		}
	}

	run := func(interpretation contextfabric.InterpretedQuestion) (map[contextfabric.FactKind]int, int) {
		input := contextfabric.SynthesisInput{
			Request:        contextfabric.InvestigationRequest{RequestID: "request_team_cap_0002", Question: "How is the Platform team doing?"},
			Interpretation: interpretation,
			Graph: contextfabric.GraphContext{Resolution: contextfabric.SubjectResolution{
				Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{chaos4101TeamSubject()},
			}},
			Facts: bundle,
		}
		model := newRecordedSynthesisModel(t)
		runtime, err := modelprovider.New(ctx, modelprovider.Config{
			Provider: modelprovider.DefaultProvider, BaseURL: model.url, Model: modelprovider.DefaultModel,
			APIKey: "sk-configured", Timeout: 10 * time.Second, MaxAttempts: 1, MaxTransportRetries: 0, AllowInsecureBaseURL: true,
			Logger: logger,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (contextfabric.RuntimeAnswerSynthesizer{Runtime: runtime}).Synthesize(ctx, principal, input); err != nil {
			t.Fatalf("Synthesize error = %q", err)
		}
		inputs := model.given()
		if len(inputs) == 0 {
			t.Fatal("the model was not called")
		}
		var given struct {
			Facts []struct {
				Kind    contextfabric.FactKind `json:"kind"`
				Subject struct {
					CanonicalID string `json:"canonical_id"`
				} `json:"subject"`
			} `json:"canonical_facts"`
		}
		if err := json.Unmarshal([]byte(inputs[0]), &given); err != nil {
			t.Fatalf("the model input is not the synthesis input: %v", err)
		}
		perKind, namedKept := map[contextfabric.FactKind]int{}, 0
		for _, fact := range given.Facts {
			perKind[fact.Kind]++
			if _, ok := namedIDs[string(fact.Kind)+"/"+fact.Subject.CanonicalID]; ok {
				namedKept++
			}
		}
		return perKind, namedKept
	}

	positionKinds, positionNamed := run(contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}})
	relevanceKinds, relevanceNamed := run(contextfabric.InterpretedQuestion{
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: namedRequirements,
	})
	t.Logf("kept per kind by position: %v; by relevance: %v", positionKinds, relevanceKinds)
	t.Logf("named facts kept (of %d): position %d, relevance %d", namedFacts, positionNamed, relevanceNamed)
	for _, kind := range kinds {
		if delta := positionKinds[kind] - relevanceKinds[kind]; delta < -2 || delta > 2 {
			t.Fatalf("kept per kind position = %v relevance = %v, want the same share of every kind (the requirements add bytes to the input, so +-2)", positionKinds, relevanceKinds)
		}
	}
	if relevanceNamed != namedFacts {
		t.Fatalf("named facts kept by relevance = %d, want all %d", relevanceNamed, namedFacts)
	}
	if positionNamed >= relevanceNamed {
		t.Fatalf("named facts kept by position = %d, want fewer than by relevance (%d): the late facts were cut", positionNamed, relevanceNamed)
	}
}
