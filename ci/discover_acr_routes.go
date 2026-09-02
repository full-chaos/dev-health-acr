// Command discover_acr_routes is the CHAOS-3273 Wave 0 source-discovery
// script for dev-health-acr: every REST route registered on (*App).Handler's
// http.ServeMux.
//
// Modelled on dev-health-ops's ci/discover_ops_routes.py (lane auth-cp/L1):
// independent re-derivation from source (never trusts
// contracts/auth/v1/endpoint-profiles.acr.json itself), a Route-shaped
// record per mux.Handle/mux.HandleFunc registration, and a JSON report a
// later CI gate can diff the checked-in profile file against.
//
// Go 1.22's http.ServeMux pattern syntax is "METHOD PATTERN" (or bare
// PATTERN for any method) -- this script parses exactly that shape from
// each `mux.Handle(...)`/`mux.HandleFunc(...)` call in internal/api/app.go,
// resolving a `"literal " + PackageConstantName` pattern by looking up
// `const ConstantName = "literal"` across internal/api/*.go (the one
// resolvable dynamic shape used in this repo, e.g.
// ContextFabricInvestigationsPath). A pattern this script cannot fully
// resolve to a string literal is reported under `unresolved`, never
// silently dropped -- matching ops's discovery script's convention for its
// own one documented dynamic-include limitation.
//
// Usage:
//
//	go run ci/discover_acr_routes.go [-root PATH] [-out PATH]
//
// Prints a JSON report to stdout (or -out) with:
//
//	{"routes": [...], "unresolved": [...], "counts": {"routes": N}}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// deployedService is the service every route this script discovers belongs
// to. It is a CONSTANT rather than a parsed value on purpose, and the
// reasoning is load-bearing: discoverRoutes walks exactly ONE file,
// internal/api/app.go, which is the mux of the dev-health-acr-api
// deployment. A route found there cannot belong to any other service.
//
// This is what makes the checker's SERVICE MISMATCH comparison meaningful.
// The endpoint-profile schema models profiles per deployed APP -- its
// service enum also carries dev-health-acr-mcp, a separately deployed acr
// service whose surfaces this script does not walk at all -- so a row
// claiming a service other than this one for a surface registered on THIS
// mux is making a false claim about which middleware stack its security
// analysis applies to.
//
// When a second acr app is profiled it gets its OWN walk emitting its own
// service, never a widening of this constant: a constant that covers two
// apps would re-open the hole it exists to close.
const deployedService = "dev-health-acr-api"

type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	// Service is the deployed app whose mux this route was found on.
	// See deployedService.
	Service string `json:"service"`
}

type Report struct {
	Routes     []Route  `json:"routes"`
	Unresolved []string `json:"unresolved"`
	Counts     struct {
		Routes int `json:"routes"`
	} `json:"counts"`
}

// muxHandleRE matches `mux.Handle(` or `mux.HandleFunc(` and captures the
// FIRST argument, up to the first comma. The first argument is always a Go
// string-literal-concatenation expression in this repo (verified by
// reading internal/api/app.go) and never itself contains a comma, so
// splitting on the first "," is safe -- a bare identifier there would fail
// resolvePatternExpr and land in `unresolved` rather than being guessed.
var muxHandleRE = regexp.MustCompile(`mux\.(?:Handle|HandleFunc)\(\s*([^,]+?)\s*,`)

// muxCallStartRE matches the START of a mux.Handle/mux.HandleFunc call
// regardless of whether its arguments fit on the line. It exists to catch
// what muxHandleRE misses: a registration whose pattern argument is not on
// the same physical line still gets REPORTED as unresolved rather than
// silently dropped.
var muxCallStartRE = regexp.MustCompile(`mux\.(?:Handle|HandleFunc)\(`)

// stringLiteralRE matches a single double-quoted Go string literal.
var stringLiteralRE = regexp.MustCompile(`"([^"]*)"`)

// identifierRE matches a bare Go identifier (used to resolve
// `"..." + SomeConstant` concatenations against package-level consts).
var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// constDeclRE matches `const Name = "value"` (single-line const decls;
// every resolvable pattern constant in this repo is declared this way --
// verified by reading internal/api/context_fabric_*.go).
var constDeclRE = regexp.MustCompile(`^const\s+(\w+)\s*=\s*"([^"]*)"`)

func main() {
	root := flag.String("root", ".", "repo root")
	out := flag.String("out", "", "output file (default: stdout)")
	flag.Parse()

	apiDir := filepath.Join(*root, "internal", "api")
	consts, err := collectStringConstants(apiDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discover_acr_routes:", err)
		os.Exit(1)
	}

	appGo := filepath.Join(apiDir, "app.go")
	routes, unresolved, err := discoverRoutes(appGo, consts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discover_acr_routes:", err)
		os.Exit(1)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})

	report := Report{Routes: routes, Unresolved: unresolved}
	report.Counts.Routes = len(routes)

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "discover_acr_routes:", err)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')

	if *out == "" {
		os.Stdout.Write(encoded)
		return
	}
	if err := os.WriteFile(*out, encoded, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "discover_acr_routes:", err)
		os.Exit(1)
	}
}

// collectStringConstants scans every .go file directly under dir for
// single-line `const Name = "value"` declarations.
func collectStringConstants(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	consts := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if m := constDeclRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				consts[m[1]] = m[2]
			}
		}
	}
	return consts, nil
}

// resolvePatternExpr resolves a Go expression like `"GET /healthz"` or
// `"POST " + ContextFabricInvestigationsPath` to its literal string value.
// Returns ok=false (never a guess) if any operand cannot be resolved.
func resolvePatternExpr(expr string, consts map[string]string) (string, bool) {
	parts := strings.Split(expr, "+")
	var sb strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if lit := stringLiteralRE.FindStringSubmatch(part); lit != nil && stringLiteralRE.FindString(part) == part {
			sb.WriteString(lit[1])
			continue
		}
		if identifierRE.MatchString(part) {
			if value, ok := consts[part]; ok {
				sb.WriteString(value)
				continue
			}
		}
		return "", false
	}
	return sb.String(), true
}

func discoverRoutes(file string, consts map[string]string) ([]Route, []string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(string(raw), "\n")
	var routes []Route
	var unresolved []string
	for i, line := range lines {
		// Count the registration CALLS on this line, then parse as many as
		// the pattern regex can resolve, and reconcile the two. Anything the
		// count says is there but the parse did not produce is REPORTED.
		//
		// Two merge-gate rounds landed on this loop, and the pair is worth
		// keeping together because it is one class approached twice:
		//   - round 2: a registration WRAPPED across lines matched nothing
		//     and hit `continue` -- invisible, not unresolved.
		//   - round 3: two registrations on ONE line matched once, because
		//     this used FindStringSubmatch. The second was invisible too,
		//     and round 2's fix did not help, since the line matches.
		// The lesson is in the shape of the second miss: round 2 keyed the
		// fix to "the line parsed to nothing" -- the shape of the reported
		// symptom -- rather than to the class, "a registration in this file
		// is not accounted for". A count-and-reconcile answers the class.
		//
		// Not the waived CHAOS-4761 limitation, which covers surfaces this
		// script never looks at. These are static registrations in the one
		// file it does scan, and the whole point of `unresolved` is that
		// "cannot parse" must never mean "cannot see".
		calls := len(muxCallStartRE.FindAllString(line, -1))
		if calls == 0 {
			continue
		}
		allMatches := muxHandleRE.FindAllStringSubmatch(line, -1)
		if len(allMatches) < calls {
			unresolved = append(unresolved, fmt.Sprintf(
				"%s:%d: %s (line holds %d mux registration call(s) but only %d could be parsed -- "+
					"a registration wrapped across lines, or in an unrecognised shape, is reported rather than dropped)",
				file, i+1, strings.TrimSpace(line), calls, len(allMatches),
			))
		}
		for _, matches := range allMatches {
			pattern, ok := resolvePatternExpr(matches[1], consts)
			if !ok {
				unresolved = append(unresolved, fmt.Sprintf("%s:%d: %s", file, i+1, strings.TrimSpace(line)))
				continue
			}
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				// A bare pattern (no method prefix) matches every method in
				// Go 1.22 ServeMux syntax -- not used in this repo today, but
				// reported precisely rather than mis-split.
				method, path = "ANY", pattern
			}
			routes = append(routes, Route{Method: method, Path: path, File: relPath(file), Line: i + 1, Service: deployedService})
		}
	}
	return routes, unresolved, nil
}

func relPath(file string) string {
	wd, err := os.Getwd()
	if err != nil {
		return file
	}
	rel, err := filepath.Rel(wd, file)
	if err != nil {
		return file
	}
	return rel
}
