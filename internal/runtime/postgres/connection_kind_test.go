package postgres

import "testing"

func TestParseConnectionKind_acceptsDirectAndPgBouncer(t *testing.T) {
	tests := []struct {
		raw  string
		want ConnectionKind
	}{
		{raw: "direct", want: ConnectionKindDirect},
		{raw: "pgbouncer", want: ConnectionKindPgBouncer},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			// When
			got, err := ParseConnectionKind(test.raw)

			// Then
			if err != nil || got != test.want {
				t.Fatalf("ParseConnectionKind(%q) = (%q, %v), want (%q, nil)", test.raw, got, err, test.want)
			}
		})
	}
}

func TestParseConnectionKind_rejectsUnknownValues(t *testing.T) {
	for _, raw := range []string{"", "auto", "Direct", "PGBOUNCER"} {
		t.Run(raw, func(t *testing.T) {
			// When
			_, err := ParseConnectionKind(raw)

			// Then
			if err == nil {
				t.Fatalf("ParseConnectionKind(%q) accepted an invalid connection kind", raw)
			}
		})
	}
}

func TestValidateConnectionKind_rejectsDirectWithPoolerAdminDSN(t *testing.T) {
	// Given / When
	err := ValidateConnectionKind(ConnectionKindDirect, "postgres://pooler-admin")

	// Then
	if err == nil {
		t.Fatal("direct connection kind accepted a PgBouncer administration DSN")
	}
}

func TestValidateConnectionKind_rejectsPgBouncerWithoutPoolerAdminDSN(t *testing.T) {
	// Given / When
	err := ValidateConnectionKind(ConnectionKindPgBouncer, "")

	// Then
	if err == nil {
		t.Fatal("pgbouncer connection kind accepted a missing administration DSN")
	}
}

func TestValidateConnectionKind_acceptsConsistentConfigurations(t *testing.T) {
	tests := []struct {
		name           string
		kind           ConnectionKind
		poolerAdminDSN string
	}{
		{name: "direct without admin DSN", kind: ConnectionKindDirect, poolerAdminDSN: ""},
		{name: "pgbouncer with admin DSN", kind: ConnectionKindPgBouncer, poolerAdminDSN: "postgres://pooler-admin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			err := ValidateConnectionKind(test.kind, test.poolerAdminDSN)

			// Then
			if err != nil {
				t.Fatalf("ValidateConnectionKind(%q, %q) = %v, want nil", test.kind, test.poolerAdminDSN, err)
			}
		})
	}
}
