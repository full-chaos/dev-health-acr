package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// subjectEchoProvider answers one fact per queried subject and takes the
// fact's subject from the query, the way the pull request and review
// producers do.
type subjectEchoProvider struct{ capability FactCapability }

func (p subjectEchoProvider) Capability() FactCapability { return p.capability }

func (p subjectEchoProvider) ReadFacts(_ context.Context, _ storage.Principal, query FactQuery) (FactProviderResult, error) {
	facts := make([]CanonicalFact, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		facts = append(facts, CanonicalFact{
			Kind: p.capability.Kind, Subject: subject,
			Fields: map[string]FactValue{"state": StringFactValue("open")},
		})
	}
	return FactProviderResult{State: SourceAvailable, Facts: facts}, nil
}

func registryLogLines(t *testing.T, logs *bytes.Buffer, message string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		entry := map[string]any{}
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != message {
			continue
		}
		lines = append(lines, entry)
	}
	return lines
}

// The production shape: a team question whose pull request subjects come from
// a truncated scope expansion, and the expansion mints those subjects with no
// label. Every fact the producer answers for them is refused by the merge.
func TestARefusedProviderResultCostsItsKindNotTheRead(t *testing.T) {
	t.Parallel()
	labelLess := []SubjectRef{
		{Kind: SubjectPullRequest, CanonicalID: "pull_request:repo-a:1"},
		{Kind: SubjectPullRequest, CanonicalID: "pull_request:repo-a:2"},
	}
	logs := &bytes.Buffer{}
	registry, err := NewFactCapabilityRegistry([]FactProvider{
		subjectEchoProvider{capability: planCapability(FactHealth, "health", SubjectTeam)},
		subjectEchoProvider{capability: planCapability(FactPullRequests, "pull_requests", SubjectPullRequest)},
		subjectEchoProvider{capability: planCapability(FactWorkload, "workload", SubjectTeam)},
	}, FactRegistryOptions{
		ScopeExpander: &recordingScopeExpander{targets: labelLess, counts: FactScopeExpansionCounts{CandidateCount: 3, Truncated: true}},
		Logger:        slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}

	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeTeam}, []FactRequirement{{Kind: FactHealth}, {Kind: FactPullRequests}, {Kind: FactWorkload}}, TemporalCurrent))

	if err != nil {
		t.Fatalf("ReadFacts error = %q, want nil: a refused provider result must degrade its own kind, never end the read", err)
	}
	if len(bundle.Scope.Derivations) != len(labelLess) {
		t.Fatalf("derivations = %d, want %d: the fixture did not reach the provider through the expansion", len(bundle.Scope.Derivations), len(labelLess))
	}
	sources := coverageBySource(bundle)
	pullRequests, ok := sources["canonical_fact:pull_requests"]
	if !ok {
		t.Fatalf("coverage has no pull_requests source: %+v", bundle.Coverage.Sources)
	}
	if pullRequests.State != SourceUnavailable {
		t.Fatalf("pull_requests state = %q, want unavailable", pullRequests.State)
	}
	if !strings.Contains(pullRequests.Reason, "canonical fact provider returned a result that was rejected") {
		t.Fatalf("pull_requests reason = %q, want the fixed rejection sentence", pullRequests.Reason)
	}
	if strings.Contains(pullRequests.Reason, "pull_request:repo-a") {
		t.Fatalf("pull_requests reason = %q carries a canonical id", pullRequests.Reason)
	}
	if !bundle.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: a kind the answer asked for is missing")
	}
	if err := bundle.Coverage.Validate(); err != nil {
		t.Fatalf("the degraded coverage is not contract-valid: %v", err)
	}
	kinds := map[FactKind]int{}
	for _, fact := range bundle.Facts {
		kinds[fact.Kind]++
	}
	if kinds[FactPullRequests] != 0 {
		t.Fatalf("bundle carries %d pull_requests facts from a refused result, want 0", kinds[FactPullRequests])
	}
	if kinds[FactHealth] != 1 || kinds[FactWorkload] != 1 {
		t.Fatalf("facts by kind = %v, want the kind planned before the refused one and the kind planned after it both read", kinds)
	}
	for _, kind := range []FactKind{FactHealth, FactWorkload} {
		if state := sources["canonical_fact:"+string(kind)].State; state != SourceAvailable {
			t.Fatalf("%s state = %q, want available", kind, state)
		}
	}
	if branch := bundle.Outcomes[FactPullRequests].Branch; branch != "rejected" {
		t.Fatalf("ledger branch = %q, want rejected", branch)
	}
	if state := bundle.Outcomes[FactPullRequests].State; state != SourceUnavailable {
		t.Fatalf("ledger state = %q, want unavailable", state)
	}

	var ledger map[string]any
	for _, line := range registryLogLines(t, logs, "context fabric fact read") {
		if line["kind"] == "pull_requests" {
			ledger = line
		}
	}
	if ledger == nil {
		t.Fatalf("no fact read line for pull_requests in:\n%s", logs.String())
	}
	if ledger["outcome"] != "rejected" || ledger["state"] != "unavailable" || ledger["facts"] != float64(len(labelLess)) {
		t.Fatalf("fact read line = %v, want outcome=rejected state=unavailable facts=%d", ledger, len(labelLess))
	}
	rejections := registryLogLines(t, logs, "context fabric fact result rejected")
	if len(rejections) != 1 {
		t.Fatalf("rejection lines = %d, want exactly 1 in:\n%s", len(rejections), logs.String())
	}
	rejection := rejections[0]
	if rejection["level"] != "WARN" || rejection["kind"] != "pull_requests" || rejection["rejection_cause"] != "fact_subject_invalid" {
		t.Fatalf("rejection line = %v, want WARN kind=pull_requests rejection_cause=fact_subject_invalid", rejection)
	}
	if strings.Contains(logs.String(), "pull_request:repo-a") {
		t.Fatalf("a registry log line carries a canonical id:\n%s", logs.String())
	}
}

// A prior read whose health result the registry refuses learned nothing
// about the prior point. The comparison says prior_read_failed, as it did
// when the refusal ended the read with an error.
func TestARefusedPriorHealthResultIsAFailedPriorReadNotAnUnknownPrior(t *testing.T) {
	t.Parallel()
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1", Label: "T1"}
	capability := planCapability(FactHealth, "health", SubjectTeam)
	capability.RequiresEvidence = true
	registry, err := NewFactCapabilityRegistry([]FactProvider{subjectEchoProvider{capability: capability}}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: registry, telemetry: telemetry}
	currentFacts := []CanonicalFact{periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("low")})}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now(), true)

	fields := currentFacts[0].Fields
	if got := factValueString(fields["period_delta_transition"]); got != string(PeriodDeltaTransitionPriorReadFailed) {
		t.Fatalf("period_delta_transition = %q, want %q", got, PeriodDeltaTransitionPriorReadFailed)
	}
	if got := factValueString(fields["period_delta_unavailable_reason"]); got != string(PeriodDeltaFailureReadFailed) {
		t.Fatalf("period_delta_unavailable_reason = %q, want %q", got, PeriodDeltaFailureReadFailed)
	}
	if len(telemetry.periodDeltaCompositions) != 1 {
		t.Fatalf("recorded %d events, want 1", len(telemetry.periodDeltaCompositions))
	}
	event := telemetry.periodDeltaCompositions[0]
	if event.PriorReadIssued || event.FailureReason != PeriodDeltaFailureReadFailed || event.TransitionCounts[PeriodDeltaTransitionPriorReadFailed] != 1 {
		t.Fatalf("event = %+v, want PriorReadIssued=false, reason read_failed, one prior_read_failed", event)
	}
}

// groupReadRefusingReader answers the group-rooted read through a real
// registry, so a result that registry refuses reaches the engine exactly as
// production hands it over.
type groupReadRefusingReader struct {
	inner    *groupReadRecorder
	registry *FactCapabilityRegistry
}

func (r *groupReadRefusingReader) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	for _, subject := range request.Subjects {
		if subject.Kind == SubjectTeam {
			r.inner.requests = append(r.inner.requests, request)
			return r.registry.ReadFacts(ctx, principal, request)
		}
	}
	return r.inner.ReadFacts(ctx, principal, request)
}

// The group read is a second fact read over the groups. A kind whose result
// the registry refuses there degrades that kind on the served document; the
// group read itself is issued, is not refused, and the turn is answered.
func TestARefusedKindInTheGroupReadDegradesThatKindNotTheGroupRead(t *testing.T) {
	health := planCapability(FactHealth, "health", SubjectTeam)
	health.RequiresEvidence = true
	registry, err := NewFactCapabilityRegistry([]FactProvider{
		subjectEchoProvider{capability: health},
		subjectEchoProvider{capability: planCapability(FactWorkload, "workload", SubjectTeam)},
	}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := groupReadServing()
	logs := captureEngineLogger(t)
	members := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, &groupReadRefusingReader{inner: recorder, registry: registry}, members, nil, SubjectProject, nil, nil)

	result, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request)

	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if sent := len(recorder.groupRootedRequests(SubjectTeam)); sent != 1 {
		t.Fatalf("group-rooted requests = %d, want 1: the fixture did not reach the group read", sent)
	}
	line := cohortGroupReadLine(t, logs)
	if line["group_read_issued"] != true || line["group_read_refused"] != false || line["group_read_refusal"] != string(GroupReadRefusalNone) {
		t.Fatalf("group read line issued=%v refused=%v refusal=%v, want an issued read that was not refused", line["group_read_issued"], line["group_read_refused"], line["group_read_refusal"])
	}
	states := map[string]SourceObservation{}
	for _, source := range result.Coverage.Sources {
		states[source.Source] = source
	}
	refused := states["canonical_fact:health"]
	if refused.State != SourceUnavailable || !strings.Contains(refused.Reason, "canonical fact provider returned a result that was rejected") {
		t.Fatalf("served health coverage = %+v, want unavailable with the rejection sentence", refused)
	}
	if !result.Coverage.Partial {
		t.Fatal("served coverage.partial = false, want true")
	}
	if states["canonical_fact:workload"].State != SourceAvailable || line["group_facts_merged"] != float64(2) {
		t.Fatalf("workload coverage = %+v, group_facts_merged = %v, want the group's other kind read and its 2 facts composed", states["canonical_fact:workload"], line["group_facts_merged"])
	}
}
