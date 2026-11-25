package api

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The 503 upstream_unavailable with retryable=false is the signal the web reads
// as "the organization is above its access limit" (docs/authentication.md, the
// principal access refusal). A deployment with no model provider is also a 503
// upstream_unavailable, and it must stay retryable so the two never read alike.
func TestAnUnwiredModelRuntimeStaysRetryableBecauseTheNotRetryable503IsTheAccessLimitSignal(t *testing.T) {
	cases := []struct {
		name string
		fail func(context.Context, storage.Principal) error
	}{
		{"interpreter without a runtime", func(ctx context.Context, principal storage.Principal) error {
			_, _, err := contextfabric.RuntimeQuestionInterpreter{}.Interpret(ctx, principal, contextfabric.InvestigationRequest{})
			return err
		}},
		{"synthesizer without a runtime", func(ctx context.Context, principal storage.Principal) error {
			_, err := contextfabric.RuntimeAnswerSynthesizer{}.Synthesize(ctx, principal, contextfabric.SynthesisInput{})
			return err
		}},
		{"a model call that failed in flight", func(context.Context, storage.Principal) error {
			return contextfabric.ErrModelUnavailable
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token := newContextFabricTestApp(t, investigatorFunc(func(ctx context.Context, principal storage.Principal, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, testCase.fail(ctx, principal)
			}))
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 body=%s", response.Code, response.Body.String())
			}
			var body contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v (body=%s)", err, response.Body.String())
			}
			if body.Error.Code != "upstream_unavailable" || !body.Error.Retryable {
				t.Fatalf("code = %q retryable = %v, want upstream_unavailable and retryable", body.Error.Code, body.Error.Retryable)
			}
		})
	}
}

// notRetryableUnavailableSites returns "file:function" for every call in src
// that can write a 503 with the code upstream_unavailable and a retryable
// argument that is not the literal true.
func notRetryableUnavailableSites(t *testing.T, name string, src string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var sites []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			var status, code, retryTrue bool
			for _, argument := range call.Args {
				switch value := argument.(type) {
				case *ast.SelectorExpr:
					status = status || value.Sel.Name == "StatusServiceUnavailable"
				case *ast.BasicLit:
					status = status || (value.Kind == token.INT && value.Value == "503")
					code = code || (value.Kind == token.STRING && value.Value == `"upstream_unavailable"`)
				case *ast.Ident:
					retryTrue = retryTrue || value.Name == "true"
				}
			}
			if status && code && !retryTrue {
				sites = append(sites, name+":"+function.Name.Name)
			}
			return true
		})
	}
	return sites
}

func TestTheNotRetryable503ScanSeesAPlantedSecondProducer(t *testing.T) {
	const planted = `package p
func a() { writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "m", false, nil) }
func b() { writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "m", retry, nil) }
func c() { writeError(w, r, 503, "upstream_unavailable", "m", false, nil) }
func d() { writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "m", true, nil) }
func e() { writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "m", false, nil) }
func f() { writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "m", false, nil) }
`
	got := notRetryableUnavailableSites(t, "planted.go", planted)
	want := []string{"planted.go:a", "planted.go:b", "planted.go:c"}
	if !slices.Equal(got, want) {
		t.Fatalf("sites = %v, want %v", got, want)
	}
}

// Only the access-limit refusal may write 503 + upstream_unavailable with a
// retryable that is not true. A second producer would read as an access limit
// to every client that keys on the triple.
func TestOnlyTheAccessLimitRefusalWritesTheNotRetryable503(t *testing.T) {
	root := repopath.Root(t)
	var got []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		got = append(got, notRetryableUnavailableSites(t, filepath.ToSlash(relative), string(source))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"internal/api/principal_access.go:refuseOverLimitAccess"}
	if !slices.Equal(got, want) {
		t.Fatalf("producers of 503 upstream_unavailable that may be not retryable = %v, want only %v", got, want)
	}
}
