package directread

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func chaos13563Slug(i int) string { return fmt.Sprintf("acme/r%04d", i) }

func chaos13563ID(i int) string {
	return fmt.Sprintf("repository:%08d-0000-4000-8000-000000000000", i)
}

func TestCHAOS13563GrantBoundIs256(t *testing.T) {
	if MaxGrantedRepositories != 256 {
		t.Fatalf("MaxGrantedRepositories = %d, want 256", MaxGrantedRepositories)
	}
}

// Domain cells of the grant listing: 0, 1, 255, 256 complete; 257 incomplete;
// a duplicated scope slug on a full grant adds no repository.
func TestCHAOS13563GrantListingBoundaryCells(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nodes      int
		dupScopes  int
		wantRefs   int
		wantCapHit bool
	}{
		{"zero", 0, 0, 0, false},
		{"one", 1, 0, 1, false},
		{"255", 255, 0, 255, false},
		{"256", 256, 0, 256, false},
		{"257", 257, 0, 0, true},
		{"256 plus one duplicate scope", 256, 1, 256, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scopes := make([]string, 0, tc.nodes+tc.dupScopes)
			for i := 0; i < tc.nodes; i++ {
				scopes = append(scopes, chaos13563Slug(i))
			}
			for i := 0; i < tc.dupScopes; i++ {
				scopes = append(scopes, chaos13563Slug(0))
			}
			graph := grantGraph(tc.nodes, chaos13563Slug)
			refs, err := NewGrantedRepositories(newLookup(graph, nil)).GrantedRepositories(context.Background(), lookupPrincipal(orgA, scopes...))
			if tc.wantCapHit {
				if !errors.Is(err, ErrGrantedRepositoriesIncomplete) || refs != nil {
					t.Fatalf("refs %d err %v, want ErrGrantedRepositoriesIncomplete", len(refs), err)
				}
				return
			}
			if err != nil || len(refs) != tc.wantRefs {
				t.Fatalf("refs %d err %v, want %d refs", len(refs), err, tc.wantRefs)
			}
			seen := map[string]bool{}
			for _, ref := range refs {
				if seen[ref.CanonicalID] {
					t.Fatalf("duplicate %s in the grant", ref.CanonicalID)
				}
				seen[ref.CanonicalID] = true
			}
		})
	}
}

// Every repository id list of the policy but the one-id scope lists (the client lists and the forced grant
// list) takes 256 items and refuses 257 with variable_out_of_range.
func TestCHAOS13563RepositoryListPolicyBoundaryCells(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, op := range cat.Operations(CallerUnrestricted) {
		for _, rule := range op.Variables {
			if rule.Subject == nil || rule.Subject.Kind != SubjectKindRepository || !isListType(rule.Type) || rule.MaxItems == 1 {
				continue
			}
			checked++
			if rule.MaxItems != 256 {
				t.Errorf("%s %s max_items = %d, want 256", op.Name, rule.Path, rule.MaxItems)
			}
			ids := func(n int, dupLast bool) []any {
				out := make([]any, n)
				for i := range out {
					out[i] = chaos13563ID(i)
				}
				if dupLast && n > 1 {
					out[n-1] = out[0]
				}
				return out
			}
			for _, cell := range []struct {
				name   string
				items  []any
				refuse bool
			}{
				{"1", ids(1, false), false},
				{"255", ids(255, false), false},
				{"256", ids(256, false), false},
				{"257", ids(257, false), true},
				{"257 with the last a duplicate", ids(257, true), true},
				{"256 with the last a duplicate", ids(256, true), false},
			} {
				_, _, refusals := checkVariables(op, nestedVars(rule.Path, cell.items))
				got := false
				for _, r := range refusals {
					if r.path == rule.Path && r.code == RefusalVariableOutOfRange {
						got = true
					}
				}
				if got != cell.refuse {
					t.Errorf("%s %s with %s items: refused=%t, want %t (%+v)", op.Name, rule.Path, cell.name, got, cell.refuse, refusals)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no repository id list found in the policy")
	}
}

func nestedVars(path string, value any) map[string]any {
	segs := parsePath(path)
	root := map[string]any{}
	cur := root
	for i, seg := range segs {
		if i == len(segs)-1 {
			cur[seg.name] = value
			break
		}
		next := map[string]any{}
		cur[seg.name] = next
		cur = next
	}
	return root
}
