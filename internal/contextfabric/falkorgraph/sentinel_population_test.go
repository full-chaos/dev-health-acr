package falkorgraph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// CHAOS-4874, round-1 finding. The first version of the exhaustiveness guard
// keyed on knownSentinels, and knownSentinels is NOT the population it needed:
// that list exists for safeDependencyError's re-wrap ("every classification
// classifyFalkorError or a caller may ALREADY have attached"), not for "every
// error this package can return". errVectorIndexNotReady is returned straight
// from a projection batch (vector_projection.go) and appears in no list, so the
// guard passed while a real, replayable production failure still classified as
// unclassified. A sweep keyed on the wrong population executes every member and
// proves nothing about the population that mattered.
//
// The population is therefore DERIVED FROM THE SOURCE: every package-level
// error var this package declares, parsed out of the package's own non-test
// files. Each must be either classified in neutralClass or listed below with a
// reason. A new error var forces that decision at compile-of-the-test time
// rather than at the next incident.
//
// declaredSentinels bridges the parsed NAMES to their values, which is the one
// thing an AST walk cannot do. It is a hand-written map -- and the AST walk is
// what stops it from silently going stale, which is the whole point: the walk
// fails if the source declares a name this map does not carry.
var declaredSentinels = map[string]error{
	"ErrNotFound":                    ErrNotFound,
	"ErrUnauthorized":                ErrUnauthorized,
	"ErrRateLimited":                 ErrRateLimited,
	"ErrConstraintViolation":         ErrConstraintViolation,
	"errAlreadyExists":               errAlreadyExists,
	"errIndexNotFound":               errIndexNotFound,
	"errConstraintBootstrapFailed":   errConstraintBootstrapFailed,
	"errConstraintBootstrapTimedOut": errConstraintBootstrapTimedOut,
	"errVectorIndexNotReady":         errVectorIndexNotReady,
	"errAdapterRequiresConn":         errAdapterRequiresConn,
}

// notAProjectionSentinel names every declared error that cannot reach a
// projection tick or the investigation route, with the reason. Membership here
// is a claim that the value never crosses a ProjectionBackend/GraphReader
// boundary -- if that stops being true the entry must move to neutralClass.
var notAProjectionSentinel = map[string]string{
	"errAdapterRequiresConn": "construction-time programming error: New() rejects a config with no conn, before any port method exists to return it",
	// Offline calibration and sweep tooling. These are returned by the
	// exported Calibrate*/Recall*/oracle helpers, which no ProjectionBackend
	// or GraphReader method calls.
	"ErrEmbeddingIdentityMismatch":          "tau calibration tooling",
	"ErrInvalidTargetRecall":                "tau calibration tooling",
	"ErrMarginReportConfigMismatch":         "tau calibration tooling",
	"ErrMarginReportControlsCountMismatch":  "tau calibration tooling",
	"ErrMarginReportInternallyInconsistent": "tau calibration tooling",
	"ErrMarginReportScoredCountMismatch":    "tau calibration tooling",
	"ErrNoCorrectSimilaritySamples":         "tau calibration tooling",
	"ErrNoFeasibleFloor":                    "tau calibration tooling",
	"ErrNoMarginSamples":                    "tau calibration tooling",
	"errOracleCorpusSizeMismatch":           "offline oracle fixture builder",
	"errOracleEmbedderRequired":             "offline oracle fixture builder",
	"errReferenceTieUnbounded":              "offline HNSW recall sweep",
}

func TestEveryDeclaredSentinelIsClassifiedOrExcused(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["falkorgraph"]
	if !ok {
		t.Fatalf("package falkorgraph not found in parsed dirs %v", keysOf(pkgs))
	}

	declared := map[string]string{} // name -> file
	for path, file := range pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i < len(value.Values) && isErrorConstructor(value.Values[i]) {
						declared[name.Name] = path
					}
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("the AST walk found no error vars at all; it is not walking what it thinks it is")
	}

	classified, excused := 0, 0
	for name, file := range declared {
		if reason, exempt := notAProjectionSentinel[name]; exempt {
			if reason == "" {
				t.Fatalf("%s (%s) is excused with no reason", name, file)
			}
			excused++
			continue
		}
		value, known := declaredSentinels[name]
		if !known {
			t.Fatalf("%s (%s) is a package-level error this test has never seen. Add it to "+
				"declaredSentinels and then either give it a neutralClass entry or excuse it in "+
				"notAProjectionSentinel with a reason. An unclassified sentinel on a projection "+
				"path logs failure_class=unclassified, which is the defect this guard exists to stop", name, file)
		}
		if _, decided := neutralClass[value]; !decided {
			t.Fatalf("%s (%s) has no neutralClass decision", name, file)
		}
		classified++
	}
	if classified == 0 {
		t.Fatal("no declared sentinel was checked against neutralClass; the guard is vacuous")
	}
	if excused == 0 {
		t.Fatal("nothing was excused; the exemption path is unexercised and may be dead")
	}
	// Anti-staleness in the other direction: an entry in either map that the
	// source no longer declares is a lie the next reader will trust.
	for name := range declaredSentinels {
		if _, still := declared[name]; !still {
			t.Fatalf("declaredSentinels lists %s but the package no longer declares it", name)
		}
	}
	for name := range notAProjectionSentinel {
		if _, still := declared[name]; !still {
			t.Fatalf("notAProjectionSentinel excuses %s but the package no longer declares it", name)
		}
	}
	t.Logf("assertion reach: %d classified, %d excused, %d declared in source", classified, excused, len(declared))
}

func isErrorConstructor(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return (pkg.Name == "errors" && sel.Sel.Name == "New") || (pkg.Name == "fmt" && sel.Sel.Name == "Errorf")
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
