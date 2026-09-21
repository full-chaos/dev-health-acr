package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// citingInvestigator answers the way the engine does: it persists the
// result it returns, and the result cites one evidence ref of every kind in
// the closed evidence-entity vocabulary, so the evidence refs the answer
// carries are exactly the refs a real investigation hands a caller.
type citingInvestigator struct {
	results *memoryinvestigation.Store
	calls   atomic.Int64
}

func (c *citingInvestigator) Investigate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	id := fmt.Sprintf("result_citing_%s_%03d", principal.OrgID, c.calls.Add(1))
	result := citingResult(id, "project_org1_owner", citedEvidenceRefs())
	result.RequestID = request.RequestID
	err := c.results.Save(ctx, principal, result, contextfabric.SourceWatermarkSnapshot{}, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{},
		contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	return result, err
}

// citedEvidenceRefs is one ref per evidence-entity kind, built by the one
// production constructor.
func citedEvidenceRefs() []string {
	var refs []string
	for _, kind := range contractsv1.ContextFabricEvidenceEntityTypeVocabulary() {
		refs = append(refs, contractsv1.EvidenceRefID(kind, "example-org/matrix:"+string(kind)+"-0001"))
	}
	return refs
}

// citingResult is matrixResult with one current driver citing refs, and the
// label map the engine stamps over the ref closure.
func citingResult(id, subjectID string, refs []string) contractsv1.ContextFabricInvestigationResult {
	result := matrixResult(id, "cites-evidence", "Cited project", subjectID)
	subject := result.SubjectResolution.Committed[0]
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{{
		DriverID: "driver_cites_evidence", Standing: contractsv1.ContextFabricDriverPrincipal, Category: "narrative",
		Title: "Delivery evidence", Summary: "Delivery evidence summary.", AffectedSubjects: []contractsv1.ContextFabricSubjectRef{subject},
		EvidenceRefIDs: slices.Clone(refs), Derivation: contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus: contractsv1.ContextFabricEpistemicObserved, Confidence: 0.8, Current: true,
	}}
	result.EvidenceRefIDs = slices.Clone(refs)
	result.EvidenceRefLabels = map[string]string{}
	for ref := range contractsv1.ContextFabricEvidenceRefClosure(result) {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		result.EvidenceRefLabels[ref] = label
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

func citingTarget(t *testing.T) *matrixTarget {
	t.Helper()
	stack := newMatrixStackWith(t, func(results *memoryinvestigation.Store) contextfabric.Investigator {
		return &citingInvestigator{results: results}
	})
	callers := issueMatrixCallers(stack)
	e := newMatrixEndpoint(t, stack)
	return &matrixTarget{
		name: "in-process-citing", url: e.url(), client: &http.Client{Timeout: matrixRigDeadline},
		bearers: callers.bearers(), fixture: inProcessFixture(callers),
	}
}

func structuredOf(t *testing.T, id string, reply rpcReply) map[string]any {
	t.Helper()
	var envelope struct {
		IsError           bool           `json:"isError"`
		StructuredContent map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal(reply.result, &envelope); err != nil || envelope.IsError || envelope.StructuredContent == nil {
		tool, _ := reply.tool()
		t.Fatalf("%s: status %d, tool error %v: %s", id, reply.status, envelope.IsError, truncate(tool.text(), 400))
	}
	return envelope.StructuredContent
}

// Every evidence ref investigate_question hands a caller expands through
// source_evidence for that same caller, over the real acr-mcp http handler
// in front of a real acr-api.
func TestInvestigationEvidenceRefsExpandThroughSourceEvidence(t *testing.T) {
	target := citingTarget(t)
	answer := structuredOf(t, "investigate", target.callRaw(t, "A", "cites-investigate", "investigate_question", map[string]any{"question": "what is the status of the project?"}))
	refs := collectEvidenceRefs(answer)
	if len(refs) == 0 {
		t.Fatal("the answer carried no evidence_ref_ids; nothing to expand")
	}
	want := citedEvidenceRefs()
	slices.Sort(want)
	if !slices.Equal(refs, want) {
		t.Fatalf("answer evidence refs %v, want every cited kind %v", refs, want)
	}
	for _, ref := range refs {
		reply := target.callRaw(t, "A", "cites-expand", "source_evidence", evidenceArgs(ref))
		expanded := structuredOf(t, "source_evidence "+ref, reply)
		got, _ := field(expanded, "structured", "evidence", "evidence_ref_id").(string)
		if got != ref {
			t.Errorf("source_evidence %s returned evidence_ref_id %q", ref, got)
		}
		kind := strings.SplitN(ref, ":", 4)[2]
		if entity, _ := field(expanded, "structured", "evidence", "source", "entity_type").(string); entity != kind {
			t.Errorf("source_evidence %s entity_type %q, want %q", ref, entity, kind)
		}
		if system, _ := field(expanded, "structured", "evidence", "source", "system").(string); system != contextfabric.ContextFabricEvidenceSystem {
			t.Errorf("source_evidence %s source.system %q, want the persisted-record system %q", ref, system, contextfabric.ContextFabricEvidenceSystem)
		}
		if provenance, _ := field(expanded, "structured", "evidence", "provenance").(string); provenance != contextfabric.ContextFabricEvidenceProvenance {
			t.Errorf("source_evidence %s provenance %q, want %q", ref, provenance, contextfabric.ContextFabricEvidenceProvenance)
		}
		if markdown, _ := field(expanded, "rendered_markdown", "markdown").(string); !strings.Contains(markdown, "Persisted evidence record") {
			t.Errorf("source_evidence %s markdown does not say it is a persisted evidence record:\n%s", ref, markdown)
		}
		if field(expanded, "rendered_markdown", "untrusted") != true {
			t.Errorf("source_evidence %s rendered_markdown.untrusted = %v, want true", ref, field(expanded, "rendered_markdown", "untrusted"))
		}
	}
}

// A caller the result is not readable by gets the same not-found as for an
// id that never existed: B is another organization, C holds a grant on a
// repository the cited subject does not live in.
func TestInvestigationEvidenceRefsStayDeniedForOtherCallers(t *testing.T) {
	target := citingTarget(t)
	answer := structuredOf(t, "investigate", target.callRaw(t, "A", "cites-investigate", "investigate_question", map[string]any{"question": "what is the status of the project?"}))
	refs := collectEvidenceRefs(answer)
	if len(refs) == 0 {
		t.Fatal("the answer carried no evidence_ref_ids")
	}
	never := target.callRaw(t, "A", "cites-never", "source_evidence", evidenceArgs(contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "never-cited-0001")))
	neverTool, ok := never.tool()
	if !ok || !neverTool.IsError {
		t.Fatalf("an uncited ref expanded: %s", string(never.raw))
	}
	for _, caller := range []string{"B", "C"} {
		for _, ref := range refs {
			reply := target.callRaw(t, caller, "cites-foreign", "source_evidence", evidenceArgs(ref))
			tool, ok := reply.tool()
			if !ok || !tool.IsError {
				t.Fatalf("caller %s expanded %s: %s", caller, ref, string(reply.raw))
			}
			if normalizeDenial(tool.text()) != normalizeDenial(neverTool.text()) {
				t.Errorf("caller %s on %s: denial %q differs from never-cited %q", caller, ref, tool.text(), neverTool.text())
			}
		}
	}
}
