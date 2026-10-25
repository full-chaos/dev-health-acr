// Command child is the helper process of emitscan's type-check: it reads one tcheck.Request from standard input, checks the
// package and, only if it finishes, prints the acknowledgement line for exactly that request.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan/tcheck"
)

func main() { os.Exit(run()) }

func run() int {
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
	if err != nil {
		return 2
	}
	var req tcheck.Request
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&req); err != nil {
		return 2
	}
	sum := sha256.Sum256(payload)
	ack := "emitscan-tcheck-ok " + hex.EncodeToString(sum[:]) + "\n"
	if req.Mode == "env" {
		var names []string
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Print("env " + strings.Join(names, ",") + "\n")
		return 0
	}
	tcheck.LimitCPU(req.CPUSeconds)
	absRoot, err := filepath.Abs(req.Root)
	if err != nil {
		return 2
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, rel := range req.Rels {
		source, ok := req.Sources[rel]
		if !ok {
			return 2
		}
		file, err := parser.ParseFile(fset, rel, source, 0)
		if err != nil {
			return 2
		}
		match, err := tcheck.Match(absRoot, rel, source)
		if err != nil {
			return 2
		}
		if match {
			files = append(files, file)
		}
	}
	if len(files) > 0 {
		_, _ = tcheck.Check(fset, files, req.Exports)
	}
	fmt.Print(ack)
	return 0
}
