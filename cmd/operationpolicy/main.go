// Command operationpolicy generates contracts/mcp/operations.v1.json, the
// run_operation policy artifact of CHAOS-7036 (design section D.5, slice
// S1a), and its embedded copy under internal/contextfabric/directread.
//
// Inputs, all vendored in this repository (no network, no ops checkout):
//
//   - contracts/mcp/ops-catalogue/registry.v1.json: the ops registered
//     documents as ops `go run ./cmd/registrydump -file
//     internal/queryapi/server/query_route.go` printed them, verbatim, with
//     the source ops commit;
//   - contracts/mcp/ops-catalogue/schema.graphql: the ops SDL at that commit;
//   - the hand-authored policy declaration in policy.go.
//
// Every document and the SDL are PARSED (github.com/vektah/gqlparser/v2).
// Generation fails on: a registry row that is not classified exactly once;
// a digest that does not recompute; a served document that is not exactly
// one query operation with the declared name, or does not validate against
// the SDL; a person-scoped variable on an allowlist; a person-named or free
// JSON output path without a written exception; a declaration that names a
// path the document does not have.
//
// Usage:
//
//	go run ./cmd/operationpolicy          # write every generated file
//	go run ./cmd/operationpolicy -check   # fail if any differs from a fresh generation
//
// It also writes the embedded SDL copy graphql_query validates against
// (internal/contextfabric/directread/ops_schema.graphql) and the graphql_query
// root allowlist (contracts/mcp/graphql_roots.v1.json), derived from the
// generated artifact by directread.NewGraphQLPolicy (CHAOS-7075).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail if the committed artifacts differ from a fresh generation")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintf(os.Stderr, "operationpolicy: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	in, err := readInputs(root)
	if err != nil {
		return err
	}
	outputs, err := generateAll(in)
	if err != nil {
		return err
	}
	for _, rel := range outputPaths {
		out := outputs[rel]
		path := filepath.Join(root, rel)
		if check {
			committed, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", rel, err)
			}
			if !bytes.Equal(committed, out) {
				return fmt.Errorf("%s is stale: run go run ./cmd/operationpolicy", rel)
			}
			continue
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

// outputPaths are every file generation writes, in write order.
var outputPaths = []string{artifactPath, embeddedCopyPath, embeddedSchemaPath, graphqlRootsPath}

// generateAll builds every output: the operation policy artifact and its
// embedded copy, the embedded SDL copy graphql_query validates against, and
// the graphql_query root allowlist derived from the SAME artifact (CHAOS-7075,
// one fact).
func generateAll(in inputs) (map[string][]byte, error) {
	artifact, err := generate(in)
	if err != nil {
		return nil, err
	}
	cat, err := directread.LoadCatalogue(artifact)
	if err != nil {
		return nil, fmt.Errorf("generated artifact does not load: %w", err)
	}
	policy, err := directread.NewGraphQLPolicy(cat, in.SDL, directread.DefaultGraphQLLimits())
	if err != nil {
		return nil, fmt.Errorf("derive the graphql_query root policy: %w", err)
	}
	roots, err := policy.RootsFileJSON(generatorName)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", graphqlRootsPath, err)
	}
	return map[string][]byte{
		artifactPath:       artifact,
		embeddedCopyPath:   artifact,
		embeddedSchemaPath: bytes.Clone(in.SDL),
		graphqlRootsPath:   roots,
	}, nil
}

func readInputs(root string) (inputs, error) {
	registryBytes, err := os.ReadFile(filepath.Join(root, registryPath))
	if err != nil {
		return inputs{}, fmt.Errorf("read registry: %w", err)
	}
	var registry registryFile
	dec := json.NewDecoder(bytes.NewReader(registryBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&registry); err != nil {
		return inputs{}, fmt.Errorf("decode registry: %w", err)
	}
	if len(registry.Rows) == 0 || registry.Source.Commit == "" {
		return inputs{}, errors.New("registry has no rows or no source commit")
	}
	sdl, err := os.ReadFile(filepath.Join(root, schemaPath))
	if err != nil {
		return inputs{}, fmt.Errorf("read schema: %w", err)
	}
	return inputs{Registry: registry, RegistryBytes: registryBytes, SDL: sdl, Policy: declaredPolicy()}, nil
}
