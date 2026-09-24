package mcp_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
)

func TestServeOptionsOAuthDiscoveryDomain(t *testing.T) {
	for _, tc := range []struct {
		name, resource, server, setting string
	}{
		{"both absent", "", "", ""},
		{"both set", "https://mcp.example.test/mcp", "https://acr.example.test", ""},
		{"loopback http", "http://127.0.0.1:8081/mcp", "http://127.0.0.1:8080", ""},
		{"resource only", "https://mcp.example.test/mcp", "", acrmcp.AuthorizationServerEnvironment},
		{"server only", "", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"resource path differs from base path", "https://mcp.example.test/other", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"resource root path", "https://mcp.example.test", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"resource http non-loopback", "http://mcp.example.test/mcp", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"resource query", "https://mcp.example.test/mcp?x=1", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"resource fragment", "https://mcp.example.test/mcp#x", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"resource userinfo", "https://u@mcp.example.test/mcp", "https://acr.example.test", acrmcp.ResourceURLEnvironment},
		{"server with path", "https://mcp.example.test/mcp", "https://acr.example.test/api", acrmcp.AuthorizationServerEnvironment},
		{"server trailing slash", "https://mcp.example.test/mcp", "https://acr.example.test/", acrmcp.AuthorizationServerEnvironment},
		{"server http non-loopback", "https://mcp.example.test/mcp", "http://acr.example.test", acrmcp.AuthorizationServerEnvironment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{acrmcp.ResourceURLEnvironment: tc.resource, acrmcp.AuthorizationServerEnvironment: tc.server}
			opts, err := acrmcp.ServeOptionsFromEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			err = opts.Validate()
			if tc.setting == "" {
				if err != nil {
					t.Fatalf("valid discovery settings refused: %v", err)
				}
				return
			}
			var invalid *acrmcp.ErrServeOptionInvalid
			if !errors.As(err, &invalid) || invalid.Setting != tc.setting {
				t.Fatalf("err = %v, want invalid %s", err, tc.setting)
			}
		})
	}
}

func TestProtectedResourceMetadataAndChallenge(t *testing.T) {
	hosted := newHostedAPI(t)
	cfg := hosted.sidecarConfig()
	opts := acrmcp.DefaultServeOptions()
	opts.Transport = acrmcp.TransportHTTP
	opts.ResourceURL = "https://mcp.example.test/mcp"
	opts.AuthorizationServer = "https://acr.example.test"
	handler, err := acrmcp.NewServeHTTPHandler(cfg, testIdentity, &syncBuffer{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{acrmcp.ProtectedResourceMetadataPath + "/mcp", acrmcp.ProtectedResourceMetadataPath} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		var metadata struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			ScopesSupported      []string `json:"scopes_supported"`
		}
		if recorder.Code != http.StatusOK || json.NewDecoder(recorder.Body).Decode(&metadata) != nil {
			t.Fatalf("%s: status %d", path, recorder.Code)
		}
		if metadata.Resource != opts.ResourceURL || len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != opts.AuthorizationServer {
			t.Fatalf("%s: metadata %+v", path, metadata)
		}
	}
	for name, header := range map[string]string{"missing": "", "malformed": "Bearer not-an-acr-token"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(rawToolsList())))
		request.Header.Set("Content-Type", "application/json")
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		handler.ServeHTTP(recorder, request)
		challenge := recorder.Header().Get("WWW-Authenticate")
		if recorder.Code != http.StatusUnauthorized || !strings.HasPrefix(challenge, `Bearer resource_metadata="https://mcp.example.test/.well-known/oauth-protected-resource/mcp", scope="context:read evidence:read"`) {
			t.Fatalf("%s bearer: status %d challenge %q", name, recorder.Code, challenge)
		}
		if name == "malformed" && !strings.HasSuffix(challenge, `error="invalid_token"`) {
			t.Fatalf("malformed bearer challenge lost its error: %q", challenge)
		}
	}

	// Discovery off: no metadata route and the bare challenge.
	off := acrmcp.DefaultServeOptions()
	off.Transport = acrmcp.TransportHTTP
	plain, err := acrmcp.NewServeHTTPHandler(cfg, testIdentity, &syncBuffer{}, off)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	plain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, acrmcp.ProtectedResourceMetadataPath+"/mcp", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("metadata without discovery: status %d, want 404", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(rawToolsList())))
	plain.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("challenge without discovery = %q, want Bearer", got)
	}
}

// CHAOS-6218: the endpoint answers at "/" as well as the base path with the
// same challenge; metadata, probes and the resource identity are unchanged.
func TestRootAliasServesSameEndpoint(t *testing.T) {
	hosted := newHostedAPI(t)
	cfg := hosted.sidecarConfig()
	opts := acrmcp.DefaultServeOptions()
	opts.Transport = acrmcp.TransportHTTP
	opts.ResourceURL = "https://mcp.example.test/mcp"
	opts.AuthorizationServer = "https://acr.example.test"
	handler, err := acrmcp.NewServeHTTPHandler(cfg, testIdentity, &syncBuffer{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/mcp"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(rawToolsList())))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, request)
		challenge := recorder.Header().Get("WWW-Authenticate")
		if recorder.Code != http.StatusUnauthorized || !strings.HasPrefix(challenge, `Bearer resource_metadata="https://mcp.example.test/.well-known/oauth-protected-resource/mcp"`) {
			t.Fatalf("%s: status %d challenge %q", path, recorder.Code, challenge)
		}
	}
	for path, want := range map[string]int{"/healthz": http.StatusOK, acrmcp.ProtectedResourceMetadataPath: http.StatusOK, acrmcp.ProtectedResourceMetadataPath + "/mcp": http.StatusOK, "/other": http.StatusNotFound} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != want {
			t.Fatalf("%s: status %d, want %d", path, recorder.Code, want)
		}
	}
}
