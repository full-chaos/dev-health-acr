package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const infoRepository = "example-org/widget-service"
const infoRequestID = "req_57550000000000000000000000000000"

// This is a collection proof, not a deployed-data proof. A private subprocess
// uses the deployed hosted constructor and Info JSON stdout handler. The parent
// collects its file with the yardstick's byte-offset tail operation, then uses
// the existing event certifier. PostgreSQL and FalkorDB are real and private;
// only model drafts and ClickHouse backend rows are controlled. No tracer or
// telemetry override is supplied to the hosted constructor.
func TestWorkItemHostedInfoCollection(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44", tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	graph, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3", ExposedPorts: []string{"6379/tcp"}, WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := graph.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	host, err := graph.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := graph.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := host + ":" + port.Port()
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: addr, GraphPrefix: "acr-cf-info-proof", RequestTimeout: 5 * time.Second, MaxAttempts: 1, MaxResults: 50, PoolSize: 4, AllowInsecure: true, TLS: false})
	if err != nil {
		t.Fatal(err)
	}
	projectID, _, _ := identity.Derive(identity.KindProject, []string{"linear", "P1"}, nil)
	for _, scenario := range []string{"normal", "zero", "s1", "retry"} {
		t.Run(scenario, func(t *testing.T) {
			org := "info-proof-" + scenario
			now := time.Now().UTC()
			batch := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_info_proof_" + scenario, OrgID: org, Source: "info-proof", SourceVersion: "v1", NextCursor: "1", GeneratedAt: now, Entities: []contextfabric.EntityProjection{{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectID, Label: "Project Alpha"}, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{infoRepository}}, EvidenceRefIDs: []string{"evidence_project_info"}, ObservedAt: now, SourceVersion: "v1"}}}
			batch.Relationships = []contextfabric.RelationshipProjection{}
			batch.Contents = []contextfabric.ContentProjection{}
			batch.Episodes = []contextfabric.EpisodeProjection{}
			batch.Tombstones = []contextfabric.ProjectionTombstone{}
			if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if evidence := os.Getenv("ACR_INFO_PROOF_OUTPUT"); evidence != "" {
				dir = filepath.Join(evidence, scenario)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "api.log")
			output, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			// The offset is measured before launch, as before each yardstick request.
			offset, err := output.Seek(0, 2)
			if err != nil {
				t.Fatal(err)
			}
			processCtx, processCancel := context.WithTimeout(ctx, 30*time.Second)
			defer processCancel()
			command := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestWorkItemHostedInfoChild$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "ACR_") {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "ACR_INFO_PROOF_CHILD="+scenario, "ACR_INFO_PROOF_DSN="+dsn, "ACR_LOG_LEVEL=info", "ACR_ENVIRONMENT=development", "ACR_LOCAL_COMPOSITION_READY=true", falkorgraph.EnvAddr+"="+addr, falkorgraph.EnvTLS+"=false", falkorgraph.EnvAllowInsecure+"=true", falkorgraph.EnvGraphPrefix+"=acr-cf-info-proof")
			command.Stdout = output
			command.Stderr = output
			runErr := command.Run()
			_ = output.Close()
			collected, err := exec.Command("tail", "-c", fmt.Sprintf("+%d", offset+1), path).Output()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "api-log-slice.log"), collected, 0600); err != nil {
				t.Fatal(err)
			}
			if runErr != nil {
				t.Fatalf("private hosted process: %v\n%s", runErr, collected)
			}
			parsed, err := certify.Parse(collected)
			if err != nil {
				t.Fatalf("collected JSON: %v\n%s", err, collected)
			}
			want := map[string]any{"org_id": org, "request_id": infoRequestID, "state": "exact", "reason": "", "population_measured": true, "population_complete": true, "authorized_population": 1, "served_members": 1}
			if scenario == "zero" {
				want["authorized_population"] = 0
				want["served_members"] = 0
			}
			if scenario == "s1" {
				want["state"] = "unmeasured"
				want["reason"] = "s1_error"
				want["population_measured"] = false
				want["population_complete"] = false
				want["authorized_population"] = 0
				want["served_members"] = 0
			}
			if scenario == "retry" {
				want["state"] = "floor"
				want["population_complete"] = false
				want["authorized_population"] = 2001
				want["served_members"] = 34
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.WorkItemMembershipS1, Want: want}); err != nil {
				t.Errorf("collected S1 certificate: %v", err)
			}
			// These pre-eventspec records use the certifier's existing line reader.
			// Count both synthesis attempts, then require the retry and terminal
			// budget decisions, including the measured cause for selecting retry.
			requireInfoLine(t, parsed, "context fabric resolution trace: decision summary", map[string]any{"request_id": infoRequestID, "committed_count": 1})
			requireInfoLine(t, parsed, "context fabric plan narrowing", map[string]any{"request_id": infoRequestID, "stage": "cardinality", "before": 250, "after": 34, "basis": "canonical_id_lexical"})
			reads := parsed.LinesWithMsg("context fabric fact read")
			sharedReads := parsed.LinesWithMsg("readers.query_org_scoped")
			wantReads, wantSubjects, wantItems, wantAttempts := 2, 1, 4, 1
			if scenario == "zero" {
				wantReads = 0
				wantItems = 2
			}
			if scenario == "s1" {
				wantReads = 0
				wantItems = 1
			}
			if scenario == "retry" {
				wantSubjects = 34
				wantItems = 36
				wantAttempts = 2
			}
			if len(reads) != wantReads || len(sharedReads) != wantReads {
				t.Errorf("collected registry/shared reader records=%d/%d want %d", len(reads), len(sharedReads), wantReads)
			}
			if wantReads > 0 {
				for _, kind := range []string{"status", "work"} {
					requireInfoLine(t, parsed, "context fabric fact read", map[string]any{"request_id": infoRequestID, "org_id": org, "kind": kind, "state": "available", "outcome": "completed", "subjects": wantSubjects, "facts": wantSubjects})
				}
				for _, reader := range []string{"ReadWorkItemStatus", "ReadWorkItemTitle"} {
					requireInfoLine(t, parsed, "readers.query_org_scoped", map[string]any{"reader": reader, "org_scoped": true})
				}
			}
			if n := len(parsed.LinesWithMsg("context fabric projected rows count")); n != wantAttempts {
				t.Errorf("collected synthesis attempts=%d want %d", n, wantAttempts)
			}
			requireInfoLine(t, parsed, "context fabric plan narrowing", map[string]any{"request_id": infoRequestID, "stage": "assembled_result", "retry_attempted": scenario == "retry", "retry_fit": scenario == "retry", "retry_failed": false, "overrun": "fits", "measured_items": wantItems, "max_items": 50})
			requireInfoLine(t, parsed, "context fabric budget assertion", map[string]any{"request_id": infoRequestID, "assert_stage": "decisive", "fits": true, "measured_items": wantItems, "max_items": 50, "capacity": "certified_fit"})
			requireInfoLine(t, parsed, "context fabric semantic state persistence", map[string]any{"request_id": infoRequestID, "decision": "persisted", "site": "decisive"})
			if scenario != "retry" && len(parsed.LinesWithMsg("context fabric synthesis retry selected")) != 0 {
				t.Error("retry selection was collected for a single-attempt result")
			}
			if scenario == "retry" {
				requireInfoRetryTrigger(t, parsed, collected)
				requireInfoLine(t, parsed, "context fabric plan narrowing", map[string]any{"stage": "cardinality", "before": 2000, "after": 34})
				requireInfoLine(t, parsed, "context fabric plan narrowing", map[string]any{"stage": "assembled_result", "before": 34, "after": 17})
				requireInfoLine(t, parsed, "context fabric fact retention", map[string]any{"facts_before": 68, "facts_after": 34, "anchors": 1})
				requireInfoLine(t, parsed, "context fabric membership cardinality", map[string]any{"served": 2000, "declared": 2000, "claimed": true, "cohort_complete": false})
			}
			// Missing measurement cannot be accepted as the measured-zero event.
			withoutS1 := []byte{}
			for _, line := range bytes.Split(collected, []byte("\n")) {
				if !bytes.Contains(line, []byte(`"msg":"context fabric work item membership s1"`)) {
					withoutS1 = append(withoutS1, line...)
					withoutS1 = append(withoutS1, '\n')
				}
			}
			negative, err := certify.Parse(withoutS1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(negative, certify.Assertion{Event: eventspec.WorkItemMembershipS1, Want: want}); err == nil {
				t.Error("missing S1 line certified as a measurement")
			}
			// Removing a mandatory field must also fail: zero is a value, not
			// permission for a collector to omit population_measured.
			missingField := []byte{}
			for _, raw := range bytes.Split(collected, []byte("\n")) {
				if len(raw) == 0 {
					continue
				}
				var line map[string]any
				if err := json.Unmarshal(raw, &line); err != nil {
					t.Fatal(err)
				}
				if line["msg"] == "context fabric work item membership s1" {
					delete(line, "population_measured")
				}
				encoded, err := json.Marshal(line)
				if err != nil {
					t.Fatal(err)
				}
				missingField = append(missingField, encoded...)
				missingField = append(missingField, '\n')
			}
			missing, err := certify.Parse(missingField)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(missing, certify.Assertion{Event: eventspec.WorkItemMembershipS1, Want: want}); err == nil {
				t.Error("missing measured field certified")
			}
			for name, raw := range map[string][]byte{"negative-missing-s1.log": withoutS1, "negative-missing-measured-field.log": missingField} {
				if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, forbidden := range []string{"SECRET QUESTION", "controlled content read failure"} {
				if bytes.Contains(collected, []byte(forbidden)) {
					t.Errorf("unsafe diagnostic text %q", forbidden)
				}
			}
			t.Logf("collected %d bytes for %s at Info", len(collected), scenario)
		})
	}
}

func TestWorkItemHostedInfoChild(t *testing.T) {
	scenario := os.Getenv("ACR_INFO_PROOF_CHILD")
	if scenario == "" {
		return
	}
	runWorkItemInfoChild(t, scenario)
	// Avoid the test runner's non-JSON PASS suffix on the service log stream.
	os.Exit(0)
}

func runWorkItemInfoChild(t *testing.T, scenario string) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatal("production log level is not Info")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	cfg.EnableContextFabricInvestigations = true
	cfg.MaxItems = 50
	cfg.MaxSerializedBytes = 262144
	cfg.RequestTimeout = 5 * time.Second
	db, err := runtimepostgres.Open(context.Background(), runtimepostgres.Config{DSN: os.Getenv("ACR_INFO_PROOF_DSN")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	memberID, _, _ := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-1"}, nil)
	client := &infoTupleQueryClient{memberID: memberID, fail: scenario}
	if scenario == "normal" || scenario == "retry" {
		client.fail = ""
	}
	if scenario == "retry" {
		client.rowsByPhase = map[string][][]any{}
		for i := 0; i < 34; i++ {
			workID := fmt.Sprintf("work-%03d", i)
			id, _, _ := identity.Derive(identity.KindWorkItem, []string{"repo-1", workID}, nil)
			client.rowsByPhase["s1"] = append(client.rowsByPhase["s1"], []any{id, "repo-1", workID, infoRepository, uint8(1), uint64(2001), uint64(2001), uint64(0), uint64(0), uint64(0)})
			client.rowsByPhase["status"] = append(client.rowsByPhase["status"], []any{workID, "open", "repo-1"})
			client.rowsByPhase["work"] = append(client.rowsByPhase["work"], []any{workID, "Title " + workID, "repo-1"})
		}
	}
	model := &infoTupleModel{statusOnly: true, frame: contextfabric.QuestionFrame{Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalCountOrAggregate}, SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Project Alpha"}, MemberKind: contextfabric.SubjectWorkItem}}, Temporal: contextfabric.TemporalIntentCurrent}}
	sizes := []int{}
	model.observe = func(input contextfabric.SynthesisInput) {
		if input.Graph.Cohort != nil {
			sizes = append(sizes, len(input.Graph.Cohort.Members))
		}
	}
	investigator, _, _, _, clarification, structure, err := buildContextFabricInvestigator(context.Background(), buildRequest{config: cfg, options: Options{ServiceVersion: "info-proof", Logger: logger, Now: time.Now, ModelRuntimeOverride: model}}, postgresComponents{db: db}, clickHouseComponents{queryClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = clarification.Close(ctx)
		_ = structure.Close(ctx)
	}()
	projectID, _, _ := identity.Derive(identity.KindProject, []string{"linear", "P1"}, nil)
	request := contextfabric.InvestigationRequest{SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: infoRequestID, Question: "SECRET QUESTION: What is the state and count of Project Alpha work items?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{infoRepository}, SubjectHints: []contextfabric.SubjectHint{{Kind: contextfabric.SubjectProject, ID: projectID, Label: "Project Alpha", Source: "info-proof"}}}, Options: contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 250, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true}, Consumer: contextfabric.ConsumerInfo{Name: "info-proof", Version: "v1", Surface: "test"}}
	ctx, cancel := context.WithTimeout(observability.WithRequestID(context.Background(), infoRequestID), 5*time.Second)
	defer cancel()
	result, err := investigator.Investigate(ctx, storage.Principal{OrgID: "info-proof-" + scenario, RepositoryScopes: []string{infoRepository}}, request)
	if err != nil {
		t.Fatalf("fresh investigation: %v", err)
	}
	want := []string{"s1", "status", "work"}
	if scenario == "zero" || scenario == "s1" {
		want = []string{"s1"}
	}
	if !reflect.DeepEqual(client.phases, want) {
		t.Fatalf("fresh phases=%v want=%v status=%s", client.phases, want, result.Status)
	}
	wantClaims := 2
	if scenario == "zero" {
		wantClaims = 1
	}
	if scenario == "s1" {
		wantClaims = 0
	}
	if scenario == "retry" {
		wantClaims = 18
	}
	if len(result.ClaimedFacts) != wantClaims {
		t.Fatalf("served claims=%d want=%d", len(result.ClaimedFacts), wantClaims)
	}
	if scenario == "retry" && !reflect.DeepEqual(sizes, []int{34, 17}) {
		t.Fatalf("synthesis attempts=%v", sizes)
	}
}

type infoTupleQueryClient struct {
	memberID    string
	phases      []string
	fail        string
	rowsByPhase map[string][][]any
	queries     []infoTupleQuery
	scanned     map[string]int
}

type infoTupleQuery struct {
	phase    string
	sql      string
	bindings []contextpacket.ClickHouseBinding
}

func (c *infoTupleQueryClient) Query(_ context.Context, sql string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	phase := "s1"
	rows := [][]any{{c.memberID, "repo-1", "work-1", infoRepository, uint8(1), uint64(1), uint64(1), uint64(0), uint64(0), uint64(0)}}
	if strings.Contains(sql, "w.status") {
		phase = "status"
		rows = [][]any{{"work-1", "open", "repo-1"}}
	}
	if strings.Contains(sql, "w.title") {
		phase = "work"
		rows = [][]any{{"work-1", "Implement the thing", "repo-1"}}
	}
	if configured, ok := c.rowsByPhase[phase]; ok {
		rows = configured
	}
	c.phases = append(c.phases, phase)
	c.queries = append(c.queries, infoTupleQuery{phase: phase, sql: sql, bindings: append([]contextpacket.ClickHouseBinding(nil), bindings...)})
	if c.fail == phase {
		return nil, errors.New("controlled content read failure")
	}
	if c.fail == "zero" {
		rows = nil
	}
	if phase == "s1" {
		var err error
		rows, err = infoTupleResolvedS1Rows(rows)
		if err != nil {
			return nil, err
		}
	}
	var rowErr error
	if c.fail == phase+"_partial" {
		rowErr = errors.New("controlled failure after scanned content rows")
	}
	if c.scanned == nil {
		c.scanned = map[string]int{}
	}
	return &infoTupleQueryRows{rows: rows, rowErr: rowErr, scanned: func() { c.scanned[phase]++ }}, nil
}

// These scenario rows describe ten member fields, not complete S1 wire rows.
// Encode only successful resolved-anchor fixtures; protocol-negative tests use
// the membership package's explicit rows. Clone before appending so later reuse
// reads see the same scenario payload, including slices with spare capacity.
func infoTupleResolvedS1Rows(members [][]any) ([][]any, error) {
	rows := make([][]any, 0, len(members)+1)
	for _, member := range members {
		if len(member) != 10 {
			return nil, fmt.Errorf("resolved S1 fixture member width %d != 10", len(member))
		}
		row := make([]any, 12)
		copy(row, member)
		row[10], row[11] = uint8(0), uint8(1)
		rows = append(rows, row)
	}
	return append(rows, []any{"", "", "", "", uint8(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint8(1), uint8(1)}), nil
}

type infoTupleQueryRows struct {
	rows    [][]any
	index   int
	rowErr  error
	scanned func()
}

func (r *infoTupleQueryRows) Next() bool { return r.index < len(r.rows) }
func (r *infoTupleQueryRows) Scan(dest ...any) error {
	row := r.rows[r.index]
	if len(row) != len(dest) {
		return fmt.Errorf("scan width %d != %d", len(row), len(dest))
	}
	for i, target := range dest {
		reflect.ValueOf(target).Elem().Set(reflect.ValueOf(row[i]))
	}
	r.index++
	if r.scanned != nil {
		r.scanned()
	}
	return nil
}
func (r *infoTupleQueryRows) Err() error { return r.rowErr }
func (*infoTupleQueryRows) Close() error { return nil }

type infoTupleModel struct {
	frame      contextfabric.QuestionFrame
	statusOnly bool
	observe    func(contextfabric.SynthesisInput)
}

func (m infoTupleModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := infoModelReceipt(contextfabric.ModelOperationInterpret)
	receipt.Outcome = "success"
	receipt.QuestionFrame = &m.frame
	receipt.QuestionFamily = contextfabric.QuestionFamilyScopedCohortStatus
	receipt.ScopeAnchorKind = contextfabric.SubjectProject
	receipt.ScopeAnchorTerm = "Project Alpha"
	receipt.RequestedSubjectKind = contextfabric.SubjectWorkItem
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Project Alpha"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}}}, receipt, nil
}
func (m infoTupleModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	if m.observe != nil {
		m.observe(input)
	}
	draft := infoSynthesisDraft()
	draft.Drivers = []contextfabric.DriverJudgment{}
	draft.DirectJudgment = "Available work item evidence."
	draft.CurrentState = "Available work item evidence."
	draft.DeterministicAnswer = "Available work item evidence."
	draft.ClaimedFacts = []contextfabric.ClaimedFact{}
	// Use the current producer-owned display subject after work/title reads.
	// The fact still supplies the claim kind, value and evidence.
	subjects := map[string]contextfabric.SubjectRef{}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			subjects[member.Subject.CanonicalID] = member.Subject
		}
	}
	refs := map[string]bool{}
	for index, fact := range input.Facts.Facts {
		if m.statusOnly && fact.Kind == contextfabric.FactWork {
			continue
		}
		field := "status"
		if fact.Kind == contextfabric.FactWork {
			field = "title"
		}
		if value := fact.Fields[field].String; value != nil {
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: fmt.Sprintf("claim_tuple_%d", index), Kind: fact.Kind, Subject: subjects[fact.Subject.CanonicalID], Field: field, Value: contextfabric.ScalarValue{String: value}})
			for _, ref := range fact.EvidenceRefIDs {
				if !refs[ref] {
					refs[ref] = true
					draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, ref)
				}
			}
		}
	}
	receipt := infoModelReceipt(contextfabric.ModelOperationSynthesize)
	receipt.Outcome = "success"
	return draft, receipt, nil
}

func infoModelReceipt(operation contextfabric.ModelOperation) contextfabric.ModelExecutionReceipt {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: "prompt-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		StartedAt: now, CompletedAt: now.Add(time.Second), Attempts: 1,
		InputDigest: contextfabric.DigestModelValue([]byte("route-test-input")),
	}
}

func infoSynthesisDraft() contextfabric.SynthesisDraft {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return contextfabric.SynthesisDraft{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "Ask Dev is not release-ready.",
		CurrentState: "Diverges.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{},
		ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, Warnings: []string{},
		Drivers: []contextfabric.DriverJudgment{{
			DriverID: "driver_12345678", Standing: contextfabric.DriverPrincipal, Category: "relationship",
			Title: "Release acceptance remains open", Summary: "Required acceptance has not completed.",
			AffectedSubjects: []contextfabric.SubjectRef{project}, Derivation: contextfabric.DerivationRuleInferred,
			EpistemicStatus: contextfabric.EpistemicInferred, Confidence: 0.9, Current: true,
			EvidenceRefIDs: []string{},
		}},
		DeterministicAnswer: "Ask Dev is not release-ready because release acceptance remains open.",
	}
}

// Match only collected production records. Numeric Want values use the JSON
// decoder's number representation; multiplicity remains exact.
func requireInfoLine(t *testing.T, log *certify.Log, message string, want map[string]any) {
	t.Helper()
	matches := 0
	for _, line := range log.LinesWithMsg(message) {
		matched := line["level"] == "INFO"
		for key, value := range want {
			if n, ok := value.(int); ok {
				value = float64(n)
			}
			if !reflect.DeepEqual(line[key], value) {
				matched = false
			}
		}
		if matched {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("collected %q matches=%d want exactly one for %v; actual=%v", message, matches, want, log.LinesWithMsg(message))
	}
}

// A retry must explain its cause before its second synthesis starts. Derive a
// lower bound on the first draft's charged items from the real provider output:
// every retained member and every emitted status claim occupies an item. This
// deliberately does not promote this fixture's exact total to a contract limit.
func requireInfoRetryTrigger(t *testing.T, log *certify.Log, collected []byte) {
	t.Helper()
	initialMinimum := float64(0)
	for _, line := range log.LinesWithMsg("context fabric fact read") {
		if line["kind"] == "status" {
			initialMinimum = line["subjects"].(float64) + line["facts"].(float64)
		}
	}
	if initialMinimum == 0 {
		t.Error("retry fixture did not measure its initial member/claim charge")
		return
	}
	attempts, triggers := 0, 0
	for _, raw := range bytes.Split(collected, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line certify.Line
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatal(err)
		}
		if line["msg"] == "context fabric projected rows count" {
			attempts++
		}
		if line["msg"] != "context fabric synthesis retry selected" || line["stage"] != "assembled_result" || line["overrun"] != "items" {
			continue
		}
		measured, measuredOK := line["measured_items"].(float64)
		ceiling, ceilingOK := line["max_items"].(float64)
		if line["level"] == "INFO" && line["request_id"] == infoRequestID && line["retry_attempted"] == false && line["retry_fit"] == false && line["retry_failed"] == false && measuredOK && ceilingOK && measured > ceiling && measured >= initialMinimum && attempts == 1 {
			triggers++
		}
	}
	if triggers != 1 {
		t.Errorf("collected retry decision has %d pre-second-synthesis measured item-overrun records; want one with measured_items >= producer member+claim charge %.0f and > max_items, plus selected identity and false attempted/fit/failed before the retry call", triggers, initialMinimum)
	}
}
