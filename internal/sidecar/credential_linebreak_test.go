package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type linebreakRow struct {
	name   string
	value  func(token string) string
	accept bool
}

func linebreakRows() []linebreakRow {
	return []linebreakRow{
		{"trailing_lf", func(t string) string { return t + "\n" }, true},
		{"trailing_crlf", func(t string) string { return t + "\r\n" }, true},
		{"leading_and_trailing_space", func(t string) string { return " " + t + " " }, true},
		{"embedded_lf", func(t string) string { return t[:20] + "\n" + t[20:] }, false},
		{"embedded_cr", func(t string) string { return t[:20] + "\r" + t[20:] }, false},
	}
}

func TestLinebreakEverySourceTrimsTheEdgesAndRefusesTheMiddle(t *testing.T) {
	control := validTestToken(31)
	for _, source := range matrixSources() {
		for _, row := range linebreakRows() {
			t.Run(source.name+"/"+row.name, func(t *testing.T) {
				source.install(t, row.value(control))

				result, err := LoadCredential()

				if row.accept {
					if err != nil || result.Token != control {
						t.Fatalf("LoadCredential = %v, want the trimmed control credential", err)
					}
					return
				}
				if !errors.Is(err, ErrCredentialShapeInvalid) {
					t.Fatalf("LoadCredential error = %v, want ErrCredentialShapeInvalid", err)
				}
			})
		}
	}
}

func TestLinebreakPersistReplaceAndVerifyTrimTheEdgesAndRefuseTheMiddle(t *testing.T) {
	control := validTestToken(32)
	for _, row := range linebreakRows() {
		t.Run(row.name, func(t *testing.T) {
			value := row.value(control)
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenFileEnvironment, writeTokenFile(t, ""))
			t.Setenv(TokenKeyringDisabledEnvironment, "true")
			t.Setenv("HOME", t.TempDir())

			_, persistErr := PersistCredential(value)
			replaceErr := ReplaceCredential(CredentialResult{Source: "file"}, value)
			verifyErr := VerifyCredential(CredentialResult{Source: "file"}, value)

			if row.accept {
				if errors.Is(persistErr, ErrCredentialShapeInvalid) || errors.Is(replaceErr, ErrCredentialShapeInvalid) || errors.Is(verifyErr, ErrCredentialShapeInvalid) {
					t.Fatalf("an edge-padded control credential was refused as malformed: %v %v %v", persistErr, replaceErr, verifyErr)
				}
				return
			}
			for name, err := range map[string]error{"persist": persistErr, "replace": replaceErr, "verify": verifyErr} {
				if !errors.Is(err, ErrCredentialShapeInvalid) {
					t.Fatalf("%s error = %v, want ErrCredentialShapeInvalid", name, err)
				}
			}
		})
	}
}

func TestLinebreakTransportRefusesBeforeAnySend(t *testing.T) {
	control := validTestToken(33)
	for _, row := range linebreakRows() {
		if row.accept && row.name != "trailing_lf" && row.name != "trailing_crlf" {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			var served int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				served++
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(row.value(control)))
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.RotateOwnCredential(context.Background())

			if !errors.Is(err, ErrCredentialShapeInvalid) {
				t.Fatalf("error = %v, want ErrCredentialShapeInvalid", err)
			}
			if served != 0 {
				t.Fatalf("server saw %d requests, want 0", served)
			}
		})
	}
}

func TestLinebreakLifecycleResponsesRefuseALineBreakInTheIssuedToken(t *testing.T) {
	control := validTestToken(34)
	for _, row := range linebreakRows() {
		if row.accept && row.name != "trailing_lf" && row.name != "trailing_crlf" {
			continue
		}
		t.Run("poll/"+row.name, func(t *testing.T) {
			response := lifecycleResponseMap(t, "device_token_response.v1.json")
			response["access_token"] = row.value(control)
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}

			_, err = newRawLifecycleClient(t, raw).PollDeviceToken(context.Background(), strings.Repeat("d", 32))

			if !errors.Is(err, ErrCredentialShapeInvalid) {
				t.Fatalf("PollDeviceToken error = %v, want ErrCredentialShapeInvalid", err)
			}
		})
		t.Run("rotate/"+row.name, func(t *testing.T) {
			response := lifecycleResponseMap(t, "credential_rotate_response.v1.json")
			response["access_token"] = row.value(control)
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}

			_, err = rawLifecycleOperationClient(t, http.StatusOK, raw).RotateOwnCredential(context.Background())

			if !errors.Is(err, ErrCredentialShapeInvalid) {
				t.Fatalf("RotateOwnCredential error = %v, want ErrCredentialShapeInvalid", err)
			}
		})
		t.Run("exchange/"+row.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": row.value(control), "expires_in": 600})
			}))
			defer server.Close()
			endpoint, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
				TokenEndpoint: endpoint, SubjectTokenFile: writeSubjectTokenFile(t, "jwt"),
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := source(); !errors.Is(err, ErrCredentialShapeInvalid) {
				t.Fatalf("exchange error = %v, want ErrCredentialShapeInvalid", err)
			}
		})
	}
}
