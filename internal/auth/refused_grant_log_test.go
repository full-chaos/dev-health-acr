package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCredentialLookupLogNamesTheTableOfARefusedGrantAndNoSQL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class *storage.DependencyErrorClass
		want  string
	}{
		{"refused grant", &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "client_credentials"}, "client_credentials"},
		{"table with SQL in it", &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "x; DROP TABLE y"}, ""},
		{"table with a password", &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "postgres://u:secret@h/db"}, ""},
		{"other class names no table", &storage.DependencyErrorClass{SQLState: "23505", Class: "unique_violation", Table: "client_credentials"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingCredentialStore{CredentialStore: newMemoryCredentialStore(t), err: fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, tc.class)}
			var buf bytes.Buffer
			authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
				Now: func() time.Time { return time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC) }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := make([]byte, tokenSecretBytes)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(raw))
			authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("store fault admitted the request")
			})).ServeHTTP(httptest.NewRecorder(), request)
			var record map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &record); err != nil {
				t.Fatalf("not one JSON record: %q: %v", buf.String(), err)
			}
			got, _ := record["db_table"].(string)
			if got != tc.want {
				t.Fatalf("db_table = %q, want %q: %s", got, tc.want, buf.String())
			}
			if strings.Contains(buf.String(), "secret") || strings.Contains(buf.String(), "DROP") {
				t.Fatalf("the log carries text from the error: %s", buf.String())
			}
		})
	}
}
