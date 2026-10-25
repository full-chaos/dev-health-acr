// Package tcheck type-checks one package directory the way emitscan needs it. The scan runs the same code first inside a
// small helper process under CPU, memory and wall-clock limits, so a source shape the checker cannot finish never runs in
// the scanning process.
package tcheck

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
)

// Request is what the helper process is given on its standard input: the scanner's inputs and nothing else.
type Request struct {
	Root       string
	Rels       []string
	Exports    map[string]string
	Sources    map[string][]byte // rel -> the bytes the scanner parsed; the helper reads nothing from disk
	CPUSeconds int
	Mode       string // "" type-checks; "env" reports the names of the environment variables the helper got (a test hook)
}

// Check type-checks the files together against the export data of the module's dependencies. The first type error is
// returned; the info is filled as far as the checker got.
func Check(fset *token.FileSet, files []*ast.File, exports map[string]string) (*types.Info, error) {
	lookup := func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(file)
	}
	var first error
	cfg := &types.Config{
		Importer:    importer.ForCompiler(fset, "gc", lookup),
		FakeImportC: true,
		Error: func(err error) {
			if first == nil {
				first = err
			}
		},
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	_, checkErr := cfg.Check(files[0].Name.Name, fset, files, info)
	if first != nil {
		return nil, first
	}
	if checkErr != nil {
		return nil, checkErr
	}
	return info, nil
}

// Match reports whether the file takes part in the default build context (build tags, file name suffixes). The decision is
// made on src, never on what is on disk now.
func Match(absRoot, rel string, src []byte) (bool, error) {
	dir := filepath.Join(absRoot, filepath.Dir(filepath.FromSlash(rel)))
	name := filepath.Base(rel)
	want := filepath.Join(dir, name)
	ctxt := build.Default
	ctxt.OpenFile = func(path string) (io.ReadCloser, error) {
		if path != want {
			return nil, fmt.Errorf("unexpected read of %q", path)
		}
		return io.NopCloser(bytes.NewReader(src)), nil
	}
	return ctxt.MatchFile(dir, name)
}
