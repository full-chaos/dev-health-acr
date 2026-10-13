package directread

import (
	"errors"
	"slices"

	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread/gatevocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestServedRelationshipsVocabularyRefusesAnOffSetValueBuiltAtRunTime(t *testing.T) {
	build := func(axis, tier string) RelationshipsResponse {
		response := RelationshipsResponse{Effective: EffectiveRelationshipsRead{Axis: axis}}
		response.Edges = []ServedEdge{{Provenance: RelationshipSource{LinkTier: tier}}}
		return response
	}
	for _, axis := range RelationshipsAxisVocabulary() {
		for _, tier := range append(LinkTierVocabulary(), "") {
			if err := validateServedRelationshipsVocabulary(build(axis, tier)); err != nil {
				t.Errorf("axis %q tier %q refused: %v", axis, tier, err)
			}
		}
	}
	offAxis := fmt.Sprintf("%s_%d", "cur", len("rent"))
	offTier := fmt.Sprintf("%s-%s", "nat", "ive")
	if err := validateServedRelationshipsVocabulary(build(offAxis, "native")); err == nil {
		t.Error("an off-set axis was served")
	}
	if err := validateServedRelationshipsVocabulary(build("current", offTier)); err == nil {
		t.Error("an off-set link tier was served")
	}
}

func TestReadRelationshipsRunsTheServedVocabularyCheckOnItsReturnPath(t *testing.T) {
	src, err := os.ReadFile("read_relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "read_relationships.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	telemetryAt, refuseAt, position := -1, -1, 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Read" || fn.Recv == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			if _, isDefer := stmt.(*ast.DeferStmt); !isDefer {
				continue
			}
			position++
			ast.Inspect(stmt, func(m ast.Node) bool {
				if id, ok := m.(*ast.Ident); ok {
					switch id.Name {
					case "RecordDirectRelationshipsRead":
						telemetryAt = position
					case "refuseOffSetServedVocabulary":
						refuseAt = position
					}
				}
				return true
			})
		}
	}
	if telemetryAt < 0 || refuseAt < 0 {
		t.Fatalf("Read has telemetry defer at %d and vocabulary defer at %d; the measurement found no call site", telemetryAt, refuseAt)
	}
	if refuseAt < telemetryAt {
		t.Fatal("the vocabulary defer is registered before the telemetry defer, so telemetry would record the unrefused result")
	}
}

func TestOffSetServedVocabularyIsRecordedWithItsOwnFailureClass(t *testing.T) {
	offAxis := fmt.Sprintf("%s_%d", "cur", 1)
	response := RelationshipsResponse{Status: RelationshipsComplete, Effective: EffectiveRelationshipsRead{Axis: offAxis}}
	var record RelationshipsReadRecord
	var err error
	refuseOffSetServedVocabulary(&record, &response, &err)
	if !errors.Is(err, ErrRelationshipsInternal) || !errors.Is(err, ErrRelationshipsServedVocabulary) {
		t.Fatalf("err = %v", err)
	}
	if record.FailureClass != gatevocab.RelationshipsFailureVocabulary || response.Status != "" {
		t.Fatalf("record class %q, response %+v", record.FailureClass, response)
	}
	if !slices.Contains(RelationshipsReadLogArgs(storage.Principal{}, record), any(string(gatevocab.RelationshipsFailureVocabulary))) {
		t.Fatalf("the read line does not carry failure_class %s: %v", gatevocab.RelationshipsFailureVocabulary, RelationshipsReadLogArgs(storage.Principal{}, record))
	}
	clean := RelationshipsResponse{Status: RelationshipsComplete, Effective: EffectiveRelationshipsRead{Axis: RelationshipsAxisCurrent}}
	var cleanRecord RelationshipsReadRecord
	var cleanErr error
	refuseOffSetServedVocabulary(&cleanRecord, &clean, &cleanErr)
	if cleanErr != nil || cleanRecord.FailureClass != "" || clean.Status != RelationshipsComplete {
		t.Fatalf("a served-set response was refused: %v %q", cleanErr, cleanRecord.FailureClass)
	}
}
