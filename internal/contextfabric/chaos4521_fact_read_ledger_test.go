package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4521, the diagnosis-in-artifacts rule (acr/AGENTS.md CANONICAL
// ARCHITECTURE). Run J wall A cost a lane because a completed run carried
// NO record of what each planned capability actually did: six of them
// reported `available` and the answer claimed nothing, and the finished
// artifacts could not say whether those six returned rows the model ignored
// or returned nothing at all.
//
// This pins the ledger that answers it: one record per PLANNED capability,
// carrying the branch that minted the coverage entry (outcome), the state
// that entry carries, the subject KINDS asked about, and the fact count.
//
// The logger is reached through slog.Default() deliberately, not through
// FactRegistryOptions.Logger: that keeps this test compiling against the
// parent commit, where it fails for the reason that matters -- no record is
// emitted at all -- rather than failing to build.
func TestChaos4521_EveryPlannedCapabilityEmitsAFactReadRecord(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	// FactStatus: a provider that runs and returns NOTHING -- the live
	// project-status shape.
	empty := &factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v2", SupportedSubjectKinds: []SubjectKind{SubjectProject}},
		result:     FactProviderResult{State: SourceNoData, Reason: "held no rows", Version: "status-v2"},
	}
	// FactHealth: a capability whose only supported subject kind is
	// repository, which a project subject can only reach through a
	// CHAOS-4099 scope expansion. With no expander wired the expansion
	// resolves policy_unavailable, so no provider is ever called -- the
	// exact shape Run J observed for status/work/blockers/actual_completion
	// on the live project question.
	pruned := &factProviderStub{
		capability: FactCapability{Kind: FactHealth, Name: "ops-health", Version: "health-v1", SupportedSubjectKinds: []SubjectKind{SubjectRepository}},
		result:     FactProviderResult{State: SourceAvailable, Version: "health-v1"},
	}

	var sink bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	registry, err := NewFactCapabilityRegistry([]FactProvider{empty, pruned}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus, FactHealth))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Facts) != 0 {
		t.Fatalf("precondition: expected an empty bundle, got %d facts", len(bundle.Facts))
	}

	records := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["msg"] != "context fabric fact read" {
			continue
		}
		kind, _ := record["kind"].(string)
		records[kind] = record
	}
	if len(records) != 2 {
		t.Fatalf("fact-read records = %v, want one per planned capability (status, health); a completed run must say what each capability did", records)
	}

	status := records[string(FactStatus)]
	if status["outcome"] != "completed" {
		t.Errorf("status outcome = %v, want %q", status["outcome"], "completed")
	}
	if status["state"] != string(SourceNoData) {
		t.Errorf("status state = %v, want %q -- the ledger must report the SAME state the coverage entry carries", status["state"], SourceNoData)
	}
	if facts, ok := status["facts"].(float64); !ok || facts != 0 {
		t.Errorf("status facts = %v, want 0 -- the count that distinguishes 'returned rows' from 'returned nothing'", status["facts"])
	}
	if status["subject_kinds"] != string(SubjectProject) {
		t.Errorf("status subject_kinds = %v, want %q", status["subject_kinds"], SubjectProject)
	}

	health := records[string(FactHealth)]
	if health["outcome"] != "scope_gap" {
		t.Errorf("health outcome = %v, want %q -- a capability whose targets were unreachable must be distinguishable from one that ran and found nothing", health["outcome"], "scope_gap")
	}
	if facts, ok := health["facts"].(float64); !ok || facts != 0 {
		t.Errorf("health facts = %v, want 0", health["facts"])
	}

	// Corpus safety: the subject's LABEL and canonical ID are content and
	// must never reach a log line, however useful they would be.
	if strings.Contains(sink.String(), "Ask Dev") || strings.Contains(sink.String(), "project_ask_dev") {
		t.Errorf("fact-read records leaked subject content")
	}
}
