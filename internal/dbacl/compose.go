package dbacl

import (
	"fmt"
	"strings"
)

// Markers bound the generated region of deploy/compose/acr-db-init.sh.
const (
	ComposeBeginMarker = "-- BEGIN GENERATED RUNTIME GRANTS (internal/dbacl; regenerate with DBACL_WRITE=1 go test ./internal/dbacl)"
	ComposeEndMarker   = "-- END GENERATED RUNTIME GRANTS"
)

// composeRevoke resets every privilege so the compose bootstrap leaves the
// role with exactly the declared set. The Kubernetes path never revokes.
const composeRevoke = "REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.%s FROM :\"runtime_user\";"

// RenderCompose returns the psql block the compose bootstrap runs: per table a
// REVOKE then the declared GRANTs, with a feature's rows inside a psql \if on
// the variable of the same name.
func RenderCompose() string {
	var out strings.Builder
	out.WriteString(ComposeBeginMarker + "\n")
	var seen []string
	index := map[string][]Grant{}
	for _, grant := range RuntimeGrants {
		if _, ok := index[grant.Table]; !ok {
			seen = append(seen, grant.Table)
		}
		index[grant.Table] = append(index[grant.Table], grant)
	}
	for position, table := range seen {
		if position > 0 {
			out.WriteString("\n")
		}
		for _, grant := range index[table] {
			fmt.Fprintf(&out, "-- %s\n", grant.Why)
		}
		fmt.Fprintf(&out, composeRevoke+"\n", table)
		for _, grant := range index[table] {
			if grant.Feature != AlwaysOn {
				fmt.Fprintf(&out, "\\if :%s\n", grant.Feature)
			}
			fmt.Fprintf(&out, "GRANT %s ON TABLE acr.%s TO :\"runtime_user\";\n", strings.Join(grant.Privileges, ", "), table)
			for _, sequence := range grant.Sequences {
				fmt.Fprintf(&out, "GRANT USAGE ON SEQUENCE acr.%s TO :\"runtime_user\";\n", sequence)
			}
			if grant.Feature != AlwaysOn {
				out.WriteString("\\endif\n")
			}
		}
	}
	out.WriteString(ComposeEndMarker + "\n")
	return out.String()
}

// Markers bound the generated purge-role region of the compose script.
const (
	ComposePurgeBeginMarker = "-- BEGIN GENERATED PURGE GRANTS (internal/dbacl; regenerate with DBACL_WRITE=1 go test ./internal/dbacl)"
	ComposePurgeEndMarker   = "-- END GENERATED PURGE GRANTS"
)

// RenderComposePurge returns the psql block that gives the purge role its
// declared privileges: inside a psql \if on purge_enabled, per table a REVOKE
// then the declared GRANT. The role exists only when the operator names one.
func RenderComposePurge() string {
	var out strings.Builder
	out.WriteString(ComposePurgeBeginMarker + "\n\\if :purge_enabled\n")
	for _, grant := range PurgeGrants {
		fmt.Fprintf(&out, "-- %s\n", grant.Why)
		fmt.Fprintf(&out, "REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.%s FROM :\"purge_user\";\n", grant.Table)
		fmt.Fprintf(&out, "GRANT %s ON TABLE acr.%s TO :\"purge_user\";\n", strings.Join(grant.Privileges, ", "), grant.Table)
	}
	out.WriteString("\\endif\n" + ComposePurgeEndMarker + "\n")
	return out.String()
}
