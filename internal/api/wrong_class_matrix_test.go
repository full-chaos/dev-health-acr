package api

// The two axes of the wrong-credential-class gate, ported from dev-health-ops
// internal/testsupport/wrongclass (CarrierFor, Baseline, LoadClasses): acr
// cannot import an ops internal package, so the carrier rules live here.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

var wcgFiller = strings.Repeat("A", 43)

// wcgCarrierKinds make a request carrier; wcgQualifierKinds only narrow one.
// Every kind of the ops class_disambiguator vocabulary must be one of them.
var (
	wcgCarrierKinds   = []string{"credential_prefix", "authorization_scheme", "dedicated_header", "signature_header", "body_field"}
	wcgQualifierKinds = []string{"jwt_header_alg", "jwt_header_typ", "content_type", "subject_token_type", "grant_type"}
)

type wcgMarker struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Shape string `json:"shape,omitempty"`
}

type wcgClass struct {
	ID         string      `json:"class_id"`
	Markers    []wcgMarker `json:"markers"`
	NoneReason string      `json:"markers_none_reason"`
}

func (c wcgClass) names(kind string) []string {
	var out []string
	for _, marker := range c.Markers {
		if marker.Kind == kind {
			out = append(out, marker.Name)
		}
	}
	return out
}

// notDrivenReason is why a class gets no carrier: its own markers_none_reason,
// or the qualifier-only markers that name no request-head carrier.
func (c wcgClass) notDrivenReason() string {
	if reason := strings.TrimSpace(c.NoneReason); reason != "" {
		return reason
	}
	if len(c.Markers) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(c.Markers))
	for _, marker := range c.Markers {
		kinds = append(kinds, marker.Kind)
	}
	return "only qualifiers: " + strings.Join(kinds, ",")
}

// wcgVocabulary reads the class_disambiguator kind enum of the ops
// endpoint-profile.schema.json.
func wcgVocabulary(raw []byte) ([]string, error) {
	var schema struct {
		Defs struct {
			EndpointProfile struct {
				Properties struct {
					ClassDisambiguator struct {
						Properties struct {
							Kind struct {
								Enum []string `json:"enum"`
							} `json:"kind"`
						} `json:"properties"`
					} `json:"class_disambiguator"`
				} `json:"properties"`
			} `json:"endpointProfile"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	kinds := schema.Defs.EndpointProfile.Properties.ClassDisambiguator.Properties.Kind.Enum
	if len(kinds) == 0 {
		return nil, fmt.Errorf("wrongclass: the schema has no class_disambiguator kind enum")
	}
	return kinds, nil
}

// wcgLoadClasses parses credential-classes.json. It fails on an empty
// registry, on a vocabulary kind with no carrier or qualifier rule, on a
// marker kind with no rule, and on a class with no marker and no reason.
func wcgLoadClasses(raw []byte, vocabulary []string) ([]wcgClass, error) {
	known := append(slices.Clone(wcgCarrierKinds), wcgQualifierKinds...)
	for _, kind := range vocabulary {
		if !slices.Contains(known, kind) {
			return nil, fmt.Errorf("wrongclass: class_disambiguator kind %q has no carrier or qualifier rule", kind)
		}
	}
	var document struct {
		Classes []wcgClass `json:"classes"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if len(document.Classes) == 0 {
		return nil, fmt.Errorf("wrongclass: no credential class")
	}
	for _, class := range document.Classes {
		if class.ID == "" {
			return nil, fmt.Errorf("wrongclass: a class has no class_id")
		}
		if len(class.Markers) == 0 && strings.TrimSpace(class.NoneReason) == "" {
			return nil, fmt.Errorf("wrongclass: class %s has no marker and no markers_none_reason", class.ID)
		}
		for _, marker := range class.Markers {
			if !slices.Contains(known, marker.Kind) {
				return nil, fmt.Errorf("wrongclass: class %s: marker kind %q has no carrier rule", class.ID, marker.Kind)
			}
		}
	}
	return document.Classes, nil
}

// wcgMint signs a JWT with the given protected-header alg and typ ("" for none).
type wcgMint func(alg, typ string) string

// wcgCarrier is a class as one request presents it: a header, or a body field.
type wcgCarrier struct {
	Class     string
	Header    string
	Value     string
	BodyField string
}

const wcgAuthorization = "Authorization"

// wcgCarrierFor is ops wrongclass.CarrierFor. ok is false for a class no
// carrier kind presents; an error is a class whose markers cannot make a
// carrier distinct from the baseline, and fails the gate.
func wcgCarrierFor(class wcgClass, mint wcgMint) (wcgCarrier, bool, error) {
	carrier := wcgCarrier{Class: class.ID}
	switch {
	case len(class.names("credential_prefix")) > 0:
		carrier.Header, carrier.Value = wcgAuthorization, "Bearer "+class.names("credential_prefix")[0]+wcgFiller
	case len(class.names("authorization_scheme")) > 0:
		if scheme := class.names("authorization_scheme"); len(scheme) != 1 || scheme[0] != "Bearer" {
			return wcgCarrier{}, false, fmt.Errorf("wrongclass: class %s: authorization scheme %v has no carrier rule", class.ID, scheme)
		}
		algs, typs := class.names("jwt_header_alg"), class.names("jwt_header_typ")
		if len(algs) != 1 || len(typs) > 1 {
			return wcgCarrier{}, false, fmt.Errorf("wrongclass: class %s: a Bearer class needs a prefix or one alg (algs %v, typs %v)", class.ID, algs, typs)
		}
		typ := ""
		if len(typs) == 1 {
			typ = typs[0]
		}
		if mint == nil {
			return wcgCarrier{}, false, fmt.Errorf("wrongclass: class %s: no minter for alg %s typ %q", class.ID, algs[0], typ)
		}
		token := mint(algs[0], typ)
		if strings.Count(token, ".") != 2 {
			return wcgCarrier{}, false, fmt.Errorf("wrongclass: class %s: the minter made no JWT for alg %s typ %q", class.ID, algs[0], typ)
		}
		carrier.Header, carrier.Value = wcgAuthorization, "Bearer "+token
	case len(class.names("dedicated_header")) > 0:
		carrier.Header, carrier.Value = class.names("dedicated_header")[0], wcgFiller
	case len(class.names("signature_header")) > 0:
		carrier.Header, carrier.Value = class.names("signature_header")[0], "sha256="+wcgFiller
	case len(class.names("body_field")) > 0:
		for _, marker := range class.Markers {
			if marker.Kind != "body_field" {
				continue
			}
			value, err := wcgShaped(marker.Shape)
			if err != nil {
				return wcgCarrier{}, false, fmt.Errorf("wrongclass: class %s: %w", class.ID, err)
			}
			carrier.BodyField, carrier.Value = marker.Name, value
			break
		}
	default:
		return wcgCarrier{}, false, nil
	}
	return carrier, true, nil
}

func wcgShaped(shape string) (string, error) {
	switch {
	case shape == "jwt":
		enc := base64.RawURLEncoding
		return enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(`{"sub":"wrongclass"}`)) + "." + wcgFiller, nil
	case strings.HasPrefix(shape, "prefix:"):
		return strings.TrimPrefix(shape, "prefix:") + wcgFiller, nil
	case strings.HasPrefix(shape, "opaque:"):
		return strings.TrimPrefix(shape, "opaque:") + wcgFiller, nil
	}
	return "", fmt.Errorf("body_field shape %q has no carrier rule", shape)
}

// Baseline is the carrier's garbage twin: the same Authorization header with a
// credential of no class, the same body field with a value of no class, or no
// credential at all for any other header.
func (c wcgCarrier) Baseline() wcgCarrier {
	switch {
	case c.Header == wcgAuthorization:
		return wcgCarrier{Class: "baseline:authorization", Header: wcgAuthorization, Value: "Bearer " + wcgFiller}
	case c.BodyField != "":
		return wcgCarrier{Class: "baseline:body:" + c.BodyField, BodyField: c.BodyField, Value: wcgFiller}
	}
	return wcgCarrier{Class: "baseline:none"}
}

type wcgRow struct {
	ID             string
	Method         string
	Route          string
	Classification string
	Accepted       []string
}

// wcgLoadRows parses endpoint-profiles.acr.json. A row id must be
// "<method> <route> [<service>]" so a row cannot drift from what it drives.
func wcgLoadRows(raw []byte, service string) ([]wcgRow, error) {
	var document struct {
		Rows []struct {
			ID             string   `json:"id"`
			Service        string   `json:"service"`
			Method         string   `json:"method"`
			Route          string   `json:"route"`
			Classification string   `json:"classification"`
			Accepted       []string `json:"accepted_credential_classes"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if len(document.Rows) == 0 {
		return nil, fmt.Errorf("wrongclass: no endpoint-profile row")
	}
	out := make([]wcgRow, 0, len(document.Rows))
	for _, entry := range document.Rows {
		if entry.Service != service || entry.ID != entry.Method+" "+entry.Route+" ["+service+"]" {
			return nil, fmt.Errorf("wrongclass: row %q is not \"<method> <route> [%s]\"", entry.ID, service)
		}
		if entry.Classification != "protected" && entry.Classification != "public" {
			return nil, fmt.Errorf("wrongclass: row %q: classification %q is neither protected nor public", entry.ID, entry.Classification)
		}
		out = append(out, wcgRow{ID: entry.ID, Method: entry.Method, Route: entry.Route, Classification: entry.Classification, Accepted: entry.Accepted})
	}
	return out, nil
}

func (r wcgRow) accepts(class string) bool { return slices.Contains(r.Accepted, class) }

func (r wcgRow) protected() bool { return r.Classification == "protected" }

var wcgPathParam = regexp.MustCompile(`\{[^}]*\}`)

// path is the row's route with every parameter set to a placeholder.
func (r wcgRow) path() string { return wcgPathParam.ReplaceAllString(r.Route, "x") }

// wcgComparedHeaders are the response headers an answer is compared on besides
// status and body.
var wcgComparedHeaders = []string{"Allow", "Content-Type", "Retry-After", "Www-Authenticate"}

var wcgNonce = regexp.MustCompile(`nonce-[^';"\s]*|nonce="[^"]*"`)

// wcgAnswer is a response with the request id and any CSP nonce masked.
type wcgAnswer struct {
	Status    int
	Body      string
	Header    string
	raw       string
	requestID string
}

func wcgCapture(recorder *httptest.ResponseRecorder) wcgAnswer {
	raw := recorder.Body.String()
	body := raw
	id := recorder.Header().Get("X-Request-ID")
	if id != "" {
		body = strings.ReplaceAll(body, id, "<request-id>")
	}
	body = wcgNonce.ReplaceAllString(body, "nonce")
	var headers []string
	for _, name := range wcgComparedHeaders {
		if values := recorder.Header().Values(name); len(values) > 0 {
			headers = append(headers, name+": "+strings.Join(values, ", "))
		}
	}
	return wcgAnswer{Status: recorder.Code, Body: body, Header: strings.Join(headers, "; "), raw: raw, requestID: id}
}

func (a wcgAnswer) same(b wcgAnswer) bool {
	return a.Status == b.Status && a.Body == b.Body && a.Header == b.Header
}

func (a wcgAnswer) String() string {
	body := a.Body
	if len(body) > 200 {
		body = body[:200] + "..."
	}
	return fmt.Sprintf("%d [%s] %q", a.Status, a.Header, body)
}

type wcgCensus struct {
	Service     string
	Rows        int
	Classes     int
	Driven      int
	WrongCells  int
	AcceptCells int
	NotDriven   []string
}

func (c wcgCensus) line() string {
	notDriven := slices.Clone(c.NotDriven)
	sort.Strings(notDriven)
	return fmt.Sprintf("WRONG_CLASS_GATE_EXECUTED service=%s rows=%d classes=%d driven=%d wrong_cells=%d accept_cells=%d not_driven=%s",
		c.Service, c.Rows, c.Classes, c.Driven, c.WrongCells, c.AcceptCells, strings.Join(notDriven, ","))
}

func wcgClassesDoc(t *testing.T, classes ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"classes": classes})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var wcgTestVocabulary = []string{"authorization_scheme", "credential_prefix", "dedicated_header", "jwt_header_alg", "jwt_header_typ", "signature_header", "content_type", "subject_token_type", "grant_type"}

func TestWrongClassGateLoadClassesRefusesWhatHasNoRule(t *testing.T) {
	for name, class := range map[string]map[string]any{
		"unknown marker kind":     {"class_id": "c", "markers": []map[string]any{{"kind": "cookie", "name": "sid"}}},
		"no marker and no reason": {"class_id": "c", "markers": []map[string]any{}},
		"no marker, blank reason": {"class_id": "c", "markers": []map[string]any{}, "markers_none_reason": "  "},
		"no class id":             {"markers": []map[string]any{}, "markers_none_reason": "r"},
	} {
		if _, err := wcgLoadClasses(wcgClassesDoc(t, class), wcgTestVocabulary); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := wcgLoadClasses(wcgClassesDoc(t), wcgTestVocabulary); err == nil {
		t.Error("an empty registry loaded")
	}
	reasoned := map[string]any{"class_id": "c", "markers": []map[string]any{}, "markers_none_reason": "outbound"}
	if _, err := wcgLoadClasses(wcgClassesDoc(t, reasoned), wcgTestVocabulary); err != nil {
		t.Errorf("a class with a reason and no marker: %v", err)
	}
	if _, err := wcgLoadClasses(wcgClassesDoc(t, reasoned), append(slices.Clone(wcgTestVocabulary), "cookie_name")); err == nil {
		t.Error("a vocabulary kind with no rule loaded")
	}
}

func TestWrongClassGateVocabularyIsReadFromTheSchema(t *testing.T) {
	raw := []byte(`{"$defs":{"endpointProfile":{"properties":{"class_disambiguator":{"properties":{"kind":{"enum":["credential_prefix","grant_type"]}}}}}}}`)
	kinds, err := wcgVocabulary(raw)
	if err != nil || !slices.Equal(kinds, []string{"credential_prefix", "grant_type"}) {
		t.Fatalf("kinds %v err %v", kinds, err)
	}
	if _, err := wcgVocabulary([]byte(`{"$defs":{}}`)); err == nil {
		t.Fatal("a schema with no kind enum gave a vocabulary")
	}
}

func TestWrongClassGateCarrierForEachKind(t *testing.T) {
	mint := func(alg, typ string) string { return "a." + alg + "-" + typ + ".b" }
	cases := []struct {
		markers []wcgMarker
		header  string
		value   string
		body    string
	}{
		{markers: []wcgMarker{{Kind: "credential_prefix", Name: "p_"}, {Kind: "authorization_scheme", Name: "Bearer"}}, header: wcgAuthorization, value: "Bearer p_" + wcgFiller},
		{markers: []wcgMarker{{Kind: "authorization_scheme", Name: "Bearer"}, {Kind: "jwt_header_alg", Name: "HS256"}}, header: wcgAuthorization, value: "Bearer a.HS256-.b"},
		{markers: []wcgMarker{{Kind: "authorization_scheme", Name: "Bearer"}, {Kind: "jwt_header_alg", Name: "EdDSA"}, {Kind: "jwt_header_typ", Name: "at+jwt"}}, header: wcgAuthorization, value: "Bearer a.EdDSA-at+jwt.b"},
		{markers: []wcgMarker{{Kind: "dedicated_header", Name: "X-K"}}, header: "X-K", value: wcgFiller},
		{markers: []wcgMarker{{Kind: "signature_header", Name: "X-S"}}, header: "X-S", value: "sha256=" + wcgFiller},
		{markers: []wcgMarker{{Kind: "body_field", Name: "f", Shape: "prefix:dhr1_"}}, body: "f", value: "dhr1_" + wcgFiller},
		{markers: []wcgMarker{{Kind: "body_field", Name: "f", Shape: "opaque:o_"}}, body: "f", value: "o_" + wcgFiller},
	}
	for _, c := range cases {
		carrier, ok, err := wcgCarrierFor(wcgClass{ID: "c", Markers: c.markers}, mint)
		if err != nil || !ok || carrier.Header != c.header || carrier.Value != c.value || carrier.BodyField != c.body {
			t.Errorf("%v: carrier %+v ok %v err %v", c.markers, carrier, ok, err)
		}
	}
	jwtBody, ok, err := wcgCarrierFor(wcgClass{ID: "c", Markers: []wcgMarker{{Kind: "body_field", Name: "f", Shape: "jwt"}}}, mint)
	if err != nil || !ok || strings.Count(jwtBody.Value, ".") != 2 {
		t.Errorf("jwt body field: %+v ok %v err %v", jwtBody, ok, err)
	}
	for name, markers := range map[string][]wcgMarker{
		"no marker":      nil,
		"qualifier only": {{Kind: "grant_type", Name: "refresh_token"}},
	} {
		if _, ok, err := wcgCarrierFor(wcgClass{ID: "c", Markers: markers}, mint); ok || err != nil {
			t.Errorf("%s: driven %v err %v", name, ok, err)
		}
	}
	for name, markers := range map[string][]wcgMarker{
		"basic scheme":   {{Kind: "authorization_scheme", Name: "Basic"}},
		"bearer, no alg": {{Kind: "authorization_scheme", Name: "Bearer"}},
		"odd body shape": {{Kind: "body_field", Name: "f", Shape: "odd"}},
	} {
		if _, _, err := wcgCarrierFor(wcgClass{ID: "c", Markers: markers}, mint); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, _, err := wcgCarrierFor(wcgClass{ID: "c", Markers: []wcgMarker{{Kind: "authorization_scheme", Name: "Bearer"}, {Kind: "jwt_header_alg", Name: "EdDSA"}}}, func(string, string) string { return "opaque" }); err == nil {
		t.Error("a minter that made no JWT passed")
	}
}

func TestWrongClassGateBaselineIsTheCarriersGarbageTwin(t *testing.T) {
	for carrier, want := range map[wcgCarrier]wcgCarrier{
		{Header: wcgAuthorization, Value: "Bearer x"}: {Class: "baseline:authorization", Header: wcgAuthorization, Value: "Bearer " + wcgFiller},
		{BodyField: "f", Value: "x"}:                  {Class: "baseline:body:f", BodyField: "f", Value: wcgFiller},
		{Header: "X-K", Value: "x"}:                   {Class: "baseline:none"},
	} {
		if got := carrier.Baseline(); got != want {
			t.Errorf("%+v: baseline %+v, want %+v", carrier, got, want)
		}
	}
}

func TestWrongClassGateRowsCarryTheirRoute(t *testing.T) {
	good := []byte(`{"rows":[{"id":"GET /a/{b} [svc]","service":"svc","method":"GET","route":"/a/{b}","classification":"public","accepted_credential_classes":[]}]}`)
	rows, err := wcgLoadRows(good, "svc")
	if err != nil || len(rows) != 1 || rows[0].path() != "/a/x" {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	for name, raw := range map[string]string{
		"id drifted":    `{"rows":[{"id":"GET /a [svc]","service":"svc","method":"GET","route":"/b","classification":"public"}]}`,
		"other service": `{"rows":[{"id":"GET /a [other]","service":"other","method":"GET","route":"/a","classification":"public"}]}`,
		"odd class":     `{"rows":[{"id":"GET /a [svc]","service":"svc","method":"GET","route":"/a","classification":"internal"}]}`,
		"no rows":       `{"rows":[]}`,
	} {
		if _, err := wcgLoadRows([]byte(raw), "svc"); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
