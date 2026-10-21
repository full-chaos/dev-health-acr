package config

import (
	"strings"
	"testing"
)

var clickHouseDSNSchemeRows = []struct {
	name    string
	dsn     string
	refused bool
}{
	{"native clickhouse", "clickhouse://u:pw-fragment@ch.row.example:9000/db", false},
	{"native tcp", "tcp://ch.row.example:9000", false},
	{"http", "http://ch.row.example:8123", true},
	{"https", "https://ch.row.example:8443", true},
	{"uppercase http", "HTTP://ch.row.example:8123", true},
	{"https with credentials and query", "https://u:pw-fragment@ch.row.example:8443/db?token=tok-fragment&secure=true", true},
	{"leading space", "  http://ch.row.example:8123", true},
	{"empty", "", true},
}

func TestClickHouseDSNSchemeRows(t *testing.T) {
	if len(clickHouseDSNSchemeRows) != 8 {
		t.Fatalf("row count = %d, want 8", len(clickHouseDSNSchemeRows))
	}
	loaders := map[string]func(dsn string) error{
		"api": func(dsn string) error {
			values := completeRuntimeEnvironment()
			values["ACR_CLICKHOUSE_DSN"] = dsn
			_, err := load(mapLookup(values))
			return err
		},
		"projector": func(dsn string) error {
			_, err := loadProjector(mapLookup(map[string]string{
				"ACR_POSTGRES_DSN":             "postgres://configured",
				"ACR_POSTGRES_CONNECTION_KIND": "direct",
				"ACR_CLICKHOUSE_DSN":           dsn,
			}), requiredStoresAll)
			return err
		},
	}
	for loaderName, loadWith := range loaders {
		for _, row := range clickHouseDSNSchemeRows {
			t.Run(loaderName+"/"+row.name, func(t *testing.T) {
				err := loadWith(row.dsn)
				if !row.refused {
					if err != nil {
						t.Fatalf("error = %v, want accepted", err)
					}
					return
				}
				if err == nil {
					t.Fatal("accepted, want refusal")
				}
				text := err.Error()
				if !strings.Contains(text, "ACR_CLICKHOUSE_DSN") {
					t.Fatalf("error %q does not name ACR_CLICKHOUSE_DSN", text)
				}
				if row.dsn != "" && (!strings.Contains(text, "http") || !strings.Contains(text, "https")) {
					t.Fatalf("error %q does not say http and https are not accepted", text)
				}
				for _, fragment := range []string{"ch.row.example", "pw-fragment", "tok-fragment", "8123", "8443"} {
					if strings.Contains(text, fragment) {
						t.Fatalf("error %q echoes the DSN fragment %q", text, fragment)
					}
				}
			})
		}
	}
}
