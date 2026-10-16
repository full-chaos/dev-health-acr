package contextfabric_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	factReadLegacyKeys = []string{
		"time", "level", "msg", "org_id", "kind", "outcome", "state", "subjects", "subject_kinds", "facts", "truncated", "request_id",
	}
	factEvaluationCoverageLegacyKeys = []string{
		"time", "level", "msg", "org_id", "kind", "evaluated_covered", "evaluated_fired", "evaluated_stale", "evaluated_before_range",
		"never_evaluated", "measured_zero_credited", "rules_evaluated", "latest_window_end", "freshness_window_days", "withheld_capped_read", "request_id",
	}
	factTableDeclarationLegacyKeys = []string{"time", "level", "msg", "org_id", "kind", "shape", "key_arity", "measure_count", "request_id"}
	factResultRejectedLegacyKeys   = []string{
		"time", "level", "msg", "org_id", "kind", "rejection_cause", "subjects", "subject_kinds", "facts", "request_id",
	}
	factReadFailedLegacyKeys = []string{"time", "level", "msg", "kind", "error_type", "provider_reason", "request_id"}
)

type registryProviderStub struct {
	capability contextfabric.FactCapability
	result     contextfabric.FactProviderResult
	err        error
	block      bool
}

func (p *registryProviderStub) Capability() contextfabric.FactCapability { return p.capability }

func (p *registryProviderStub) ReadFacts(ctx context.Context, _ storage.Principal, _ contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if p.block {
		<-ctx.Done()
		return contextfabric.FactProviderResult{}, ctx.Err()
	}
	return p.result, p.err
}

var registryProject = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

func registryCapability(kind contextfabric.FactKind, subjectKind contextfabric.SubjectKind) contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: kind, Name: "ops-" + string(kind), Version: "v1", SupportedSubjectKinds: []contextfabric.SubjectKind{subjectKind},
		Dimension: contextfabric.HealthDimensionExecutionCompletion, SubjectRoles: []contextfabric.FactRole{contextfabric.FactRoleSubject},
	}
}

func registryRequest(kinds ...contextfabric.FactKind) contextfabric.CanonicalFactRequest {
	requirements := make([]contextfabric.FactRequirement, 0, len(kinds))
	for _, kind := range kinds {
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind, Parameters: map[string]string{}})
	}
	return contextfabric.CanonicalFactRequest{
		Question: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: requirements,
		},
		Subjects: []contextfabric.SubjectRef{registryProject}, Requirements: requirements,
	}
}

// readThroughRegistry runs one fact read through a real registry whose logger
// is a JSON handler and returns every line it wrote.
func readThroughRegistry(t *testing.T, ctx context.Context, providers []contextfabric.FactProvider, request contextfabric.CanonicalFactRequest) []byte {
	t.Helper()
	var buf bytes.Buffer
	registry, err := contextfabric.NewFactCapabilityRegistry(providers, contextfabric.FactRegistryOptions{
		Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	ctx = observability.WithRequestID(ctx, factScopeCertifyRequestID)
	_, _ = registry.ReadFacts(ctx, storage.Principal{OrgID: "org_1"}, request)
	return buf.Bytes()
}

func linesOf(t *testing.T, raw []byte, msg string) [][]byte {
	t.Helper()
	var out [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if bytes.Contains(line, []byte(`"msg":"`+msg+`"`)) {
			out = append(out, line)
		}
	}
	return out
}

func factWithTable(kind contextfabric.FactKind, table contextfabric.FactTable) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{
		Kind: kind, Subject: registryProject, SourceState: contextfabric.SourceAvailable,
		Fields: map[string]contextfabric.FactValue{"breakdown": contextfabric.TableFactValue(table)},
	}
}

func aBreakdownTable(measure contextfabric.FactValue) contextfabric.FactTable {
	return contextfabric.FactTable{
		Shape: contextfabric.FactTableBreakdown, Key: []string{"scope"}, Measures: []string{"items"},
		Rows: []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{"scope": contextfabric.StringFactValue("a"), "items": measure}}},
	}
}

// certifyRegistryLine is certifyRequestLine plus the declared-key check: the
// certifier does not refuse a key the declaration does not list, so the keys of
// every registry line are compared with the event's declared keys here.
func certifyRegistryLine(t *testing.T, raw []byte, event eventspec.Event, want map[string]any) certify.Line {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("line %q: %v", raw, err)
	}
	declared := map[string]bool{"time": true, "level": true, "msg": true}
	for _, key := range eventspec.FieldKeys(event) {
		declared[key] = true
	}
	for key := range decoded {
		if !declared[key] {
			t.Fatalf("%s: the line carries key %q, which the declaration does not list: %s", event.ID, key, raw)
		}
	}
	for _, field := range event.Fields {
		if _, ok := decoded[field.Key]; !ok && field.Presence == eventspec.PresenceRequired {
			t.Fatalf("%s: the line lacks declared required key %q: %s", event.ID, field.Key, raw)
		}
	}
	return certifyRequestLine(t, raw, event, want)
}

// TestFactRegistryLinesCertifyEveryOutcomeAndCauseTheRegistryProduces drives a
// real registry through every outcome, every rejection cause and the failure
// classes a provider can set, certifies each line the registry wrote, and
// requires the vocabularies the specification reads to be exactly what the
// drive produced.
func TestFactRegistryLinesCertifyEveryOutcomeAndCauseTheRegistryProduces(t *testing.T) {
	t.Parallel()
	status := contextfabric.FactStatus
	seenOutcomes := map[string]bool{}
	seenCauses := map[string]bool{}
	seenClasses := map[string]bool{}

	certifyReadLine := func(raw []byte, kind contextfabric.FactKind, want map[string]any) {
		t.Helper()
		lines := linesOf(t, raw, "context fabric fact read")
		if len(lines) != 1 {
			t.Fatalf("want one fact read line, got %d: %s", len(lines), raw)
		}
		if want == nil {
			want = map[string]any{}
		}
		want["kind"] = string(kind)
		want["org_id"] = "org_1"
		line := certifyRegistryLine(t, lines[0], eventspec.FactRead, want)
		seenOutcomes[line["outcome"].(string)] = true
	}

	// Completed with a table, evaluation coverage and one fact.
	completed := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		result: contextfabric.FactProviderResult{
			State: contextfabric.SourceAvailable, Version: "v1",
			Facts:             []contextfabric.CanonicalFact{factWithTable(status, aBreakdownTable(contextfabric.IntegerFactValue(3)))},
			EvaluatedSubjects: []contextfabric.SubjectRef{registryProject},
			Evaluation: &contextfabric.FactEvaluationCoverage{
				Covered: 4, Fired: 3, Stale: 2, BeforeRange: 1, NeverEvaluated: 5, RulesEvaluated: 6, LatestWindowEnd: "2026-10-15", FreshnessWindowDays: 7, Withheld: true,
			},
		},
	}
	raw := readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{completed}, registryRequest(status))
	pinKeys(t, bytes.NewBuffer(linesOf(t, raw, "context fabric fact read")[0]), factReadLegacyKeys, 1)
	pinKeys(t, bytes.NewBuffer(linesOf(t, raw, "context fabric evaluation coverage")[0]), factEvaluationCoverageLegacyKeys, 1)
	pinKeys(t, bytes.NewBuffer(linesOf(t, raw, "context fabric fact table declaration")[0]), factTableDeclarationLegacyKeys, 1)
	certifyReadLine(raw, status, map[string]any{
		"outcome": "completed", "state": "available", "subjects": 1, "subject_kinds": "project", "facts": 1, "truncated": false,
	})
	certifyRegistryLine(t, linesOf(t, raw, "context fabric evaluation coverage")[0], eventspec.FactEvaluationCoverage, map[string]any{
		"org_id": "org_1", "kind": "status", "evaluated_covered": 4, "evaluated_fired": 3, "evaluated_stale": 2, "evaluated_before_range": 1,
		"never_evaluated": 5, "measured_zero_credited": 1, "rules_evaluated": 6, "latest_window_end": "2026-10-15", "freshness_window_days": 7,
		"withheld_capped_read": true,
	})
	certifyRegistryLine(t, linesOf(t, raw, "context fabric fact table declaration")[0], eventspec.FactTableDeclaration, map[string]any{
		"org_id": "org_1", "kind": "status", "shape": "breakdown", "key_arity": 1, "measure_count": 1,
	})

	// Corpus safety: no subject identity reaches any of the lines. An
	// organization subject's canonical id is "organization:<org id>", so the
	// same check keeps the organization id out of every subject-derived value.
	orgSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org_1", Label: "org_1"}
	orgRequest := registryRequest(status)
	orgRequest.Subjects = []contextfabric.SubjectRef{orgSubject}
	orgProvider := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectOrganization),
		result: contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: []contextfabric.CanonicalFact{
			{Kind: status, Subject: orgSubject, SourceState: contextfabric.SourceAvailable, Fields: map[string]contextfabric.FactValue{"status": contextfabric.StringFactValue("ok")}},
		}},
	}
	orgRaw := readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{orgProvider}, orgRequest)
	certifyReadLine(orgRaw, status, map[string]any{"subject_kinds": "organization"})
	for _, leaked := range []string{"organization:org_1", "project_ask_dev", "Ask Dev"} {
		if strings.Contains(string(orgRaw), leaked) || strings.Contains(string(raw), leaked) {
			t.Fatalf("a registry line carries the subject identity %q", leaked)
		}
	}

	// No coverage reported: no coverage line. No table: no table line.
	noData := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		result:     contextfabric.FactProviderResult{State: contextfabric.SourceNoData, Version: "v1", Reason: "held no rows"},
	}
	raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{noData}, registryRequest(status))
	certifyReadLine(raw, status, map[string]any{"state": "no_data", "facts": 0})
	if n := len(linesOf(t, raw, "context fabric evaluation coverage")) + len(linesOf(t, raw, "context fabric fact table declaration")); n != 0 {
		t.Fatalf("a read with no coverage and no table wrote %d coverage/table lines", n)
	}

	// Truncated by an omission count.
	truncated := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		result: contextfabric.FactProviderResult{
			State: contextfabric.SourceAvailable, Version: "v1", OmittedCount: 2,
			Facts: []contextfabric.CanonicalFact{factWithTable(status, aBreakdownTable(contextfabric.IntegerFactValue(1)))},
		},
	}
	certifyReadLine(readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{truncated}, registryRequest(status)), status, map[string]any{"state": "truncated", "truncated": true})

	// Unconfigured: a kind no provider is registered for.
	certifyReadLine(readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{completed}, registryRequest(contextfabric.FactHealth)), contextfabric.FactHealth,
		map[string]any{"outcome": "unconfigured", "state": "unconfigured"})

	// A scope gap: the only supported kind is unreachable from a project.
	gap := &registryProviderStub{capability: registryCapability(contextfabric.FactHealth, contextfabric.SubjectRepository), result: contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1"}}
	certifyReadLine(readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{gap}, registryRequest(contextfabric.FactHealth)), contextfabric.FactHealth, map[string]any{"outcome": "scope_gap"})

	// Pruned: no known path from a work item to repository-only metrics.
	pruneRequest := registryRequest(contextfabric.FactMetrics)
	pruneRequest.Subjects = []contextfabric.SubjectRef{{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:ABC-1", Label: "ABC-1"}}
	repoOnly := &registryProviderStub{capability: registryCapability(contextfabric.FactMetrics, contextfabric.SubjectRepository), result: contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1"}}
	certifyReadLine(readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{repoOnly}, pruneRequest), contextfabric.FactMetrics,
		map[string]any{"outcome": "pruned", "state": "pruned", "subjects": 0, "subject_kinds": ""})

	// Cancelled: the caller's context ends while the provider runs.
	cancelled := &registryProviderStub{capability: registryCapability(status, contextfabric.SubjectProject), block: true}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	raw = readThroughRegistry(t, ctx, []contextfabric.FactProvider{cancelled}, registryRequest(status))
	certifyReadLine(raw, status, map[string]any{"outcome": "cancelled", "state": ""})

	// Failed: typed failures of every class a provider sets, and an untyped error.
	for _, class := range []contextfabric.FactReadFailureClass{
		contextfabric.FactReadFailureBudgetExceeded, contextfabric.FactReadFailureContended, contextfabric.FactReadFailureReadFailed,
		contextfabric.FactReadFailureOrganizationRequired, "",
	} {
		failing := &registryProviderStub{
			capability: registryCapability(status, contextfabric.SubjectProject),
			err: &contextfabric.FactReadFailure{
				State: contextfabric.SourceUnavailable, Class: class,
				Reason: "devhealthfacts: query secret tenant widgets failed", Cause: errors.New("dial tcp 10.0.0.9:9000: tenant widgets password=hunter2"),
			},
		}
		raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{failing}, registryRequest(status))
		certifyReadLine(raw, status, map[string]any{"outcome": "failed", "state": "unavailable"})
		failed := linesOf(t, raw, "context fabric fact read failed")
		if len(failed) != 1 {
			t.Fatalf("want one read failed line, got %d", len(failed))
		}
		pinKeys(t, bytes.NewBuffer(failed[0]), factReadFailedLegacyKeys, 1)
		wantClass := string(class)
		if class == "" {
			wantClass = string(contextfabric.FactReadFailureUnclassified)
		}
		line := certifyRegistryLine(t, failed[0], eventspec.FactReadFailed, map[string]any{"kind": "status", "provider_reason": wantClass, "error_type": "*contextfabric.FactReadFailure"})
		seenClasses[line["provider_reason"].(string)] = true
		if _, leaked := line["org_id"]; leaked {
			t.Fatalf("the read failed line must carry no org_id: %s", failed[0])
		}
		for _, secret := range []string{"hunter2", "tenant", "widgets", "10.0.0.9"} {
			if strings.Contains(string(failed[0]), secret) {
				t.Fatalf("the read failed line carries provider text %q: %s", secret, failed[0])
			}
		}
	}
	untyped := &registryProviderStub{capability: registryCapability(status, contextfabric.SubjectProject), err: errors.New("boom tenant text")}
	raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{untyped}, registryRequest(status))
	line := certifyRegistryLine(t, linesOf(t, raw, "context fabric fact read failed")[0], eventspec.FactReadFailed, map[string]any{"kind": "status", "provider_reason": "none"})
	seenClasses[line["provider_reason"].(string)] = true

	// A timeout.
	slow := &registryProviderStub{capability: registryCapability(status, contextfabric.SubjectProject), block: true}
	slow.capability.Timeout = 20 * time.Millisecond
	raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{slow}, registryRequest(status))
	line = certifyRegistryLine(t, linesOf(t, raw, "context fabric fact read failed")[0], eventspec.FactReadFailed, map[string]any{"kind": "status", "provider_reason": "timed_out"})
	seenClasses[line["provider_reason"].(string)] = true

	// Every rejection cause.
	otherSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_other", Label: "Other"}
	unlabeled := registryProject
	unlabeled.Label = ""
	goodFact := func() contextfabric.CanonicalFact {
		return contextfabric.CanonicalFact{Kind: status, Subject: registryProject, SourceState: contextfabric.SourceAvailable, Fields: map[string]contextfabric.FactValue{"status": contextfabric.StringFactValue("ok")}}
	}
	mutated := func(edit func(*contextfabric.CanonicalFact)) []contextfabric.CanonicalFact {
		fact := goodFact()
		edit(&fact)
		return []contextfabric.CanonicalFact{fact}
	}
	zero := time.Time{}
	rejections := []struct {
		cause  string
		result contextfabric.FactProviderResult
		strict bool
	}{
		{"invalid_source_state", contextfabric.FactProviderResult{State: "bogus", Version: "v1"}, false},
		{"invalid_observed_timestamp", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", ObservedAt: &zero}, false},
		{"invalid_version", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: " v1 "}, false},
		{"state_cannot_carry_facts", contextfabric.FactProviderResult{State: contextfabric.SourceNoData, Version: "v1", Facts: []contextfabric.CanonicalFact{goodFact()}}, false},
		{"evaluated_subject_outside_set", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", EvaluatedSubjects: []contextfabric.SubjectRef{otherSubject}}, false},
		{"fact_kind_mismatch", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: mutated(func(f *contextfabric.CanonicalFact) { f.Kind = contextfabric.FactHealth })}, false},
		{"fact_subject_outside_set", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: mutated(func(f *contextfabric.CanonicalFact) { f.Subject = otherSubject })}, false},
		{"fact_subject_invalid", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: mutated(func(f *contextfabric.CanonicalFact) { f.Subject = unlabeled })}, false},
		{"fact_source_state_invalid", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: mutated(func(f *contextfabric.CanonicalFact) { f.SourceState = "bogus" })}, false},
		{"fact_source_state_cannot_carry_facts", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: mutated(func(f *contextfabric.CanonicalFact) { f.SourceState = contextfabric.SourceNoData })}, false},
		{"fact_invalid", contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: mutated(func(f *contextfabric.CanonicalFact) { f.EvidenceRefIDs = nil })}, true},
	}
	for _, tc := range rejections {
		capability := registryCapability(status, contextfabric.SubjectProject)
		capability.RequiresEvidence = tc.strict
		raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{&registryProviderStub{capability: capability, result: tc.result}}, registryRequest(status))
		rejected := linesOf(t, raw, "context fabric fact result rejected")
		if len(rejected) != 1 {
			t.Fatalf("cause %s: want one rejection line, got %d: %s", tc.cause, len(rejected), raw)
		}
		if tc.cause == "invalid_source_state" {
			pinKeys(t, bytes.NewBuffer(rejected[0]), factResultRejectedLegacyKeys, 1)
		}
		got := certifyRegistryLine(t, rejected[0], eventspec.FactResultRejected, map[string]any{"org_id": "org_1", "kind": "status", "rejection_cause": tc.cause, "subjects": 1, "subject_kinds": "project"})
		seenCauses[got["rejection_cause"].(string)] = true
		certifyReadLine(raw, status, map[string]any{"state": "unavailable"})
		failedLines := linesOf(t, raw, "context fabric fact read failed")
		if len(failedLines) != 1 {
			t.Fatalf("cause %s: want one read failed line, got %d", tc.cause, len(failedLines))
		}
		classLine := certifyRegistryLine(t, failedLines[0], eventspec.FactReadFailed, map[string]any{"kind": "status", "provider_reason": "result_rejected"})
		seenClasses[classLine["provider_reason"].(string)] = true
	}
	// A non-numeric measure is the narrowed rejection outcome.
	bad := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		result: contextfabric.FactProviderResult{
			State: contextfabric.SourceAvailable, Version: "v1",
			Facts: []contextfabric.CanonicalFact{factWithTable(status, aBreakdownTable(contextfabric.StringFactValue("many")))},
		},
	}
	raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{bad}, registryRequest(status))
	certifyReadLine(raw, status, map[string]any{"outcome": "rejected_non_numeric_measure"})

	assertSameSet(t, "fact read outcomes", seenOutcomes, contextfabric.FactReadOutcomeVocabulary())
	assertSameSet(t, "rejection causes", seenCauses, contextfabric.FactResultRejectionCauseVocabulary())
	assertSameSet(t, "failure classes", seenClasses, contextfabric.FactReadFailureClassVocabulary())
}

// assertSameSet requires the drive to have produced every member of the
// producer's vocabulary (less the named members driven elsewhere) and nothing
// outside it.
func assertSameSet(t *testing.T, what string, seen map[string]bool, vocabulary []string, drivenElsewhere ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, member := range vocabulary {
		want[member] = true
	}
	for _, member := range drivenElsewhere {
		delete(want, member)
	}
	var missing, extra []string
	for member := range want {
		if !seen[member] {
			missing = append(missing, member)
		}
	}
	for member := range seen {
		if !want[member] {
			extra = append(extra, member)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: the drive did not produce %v, and produced %v outside the vocabulary", what, missing, extra)
	}
}

// TestFactRegistryLinesRefuseOpenText: the specification refuses a free
// provider string where a closed class is declared, a subject kind list that is
// unsorted or holds an unknown member, and an org_id on the read failed line.
func TestFactRegistryLinesRefuseOpenText(t *testing.T) {
	t.Parallel()
	status := contextfabric.FactStatus
	failing := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		err:        &contextfabric.FactReadFailure{State: contextfabric.SourceUnavailable, Class: contextfabric.FactReadFailureReadFailed, Reason: "x"},
	}
	raw := readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{failing}, registryRequest(status))
	failed := linesOf(t, raw, "context fabric fact read failed")[0]
	certifyRegistryLine(t, failed, eventspec.FactReadFailed, map[string]any{"kind": "status"})
	withText := bytes.Replace(failed, []byte(`"provider_reason":"read_failed"`), []byte(`"provider_reason":"query secret tenant widgets failed"`), 1)
	if bytes.Equal(withText, failed) {
		t.Fatalf("fixture did not carry the class: %s", failed)
	}
	certifyRefused(t, withText, eventspec.FactReadFailed, "free provider text where a closed class is declared", map[string]any{"kind": "status"})

	read := linesOf(t, raw, "context fabric fact read")[0]
	certifyRegistryLine(t, read, eventspec.FactRead, map[string]any{"kind": "status"})
	for _, subjectKinds := range []string{"repository,project", "project,project", "project,future_kind"} {
		mutated := bytes.Replace(read, []byte(`"subject_kinds":"project"`), []byte(`"subject_kinds":"`+subjectKinds+`"`), 1)
		if bytes.Equal(mutated, read) {
			t.Fatalf("fixture did not carry subject_kinds: %s", read)
		}
		certifyRefused(t, mutated, eventspec.FactRead, "subject_kinds "+subjectKinds, map[string]any{"kind": "status"})
	}
	sorted := bytes.Replace(read, []byte(`"subject_kinds":"project"`), []byte(`"subject_kinds":"project,repository"`), 1)
	certifyRegistryLine(t, sorted, eventspec.FactRead, map[string]any{"kind": "status", "subject_kinds": "project,repository"})
	var decoded map[string]any
	if err := json.Unmarshal(failed, &decoded); err != nil {
		t.Fatalf("read failed line: %v", err)
	}
	if _, has := decoded["org_id"]; has {
		t.Fatalf("the read failed line must carry no org_id: %s", failed)
	}
}

// TestFactTableDeclarationLinesAreCappedPerRead drives a provider that returns
// more table-carrying facts than the bundle cap keeps: the registry writes at
// most FactTableDeclarationsPerReadCap lines for the read, and the declared
// per-scope bound covers the three reads a request can make.
func TestFactTableDeclarationLinesAreCappedPerRead(t *testing.T) {
	t.Parallel()
	status := contextfabric.FactStatus
	facts := make([]contextfabric.CanonicalFact, 0, contextfabric.FactTableDeclarationsPerReadCap+5)
	for i := 0; i < contextfabric.FactTableDeclarationsPerReadCap+5; i++ {
		facts = append(facts, factWithTable(status, aBreakdownTable(contextfabric.IntegerFactValue(int64(i)))))
	}
	provider := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		result:     contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: facts},
	}
	raw := readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{provider}, registryRequest(status))
	lines := linesOf(t, raw, "context fabric fact table declaration")
	if len(lines) != contextfabric.FactTableDeclarationsPerReadCap {
		t.Fatalf("a read of %d table facts wrote %d table declaration lines, want exactly the cap %d", len(facts), len(lines), contextfabric.FactTableDeclarationsPerReadCap)
	}
	if eventspec.FactTableDeclaration.MaxLinesPerScope < len(lines)*contextfabric.EngineFactGateReadsPerRequest {
		t.Fatalf("the declared scope bound %d is below the cap times the reads per request", eventspec.FactTableDeclaration.MaxLinesPerScope)
	}
	certifyRegistryLine(t, lines[0], eventspec.FactTableDeclaration, map[string]any{"org_id": "org_1", "kind": "status", "shape": "breakdown", "key_arity": 1, "measure_count": 1})
}

// TestRegistryLinesNeverCarryProviderControlledText: a failure class outside
// the vocabulary is logged as unclassified, and a table whose shape is outside
// the vocabulary writes no declaration line.
func TestRegistryLinesNeverCarryProviderControlledText(t *testing.T) {
	t.Parallel()
	status := contextfabric.FactStatus
	failing := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		err:        &contextfabric.FactReadFailure{State: contextfabric.SourceUnavailable, Class: "tenant widgets password=hunter2", Reason: "x"},
	}
	raw := readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{failing}, registryRequest(status))
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "widgets") {
		t.Fatalf("a provider-set class reached a log line: %s", raw)
	}
	certifyRegistryLine(t, linesOf(t, raw, "context fabric fact read failed")[0], eventspec.FactReadFailed, map[string]any{"kind": "status", "provider_reason": "unclassified"})

	table := aBreakdownTable(contextfabric.IntegerFactValue(1))
	table.Shape = "tenant widgets shape"
	shaped := &registryProviderStub{
		capability: registryCapability(status, contextfabric.SubjectProject),
		result:     contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Version: "v1", Facts: []contextfabric.CanonicalFact{factWithTable(status, table)}},
	}
	raw = readThroughRegistry(t, context.Background(), []contextfabric.FactProvider{shaped}, registryRequest(status))
	if strings.Contains(string(raw), "widgets") {
		t.Fatalf("a provider-set table shape reached a log line: %s", raw)
	}
	if n := len(linesOf(t, raw, "context fabric fact table declaration")); n != 0 {
		t.Fatalf("a table with an unknown shape wrote %d declaration lines", n)
	}
}
