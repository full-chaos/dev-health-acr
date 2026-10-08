package contextfabric

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// Work-unit listing behind an investment allocation. The request rides the
// context, not FactRequirement.Parameters: a parameter is a capability input
// the interpretation model can author and the prompt vocabulary lists, and this
// listing is a direct-read option only.
const (
	// InvestmentUnitsDefaultMax and InvestmentUnitsMaxMax bound one page.
	InvestmentUnitsDefaultMax = 100
	InvestmentUnitsMaxMax     = 150

	// InvestmentUnitKind and InvestmentUnitPageKind are the unit_kind field
	// values of a per-unit fact and of the page summary fact.
	InvestmentUnitKind     = "work_unit_share"
	InvestmentUnitPageKind = "work_unit_page"

	// InvestmentUnitsWeight names the weight the unit shares carry.
	InvestmentUnitsWeight = "share_in_scope = (distinct PR refs of the unit in this repository / distinct refs of the unit) * persisted effort_value; a unit with no PR ref counts its stored repository as one reference"
)

// InvestmentUnitsRequest asks a FactInvestment provider to list the work units
// behind the allocation it serves, one page.
type InvestmentUnitsRequest struct {
	Max    int
	Cursor *InvestmentUnitsCursor
}

// InvestmentUnitsCursor is the keyset position after the last row of a page:
// rows are ordered by share descending, then work unit id, then repository.
type InvestmentUnitsCursor struct {
	Share      float64 `json:"s"`
	WorkUnitID string  `json:"u"`
	RepoID     string  `json:"r"`
}

var errInvestmentUnitsCursor = errors.New("units.cursor is not a cursor this server issued")

// EncodeInvestmentUnitsCursor renders c as an opaque token.
func EncodeInvestmentUnitsCursor(c InvestmentUnitsCursor) string {
	raw, _ := json.Marshal(struct {
		S string `json:"s"`
		U string `json:"u"`
		R string `json:"r"`
	}{strconv.FormatFloat(c.Share, 'g', -1, 64), c.WorkUnitID, c.RepoID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeInvestmentUnitsCursor parses a token EncodeInvestmentUnitsCursor made.
func DecodeInvestmentUnitsCursor(token string) (InvestmentUnitsCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return InvestmentUnitsCursor{}, errInvestmentUnitsCursor
	}
	var wire struct {
		S string `json:"s"`
		U string `json:"u"`
		R string `json:"r"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return InvestmentUnitsCursor{}, errInvestmentUnitsCursor
	}
	share, err := strconv.ParseFloat(wire.S, 64)
	if err != nil || math.IsNaN(share) || math.IsInf(share, 0) || wire.U == "" || wire.R == "" {
		return InvestmentUnitsCursor{}, errInvestmentUnitsCursor
	}
	return InvestmentUnitsCursor{Share: share, WorkUnitID: wire.U, RepoID: wire.R}, nil
}

type investmentUnitsKey struct{}

// WithInvestmentUnits marks ctx so a FactInvestment provider also lists the
// work units behind each allocation it serves.
func WithInvestmentUnits(ctx context.Context, request InvestmentUnitsRequest) context.Context {
	return context.WithValue(ctx, investmentUnitsKey{}, request)
}

// InvestmentUnitsFrom returns the listing request carried by ctx.
func InvestmentUnitsFrom(ctx context.Context) (InvestmentUnitsRequest, bool) {
	request, ok := ctx.Value(investmentUnitsKey{}).(InvestmentUnitsRequest)
	return request, ok
}
