package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
)

// Real-database cell of the compose bootstrap's runtime grants. Lead-run.

// composeStatements returns the statements of the script's generated runtime-grants block that psql would run under
// vars: \if :v / \else / \endif evaluated, comments dropped, :"runtime_user" replaced by role.
func composeStatements(t *testing.T, role string, vars map[string]bool) []string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/compose/acr-db-init.sh")
	require.NoError(t, err)
	script := string(raw)
	begin := strings.Index(script, dbacl.ComposeBeginMarker)
	end := strings.Index(script, dbacl.ComposeEndMarker)
	require.True(t, begin >= 0 && end > begin, "the generated block is in the script")
	var out []string
	type frame struct{ parent, cond, inElse bool }
	var stack []frame
	active := func() bool {
		if len(stack) == 0 {
			return true
		}
		top := stack[len(stack)-1]
		if top.inElse {
			return top.parent && !top.cond
		}
		return top.parent && top.cond
	}
	for _, line := range strings.Split(script[begin:end], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "--"):
		case strings.HasPrefix(line, "\\if :"):
			stack = append(stack, frame{parent: active(), cond: vars[strings.TrimPrefix(line, "\\if :")]})
		case line == "\\else":
			stack[len(stack)-1].inElse = true
		case line == "\\endif":
			stack = stack[:len(stack)-1]
		default:
			if active() {
				out = append(out, strings.ReplaceAll(line, `:"runtime_user"`, dbacl.QuoteIdent(role)))
			}
		}
	}
	require.Empty(t, stack, "balanced \\if")
	return out
}

func TestComposeBlock_OnToOffRevokesTheOutboxColumnGrant(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_compose_column", SkipGrant: true})
	runBlock := func(vars map[string]bool) {
		for _, statement := range composeStatements(t, database.Role, vars) {
			_, err := database.Owner.ExecContext(ctx, statement)
			require.NoError(t, err, statement)
		}
	}
	held := func(privilege string) bool {
		var has bool
		require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_column_privilege($1, 'acr.credential_lifecycle_outbox', 'published_at', $2)`, database.Role, privilege).Scan(&has))
		return has
	}
	runBlock(map[string]bool{"auth_projection": true})
	require.True(t, held("UPDATE"), "the column grant is held after the compose run with the publisher on")
	runBlock(map[string]bool{"auth_projection": false})
	require.False(t, held("UPDATE"), "the compose run with the publisher off clears the column grant")
	var tableSelect, tableInsert bool
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, 'acr.credential_lifecycle_outbox', 'SELECT'), has_table_privilege($1, 'acr.credential_lifecycle_outbox', 'INSERT')`, database.Role).Scan(&tableSelect, &tableInsert))
	require.True(t, tableSelect && tableInsert, "the mutation path's SELECT and INSERT stay after the publisher goes off")
	runBlock(map[string]bool{"auth_projection": false})
	require.False(t, held("UPDATE"), "a second run with the publisher off is idempotent")
}

func TestComposeStatements_EvaluateTheElseBranch(t *testing.T) {
	on := composeStatements(t, "r", map[string]bool{"auth_projection": true})
	off := composeStatements(t, "r", map[string]bool{})
	has := func(statements []string, want string) bool {
		for _, s := range statements {
			if s == want {
				return true
			}
		}
		return false
	}
	const grant = `GRANT UPDATE (published_at) ON TABLE acr.credential_lifecycle_outbox TO "r";`
	const revoke = `REVOKE UPDATE (published_at) ON TABLE acr.credential_lifecycle_outbox FROM "r";`
	require.True(t, has(on, grant) && !has(on, revoke), "publisher on: grant, no column revoke")
	require.True(t, !has(off, grant) && has(off, revoke), "publisher off: column revoke, no grant")
}
