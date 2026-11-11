package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-13562 (acp ruling, preview): the device preview reads the grant from
// the body (repository_scopes) like the approval; the claim is optional and a
// present claim must equal the body. A preview whose body names no list keeps
// today's form: the claim carries it (version skew with the live web).

func (h chaos13562DeviceHarness) startWithHints(t *testing.T, hints []string) contractsv1.DeviceAuthorizationResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{
		SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema, RepositoryHints: &hints,
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(recorder.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	return authorization
}

// preview sends a raw preview body signed with assertion.
func (h chaos13562DeviceHarness) preview(t *testing.T, body []byte, assertion chaos13562Assertion, jti string) *httptest.ResponseRecorder {
	t.Helper()
	assertion.key, assertion.now, assertion.subject, assertion.org = h.key, h.now, "user_123", "org_1"
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, assertion.sign(t, http.MethodPost, "/api/v1/oauth/device_approval", body, jti))
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func chaos13562PreviewBody(t *testing.T, userCode string, scopes any) []byte {
	t.Helper()
	fields := map[string]any{"schema_version": contractsv1.DeviceApprovalPreviewRequestSchema, "user_code": userCode}
	if scopes != nil {
		fields["repository_scopes"] = scopes
	}
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func chaos13562PreviewHints(t *testing.T, recorder *httptest.ResponseRecorder) []string {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status = %d body=%.300s, want 200", recorder.Code, recorder.Body.String())
	}
	var preview contractsv1.DeviceApprovalPreviewResponse
	if err := json.NewDecoder(recorder.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	return preview.RepositoryHints
}

func TestCHAOS13562_PreviewTakesTheGrantFromTheBody(t *testing.T) {
	hints := []string{"example-org/a", "example-org/b"}
	cells := []struct {
		name      string
		body      []string
		assertion chaos13562Assertion
		want      []string
	}{
		{"claim omitted, body names one", []string{"example-org/a"}, chaos13562Assertion{omitScopes: true}, []string{"example-org/a"}},
		{"claim omitted, body organization-wide", []string{"*"}, chaos13562Assertion{omitScopes: true}, hints},
		{"claim equal to the body", []string{"example-org/b"}, chaos13562Assertion{scopes: []string{"example-org/b"}}, []string{"example-org/b"}},
		{"claim omitted, body 256 max-length with one hint", append(chaos13562Slugs(255), "example-org/a"), chaos13562Assertion{omitScopes: true}, []string{"example-org/a"}},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			h := newCHAOS13562DeviceHarness(t)
			authorization := h.startWithHints(t, hints)
			got := chaos13562PreviewHints(t, h.preview(t, chaos13562PreviewBody(t, authorization.UserCode, cell.body), cell.assertion, "preview"))
			if fmt.Sprint(got) != fmt.Sprint(cell.want) {
				t.Fatalf("hints = %v, want %v", got, cell.want)
			}
		})
	}
}

func TestCHAOS13562_PreviewRefusesAClaimThatDiffersFromTheBody(t *testing.T) {
	hints := []string{"example-org/a", "example-org/b"}
	for _, cell := range []struct {
		name        string
		claim, body []string
	}{
		{"claim wider than body", []string{"example-org/a", "example-org/b"}, []string{"example-org/a"}},
		{"claim narrower than body", []string{"example-org/a"}, []string{"example-org/a", "example-org/b"}},
		{"claim organization-wide, body named", []string{"*"}, []string{"example-org/a"}},
		{"claim named, body organization-wide", []string{"example-org/a"}, []string{"*"}},
	} {
		t.Run(cell.name, func(t *testing.T) {
			h := newCHAOS13562DeviceHarness(t)
			authorization := h.startWithHints(t, hints)
			recorder := h.preview(t, chaos13562PreviewBody(t, authorization.UserCode, cell.body), chaos13562Assertion{scopes: cell.claim}, "preview")
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("claim %v body %v: status = %d body=%s, want 400", cell.claim, cell.body, recorder.Code, recorder.Body.String())
			}
			// The same request with the claim equal to the body is served.
			if got := chaos13562PreviewHints(t, h.preview(t, chaos13562PreviewBody(t, authorization.UserCode, cell.body), chaos13562Assertion{scopes: cell.body}, "again")); len(got) == 0 {
				t.Fatal("matching preview returned no hints")
			}
		})
	}
}

func TestCHAOS13562_PreviewRefuses257RepositoriesByName(t *testing.T) {
	h := newCHAOS13562DeviceHarness(t)
	authorization := h.start(t)
	recorder := h.preview(t, chaos13562PreviewBody(t, authorization.UserCode, chaos13562Slugs(chaos13562Bound+1)), chaos13562Assertion{omitScopes: true}, "preview")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%.300s, want 400", recorder.Code, recorder.Body.String())
	}
	details := chaos13562Details(t, recorder)
	if details["reason"] != "too_many_repositories" || details["max_repository_scopes"] != float64(chaos13562Bound) {
		t.Fatalf("details = %v, want too_many_repositories 256", details)
	}
}

// A preview body without a list keeps today's form: the claim carries the
// grant. Without either, or with a null list, the preview is refused.
func TestCHAOS13562_PreviewWithoutABodyListUsesTheClaim(t *testing.T) {
	hints := []string{"example-org/a", "example-org/b"}
	h := newCHAOS13562DeviceHarness(t)
	authorization := h.startWithHints(t, hints)
	got := chaos13562PreviewHints(t, h.preview(t, chaos13562PreviewBody(t, authorization.UserCode, nil), chaos13562Assertion{scopes: []string{"example-org/b"}}, "legacy"))
	if fmt.Sprint(got) != "[example-org/b]" {
		t.Fatalf("legacy preview hints = %v, want [example-org/b]", got)
	}
	for _, cell := range []struct {
		name      string
		body      []byte
		assertion chaos13562Assertion
	}{
		{"no list, no claim", chaos13562PreviewBody(t, authorization.UserCode, nil), chaos13562Assertion{omitScopes: true}},
		{"null list, claim present", []byte(`{"schema_version":"device_approval_preview_request.v1","user_code":"` + authorization.UserCode + `","repository_scopes":null}`), chaos13562Assertion{scopes: []string{"example-org/b"}}},
		{"empty list, claim present", chaos13562PreviewBody(t, authorization.UserCode, []string{}), chaos13562Assertion{scopes: []string{"example-org/b"}}},
	} {
		recorder := h.preview(t, cell.body, cell.assertion, cell.name)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s, want 400", cell.name, recorder.Code, recorder.Body.String())
		}
	}
}
