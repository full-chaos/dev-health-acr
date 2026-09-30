package evidenceref_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// adversarial component values: every shape a real id takes that the
// retired bare-':' joins could not tell apart (provider-prefixed work items,
// Atlassian team ARIs, relation keys), plus the codec's own escape texts, a
// bare '%', a lower-case escape the decoder would also accept, and the empty
// string.
var adversarial = []string{"", "a", "b", ":", "::", "a:b", "b:a", "a:", ":b", "%", "%3A", "%25", "%3a", "%%3A", "%253A", "jira:A:B", "B:jira:C", "ari:cloud:identity::team/1", "blocks:fwd", "é:ü"}

var uuids = []string{"00000000-0000-0000-0000-000000000000", "20000000-0000-4000-8000-000000000002"}

// tuples lists every value tuple of kind's grammar over the adversarial
// sets (UUID segments over uuids), capped per segment so four-segment kinds
// stay small.
func tuples(t *testing.T, kind contractsv1.ContextFabricEvidenceEntityType, width int) [][]string {
	t.Helper()
	grammar, ok := evidenceref.Lookup(kind)
	if !ok {
		t.Fatalf("%s has no grammar", kind)
	}
	out := [][]string{nil}
	for _, segment := range grammar.Segments {
		values := adversarial
		if len(values) > width {
			values = values[:width]
		}
		if segment.Form == evidenceref.UUID {
			values = uuids
		}
		var next [][]string
		for _, prefix := range out {
			for _, value := range values {
				next = append(next, append(append([]string(nil), prefix...), value))
			}
		}
		out = next
	}
	return out
}

func width(kind contractsv1.ContextFabricEvidenceEntityType) int {
	grammar, _ := evidenceref.Lookup(kind)
	encoded := 0
	for _, segment := range grammar.Segments {
		if segment.Form == evidenceref.Encoded {
			encoded++
		}
	}
	if encoded >= 4 {
		return 10
	}
	return len(adversarial)
}

// Mint is injective and Parse is its exact inverse, over every tuple.
func TestMintIsInjectiveAndParseInvertsIt(t *testing.T) {
	for _, kind := range evidenceref.Kinds() {
		seen := map[string][]string{}
		for _, values := range tuples(t, kind, width(kind)) {
			ref, _ := evidenceref.Mint(kind, values...)
			if ref == "" {
				t.Fatalf("%s %q: Mint refused valid values", kind, values)
			}
			if earlier, dup := seen[ref]; dup {
				t.Fatalf("%s: %q and %q both mint %q", kind, earlier, values, ref)
			}
			seen[ref] = values
			id := strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(kind)+":")
			parsed, ok := evidenceref.Parse(kind, id)
			if !ok || !reflect.DeepEqual(parsed, values) {
				t.Fatalf("%s: Parse(%q) = %q, %v; want %q", kind, id, parsed, ok, values)
			}
		}
		if len(seen) < 100 {
			t.Fatalf("%s: only %d tuples swept", kind, len(seen))
		}
	}
}

// Parse accepts only what Mint produces: a wrong segment count, a
// non-canonical UUID, or an escape the codec never emits is refused, so no
// two strings parse to one row.
func TestParseRefusesWhatMintNeverProduces(t *testing.T) {
	uuid := uuids[1]
	cases := map[contractsv1.ContextFabricEvidenceEntityType][]string{
		contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2: {
			"a:b", "a:b:c:d", "a:b:c:", "%:b:c", "a:%3a:c", "a:%2:c", "a:b:%", "a:b:c%", "",
		},
		contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2: {
			uuid + ":a", uuid + ":a:b:c", uuid + ":%:b", "%" + uuid + ":a:b",
		},
		contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2: {
			uuid + ":a:b", uuid + ":a:b:c:d", uuid + ":a:b:%2",
		},
		contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2: {
			uuid + ":a:b", "edge-1", strings.Repeat("a", 64),
		},
		contractsv1.ContextFabricEvidenceEntityProjectTeamV2: {
			"gitlab:p:t", "gitlab:p:t:s:x",
		},
	}
	for kind, ids := range cases {
		for _, id := range ids {
			if values, ok := evidenceref.Parse(kind, id); ok {
				t.Errorf("%s: Parse(%q) accepted %q", kind, id, values)
			}
		}
	}
	if _, ok := evidenceref.Parse(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:b:c"); ok {
		t.Error("Parse accepted a kind with no grammar")
	}
}

// The r2 P1 collision and its shapes on every kind: the retired joins gave
// one string for two rows; the grammar gives two.
func TestRetiredCollisionsMintDistinctRefs(t *testing.T) {
	uuid := uuids[1]
	pairs := []struct {
		kind contractsv1.ContextFabricEvidenceEntityType
		a, b []string
	}{
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, []string{"jira:A:B", "jira:C", "blocks:fwd"}, []string{"jira:A", "B:jira:C", "blocks:fwd"}},
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, []string{uuid, "jira:A:B", "jira:C"}, []string{uuid, "jira:A", "B:jira:C"}},
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, []string{uuid, "jira:A", "ari:cloud:identity::team/1", "native_team"}, []string{uuid, "jira:A:ari", "cloud:identity::team/1", "native_team"}},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, []string{uuid, "dep:1", "inc", "native"}, []string{uuid, "dep", "1:inc", "native"}},
		{contractsv1.ContextFabricEvidenceEntityProjectTeamV2, []string{"gitlab", "org:gitlab:1", "gl:team", "native"}, []string{"gitlab", "org:gitlab:1:gl", "team", "native"}},
	}
	for _, pair := range pairs {
		if strings.Join(pair.a, ":") != strings.Join(pair.b, ":") {
			t.Fatalf("%s: the pair does not collide under a bare ':' join", pair.kind)
		}
		a, _ := evidenceref.Mint(pair.kind, pair.a...)
		b, _ := evidenceref.Mint(pair.kind, pair.b...)
		if a == b {
			t.Fatalf("%s: %q and %q mint one ref %q", pair.kind, pair.a, pair.b, a)
		}
	}
}

// Mint returns the ref even when it breaks the contract bound, and says so:
// the producer's contract validator then quarantines the item instead of the
// ref being truncated or dropped unseen.
func TestMintReportsTheContractBound(t *testing.T) {
	kind := contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2
	if ref, fits := evidenceref.Mint(kind, "jira:A", "jira:B", "blocks:fwd"); !fits || ref != "acr:v1:work-item-dependency.v2:jira%3AA:jira%3AB:blocks%3Afwd" {
		t.Fatalf("ordinary ref = %q, fits %v", ref, fits)
	}
	long := strings.Repeat("x", 250)
	if ref, fits := evidenceref.Mint(kind, long, "b", "c"); fits || ref == "" {
		t.Fatalf("oversize ref: fits %v, ref %q", fits, ref)
	}
	if ref, fits := evidenceref.Mint(kind, "a|b", "b", "c"); fits || ref == "" {
		t.Fatalf("a '|' ref: fits %v, ref %q", fits, ref)
	}
	if ref, fits := evidenceref.Mint(kind, "a", "b", "c "); fits || ref == "" {
		t.Fatalf("a trailing-space ref: fits %v, ref %q", fits, ref)
	}
	if ref, _ := evidenceref.Mint(kind, "a", "b"); ref != "" {
		t.Fatalf("a short tuple minted %q", ref)
	}
}

// The grammar covers exactly the ".v2" successors contracts/v1 declares, and
// each successor's label reads as its unescaped components joined by ':'
// (D5: the label is not a key, so the escaping must not show).
func TestGrammarsAreTheEncodedKindsAndLabelsDecode(t *testing.T) {
	declared := map[contractsv1.ContextFabricEvidenceEntityType]bool{}
	for _, kind := range contractsv1.ContextFabricEvidenceEntityTypeVocabulary() {
		if contractsv1.EncodedEvidenceEntityType(kind) {
			declared[kind] = true
		}
		if successor, retired := contractsv1.RetiredEvidenceEntityType(kind); retired && !contractsv1.EncodedEvidenceEntityType(successor) {
			t.Errorf("retired %s names a successor %s that is not encoded", kind, successor)
		}
	}
	kinds := evidenceref.Kinds()
	if len(kinds) != len(declared) || len(kinds) != 5 {
		t.Fatalf("grammars %v, encoded kinds %v", kinds, declared)
	}
	for _, kind := range kinds {
		if !declared[kind] {
			t.Fatalf("%s has a grammar but is not an encoded kind", kind)
		}
		for _, values := range tuples(t, kind, 6) {
			ref, _ := evidenceref.Mint(kind, values...)
			label, known := contractsv1.ContextFabricEvidenceRefLabel(ref)
			noun, _ := contractsv1.ContextFabricEvidenceRefLabel(contractsv1.ContextFabricEvidenceRefPrefix + string(kind) + ":x")
			want := strings.TrimSuffix(noun, ": x") + ": " + strings.Join(values, ":")
			if !known || label != want {
				t.Fatalf("%s %q: label %q, want %q", kind, values, label, want)
			}
		}
	}
}

// SQL spells the same grammar: the kind literal, one encode per segment in
// grammar order, a UUID column through toString first. The executed parity (the
// same rows through Mint and through this expression) is the ClickHouse
// integration test's.
func TestSQLSpellsTheGrammar(t *testing.T) {
	got := evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, "c.repo_id", "c.work_item_id", "c.parent_id")
	want := "concat('acr:v1:work-item-hierarchy.v2:', concat(replaceAll(replaceAll(toString(c.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.work_item_id, '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(c.parent_id, '%', '%25'), ':', '%3A')))"
	if got != want {
		t.Fatalf("SQL =\n%s\nwant\n%s", got, want)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("SQL accepted a wrong expression count")
		}
	}()
	_ = evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, "c.repo_id")
}
