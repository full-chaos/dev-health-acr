package sourcerow_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const projectTeamQuery = "project_teams.v2"

var projectTeamKind = contractsv1.ContextFabricEvidenceEntityProjectTeamV2

func projectTeamWorld(t *testing.T, values ...string) (w *world, ref, id string) {
	t.Helper()
	ref, id = mustMint(t, projectTeamKind, values...)
	w = newWorld()
	w.orgRows[projectTeamQuery+"|"+ref] = []contextpacket.OrganizationRowReference{orgRowWithKey(projectTeamQuery, ref, "project_team", id, values...)}
	return w, ref, id
}

// A project-team.v2 row is the projector's ownership group. A caller whose
// repository list is "*" is served the row read by its ref; every
// repository-restricted caller stays on the persisted record, decided before
// any read.
func TestProjectTeamV2IsServedToUnrestrictedCallersOnly(t *testing.T) {
	values := []string{"gitlab", "org:gitlab:1", "gl:team", "native"}
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		served    bool
	}{
		{"unrestricted", unrestricted, true},
		{"universal", universal, true},
		{"restricted", restricted, false},
		{"owner wildcard", ownerWide, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, ref, id := projectTeamWorld(t, values...)
			expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), tc.principal, string(projectTeamKind), id)
			if !tc.served {
				if decision.Reason != contextfabric.SourceRowKindOnRecord || len(w.calls) != 0 || decision.Rows != 0 {
					t.Fatalf("restricted: %+v, calls %v", decision, w.calls)
				}
				return
			}
			if decision.Reason != contextfabric.SourceRowServed || decision.Grammar != contextfabric.SourceRowGrammarEncoded || decision.Rows != 1 {
				t.Fatalf("served: %+v, calls %v", decision, w.calls)
			}
			if want := []string{fmt.Sprintf("org_row(%s,%s,%s)", orgID, projectTeamQuery, ref)}; !reflect.DeepEqual(w.calls, want) {
				t.Fatalf("calls = %v, want %v", w.calls, want)
			}
			if expanded.Evidence.EvidenceRefID != ref || expanded.Evidence.Metadata["record"] != "source_row" || expanded.Evidence.Metadata["source_query"] != projectTeamQuery {
				t.Fatalf("expanded = %+v", expanded.Evidence)
			}
			if _, has := expanded.Structured["repository"]; has {
				t.Fatalf("an organization-level row names a repository: %v", expanded.Structured)
			}
		})
	}
}

// A row whose own key columns are not the ref's segments is another group
// (or a spelling twin) and is refused as no_row with the mismatch flagged;
// two rows for one ref are ambiguous; an absent row is no_row.
func TestProjectTeamV2RefusesAnotherGroupsRow(t *testing.T) {
	values := []string{"gitlab", "org:gitlab:1", "gl:team", "native"}
	for index := range values {
		t.Run(fmt.Sprintf("key segment %d", index), func(t *testing.T) {
			w, ref, id := projectTeamWorld(t, values...)
			other := append([]string(nil), values...)
			other[index] += "x"
			w.orgRows[projectTeamQuery+"|"+ref] = []contextpacket.OrganizationRowReference{orgRowWithKey(projectTeamQuery, ref, "project_team", id, other...)}
			_, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(projectTeamKind), id)
			if decision.Reason != contextfabric.SourceRowNoRow || !decision.SubjectMismatch {
				t.Fatalf("%+v", decision)
			}
		})
	}
	t.Run("two rows", func(t *testing.T) {
		w, ref, _ := projectTeamWorld(t, values...)
		w.orgRows[projectTeamQuery+"|"+ref] = append(w.orgRows[projectTeamQuery+"|"+ref], w.orgRows[projectTeamQuery+"|"+ref][0])
		_, id := mustMint(t, projectTeamKind, values...)
		if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(projectTeamKind), id); decision.Reason != contextfabric.SourceRowAmbiguous {
			t.Fatalf("%+v", decision)
		}
	})
	t.Run("absent", func(t *testing.T) {
		w := newWorld()
		_, id := mustMint(t, projectTeamKind, values...)
		if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(projectTeamKind), id); decision.Reason != contextfabric.SourceRowNoRow {
			t.Fatalf("%+v", decision)
		}
	})
	t.Run("malformed provider", func(t *testing.T) {
		w := newWorld()
		_, id := mustMint(t, projectTeamKind, "Git Lab", "p", "t", "native")
		if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(projectTeamKind), id); decision.Reason != contextfabric.SourceRowIDMalformed || len(w.calls) != 0 {
			t.Fatalf("%+v", decision)
		}
	})
}
