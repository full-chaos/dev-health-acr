// Package retirednames is the census of names that ops retired and acr no longer uses. No file in the tree may name
// one of them outside the exact-path allowlist.
package retirednames

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// retiredNames maps a name to the decision that retired it.
var retiredNames = map[string]*regexp.Regexp{
	"AUTH_ACR_WORKLOAD_SUBJECT (go-api reads the acr-api workload from the registry, CHAOS-13177)":      regexp.MustCompile(`AUTH_ACR_WORKLOAD_SUBJECT`),
	"internal_service_credentials (table retired, CHAOS-11245)":                                         regexp.MustCompile(`\binternal_service_credentials\b`),
	"dho service-credentials (verbs removed, ops CHAOS-13170)":                                          regexp.MustCompile(`\bservice-credentials\b`),
	"servicecredcli / servicecred store (ops CHAOS-11245)":                                              regexp.MustCompile(`servicecredcli|internal/auth/servicecred\b`),
	"e2e service-token helpers (retired with svc_acr_ minting)":                                         regexp.MustCompile(`mint_ops_service_token|ops-probe-token`),
	"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE (service token entitlement credential removed, CHAOS-13600)": regexp.MustCompile(`ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE`),
	"svc_acr_ credential prefix (retired with the ops service credential, CHAOS-13170; CHAOS-11249)":    regexp.MustCompile(`\bsvc_acr_`),
	"service_token entitlement credential value (removed, CHAOS-13600)":                                 regexp.MustCompile(`\bservice_token\b`),
}

// retiredLiterals holds, for every retiredNames pattern, the literals one of
// which each of its matches must contain, derived from the parsed pattern. The
// tree walk line-scans only a file that holds one of them: eight patterns per
// line over the whole tree cost about 350 s under -race, a substring search
// costs almost nothing. nil means some pattern has no required literal, and
// then every file is line-scanned.
var retiredLiterals = func() [][]byte {
	var all [][]byte
	for _, re := range retiredNames {
		tree, err := syntax.Parse(re.String(), syntax.Perl)
		if err != nil {
			return nil
		}
		literals := requiredLiterals(tree.Simplify())
		if literals == nil {
			return nil
		}
		for _, literal := range literals {
			all = append(all, []byte(literal))
		}
	}
	return all
}()

// requiredLiterals returns strings one of which every match of re contains,
// or nil when it cannot name such a set.
func requiredLiterals(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return nil
		}
		return []string{string(re.Rune)}
	case syntax.OpCapture:
		return requiredLiterals(re.Sub[0])
	case syntax.OpConcat:
		var best []string
		for _, sub := range re.Sub {
			literals := requiredLiterals(sub)
			if literals != nil && (best == nil || shortestLength(literals) > shortestLength(best)) {
				best = literals
			}
		}
		return best
	case syntax.OpAlternate:
		var all []string
		for _, sub := range re.Sub {
			literals := requiredLiterals(sub)
			if literals == nil {
				return nil
			}
			all = append(all, literals...)
		}
		return all
	}
	return nil
}

func shortestLength(literals []string) int {
	shortest := len(literals[0])
	for _, literal := range literals[1:] {
		shortest = min(shortest, len(literal))
	}
	return shortest
}

// mayHoldRetiredName is false only when data holds none of retiredLiterals,
// so no retiredNames pattern can match it.
func mayHoldRetiredName(data []byte) bool {
	if retiredLiterals == nil {
		return true
	}
	for _, literal := range retiredLiterals {
		if bytes.Contains(data, literal) {
			return true
		}
	}
	return false
}

// allowed is the exact-path allowlist: a listed path may name a retired name because it bans it.
var allowed = map[string]bool{
	"scripts/e2e/test-compose.sh":                  true,
	"internal/cigates/retirednames/census_test.go": true,
	// The Warn site of the ignored token file setting and the refusal of the removed credential value, and its test.
	"internal/config/entitlement.go":                 true,
	"internal/config/entitlement_credential_test.go": true,
	// Refusal cells: each presents a svc_acr_ bearer on purpose to prove acr refuses it.
	// Redaction/leak detection of a retired token: a retired token can still exist in the wild, so these keep matching it.
	"scripts/e2e/fullstack-opencode.sh":                          true,
	"tests/fullstack/assertrun/redact.go":                        true,
	"tests/fullstack/assertrun/redact_test.go":                   true,
	"scripts/e2e/compose.sh":                                     true,
	"internal/auth/middleware_credential_classes_test.go":        true,
	"internal/api/public_routes_foreign_tokens_test.go":          true,
	"internal/sidecar/credential_class_matrix_test.go":           true,
	"internal/sidecar/api_client_credential_class_guard_test.go": true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func scanText(rel string, data []byte) []string {
	var hits []string
	for i, line := range strings.Split(string(data), "\n") {
		for name, re := range retiredNames {
			if re.MatchString(line) {
				hits = append(hits, rel+":"+strconv.Itoa(i+1)+": "+name)
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func TestNoRetiredNameInTree(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		if allowed[rel] || !mayHoldRetiredName(data) {
			return nil
		}
		hits = append(hits, scanText(rel, data)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d files: the census read too little of the tree", scanned)
	}
	for _, h := range hits {
		t.Errorf("retired name returned: %s", h)
	}
	for rel := range allowed {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("allowlist path %s does not exist: %v", rel, err)
		}
	}
}

func TestEveryRetiredNameIsDetected(t *testing.T) {
	planted := map[string]string{
		"AUTH_ACR_WORKLOAD_SUBJECT":             "set AUTH_ACR_WORKLOAD_SUBJECT=prn_1",
		"internal_service_credentials":          "select * from internal_service_credentials",
		"dho service-credentials":               "dho service-credentials list",
		"servicecredcli / servicecred":          "import internal/auth/servicecred",
		"e2e service-token helpers":             "mint_ops_service_token",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE: /var/run/acr/entitlement/token",
		"service_token entitlement":             "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL=service_token",
		"svc_acr_ credential prefix":            `Authorization: Bearer svc_acr_abc`,
	}
	for prefix, line := range planted {
		matched := false
		for name := range retiredNames {
			if strings.HasPrefix(name, prefix) {
				matched = len(scanText("x", []byte(line))) > 0
			}
		}
		if !matched {
			t.Errorf("planted line for %q was not detected", prefix)
		}
	}
	for prefix, line := range planted {
		if !mayHoldRetiredName([]byte(line)) {
			t.Errorf("the file prefilter misses the planted line for %q, so the tree walk would skip its file", prefix)
		}
	}
	for _, kept := range []string{"internal_service_credential_audits", "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL=workload", "a_service_token_b", "internal_svc_acr_token", "svc_acr"} {
		if got := scanText("x", []byte(kept)); len(got) != 0 {
			t.Errorf("%q is live and must not match: %v", kept, got)
		}
	}
}

// TestEveryRetiredPatternHasARequiredLiteral keeps the file prefilter in force:
// a pattern with no required literal would turn it off for the whole tree.
func TestEveryRetiredPatternHasARequiredLiteral(t *testing.T) {
	if retiredLiterals == nil {
		t.Fatal("a retiredNames pattern has no required literal, so the tree walk line-scans every file")
	}
	for name, re := range retiredNames {
		tree, err := syntax.Parse(re.String(), syntax.Perl)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if requiredLiterals(tree.Simplify()) == nil {
			t.Errorf("%s: no required literal", name)
		}
	}
}

func TestRequiredLiteralsNamesWhatEveryMatchHolds(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{`AUTH_ACR_WORKLOAD_SUBJECT`, []string{"AUTH_ACR_WORKLOAD_SUBJECT"}},
		{`\binternal_service_credentials\b`, []string{"internal_service_credentials"}},
		{`mint_ops_service_token|ops-probe-token`, []string{"mint_ops_service_token", "ops-probe-token"}},
		{`(svc)_acr_\w+`, []string{"_acr_"}},
		{`(?i)svc_acr_`, nil},
		{`[a-z]+`, nil},
		{`svc_acr_|[0-9]+`, nil},
	} {
		tree, err := syntax.Parse(tc.pattern, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		got := requiredLiterals(tree.Simplify())
		re := regexp.MustCompile(tc.pattern)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("requiredLiterals(%q) = %q, want %q", tc.pattern, got, tc.want)
		}
		for _, line := range []string{"x AUTH_ACR_WORKLOAD_SUBJECT y", "internal_service_credentials", "ops-probe-token", "svc_acr_abc", "SVC_ACR_x", "abc", "123"} {
			if got != nil && re.MatchString(line) && !containsAny(line, got) {
				t.Errorf("%q matches %q but holds none of %q", line, tc.pattern, got)
			}
		}
	}
}

func containsAny(line string, literals []string) bool {
	for _, literal := range literals {
		if strings.Contains(line, literal) {
			return true
		}
	}
	return false
}
