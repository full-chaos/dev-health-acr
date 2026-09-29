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
//	go run ./cmd/operationpolicy          # write both artifact copies
//	go run ./cmd/operationpolicy -check   # fail if either differs from a fresh generation
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	out, err := generate(in)
	if err != nil {
		return err
	}
	for _, rel := range []string{artifactPath, embeddedCopyPath} {
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
