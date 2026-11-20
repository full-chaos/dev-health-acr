package factoracle

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	contentEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	contentHost  = regexp.MustCompile(`(?i)[a-z]+://|\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|net|org|io|dev|app|ai|cloud|internal|local|lan|home)\b`)
)

// orgKeys are the object keys whose value is an organization id.
var orgKeys = map[string]bool{"org_id": true, "orgId": true, "fixture_org": true}

// capturedContentProblems names every string of a capture (values and object
// keys) that holds an email, a host name or a URL, every organization id that
// is not FixtureOrgID, every build field that is more than repository names,
// commits and versions, and every string that holds one of forbidden.
func capturedContentProblems(t *testing.T, dir string, forbidden []string) []string {
	t.Helper()
	var problems []string
	note := func(file, path, what string) { problems = append(problems, file+" "+path+": "+what) }
	var walk func(file, path, key string, v any)
	check := func(file, path, s string) {
		switch {
		case contentEmail.MatchString(s):
			note(file, path, "an email")
		case contentHost.MatchString(s):
			note(file, path, "a host name or URL")
		}
		for _, f := range forbidden {
			if strings.Contains(strings.ToLower(s), f) {
				note(file, path, "a forbidden organization prefix")
			}
		}
	}
	walk = func(file, path, key string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, item := range t {
				check(file, path+"{key}", k)
				walk(file, path+"."+k, k, item)
			}
		case []any:
			for _, item := range t {
				walk(file, path+"[*]", key, item)
			}
		case string:
			check(file, path, t)
			if orgKeys[key] && t != FixtureOrgID {
				note(file, path, "an organization id that is not FixtureOrgID")
			}
			if (key == "ops_build" || key == "acr_build") && ScrubBuild(t) != t {
				note(file, path, "a build field with words other than repository names, commits and versions")
			}
		}
	}
	decode := func(file string, r io.Reader, lines bool) {
		dec := json.NewDecoder(r)
		dec.UseNumber()
		for {
			var v any
			if err := dec.Decode(&v); err == io.EOF {
				return
			} else if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			walk(file, "", "", v)
			if !lines {
				return
			}
		}
	}
	for _, name := range []string{ManifestFile, RepliesFile} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		decode(name, f, false)
		_ = f.Close()
	}
	extracts, err := filepath.Glob(filepath.Join(dir, ExtractDir, "*.jsonl.gz"))
	if err != nil || len(extracts) == 0 {
		t.Fatalf("no extract files under %s", dir)
	}
	for _, path := range extracts {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		decode(filepath.Join(ExtractDir, filepath.Base(path)), gz, true)
		_ = gz.Close()
		_ = f.Close()
	}
	sort.Strings(problems)
	return problems
}

// forbiddenOrgPrefixes reads ACR_O4_FORBIDDEN_ORG_PREFIXES: organization id
// prefixes (comma separated) the capture must not hold. The venue operator
// sets it before a push; it is never committed.
func forbiddenOrgPrefixes() []string {
	var out []string
	for _, item := range strings.Split(os.Getenv("ACR_O4_FORBIDDEN_ORG_PREFIXES"), ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// TestTheCaptureHoldsNoHostEmailOrRealOrganization: the capture in the
// repository is synthetic. It holds no email, no host name or URL, no
// organization id but FixtureOrgID, and build fields of repository names,
// commits and versions only.
func TestTheCaptureHoldsNoHostEmailOrRealOrganization(t *testing.T) {
	problems := capturedContentProblems(t, captureDir, forbiddenOrgPrefixes())
	const shown = 20
	for i, p := range problems {
		if i == shown {
			t.Errorf("... and %d more", len(problems)-shown)
			break
		}
		t.Error(p)
	}
}

// TestTheCaptureContentCheckFindsEachClass plants one string of each class in
// a copy of a capture and requires the check to name it.
func TestTheCaptureContentCheckFindsEachClass(t *testing.T) {
	plants := map[string]map[string]any{
		"an email":                        {"note": "write to someone@example.invalid"},
		"a host name or URL":              {"note": "served by db.internal"},
		"a URL":                           {"note": "see https://x"},
		"an organization id":              {"org_id": "11111111-1111-4111-8111-111111111111"},
		"a build field":                   {"ops_build": "dev-health-ops 0123abcd (venue-host-1)"},
		"a forbidden organization prefix": {"note": "id 99aa77bb-0000"},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			manifest := map[string]any{"fixture_org": FixtureOrgID, "ops_build": "dev-health-ops 0123abcd", "acr_build": "dev-health-acr 4567cdef", "extra": plant}
			writeJSON(t, filepath.Join(dir, ManifestFile), manifest)
			writeJSON(t, filepath.Join(dir, RepliesFile), map[string]any{"replies": map[string]any{}})
			if err := os.MkdirAll(filepath.Join(dir, ExtractDir), 0o755); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(dir, ExtractDir, "teams.jsonl.gz"))
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			_, _ = gz.Write([]byte(`{"org_id":"` + FixtureOrgID + `","name":"h_0001"}` + "\n"))
			_ = gz.Close()
			_ = f.Close()
			if got := capturedContentProblems(t, dir, []string{"99aa77bb"}); len(got) != 1 {
				t.Fatalf("problems = %q, want exactly the planted one", got)
			}
		})
	}
	clean := t.TempDir()
	writeJSON(t, filepath.Join(clean, ManifestFile), map[string]any{"fixture_org": FixtureOrgID, "ops_build": "dev-health-ops 2c115402 sha-2c11540", "acr_build": "dev-health-acr 24513ae2 v0.1.1"})
	writeJSON(t, filepath.Join(clean, RepliesFile), map[string]any{"replies": map[string]any{}})
	if err := os.MkdirAll(filepath.Join(clean, ExtractDir), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(clean, ExtractDir, "teams.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	_ = gz.Close()
	_ = f.Close()
	if got := capturedContentProblems(t, clean, nil); len(got) != 0 {
		t.Fatalf("a clean capture gave problems %q", got)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScrubBuildKeepsRepositoriesCommitsAndVersions(t *testing.T) {
	for in, want := range map[string]string{
		"dev-health-ops 2c115402 (venue-1 query-api 2c11540)":                "dev-health-ops 2c115402 2c11540",
		"dev-health-acr 24513ae2 (registry.example.io/acr-api:sha-24513ae2)": "dev-health-acr 24513ae2",
		"dev-health-ops sha-2c11540 v0.1.1-main.24513ae2":                    "dev-health-ops sha-2c11540 v0.1.1-main.24513ae2",
		"venue-1": "",
	} {
		if got := ScrubBuild(in); got != want {
			t.Errorf("ScrubBuild(%q) = %q, want %q", in, got, want)
		}
	}
}
