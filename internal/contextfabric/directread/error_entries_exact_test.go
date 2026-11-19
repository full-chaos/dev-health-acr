package directread

import "testing"

// CHAOS-14024: ReadBudgetOf, parseUpstreamGraphQLError and errorEntriesOf read each error entry on its
// own and look the protocol keys up by exact name.
func TestReadBudgetOfEntriesAndKeys(t *testing.T) {
	const (
		budget  = `{"extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"bytes_ceiling"}}`
		numCode = `{"extensions":{"code":7}}`
		objRsn  = `{"extensions":{"reason":{"x":1}}}`
	)
	for _, tc := range []struct {
		name string
		body string
		want ReadBudgetReason
	}{
		{"numeric code before", `{"errors":[` + numCode + `,` + budget + `]}`, ReadBudgetBytes},
		{"numeric code after", `{"errors":[` + budget + `,` + numCode + `]}`, ReadBudgetBytes},
		{"object reason before", `{"errors":[` + objRsn + `,` + budget + `]}`, ReadBudgetBytes},
		{"object reason after", `{"errors":[` + budget + `,` + objRsn + `]}`, ReadBudgetBytes},
		{"typed entry with object reason", `{"errors":[{"extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":{"x":1}}}]}`, ReadBudgetUnknown},
		{"aliased errors key alone", `{"Errors":[` + budget + `]}`, ""},
		{"aliased errors key beside canonical", `{"Errors":[` + budget + `],"errors":[]}`, ""},
		{"aliased extensions key alone", `{"errors":[{"Extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"bytes_ceiling"}}]}`, ""},
		{"aliased extensions key beside canonical", `{"errors":[{"Extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"bytes_ceiling"},"extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"time_ceiling"}}]}`, ReadBudgetTime},
		{"aliased code key alone", `{"errors":[{"extensions":{"Code":"MCP_READ_BUDGET_EXCEEDED","reason":"bytes_ceiling"}}]}`, ""},
		{"aliased code key beside canonical", `{"errors":[{"extensions":{"Code":"X","code":"MCP_READ_BUDGET_EXCEEDED","reason":"bytes_ceiling"}}]}`, ReadBudgetBytes},
		{"aliased reason key alone", `{"errors":[{"extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","Reason":"bytes_ceiling"}}]}`, ReadBudgetUnknown},
		{"aliased reason key beside canonical", `{"errors":[{"extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","Reason":"bytes_ceiling","reason":"time_ceiling"}}]}`, ReadBudgetTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReadBudgetOf([]byte(tc.body)); got != tc.want {
				t.Fatalf("ReadBudgetOf=%q want %q", got, tc.want)
			}
		})
	}
}

func TestParseUpstreamGraphQLErrorEntriesAndKeys(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantCode UpstreamGraphQLCode
		wantVar  string
	}{
		{"typed first entry, object reason", `{"errors":[{"path":["variable","org"],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED","reason":{"x":1}}}]}`, GraphQLCodeValidationFailed, "org"},
		{"numeric code first", `{"errors":[{"extensions":{"code":7},"path":["variable","org"]}]}`, "", "org"},
		{"malformed second entry", `{"errors":[{"extensions":{"code":"MCP_REFUSED"}},{"extensions":{"code":7}}]}`, GraphQLCodeMCPRefused, ""},
		{"extensions of wrong type", `{"errors":[{"extensions":"x","path":["variable","org"]}]}`, "", "org"},
		{"aliased errors key alone", `{"Errors":[{"extensions":{"code":"MCP_REFUSED"}}]}`, "", ""},
		{"aliased extensions key alone", `{"errors":[{"Extensions":{"code":"MCP_REFUSED"}}]}`, "", ""},
		{"aliased extensions beside canonical", `{"errors":[{"Extensions":{"code":"MCP_REFUSED"},"extensions":{"code":"GRAPHQL_PARSE_FAILED"}}]}`, GraphQLCodeParseFailed, ""},
		{"aliased code alone", `{"errors":[{"extensions":{"Code":"MCP_REFUSED"}}]}`, "", ""},
		{"aliased code beside canonical", `{"errors":[{"extensions":{"Code":"MCP_REFUSED","code":"GRAPHQL_PARSE_FAILED"}}]}`, GraphQLCodeParseFailed, ""},
		{"aliased path alone", `{"errors":[{"Path":["variable","org"]}]}`, "", ""},
		{"aliased path beside canonical", `{"errors":[{"Path":["variable","bad"],"path":["variable","org"]}]}`, "", "org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, v := parseUpstreamGraphQLError([]byte(tc.body))
			if code != tc.wantCode || v != tc.wantVar {
				t.Fatalf("got %q %q want %q %q", code, v, tc.wantCode, tc.wantVar)
			}
		})
	}
}

func TestListenerRefusalOfExactKeys(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       ListenerRefusalClass
		wantReason string
	}{
		{"aliased errors alone", `{"Errors":[{"extensions":{"code":"MCP_REFUSED","reason":"no_carrier"}}]}`, "", ""},
		{"aliased extensions alone", `{"errors":[{"Extensions":{"code":"MCP_REFUSED","reason":"no_carrier"}}]}`, "", ""},
		{"aliased reason beside canonical", `{"errors":[{"extensions":{"code":"MCP_REFUSED","Reason":"not_a_query","reason":"no_carrier"}}]}`, ListenerRefusalCarrier, "no_carrier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r := listenerRefusalOf([]byte(tc.body))
			if c != tc.want || r != tc.wantReason {
				t.Fatalf("got %q %q want %q %q", c, r, tc.want, tc.wantReason)
			}
		})
	}
}

// A duplicated canonical key keeps its first value (the struct decode this replaced kept a first object against
// a later null), so a typed refusal is not hidden by a trailing null.
func TestErrorEntriesDuplicateCanonicalKeyFirstWins(t *testing.T) {
	typed := `{"code":"MCP_REFUSED","reason":"no_carrier"}`
	for _, tc := range []struct{ name, body string }{
		{"extensions then null", `{"errors":[{"extensions":` + typed + `,"extensions":null}]}`},
		{"code then null", `{"errors":[{"extensions":{"code":"MCP_REFUSED","code":null,"reason":"no_carrier"}}]}`},
		{"errors then null", `{"errors":[{"extensions":` + typed + `}],"errors":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r := listenerRefusalOf([]byte(tc.body))
			if c != ListenerRefusalCarrier || r != "no_carrier" {
				t.Fatalf("got %q %q", c, r)
			}
		})
	}
	for _, body := range []string{`{"errors":[]} x`, `{"errors":[{"extensions":{"code":"MCP_REFUSED"}}]`, `[]`, ``} {
		if got := errorEntriesOf([]byte(body)); len(got) != 0 {
			t.Fatalf("%q: got %d entries", body, len(got))
		}
	}
}
