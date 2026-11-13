// Package retirednames is the census of names that ops retired and acr no longer uses. No file in the tree may name
// one of them outside the exact-path allowlist.
package retirednames

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	"service_token entitlement credential value (removed, CHAOS-13600)":                                 regexp.MustCompile(`\bservice_token\b`),
}

// allowed is the exact-path allowlist: a listed path may name a retired name because it bans it.
var allowed = map[string]bool{
	"scripts/e2e/test-compose.sh":                  true,
	"internal/cigates/retirednames/census_test.go": true,
	// The Warn site of the ignored token file setting and the refusal of the removed credential value, and its test.
	"internal/config/entitlement.go":                 true,
	"internal/config/entitlement_credential_test.go": true,
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
		if allowed[rel] {
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
	for _, kept := range []string{"internal_service_credential_audits", "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL=workload", "a_service_token_b", "svc_acr_"} {
		if got := scanText("x", []byte(kept)); len(got) != 0 {
			t.Errorf("%q is live and must not match: %v", kept, got)
		}
	}
}
