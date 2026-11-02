package dbacl

import (
	"os"
	"strings"
	"testing"
)

func TestTheOutboxHoldsDeleteOnlyWithThePublisher(t *testing.T) {
	var deleteRows int
	for _, grant := range RuntimeGrants {
		if grant.Table != "credential_lifecycle_outbox" {
			continue
		}
		for _, privilege := range grant.Privileges {
			if privilege == "DELETE" {
				deleteRows++
				if grant.Feature != AuthProjection {
					t.Errorf("the DELETE row is gated on %q, want the publisher's feature", grant.Feature)
				}
				if len(grant.Privileges) != 1 || len(grant.UpdateColumns) != 0 || len(grant.SelectColumns) != 0 {
					t.Errorf("the DELETE row carries more than DELETE: %+v", grant)
				}
				if !grant.RevokeWhenOff {
					t.Error("the DELETE row is revoked when the feature is off, like the column grant")
				}
			}
		}
	}
	if deleteRows != 1 {
		t.Fatalf("%d DELETE rows on the outbox, want 1", deleteRows)
	}
}

func TestTheDeleteGrantAndRevokeFollowTheFeature(t *testing.T) {
	has := func(statements []Statement, sql string) bool {
		for _, s := range statements {
			if s.SQL == sql {
				return true
			}
		}
		return false
	}
	const grant = `GRANT DELETE ON TABLE acr.credential_lifecycle_outbox TO "r"`
	const revoke = `REVOKE DELETE ON TABLE acr.credential_lifecycle_outbox FROM "r"`
	if !has(GrantStatements(`"r"`, Features{AuthProjection: true}), grant) || has(GrantStatements(`"r"`, Features{}), grant) {
		t.Error("the DELETE grant is issued exactly with the feature on")
	}
	if has(RevokeStatements(`"r"`, Features{AuthProjection: true}), revoke) {
		t.Error("a feature that is on is never revoked")
	}
	if !has(RevokeStatements(`"r"`, Features{}), revoke) {
		t.Error("a feature that is off revokes the DELETE an earlier run granted")
	}
}

// Only a row that asks for it is revoked: the other feature-gated DELETE rows (credential and family purges) keep the
// bare-GRANT form, so the job never narrows a table-level privilege a running purge loop uses.
func TestOnlyRowsThatAskAreRevokedAtTableLevel(t *testing.T) {
	for _, s := range RevokeStatements(`"r"`, Features{}) {
		if strings.Contains(s.SQL, "client_credentials") || strings.Contains(s.SQL, "browser_token_families") {
			t.Errorf("a purge table is revoked: %s", s.SQL)
		}
		if strings.Contains(s.SQL, "ALL") {
			t.Errorf("a revoke narrows more than the declared privilege: %s", s.SQL)
		}
	}
}

func TestTheComposeBlockGatesTheOutboxDeleteOnThePublisher(t *testing.T) {
	const want = "\\if :auth_projection\nGRANT DELETE ON TABLE acr.credential_lifecycle_outbox TO :\"runtime_user\";\n\\endif"
	if block := RenderCompose(); !strings.Contains(block, want) {
		t.Errorf("the generated compose block lacks the gated DELETE:\n%s", block)
	}
	raw, err := os.ReadFile(composeScript)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), want) {
		t.Errorf("%s lacks the gated DELETE", composeScript)
	}
}
