package graphrank

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestEveryCommitSiteAfterTheExactStepHonoursTheRefusal(t *testing.T) {
	t.Parallel()
	const term = exactLabelCompletenessTerm
	exact := func() contextfabric.SubjectCandidate {
		c := exactLabelCandidate(contextfabric.SubjectRepository, "repo_payments", term, term)
		c.MatchMechanisms = append(c.MatchMechanisms, contextfabric.MatchVector)
		return c
	}
	rival := repoAliasCandidate("repo_payments_ledger", term)
	neighbour := corroborationCandidate("project_payments_rollout", 0.3, contextfabric.MatchLexical, contextfabric.MatchVector)
	vectorNeighbour := corroborationCandidate("project_payments_rollout", 0.5, contextfabric.MatchLexical, contextfabric.MatchVector)
	pr := corroborationCandidate("pull_request_532", 0.9, contextfabric.MatchLexical)
	pr.Subject.Kind = contextfabric.SubjectPullRequest
	type run struct {
		pool      []contextfabric.SubjectCandidate
		lookup    IdentityLookupState
		truncated bool
		sims      map[string]float64
		census    string
	}
	resolve := func(r run) (contextfabric.SubjectResolution, contextfabric.CommitDecisionDigestSet) {
		identity, terms := identitySideChannels(r.pool...)
		resolution, _, digests := resolveFromMergedCandidatesWithAnchorSlot(
			identityBySubject(r.pool...), map[string]string{}, map[string]bool{}, 10, true, r.truncated,
			r.sims, 0.25, false, 10, 20, true,
			DefaultCommitGatePolicy(), identity, terms, r.lookup, nil, "", r.census, false, false, nil, anchorReservedSlot{}, nil, 1)
		return resolution, digests
	}
	key := func(c contextfabric.SubjectCandidate) string { return SubjectKey(c.Subject) }
	cases := []struct {
		site    string
		control run
		refused []run
		allowed string
	}{
		{site: "exact_index",
			control: run{pool: []contextfabric.SubjectCandidate{exact()}, lookup: IdentityLookupComplete, truncated: true},
			refused: []run{
				{pool: []contextfabric.SubjectCandidate{exact()}, lookup: IdentityLookupIncomplete, truncated: true},
				{pool: []contextfabric.SubjectCandidate{exact(), rival}, lookup: IdentityLookupComplete, truncated: true},
			}},
		{site: "identity_fast_path",
			control: run{pool: []contextfabric.SubjectCandidate{rival}, lookup: IdentityLookupComplete, truncated: true},
			refused: []run{
				{pool: []contextfabric.SubjectCandidate{exact(), rival}, lookup: IdentityLookupComplete, truncated: true},
				{pool: []contextfabric.SubjectCandidate{exact(), rival}, lookup: IdentityLookupIncomplete, truncated: true},
			}},
		{site: "lone_floor",
			control: run{pool: []contextfabric.SubjectCandidate{corroborationCandidate("project_lone", 0.95, contextfabric.MatchLexical)}, lookup: IdentityLookupIncomplete, truncated: false},
			refused: []run{
				{pool: []contextfabric.SubjectCandidate{exact()}, lookup: IdentityLookupIncomplete, truncated: false},
			}},
		{site: "top_of_two",
			control: run{pool: []contextfabric.SubjectCandidate{corroborationCandidate("project_top", 0.99, contextfabric.MatchLexical), neighbour}, lookup: IdentityLookupIncomplete, truncated: false},
			refused: []run{
				{pool: []contextfabric.SubjectCandidate{exact(), neighbour}, lookup: IdentityLookupIncomplete, truncated: false},
				{pool: []contextfabric.SubjectCandidate{exact(), rival, neighbour}, lookup: IdentityLookupComplete, truncated: false},
			}},
		{site: "vector_margin_rescue",
			control: run{pool: []contextfabric.SubjectCandidate{corroborationCandidate("project_other", 0.4, contextfabric.MatchLexical, contextfabric.MatchVector), vectorNeighbour}, lookup: IdentityLookupIncomplete, truncated: true,
				sims: map[string]float64{key(vectorNeighbour): 0.95, SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_other"}): 0.40}},
			refused: []run{
				{pool: []contextfabric.SubjectCandidate{exact(), vectorNeighbour}, lookup: IdentityLookupIncomplete, truncated: true, sims: map[string]float64{key(exact()): 0.40, key(vectorNeighbour): 0.95}},
				{pool: []contextfabric.SubjectCandidate{exact(), rival, vectorNeighbour}, lookup: IdentityLookupComplete, truncated: true, sims: map[string]float64{key(exact()): 0.40, key(rival): 0.38, key(vectorNeighbour): 0.95}},
			}},
		{site: "evidence_census",
			control: run{pool: []contextfabric.SubjectCandidate{corroborationCandidate("project_other", 0.5, contextfabric.MatchLexical), vectorNeighbour}, lookup: IdentityLookupIncomplete, truncated: true, census: key(vectorNeighbour)},
			refused: []run{
				{pool: []contextfabric.SubjectCandidate{exact(), vectorNeighbour}, lookup: IdentityLookupIncomplete, truncated: true, census: key(vectorNeighbour)},
				{pool: []contextfabric.SubjectCandidate{exact(), vectorNeighbour}, lookup: IdentityLookupIncomplete, truncated: true, census: key(exact())},
				{pool: []contextfabric.SubjectCandidate{exact(), rival, vectorNeighbour}, lookup: IdentityLookupComplete, truncated: true, census: key(vectorNeighbour)},
			}},
		{site: "evidence_census of a census kind (the one allowed commit)",
			control: run{pool: []contextfabric.SubjectCandidate{exact(), pr}, lookup: IdentityLookupIncomplete, truncated: true, census: key(pr)},
			allowed: "evidence_census"},
	}
	for _, tc := range cases {
		t.Run(tc.site, func(t *testing.T) {
			control, digests := resolve(tc.control)
			want := tc.site
			if tc.allowed != "" {
				want = tc.allowed
			}
			if len(control.Committed) != 1 || digests.For(control.Committed[0]).CommitGate != want {
				t.Fatalf("control: Committed = %#v gate %q, want one commit through %s (the row must reach the site)", control.Committed, gateOf(control, digests), want)
			}
			for i, refused := range tc.refused {
				resolution, digests := resolve(refused)
				if len(resolution.Committed) != 0 {
					t.Fatalf("refused row %d (lookup %s): Committed = %#v through %q, want nothing after the exact-label refusal", i, refused.lookup, resolution.Committed, gateOf(resolution, digests))
				}
			}
		})
	}
}

func gateOf(resolution contextfabric.SubjectResolution, digests contextfabric.CommitDecisionDigestSet) string {
	if len(resolution.Committed) == 0 {
		return ""
	}
	return digests.For(resolution.Committed[0]).CommitGate
}

func TestEveryCoreCommitGoesThroughCommitAfterExactStep(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "resolution.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var core *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "resolveFromMergedCandidatesWithAnchorSlot" {
			core = fn
		}
	}
	if core == nil {
		t.Fatal("resolveFromMergedCandidatesWithAnchorSlot not found in resolution.go")
	}
	var closure *ast.FuncLit
	ast.Inspect(core.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 {
			if ident, ok := assign.Lhs[0].(*ast.Ident); ok && ident.Name == "commitAfterExactStep" {
				closure, _ = assign.Rhs[0].(*ast.FuncLit)
			}
		}
		return true
	})
	if closure == nil {
		t.Fatal("commitAfterExactStep closure not found")
	}
	inside := func(n ast.Node) bool { return n.Pos() >= closure.Pos() && n.End() <= closure.End() }
	type count struct{ inside, outside int }
	var stateWrites, committedWrites count
	calls := 0
	ast.Inspect(core.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				switch {
				case sel.Sel.Name == "State" && len(node.Rhs) == 1 && isResolutionCommitted(node.Rhs[0]):
					if inside(node) {
						stateWrites.inside++
					} else {
						stateWrites.outside++
					}
				case sel.Sel.Name == "Committed":
					if inside(node) {
						committedWrites.inside++
					} else {
						committedWrites.outside++
					}
				}
			}
		case *ast.CallExpr:
			if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "commitAfterExactStep" {
				calls++
			}
		}
		return true
	})
	// Outside the closure the only write is the pre-commit of caller hints,
	// which runs before the exact-label step and is not after a refusal.
	if stateWrites != (count{inside: 1}) || committedWrites != (count{inside: 1, outside: 1}) {
		t.Fatalf("committed-state writes: State %+v, Committed %+v; want every commit after the exact-label step to go through commitAfterExactStep", stateWrites, committedWrites)
	}
	if calls != 6 {
		t.Fatalf("commitAfterExactStep calls = %d, want 6 (exact_index, identity_fast_path, lone_floor, top_of_two, vector_margin_rescue, evidence_census): a commit site was added or removed, update the refusal table test with it", calls)
	}
}

func isResolutionCommitted(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "ResolutionCommitted"
}
