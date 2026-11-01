package dbacl

import (
	"strings"
	"testing"
)

func TestTheOutboxHoldsUpdateOnPublishedAtOnlyAndOnlyWithThePublisher(t *testing.T) {
	var columnRows int
	for _, grant := range RuntimeGrants {
		if grant.Table != "credential_lifecycle_outbox" {
			continue
		}
		for _, privilege := range grant.Privileges {
			if privilege != "SELECT" && privilege != "INSERT" {
				t.Errorf("the outbox holds table-level %s: the runtime role may only read and append it", privilege)
			}
		}
		if len(grant.UpdateColumns) == 0 {
			if grant.Feature != AlwaysOn {
				t.Errorf("the SELECT, INSERT row is the mutation path's and is always on, not %q", grant.Feature)
			}
			continue
		}
		columnRows++
		if strings.Join(grant.UpdateColumns, ",") != "published_at" {
			t.Errorf("update columns = %v, want published_at only", grant.UpdateColumns)
		}
		if grant.Feature != AuthProjection {
			t.Errorf("the column grant is gated on %q, want the publisher's feature", grant.Feature)
		}
		if len(grant.Privileges) != 0 {
			t.Errorf("the column row carries table privileges %v", grant.Privileges)
		}
	}
	if columnRows != 1 {
		t.Fatalf("%d column rows on the outbox, want 1", columnRows)
	}
}

func TestTheColumnGrantAndRevokeStatementsFollowTheFeature(t *testing.T) {
	on := GrantStatements(`"r"`, Features{AuthProjection: true})
	off := GrantStatements(`"r"`, Features{})
	has := func(statements []Statement, sql string) bool {
		for _, s := range statements {
			if s.SQL == sql {
				return true
			}
		}
		return false
	}
	const grant = `GRANT UPDATE (published_at) ON TABLE acr.credential_lifecycle_outbox TO "r"`
	if !has(on, grant) || has(off, grant) {
		t.Errorf("the column grant must be issued exactly with the feature on")
	}
	const revoke = `REVOKE UPDATE (published_at) ON TABLE acr.credential_lifecycle_outbox FROM "r"`
	if has(RevokeStatements(`"r"`, Features{AuthProjection: true}), revoke) {
		t.Error("a feature that is on is never revoked")
	}
	if !has(RevokeStatements(`"r"`, Features{}), revoke) {
		t.Error("a feature that is off revokes the column grant an earlier run made")
	}
	for _, s := range RevokeStatements(`"r"`, Features{}) {
		if strings.Contains(s.SQL, "ALL") || !strings.Contains(s.SQL, "(") {
			t.Errorf("a revoke narrows more than a column: %s", s.SQL)
		}
	}
}

func TestTheComposeBlockGatesTheColumnGrantOnThePublisher(t *testing.T) {
	block := RenderCompose()
	want := "\\if :auth_projection\nGRANT UPDATE (published_at) ON TABLE acr.credential_lifecycle_outbox TO :\"runtime_user\";\n\\endif"
	if !strings.Contains(block, want) {
		t.Errorf("the compose block lacks the gated column grant:\n%s", block)
	}
}
