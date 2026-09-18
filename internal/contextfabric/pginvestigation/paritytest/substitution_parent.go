package paritytest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The subject-substitution guard reads the identity a parent SERVED from
// that parent's stored result PAYLOAD. A store returns a valid payload
// beside a semantic snapshot that is absent or malformed, and the guard must
// compare against the parent's served subject exactly as it would with the
// snapshot. RunSubstitutionParentReadSuite drives the REAL Engine.Investigate
// over a store, planting the parent row through the store's own raw seed so
// every snapshot state is the one the store itself reads back, and reads the
// decision off the production confirmed-need-ledger line.

// substitutionPrincipal owns every row these cells plant.
var substitutionPrincipal = storage.Principal{OrgID: "org_substitution_parent"}

// substitutionFollowUpSubject is what every follow-up's resolution commits:
// a project the parent never committed.
var substitutionFollowUpSubject = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-substitute", Label: "Substitute"}

type substitutionInterpreter struct{}

func (substitutionInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		contextfabric.QuestionFamilyOutcome{Family: contextfabric.QuestionFamilyUnclassified, Source: contextfabric.QuestionFamilySourceNone}, nil
}

type substitutionGraph struct{}

func (substitutionGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "substitution-parent-key", Epoch: 0}, nil
}

func (substitutionGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	bases := contextfabric.CommitBasisSet{}
	bases.Record(substitutionFollowUpSubject, contextfabric.CommitBasisAuthoritativeIdentity)
	return contextfabric.SubjectResolution{
		Committed: []contextfabric.SubjectRef{substitutionFollowUpSubject},
		Candidates: []contextfabric.SubjectCandidate{{
			ReceiptID: "receipt_substitution_parent", Subject: substitutionFollowUpSubject, State: contractsv1.ContextFabricResolutionCommitted,
			MatchedTerms: []string{"project"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		}},
	}, contextfabric.StructureOfferMaterial{}, bases, nil, nil
}

func (substitutionGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{Paths: []contextfabric.RelationshipPath{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

type substitutionFacts struct{}

func (substitutionFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{Facts: []contextfabric.CanonicalFact{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[contextfabric.FactKind]string{}, Watermarks: map[contextfabric.FactKind]string{}}, nil
}

type substitutionSynth struct{}

func (substitutionSynth) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	return result("result-substitution-synth", "how is the substitute doing?"), nil
}

// SubstitutionParentCell is one executed cell and what it produced,
// including the shadow binder's decision for the follow-up.
type SubstitutionParentCell struct {
	Name, Guard, ParentID, Status string
	Committed                     int
	// Bindings are the follow-up's anchor-binding transition lines:
	// to_state, to_id, reason and contender_id, one entry per line.
	Bindings []SubstitutionParentBinding
}

// SubstitutionParentBinding is one anchor-binding transition decision.
type SubstitutionParentBinding struct {
	State, ID, Reason, ContenderID string
}

// RunSubstitutionParentReadSuite runs the whole parent-read domain against one
// store: parent payload {readable, unreadable} × snapshot {available,
// available with the parent's anchor binding, malformed, absent} × parent
// identity {one, none}. A readable payload decides by its served subject
// whatever the snapshot is; only an unreadable payload reads
// parent_unreadable. On every cell the guard fires on, the shadow binder
// never binds the withheld subject: it contests the parent's binding when
// the snapshot carries one, and stays unbound on ambiguous proof otherwise.
func RunSubstitutionParentReadSuite(t *testing.T, newStore func(t *testing.T) (contextfabric.InvestigationResultStore, SemanticSeed)) []SubstitutionParentCell {
	t.Helper()
	encoded := snapshotAvailableFixture(t)
	snapshots := []struct {
		name  string
		bytes func(parentID string) []byte
	}{
		{"snapshot_available", func(string) []byte { return encoded }},
		{"snapshot_bound", func(parentID string) []byte { return snapshotBoundFixture(t, parentID) }},
		// Valid JSON in a document this build refuses: a jsonb column accepts
		// it, and the store reads it back malformed.
		{"snapshot_malformed", func(string) []byte {
			return []byte(`{"format_version":"semantic-state.v1","family":"not-a-family"}`)
		}},
		{"snapshot_absent", func(string) []byte { return nil }},
	}
	var cells []SubstitutionParentCell
	for _, identity := range []string{"one_identity", "no_identity"} {
		for _, snapshot := range snapshots {
			for _, payloadReadable := range []bool{true, false} {
				payload := "payload_readable"
				if !payloadReadable {
					payload = "payload_unreadable"
				}
				name := payload + "/" + snapshot.name + "/" + identity
				cell := runSubstitutionParentCell(t, newStore, name, identity == "one_identity", payloadReadable, snapshot.bytes(substitutionParentID(name)))
				cells = append(cells, cell)
				want := "clarified_subject_changed"
				switch {
				case !payloadReadable:
					want = "parent_unreadable"
				case identity == "no_identity":
					want = "parent_no_identity"
				}
				t.Logf("%-58s guard=%-26s parent_id=%-40q status=%s committed=%d", name, cell.Guard, cell.ParentID, cell.Status, cell.Committed)
				if cell.Guard != want {
					t.Errorf("%s: substitution_guard = %q, want %q", name, cell.Guard, want)
				}
				if want == "clarified_subject_changed" && (cell.Committed != 0 || cell.Status != string(contextfabric.InvestigationClarificationRequired)) {
					t.Errorf("%s: served %d committed with status %q; a readable parent's subject is never substituted", name, cell.Committed, cell.Status)
				}
				if want == "clarified_subject_changed" {
					assertWithheldNeverBound(t, name, cell, snapshot.name == "snapshot_bound")
				}
			}
		}
	}
	return cells
}

func runSubstitutionParentCell(t *testing.T, newStore func(t *testing.T) (contextfabric.InvestigationResultStore, SemanticSeed), name string, oneIdentity, payloadReadable bool, snapshot []byte) SubstitutionParentCell {
	t.Helper()
	store, seed := newStore(t)
	parentID := substitutionParentID(name)
	parent := result(parentID, "how is the parent project doing?")
	if !oneIdentity {
		parent.SubjectResolution.Committed = []contextfabric.SubjectRef{}
	}
	payload, err := json.Marshal(parent)
	if err != nil {
		t.Fatalf("%s: marshal parent: %v", name, err)
	}
	if !payloadReadable {
		// Valid JSON the store refuses on read: the payload is unavailable.
		payload = []byte(`{"schema_version":"context_fabric_investigation_result.v1"}`)
	}
	seed(t, substitutionPrincipal.OrgID, parentID, payload, snapshot)
	// The cell is only meaningful if the store reads the parent back the way
	// its name says: a readable payload beside the named snapshot state, or
	// no readable payload at all.
	stored, getErr := store.Get(context.Background(), substitutionPrincipal, parentID)
	switch {
	case !payloadReadable && getErr == nil:
		t.Fatalf("%s: fixture defect: the unreadable payload read back", name)
	case payloadReadable && getErr != nil:
		t.Fatalf("%s: fixture defect: the readable payload did not read: %v", name, getErr)
	case payloadReadable:
		want := contextfabric.SemanticStateReadAvailable
		switch {
		case snapshot == nil:
			want = contextfabric.SemanticStateReadAbsent
		case !bytes.Equal(snapshot, snapshotAvailableFixture(t)) && !bytes.Equal(snapshot, snapshotBoundFixture(t, parentID)):
			want = contextfabric.SemanticStateReadMalformed
		}
		if stored.SemanticStateRead != want {
			t.Fatalf("%s: fixture defect: the snapshot read back %s, want %s", name, stored.SemanticStateRead, want)
		}
	}

	var sink bytes.Buffer
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: substitutionInterpreter{}, Graph: substitutionGraph{}, Facts: substitutionFacts{}, Synthesizer: substitutionSynth{},
		Results:   store,
		Telemetry: contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelInfo}))),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{
		ServiceVersion: "substitution-parent-parity",
		Now:            func() time.Time { return time.Unix(900, 0).UTC() },
		NewResultID:    func() string { return "result-substitution-child-" + sanitizeCellName(name) },
	})
	if err != nil {
		t.Fatalf("%s: NewEngine: %v", name, err)
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request-substitution-" + sanitizeCellName(name),
		Question:      "And how does the other project compare?",
		TimeContext:   contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer:       contextfabric.ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
		ParentResultID: parentID,
	}
	served, err := engine.Investigate(context.Background(), substitutionPrincipal, request)
	if err != nil {
		t.Fatalf("%s: Investigate: %v", name, err)
	}
	cell := SubstitutionParentCell{Name: name, Status: string(served.Status), Committed: len(served.SubjectResolution.Committed)}
	scanner := bufio.NewScanner(&sink)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line["msg"] == contextfabric.AnchorBindingTransitionLogMessage {
			var binding SubstitutionParentBinding
			binding.State, _ = line["to_state"].(string)
			binding.ID, _ = line["to_id"].(string)
			binding.Reason, _ = line["reason"].(string)
			binding.ContenderID, _ = line["contender_id"].(string)
			cell.Bindings = append(cell.Bindings, binding)
			continue
		}
		if line["msg"] != "context fabric confirmed need ledger" {
			continue
		}
		cell.Guard, _ = line["substitution_guard"].(string)
		cell.ParentID, _ = line["substitution_parent_id"].(string)
	}
	if cell.Guard == "" {
		t.Fatalf("%s: no confirmed-need-ledger line was emitted", name)
	}
	return cell
}

// assertWithheldNeverBound is the binder half of a fired cell: every
// transition line keeps the withheld subject a contender only.
func assertWithheldNeverBound(t *testing.T, name string, cell SubstitutionParentCell, parentBound bool) {
	t.Helper()
	if len(cell.Bindings) == 0 {
		t.Errorf("%s: no anchor-binding transition line was emitted", name)
	}
	for _, binding := range cell.Bindings {
		if binding.ID == substitutionFollowUpSubject.CanonicalID {
			t.Errorf("%s: the shadow binder bound the withheld subject: %+v", name, binding)
		}
		switch {
		case parentBound && (binding.State != "contested" || binding.ContenderID != substitutionFollowUpSubject.CanonicalID):
			t.Errorf("%s: binding = %+v, want the parent's binding contested by the withheld subject", name, binding)
		case !parentBound && (binding.State != "unbound" || binding.Reason != "ambiguous_proof"):
			t.Errorf("%s: binding = %+v, want unbound on ambiguous proof", name, binding)
		}
	}
}

// substitutionParentID is the parent result id a cell plants.
func substitutionParentID(name string) string {
	return "result-substitution-parent-" + sanitizeCellName(name)
}

// snapshotBoundFixture is the readable snapshot carrying the parent's own
// anchor binding: bound to the project the parent committed, at the graph
// epoch the follow-up resolves.
func snapshotBoundFixture(t *testing.T, parentID string) []byte {
	t.Helper()
	state := SemanticStateFixture(contextfabric.SubjectTeam, contextfabric.SubjectRepository)
	binding, err := json.Marshal(map[string]any{
		"state": "bound", "kind": string(contextfabric.SubjectProject), "canonical_id": "project-" + parentID,
		"proof": "identity_proven", "reason": "identity_proven", "origin_result_id": parentID, "graph_epoch": 0,
	})
	if err != nil {
		t.Fatalf("encode binding fixture: %v", err)
	}
	state.Extensions = contextfabric.SemanticStateExtensions{"anchor_binding": binding}
	encoded, err := contextfabric.EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("encode bound snapshot fixture: %v", err)
	}
	return encoded
}

// sanitizeCellName makes a cell name safe inside a result id.
func sanitizeCellName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, c)
			continue
		}
		out = append(out, '-')
	}
	return string(out)
}

// snapshotAvailableFixture is the one readable snapshot these cells plant.
func snapshotAvailableFixture(t *testing.T) []byte {
	t.Helper()
	encoded, err := contextfabric.EncodeSemanticState(SemanticStateFixture(contextfabric.SubjectTeam, contextfabric.SubjectRepository))
	if err != nil {
		t.Fatalf("encode snapshot fixture: %v", err)
	}
	return encoded
}
