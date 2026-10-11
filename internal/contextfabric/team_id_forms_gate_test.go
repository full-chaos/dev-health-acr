package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type teamIDFormsInner struct{ fact CanonicalFact }

func (r *teamIDFormsInner) ReadFacts(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
	return CanonicalFactBundle{Facts: []CanonicalFact{r.fact}, Version: "v1"}, nil
}

func (*teamIDFormsInner) Capabilities() []FactCapability {
	return []FactCapability{{Kind: FactIdentity, Fields: []FactFieldDeclaration{
		{Name: "owning_teams", Type: FactFieldTable, Columns: []FactColumnDeclaration{
			{Name: "team_id", Type: FactFieldString, SubjectRef: &FactSubjectRefDeclaration{Kind: SubjectTeam, IDForm: FactSubjectIDTeamID}},
			{Name: "team_name", Type: FactFieldString, Nullable: true},
		}},
	}}}
}

func TestATeamIDFormAcceptsBothKeyShapes(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{"x": "team:x", "team:x": "team:team:x", "gl:full.chaos": "team:gl:full.chaos"} {
		got, ok := FactSubjectIDTeamID.CanonicalSubjectID(raw)
		if !ok || got != want {
			t.Fatalf("CanonicalSubjectID(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	a, _ := FactSubjectIDTeamID.CanonicalSubjectID("x")
	b, _ := FactSubjectIDTeamID.CanonicalSubjectID("team:x")
	if a == b {
		t.Fatalf("the keys x and team:x are two teams and must not collapse to %q", a)
	}
}

// A restricted caller reads a repository owned by teams whose raw keys are "x",
// "team:x" and "denied": the first two are admitted by the gate and kept, the
// third is withheld.
func TestARestrictedCallerKeepsOwnerRowsOfBothTeamKeyShapes(t *testing.T) {
	t.Parallel()
	row := func(id string) FactValueRow {
		return FactValueRow{Fields: map[string]FactValue{"team_id": StringFactValue(id), "team_name": StringFactValue("Team " + id)}}
	}
	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + chaos7127Allowed, Label: "acme/allowed"}
	fact := CanonicalFact{
		Kind: FactIdentity, Subject: repo,
		Fields:      map[string]FactValue{"owning_teams": TableFactValue(FactTable{Rows: []FactValueRow{row("x"), row("team:x"), row("denied")}})},
		SourceState: SourceAvailable, Source: "test", SourceVersion: "v1",
	}
	graph := &storedSubjectGraph{
		capturingGraphReader: &capturingGraphReader{},
		outcomes: map[string]StoredSubjectOutcome{
			SubjectMapKey(repo): StoredSubjectAdmitted,
			SubjectMapKey(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:x"}):      StoredSubjectAdmitted,
			SubjectMapKey(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:team:x"}): StoredSubjectAdmitted,
			SubjectMapKey(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:denied"}): StoredSubjectDenied,
		},
	}
	reader := newGatedFactReader(&teamIDFormsInner{fact: fact}, NewStoredResultGate(graph), NewSlogEngineTelemetry(nil))
	bundle, err := reader.ReadFacts(context.Background(), chaos7127Restricted, CanonicalFactRequest{Subjects: []SubjectRef{repo}})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	var kept []string
	for _, row := range bundle.Facts[0].Fields["owning_teams"].Rows {
		kept = append(kept, *row.Fields["team_id"].String)
	}
	if len(kept) != 2 || kept[0] != "x" || kept[1] != "team:x" {
		t.Fatalf("kept owner rows = %v, want [x team:x] (the denied team withheld)", kept)
	}
}
