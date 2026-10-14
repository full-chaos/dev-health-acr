package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// outOfBandGranted lists the tables whose runtime-role grants on Kubernetes
// were applied by the operator's out-of-band bootstrap (deploy/README.md,
// "Documented gaps": the chart never holds the admin credential), before
// grant-runtime-acl existed. Each entry names why it is allowed to be absent
// from grant_runtime_acl.go. A table created by a LATER migration has no such
// history and must be granted in grant_runtime_acl.go, so it cannot be added
// here: the test below rejects an entry created after migration 0040.
// Emptied by the follow-up that makes one grant declaration.
var outOfBandGranted = map[string]string{
	"agent_episodes":                                 "granted out of band by the operator bootstrap, before migration 0042 (created in 0001)",
	"audit_events":                                   "granted out of band by the operator bootstrap, before migration 0042 (created in 0001)",
	"client_credentials":                             "granted out of band by the operator bootstrap, before migration 0042 (created in 0001)",
	"context_packet_snapshots":                       "granted out of band by the operator bootstrap, before migration 0042 (created in 0001)",
	"device_authorizations":                          "granted out of band by the operator bootstrap, before migration 0042 (created in 0004); grant-runtime-acl adds DELETE",
	"context_fabric_projection_checkpoints":          "granted out of band by the operator bootstrap, before migration 0042 (created in 0006)",
	"context_fabric_projection_rebuild_markers":      "granted out of band by the operator bootstrap, before migration 0042 (created in 0007)",
	"context_fabric_investigation_results":           "granted out of band by the operator bootstrap, before migration 0042 (created in 0009)",
	"context_fabric_model_execution_receipts":        "granted out of band by the operator bootstrap, before migration 0042 (created in 0010)",
	"context_fabric_org_model_config":                "granted out of band by the operator bootstrap, before migration 0042 (created in 0010)",
	"context_fabric_reuse_invalidations":             "granted out of band by the operator bootstrap, before migration 0042 (created in 0011)",
	"context_fabric_clarification_selections":        "granted out of band by the operator bootstrap, before migration 0042 (created in 0016)",
	"context_fabric_graph_build_source_progress":     "granted out of band by the operator bootstrap, before migration 0042 (created in 0019)",
	"context_fabric_graph_epoch_retirements":         "granted out of band by the operator bootstrap, before migration 0042 (created in 0019)",
	"context_fabric_graph_lifecycle":                 "granted out of band by the operator bootstrap, before migration 0042 (created in 0019)",
	"context_fabric_structure_supersession_claims":   "granted out of band by the operator bootstrap, before migration 0042 (created in 0023)",
	"context_fabric_structure_selections":            "granted out of band by the operator bootstrap, before migration 0042 (created in 0024)",
	"context_fabric_structure_prior_pointer":         "granted out of band by the operator bootstrap, before migration 0042 (created in 0028)",
	"context_fabric_structure_prior_pointer_history": "granted out of band by the operator bootstrap, before migration 0042 (created in 0028)",
	"context_fabric_structure_prior_revocations":     "granted out of band by the operator bootstrap, before migration 0042 (created in 0028)",
	"context_fabric_structure_priors":                "granted out of band by the operator bootstrap, before migration 0042 (created in 0028)",
	"workload_bindings":                              "created in 0030; no runtime grant in acr-db-init.sh either, tracked as its own gap",
	"oauth_authorization_requests":                   "granted out of band by the operator bootstrap, before migration 0042 (created in 0040); grant-runtime-acl adds DELETE",
	"oauth_clients":                                  "granted out of band by the operator bootstrap, before migration 0042 (created in 0040); grant-runtime-acl adds DELETE",
}

const lastOutOfBandMigration = 40

var (
	createTablePattern = regexp.MustCompile(`(?i)CREATE TABLE(?: IF NOT EXISTS)?\s+acr\.([a-z0-9_]+)`)
	grantTablePattern  = regexp.MustCompile("(?s)GRANT [A-Z, ]+ ON TABLE ((?:acr\\.[a-z0-9_]+(?:,\\s*)?)+)\\s+TO")
)

// TestGrantRuntimeACL_everyMigrationTableHasAKubernetesGrant is the class test:
// every table the migrations create is either granted to the runtime role in
// grant_runtime_acl.go or is listed above with the reason it is not. A new
// table with neither fails here, so it cannot ship ungranted on Kubernetes.
func TestGrantRuntimeACL_everyMigrationTableHasAKubernetesGrant(t *testing.T) {
	created := map[string]int{}
	files, err := filepath.Glob("../../migrations/postgres/*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files, "no migration files found")
	for _, file := range files {
		version, err := strconv.Atoi(strings.SplitN(filepath.Base(file), "_", 2)[0])
		require.NoError(t, err, file)
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, match := range createTablePattern.FindAllStringSubmatch(string(body), -1) {
			if _, seen := created[match[1]]; !seen {
				created[match[1]] = version
			}
		}
	}
	require.NotEmpty(t, created)

	source, err := os.ReadFile("grant_runtime_acl.go")
	require.NoError(t, err)
	granted := map[string]bool{}
	for _, match := range grantTablePattern.FindAllStringSubmatch(string(source), -1) {
		for _, table := range strings.Split(match[1], ",") {
			granted[strings.TrimPrefix(strings.TrimSpace(table), "acr.")] = true
		}
	}
	require.NotEmpty(t, granted, "no GRANT statements parsed from grant_runtime_acl.go")

	var ungranted, staleAllow, lateAllow []string
	for table, version := range created {
		if granted[table] {
			continue
		}
		reason, allowed := outOfBandGranted[table]
		if !allowed || strings.TrimSpace(reason) == "" {
			ungranted = append(ungranted, table)
			continue
		}
		if version > lastOutOfBandMigration {
			lateAllow = append(lateAllow, table)
		}
	}
	for table := range outOfBandGranted {
		if _, exists := created[table]; !exists {
			staleAllow = append(staleAllow, table)
		}
	}
	sort.Strings(ungranted)
	sort.Strings(staleAllow)
	sort.Strings(lateAllow)
	require.Empty(t, ungranted, "tables created by a migration with no grant in grant_runtime_acl.go and no out-of-band entry: the runtime role cannot use them on Kubernetes")
	require.Empty(t, lateAllow, "tables created after migration 0040 cannot be allowed out of band: grant them in grant_runtime_acl.go")
	require.Empty(t, staleAllow, "out-of-band entries for tables no migration creates")
}
