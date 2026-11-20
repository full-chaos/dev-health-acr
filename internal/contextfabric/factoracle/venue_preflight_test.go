//go:build o4venue

package factoracle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// emptyAllowed are the extract tables a measured capture may hold no row of.
var emptyAllowed = map[string]bool{tableWorkUnitSupersessions: true}

// TestVenuePreflight checks, from the environment alone and read only, that a
// venue can be captured: the venue variables and credential files, a
// read-only ClickHouse user that holds rows of the organization in the window
// for every extract table, and an MCP endpoint that answers the credential.
// Any host that serves the required services passes it; nothing in it names
// a host, a container runtime or a deployment.
func TestVenuePreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := venueConfig(t)
	if ScrubBuild(cfg.OpsBuild) == "" {
		t.Errorf("ACR_O4_OPS_BUILD names no commit of the ops build")
	}
	for _, file := range []struct{ name, path string }{{"ACR_O4_TOKEN_FILE", cfg.TokenFile}, {"ACR_O4_CLICKHOUSE_DSN_FILE", cfg.ClickHouseDSNFile}} {
		info, err := os.Stat(file.path)
		switch {
		case err != nil:
			t.Errorf("%s is not readable", file.name)
		case info.Mode().Perm()&0o077 != 0:
			t.Errorf("%s is mode %o; a credential file must be 0600", file.name, info.Mode().Perm())
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	conn, err := openVenueClickHouse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	db := clickHouseQuerier{conn: conn}
	readonly := ""
	if err := db.QueryStrings(ctx, "SELECT toString(getSetting('readonly'))", nil, func(s string) error { readonly = s; return nil }); err != nil {
		t.Fatalf("ClickHouse: %v", err)
	}
	if readonly == "0" || readonly == "" {
		t.Errorf("the ClickHouse user of ACR_O4_CLICKHOUSE_DSN_FILE is not read only (readonly=%q)", readonly)
	}
	params := map[string]string{"org": cfg.OrgID, "start": cfg.Window.startDate(), "end": cfg.Window.endDate()}
	for _, spec := range extractTables {
		count := ""
		statement := "SELECT toString(count()) FROM " + spec.Table + " WHERE org_id = {org:String} AND (" + spec.Where + ")"
		if err := db.QueryStrings(ctx, statement, params, func(s string) error { count = s; return nil }); err != nil {
			t.Errorf("table %s: %v", spec.Table, err)
			continue
		}
		t.Logf("table %s: %s rows of the organization in the window %s to %s", spec.Table, count, cfg.Window.startDate(), cfg.Window.endDate())
		if count == "0" && !emptyAllowed[spec.Table] {
			t.Errorf("table %s has no row of the organization in the window: seed or derive it before the capture", spec.Table)
		}
	}

	// A refusal of the query is an answer: the endpoint took the credential.
	planes := &VenuePlanes{URL: cfg.MCPURL, TokenFile: cfg.TokenFile}
	raw, status, _, err := planes.post(ctx, "graphql_query", map[string]any{"query": "{ __typename }"})
	switch {
	case err != nil:
		t.Errorf("MCP endpoint: %v", err)
	case status != 200:
		t.Errorf("MCP endpoint answered HTTP %d (401 or 403: the credential of ACR_O4_TOKEN_FILE is not accepted)", status)
	default:
		if _, _, err := venueStructuredContent("graphql_query", raw); err != nil && strings.Contains(err.Error(), "JSON-RPC") {
			t.Errorf("MCP endpoint: %v", err)
		}
	}
}
