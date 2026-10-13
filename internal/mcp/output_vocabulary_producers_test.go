package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// producerSet names the files that emit one served value set and the exported
// constants they must use for it.
type producerSet struct {
	name   string
	values []string
	files  []string
	consts []string
}

// A set whose name ends in "(SQL)" scans SQL text: only a value emitted or
// compared as the provenance column counts, because other columns reuse the
// same words.
func (s producerSet) sql() bool { return strings.HasSuffix(s.name, "(SQL)") }

var producerSets = []producerSet{
	{"effective.axis", []string{"current", "as_of"}, []string{"internal/contextfabric/directread/read_relationships.go"}, []string{"RelationshipsAxisCurrent", "RelationshipsAxisAsOf"}},
	{"provenance.link_tier", []string{"native", "explicit_text", "heuristic"}, []string{"internal/contextfabric/directread/read_relationships.go"}, []string{"TreeLinkTierNative", "TreeLinkTierExplicitText", "TreeLinkTierHeuristic"}},
	{"record_episode.status", []string{"recorded", "no_persist"}, []string{"internal/mcp/record_episode.go", "internal/contracts/v1/mcp_validate.go"}, []string{"MCPRecordEpisodeStatusRecorded", "MCPRecordEpisodeStatusNoPersist"}},
	{"redaction_state", []string{"active", "redacted", "purged_tombstone"}, []string{
		"internal/mcp/record_episode.go", "internal/contracts/v1/validate.go",
		"internal/storage/memory/episodes.go", "internal/storage/memory/episodes_get.go",
		"internal/storage/postgres/episodes.go", "internal/contextfabric/devhealthsource/episodes.go",
	}, []string{"AgentEpisodeRedactionActive", "AgentEpisodeRedactionRedacted", "AgentEpisodeRedactionPurgedTombstone"}},
	{"transcript_disposition", []string{"not_submitted", "accepted", "redacted"}, []string{"internal/mcp/record_episode.go", "internal/contracts/v1/mcp_validate.go"}, []string{"MCPTranscriptDispositionNotSubmitted", "MCPTranscriptDispositionAccepted", "MCPTranscriptDispositionRedacted"}},
	{"commit_gate", []string{"caller_hint_short_circuit", "pre_committed_exact_hint", "exact_index", "identity_fast_path", "lone_floor", "top_of_two", "vector_margin_rescue", "evidence_census"}, []string{
		"internal/contextfabric/graphrank/resolution.go", "internal/contextfabric/graphrank/resolve.go",
		"internal/contextfabric/graphrank/explicit_handle_commit.go", "internal/contextfabric/graphrank/exact_label_proof.go",
		"internal/contextfabric/count_population_scope.go",
	}, []string{"CommitGatePreCommittedExactHint", "CommitGateExactIndex", "CommitGateIdentityFastPath", "CommitGateLoneFloor", "CommitGateTopOfTwo", "CommitGateVectorMarginRescue", "CommitGateEvidenceCensus", "CommitGateCallerHintShortCircuit"}},
	{"watermark.status", []string{"fresh", "stale", "missing", "unavailable"}, []string{
		"internal/contextpacket/read_adapter.go", "internal/contextpacket/evidence_quarantine.go", "internal/contextpacket/source_catalog.go",
		"internal/contextpacket/packet_state.go", "internal/contextpacket/observation.go", "internal/contextpacket/packet_output_validation.go",
		"internal/contracts/v1/validate_packet_nested.go",
	}, []string{"SourceWatermarkStatusFresh", "SourceWatermarkStatusStale", "SourceWatermarkStatusMissing", "SourceWatermarkStatusUnavailable", "SourceWatermarkStatusVocabulary"}},
	{"evidence.provenance", []string{"native", "explicit_text", "heuristic", "derived"}, []string{
		"internal/contextpacket/read_adapter.go", "internal/contextpacket/evidence_validation.go", "internal/contextpacket/ranking.go",
		"internal/mcp/federation.go", "internal/contextfabric/evidence_expansion.go", "internal/contracts/v1/validate_evidence.go",
	}, []string{"EvidenceProvenanceNative", "EvidenceProvenanceExplicitText", "EvidenceProvenanceHeuristic", "EvidenceProvenanceDerived", "EvidenceProvenanceVocabulary"}},
	{"evidence.provenance (SQL)", []string{"native", "explicit_text", "heuristic", "derived"}, []string{
		"internal/contextpacket/source_queries.go", "internal/contextpacket/source_rows.go", "internal/contextpacket/read_adapter.go", "internal/contextpacket/source_executor.go", "internal/contextpacket/provenance_sql.go",
	}, []string{"sqlProvenanceNative", "sqlProvenanceExplicitText", "sqlProvenanceHeuristic", "sqlProvenanceDerived", "provenanceRankSQL"}},
}

// producerFields are struct fields that carry a pinned set; no production file
// may assign a string literal to one.
var producerFields = map[string]bool{"RedactionState": true, "TranscriptDisposition": true, "Axis": true, "LinkTier": true, "CommitGate": true, "Provenance": true}

type producerScan struct {
	bareLiterals []string
	constUses    int
}

func scanProducerSource(t *testing.T, name string, src []byte, set producerSet) producerScan {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out producerScan
	isConst := map[string]bool{}
	for _, c := range set.consts {
		isConst[c] = true
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(v.Value)
			if err != nil {
				return true
			}
			for _, value := range set.values {
				if set.sql() {
					if regexp.MustCompile(`'` + value + `'\s+(AS\s+)?provenance\b|provenance\s*=\s*'` + value + `'`).MatchString(text) {
						out.bareLiterals = append(out.bareLiterals, fset.Position(v.Pos()).String()+" "+strconv.Quote(value))
					}
					continue
				}
				if text == value || strings.Contains(text, "'"+value+"'") {
					out.bareLiterals = append(out.bareLiterals, fset.Position(v.Pos()).String()+" "+strconv.Quote(value))
				}
			}
		case *ast.SelectorExpr:
			if isConst[v.Sel.Name] {
				out.constUses++
			}
		case *ast.Ident:
			if isConst[v.Name] {
				out.constUses++
			}
		}
		return true
	})
	return out
}

func assignedFieldLiterals(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out []string
	isString := func(e ast.Expr) bool {
		lit, ok := e.(*ast.BasicLit)
		return ok && lit.Kind == token.STRING
	}
	fieldOf := func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.SelectorExpr:
			return v.Sel.Name
		case *ast.Ident:
			return v.Name
		}
		return ""
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.KeyValueExpr:
			if producerFields[fieldOf(v.Key)] && isString(v.Value) {
				out = append(out, fset.Position(v.Pos()).String())
			}
		case *ast.AssignStmt:
			for i, l := range v.Lhs {
				if i < len(v.Rhs) {
					if sel, ok := l.(*ast.SelectorExpr); ok && producerFields[sel.Sel.Name] && isString(v.Rhs[i]) {
						out = append(out, fset.Position(v.Pos()).String())
					}
				}
			}
		}
		return true
	})
	return out
}

func TestOutputVocabularyProducersUseTheExportedConstants(t *testing.T) {
	for _, set := range producerSets {
		total := 0
		for _, rel := range set.files {
			src, err := os.ReadFile(repopath.Path(t, strings.Split(rel, "/")...))
			if err != nil {
				t.Fatal(err)
			}
			scan := scanProducerSource(t, rel, src, set)
			for _, lit := range scan.bareLiterals {
				t.Errorf("%s: producer %s carries the bare literal %s; use the exported constant", set.name, rel, lit)
			}
			if scan.constUses == 0 && !strings.HasSuffix(rel, "validate.go") && !strings.HasSuffix(rel, "mcp_validate.go") {
				t.Errorf("%s: %s uses none of %v; a producer scan that finds no producer site did not measure", set.name, rel, set.consts)
			}
			total += scan.constUses
		}
		if total == 0 {
			t.Errorf("%s: no producer site found in %v; the measurement did not happen", set.name, set.files)
		}
	}
}

func TestOutputVocabularyNoProductionFileAssignsAPinnedFieldFromALiteral(t *testing.T) {
	root := repopath.Path(t, "internal")
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, pos := range assignedFieldLiterals(t, path, src) {
			t.Errorf("a pinned vocabulary field is assigned a string literal at %s", pos)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("scanned %d files; the measurement did not happen", scanned)
	}
}

func TestOutputVocabularyProducerScanSeesAPlantedLiteral(t *testing.T) {
	set := producerSets[3]
	planted := []byte("package x\nfunc f() { _ = T{RedactionState: \"active\"}; q := `SELECT 1 WHERE s <> 'purged_tombstone'`; _ = q }\n")
	scan := scanProducerSource(t, "planted.go", planted, set)
	if len(scan.bareLiterals) != 2 {
		t.Fatalf("planted literals found = %v, want 2", scan.bareLiterals)
	}
	if got := assignedFieldLiterals(t, "planted.go", planted); len(got) != 1 {
		t.Fatalf("planted field assignment found = %v, want 1", got)
	}
	clean := scanProducerSource(t, "clean.go", []byte("package x\nfunc f() { _ = T{RedactionState: c.AgentEpisodeRedactionActive} }\n"), set)
	if len(clean.bareLiterals) != 0 || clean.constUses == 0 {
		t.Fatalf("clean scan = %+v", clean)
	}
}
