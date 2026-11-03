package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The scoped investment entries investmentBreakdownTeam and
// investmentBreakdownRepository (CHAOS-12691) are not in the vendored ops
// catalogue yet: ops registers their documents in CHAOS-12698
// (full-chaos/dev-health-ops#5574, head 14f54b41), and the vendored copy must
// name a merged ops commit. Until it does, they are generated against a
// test-local copy of the ops catalogue at 14f54b41: the registrydump output
// and the SDL of that commit, verbatim, under
// internal/contextfabric/directread/testdata/ops_14f54b41. The generated
// artifact there is what the directread edge and graphql_query tests load.
const (
	scopedFixtureCommit   = "14f54b4184e1ec9e44883921ef2eb26bb83c3faf"
	scopedFixtureDir      = repoRoot + "/internal/contextfabric/directread/testdata/ops_14f54b41"
	scopedFixtureArtifact = scopedFixtureDir + "/operations.v1.json"
	// writeScopedFixtureEnv=1 rewrites the fixture artifact.
	writeScopedFixtureEnv = "ACR_WRITE_SCOPED_INVESTMENT_FIXTURE"
)

func scopedFixtureInputs(t *testing.T) inputs {
	t.Helper()
	in, err := readInputsAt(filepath.Join(scopedFixtureDir, "registry.v1.json"), filepath.Join(scopedFixtureDir, "schema.graphql"), declaredPolicyFor(true))
	if err != nil {
		t.Fatalf("read the ops %s fixture: %v", scopedFixtureCommit[:8], err)
	}
	if in.Registry.Source.Commit != scopedFixtureCommit {
		t.Fatalf("fixture registry names ops %s, want %s", in.Registry.Source.Commit, scopedFixtureCommit)
	}
	return in
}

func decodeArtifact(t *testing.T, artifact []byte) map[string]directread.OperationPolicy {
	t.Helper()
	var file directread.CatalogueFile
	if err := json.Unmarshal(artifact, &file); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	out := map[string]directread.OperationPolicy{}
	for _, op := range file.Operations {
		out[op.Name] = op
	}
	return out
}

// The fixture artifact equals a fresh generation from the fixture catalogue
// and the declaration with the scoped entries, and the graphql_query root
// policy derives over it.
func TestScopedInvestmentFixtureIsFresh(t *testing.T) {
	in := scopedFixtureInputs(t)
	artifact, err := generate(in)
	if err != nil {
		t.Fatalf("generate over the ops %s fixture: %v", scopedFixtureCommit[:8], err)
	}
	cat, err := directread.LoadCatalogue(artifact)
	if err != nil {
		t.Fatalf("fixture artifact does not load: %v", err)
	}
	if _, err := directread.NewGraphQLPolicy(cat, in.SDL, directread.DefaultGraphQLLimits()); err != nil {
		t.Fatalf("graphql_query root policy over the fixture: %v", err)
	}
	for _, name := range []string{"investmentBreakdownTeam", "investmentBreakdownRepository"} {
		if _, refusal := cat.Lookup(name); refusal != nil {
			t.Fatalf("fixture artifact does not serve %s: %v", name, refusal)
		}
	}
	if os.Getenv(writeScopedFixtureEnv) == "1" {
		if err := os.WriteFile(scopedFixtureArtifact, artifact, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(scopedFixtureArtifact)
	if err != nil {
		t.Fatalf("read %s: %v", scopedFixtureArtifact, err)
	}
	if !bytes.Equal(committed, artifact) {
		t.Fatalf("%s is stale: run %s=1 go test ./cmd/operationpolicy -run TestScopedInvestmentFixtureIsFresh", scopedFixtureArtifact, writeScopedFixtureEnv)
	}
}

// The vendored catalogue does not carry the scoped rows yet, so the committed
// artifact serves neither entry and keeps the ORG entries' old reasons.
func TestVendoredCatalogueDoesNotServeTheScopedEntriesYet(t *testing.T) {
	if opsServesScopedInvestment {
		t.Skip("the vendored catalogue serves the scoped entries")
	}
	in := vendoredInputs(t)
	for _, row := range in.Registry.Rows {
		if _, scoped := scopedInvestmentEntries()[row.Operation]; scoped {
			t.Fatalf("the vendored registry carries %s: set opsServesScopedInvestment and regenerate", row.Operation)
		}
	}
	committed, err := os.ReadFile(filepath.Join(repoRoot, artifactPath))
	if err != nil {
		t.Fatal(err)
	}
	ops := decodeArtifact(t, committed)
	for name := range scopedInvestmentEntries() {
		if _, ok := ops[name]; ok {
			t.Fatalf("committed artifact serves %s", name)
		}
	}
}

// Each scoped entry is the investmentBreakdown entry of the same catalogue
// except: its name and document, the scope level and ids rules, the scope
// constraints and the unrestricted basis. The restricted caller keeps the
// K14-A refusal, word for word.
func TestScopedInvestmentEntriesDifferFromInvestmentBreakdownOnlyInScope(t *testing.T) {
	in := scopedFixtureInputs(t)
	artifact, err := generate(in)
	if err != nil {
		t.Fatal(err)
	}
	ops := decodeArtifact(t, artifact)
	org := ops["investmentBreakdown"]
	for _, tc := range []struct {
		name, document, level, kind string
	}{
		{"investmentBreakdownTeam", "InvestmentBreakdownTeam", "TEAM", directread.SubjectKindTeam},
		{"investmentBreakdownRepository", "InvestmentBreakdownRepository", "REPO", directread.SubjectKindRepository},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op, ok := ops[tc.name]
			if !ok {
				t.Fatalf("not generated")
			}
			if op.DocumentOperationName != tc.document {
				t.Fatalf("document %s, want %s", op.DocumentOperationName, tc.document)
			}
			// The document is the InvestmentBreakdown body under its own name.
			if got, want := strings.Replace(op.DocumentText, tc.document, "X", 1), strings.Replace(org.DocumentText, "InvestmentBreakdown", "X", 1); got != want {
				t.Fatalf("document body differs from InvestmentBreakdown's")
			}
			level := ruleAt(t, op, "batch.filters.scope.level")
			if !slices.Equal(level.AllowedValues, []string{tc.level}) {
				t.Fatalf("scope.level allows %v, want exactly [%s]", level.AllowedValues, tc.level)
			}
			ids := ruleAt(t, op, "batch.filters.scope.ids")
			if ids.MinItems != 1 || ids.MaxItems != 1 || ids.Subject == nil || ids.Subject.Kind != tc.kind || ids.Source != directread.SourceClient {
				t.Fatalf("scope.ids rule %+v, want exactly one client id of kind %s", ids, tc.kind)
			}
			var levelConstraint *directread.Constraint
			for i, con := range op.Constraints {
				if con.Other == "batch.filters.scope.level" {
					levelConstraint = &op.Constraints[i]
				}
				if con.Path == "batch.filters.scope.ids" || con.Path == "batch.filters.scope" {
					t.Fatalf("scoped entry keeps the ORG scope constraint %+v", con)
				}
			}
			if levelConstraint == nil || levelConstraint.Kind != directread.ConstraintRequiresValue || levelConstraint.Path != "" ||
				levelConstraint.OtherDefault != "" || !slices.Equal(levelConstraint.Values, []string{tc.level}) {
				t.Fatalf("level constraint %+v, want an unconditional requires_value %s with no default", levelConstraint, tc.level)
			}
			if op.Scope(directread.CallerUnrestricted).Basis == org.Scope(directread.CallerUnrestricted).Basis {
				t.Fatalf("unrestricted basis is the ORG entry's")
			}

			// Everything else equals the ORG entry.
			normalized := op
			normalized.Name, normalized.DocumentOperationName = org.Name, org.DocumentOperationName
			normalized.Digest, normalized.DocumentText = org.Digest, org.DocumentText
			normalized.Constraints = org.Constraints
			normalized.Variables = slices.Clone(op.Variables)
			for i, v := range normalized.Variables {
				if v.Path == "batch.filters.scope.level" || v.Path == "batch.filters.scope.ids" {
					normalized.Variables[i] = ruleAt(t, org, v.Path)
				}
				// An unlisted path's reason names the document.
				if v.Refusal != nil && v.Refusal.Reason == fmt.Sprintf(unlistedReasonFmt, tc.document) {
					refusal := *v.Refusal
					refusal.Reason = fmt.Sprintf(unlistedReasonFmt, org.DocumentOperationName)
					normalized.Variables[i].Refusal = &refusal
				}
			}
			normalized.Scopes = slices.Clone(op.Scopes)
			for i := range normalized.Scopes {
				if normalized.Scopes[i].Caller == directread.CallerUnrestricted {
					normalized.Scopes[i].Basis = org.Scope(directread.CallerUnrestricted).Basis
				}
			}
			got, _ := json.Marshal(normalized)
			want, _ := json.Marshal(org)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs from investmentBreakdown outside the scope rules:\n got %s\nwant %s", tc.name, got, want)
			}
		})
	}
}

// The ORG entries change only the TEAM and REPO scope-level refusal reasons
// when the scoped entries are served: the generated entries are byte-equal
// once those two reasons are put back.
func TestOrgInvestmentEntriesChangeOnlyTheLevelReasons(t *testing.T) {
	scoped := scopedFixtureInputs(t)
	legacy := cloneInputs(scoped)
	before := declaredPolicyFor(false)
	for _, name := range []string{"investmentBreakdown", "investmentFull"} {
		legacy.Policy.Served[name] = before.Served[name]
	}
	scopedArtifact, err := generate(scoped)
	if err != nil {
		t.Fatal(err)
	}
	legacyArtifact, err := generate(legacy)
	if err != nil {
		t.Fatal(err)
	}
	now, was := decodeArtifact(t, scopedArtifact), decodeArtifact(t, legacyArtifact)
	for _, name := range []string{"investmentBreakdown", "investmentFull"} {
		op := now[name]
		level := ruleAt(t, op, "batch.filters.scope.level")
		named := 0
		for i, rv := range level.RefusedValues {
			want, ok := scopedLevelReasons[rv.Value]
			if !ok {
				continue
			}
			if rv.Reason != want || rv.Code != directread.RefusalBasisDependentShape {
				t.Fatalf("%s level %s: %s %q", name, rv.Value, rv.Code, rv.Reason)
			}
			level.RefusedValues[i].Reason = legacyLevelReasons[rv.Value]
			named++
		}
		if named != 2 || !strings.Contains(scopedLevelReasons["TEAM"], "investmentBreakdownTeam") || !strings.Contains(scopedLevelReasons["REPO"], "investmentBreakdownRepository") {
			t.Fatalf("%s: %d level reasons name the scoped entries", name, named)
		}
		for i, v := range op.Variables {
			if v.Path == level.Path {
				op.Variables[i] = level
			}
		}
		got, _ := json.Marshal(op)
		want, _ := json.Marshal(was[name])
		if !bytes.Equal(got, want) {
			t.Fatalf("%s changes beyond the TEAM and REPO level reasons:\n got %s\nwant %s", name, got, want)
		}
	}
}

func ruleAt(t *testing.T, op directread.OperationPolicy, path string) directread.VariableRule {
	t.Helper()
	for _, v := range op.Variables {
		if v.Path == path {
			v.AllowedValues = slices.Clone(v.AllowedValues)
			v.RefusedValues = slices.Clone(v.RefusedValues)
			return v
		}
	}
	t.Fatalf("%s has no variable %s", op.Name, path)
	return directread.VariableRule{}
}

// The scoped entries pass the SDL checks the served entries pass: their
// level allowlist drops the SDL default ORG, which is safe only because an
// omitted level is refused (refusedWhenOmitted); a plant that drops that
// constraint or gives it the default ORG is a violation.
func TestScopedInvestmentEntriesAgreeWithTheSDL(t *testing.T) {
	in := scopedFixtureInputs(t)
	f := loadSDLFixture(t, in)
	limits, checked := limitViolations(f)
	for _, v := range limits {
		t.Error(v)
	}
	requirements, _ := requirementViolations(f, deliberateAcrRequirements)
	for _, v := range requirements {
		t.Error(v)
	}
	if checked < 20 {
		t.Fatalf("only %d limited variables checked", checked)
	}
	for _, plant := range []struct {
		label string
		edit  func(cons []directread.Constraint) []directread.Constraint
	}{
		{"constraint dropped", func(cons []directread.Constraint) []directread.Constraint {
			return slices.DeleteFunc(cons, func(c directread.Constraint) bool { return c.Other == "batch.filters.scope.level" })
		}},
		{"constraint defaults to ORG", func(cons []directread.Constraint) []directread.Constraint {
			for i := range cons {
				if cons[i].Other == "batch.filters.scope.level" {
					cons[i].OtherDefault, cons[i].Values = "ORG", append(cons[i].Values, "ORG")
				}
			}
			return cons
		}},
	} {
		t.Run(plant.label, func(t *testing.T) {
			planted := cloneInputs(in)
			decl := planted.Policy.Served["investmentBreakdownTeam"]
			decl.Constraints = plant.edit(slices.Clone(decl.Constraints))
			planted.Policy.Served["investmentBreakdownTeam"] = decl
			got, _ := limitViolations(loadSDLFixture(t, planted))
			if !slices.ContainsFunc(got, func(v string) bool {
				return strings.Contains(v, "investmentBreakdownTeam batch.filters.scope.level: the SDL default ORG is not an allowed value")
			}) {
				t.Fatalf("no violation for the planted level constraint: %v", got)
			}
		})
	}
}
