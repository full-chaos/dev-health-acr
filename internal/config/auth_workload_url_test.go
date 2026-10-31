package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

type authWorkloadURLCell struct {
	field, value, verdict, note string
	plainHTTP                   bool
}

// authWorkloadURLCells is the one corpus both paths are held to: this Go validator and the Helm check
// (scripts/deploy/test-helm.sh scenario auth-workload-client-url-corpus reads the same file).
func authWorkloadURLCells(t *testing.T) []authWorkloadURLCell {
	t.Helper()
	file, err := os.Open("testdata/auth_workload_urls.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var cells []authWorkloadURLCell
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 5 || (parts[3] != "accept" && parts[3] != "refuse") || (parts[0] != "exchange" && parts[0] != "issuer") {
			t.Fatalf("bad corpus line %q", line)
		}
		cells = append(cells, authWorkloadURLCell{field: parts[0], plainHTTP: parts[1] == "true", value: parts[2], verdict: parts[3], note: parts[4]})
	}
	if err := scanner.Err(); err != nil || len(cells) < 100 {
		t.Fatalf("corpus: %d cells, err=%v", len(cells), err)
	}
	return cells
}

func TestAuthWorkloadURLCorpusThroughTheLoader(t *testing.T) {
	for _, cell := range authWorkloadURLCells(t) {
		env := map[string]string{
			"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "https://auth-service.example.test",
			"ACR_AUTH_WORKLOAD_ISSUER":       "https://auth.example.test",
			"ACR_AUTH_WORKLOAD_SUBJECT_FILE": "/var/run/secrets/dev-health/auth-workload/token",
		}
		varName := "ACR_AUTH_WORKLOAD_EXCHANGE_URL"
		if cell.field == "issuer" {
			varName = "ACR_AUTH_WORKLOAD_ISSUER"
		}
		env[varName] = cell.value
		if cell.plainHTTP {
			env["ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP"] = "true"
		}
		_, err := LoadAuthWorkload(mapLookup(env))
		switch {
		case cell.verdict == "accept" && err != nil:
			t.Errorf("%s %q (%s): refused: %v", cell.field, cell.value, cell.note, err)
		case cell.verdict == "refuse" && err == nil:
			t.Errorf("%s %q (%s): accepted", cell.field, cell.value, cell.note)
		case err != nil && !strings.Contains(err.Error(), varName):
			t.Errorf("%s %q (%s): the error must name %s: %v", cell.field, cell.value, cell.note, varName, err)
		case err != nil && cell.value != "" && strings.Contains(err.Error(), cell.value):
			t.Errorf("%s %q (%s): the error echoes the value: %v", cell.field, cell.value, cell.note, err)
		}
	}
}

func TestAuthWorkloadURLCorpusThroughValidate(t *testing.T) {
	for _, cell := range authWorkloadURLCells(t) {
		cfg := AuthWorkloadConfig{ExchangeURL: "https://auth-service.example.test", Issuer: "https://auth.example.test", SubjectFile: "/token", AllowPlainHTTP: cell.plainHTTP}
		if cell.field == "issuer" {
			cfg.Issuer = cell.value
		} else {
			cfg.ExchangeURL = cell.value
		}
		err := cfg.Validate()
		if (err == nil) != (cell.verdict == "accept") {
			t.Errorf("Validate %s %q (%s): verdict %s, err=%v", cell.field, cell.value, cell.note, cell.verdict, err)
		}
	}
}
