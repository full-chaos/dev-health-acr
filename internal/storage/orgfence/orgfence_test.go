package orgfence

import (
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyNamesOnlyThePurgedOrganizationRefusal(t *testing.T) {
	refusal := fmt.Errorf("insert row: %w", &pgconn.PgError{Code: PurgedSQLState, Message: "organization was purged"})
	if !Refused(refusal) || !errors.Is(Classify(refusal), storage.ErrOrgPurged) {
		t.Fatalf("the fence's refusal must classify as storage.ErrOrgPurged, got %v", Classify(refusal))
	}
	if !Refused(fmt.Errorf("save: %w", storage.ErrOrgPurged)) {
		t.Fatal("an already classified refusal must stay a refusal")
	}
	for _, other := range []error{
		nil,
		errors.New("organization was purged"),
		&pgconn.PgError{Code: IsolationSQLState},
		&pgconn.PgError{Code: "23505"},
		storage.ErrUnavailable,
	} {
		if Refused(other) {
			t.Fatalf("%v must not classify as a purged organization", other)
		}
		if got := Classify(other); got != other {
			t.Fatalf("Classify changed an unrelated error: %v -> %v", other, got)
		}
	}
}
