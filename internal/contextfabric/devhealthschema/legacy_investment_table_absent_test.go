package devhealthschema_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// legacyInvestmentTable is the deprecated rule classifier's daily table. No reader in
// this module reads it (a project's, a team's and a repository's investment is the
// canonical work_unit_investments mix), so the production declaration must not carry it:
// a declared table reads as a supported source and keeps fixtures building it.
const legacyInvestmentTable = "investment_metrics_daily"

func TestLegacyInvestmentTableIsNotDeclared(t *testing.T) {
	if _, declared := devhealthschema.ProductionColumns[legacyInvestmentTable]; declared {
		t.Errorf("ProductionColumns declares %q: the deprecated legacy table must not be declared", legacyInvestmentTable)
	}
	if _, declared := devhealthschema.EngineFull[legacyInvestmentTable]; declared {
		t.Errorf("EngineFull declares %q: the deprecated legacy table must not be declared", legacyInvestmentTable)
	}
}
