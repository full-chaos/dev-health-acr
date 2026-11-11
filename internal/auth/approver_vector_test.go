package auth

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CHAOS-13586 (B-2): web's real signers made testdata/web-approver.v1.json
// (web src/lib/acr/__tests__/approver-vector.test.ts, which fails in web CI
// when the signers emit other bytes). This runs the REAL web assertion
// verifier and the REAL pair check on it. web-approver.v1.source names the web
// commit and the file's sha256.

type webApproverVector struct {
	SchemaVersion     string          `json:"schema_version"`
	Clock             int64           `json:"clock"`
	Issuer            string          `json:"issuer"`
	AssertionAudience string          `json:"assertion_audience"`
	JWKS              json.RawMessage `json:"jwks"`
	Method            string          `json:"method"`
	Principal         struct {
		OrgID  string `json:"org_id"`
		SID    string `json:"sid"`
		UserID string `json:"user_id"`
	} `json:"principal"`
	Cases []struct {
		Name              string `json:"name"`
		Path              string `json:"path"`
		Body              string `json:"body"`
		WebAssertion      string `json:"web_assertion"`
		ApproverReference string `json:"approver_reference"`
		Expect            struct {
			ACRPair string `json:"acr_pair"`
		} `json:"expect"`
	} `json:"cases"`
}

func readWebApproverVector(t *testing.T) webApproverVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "web-approver.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	source, err := os.Open(filepath.Join("testdata", "web-approver.v1.source"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	recorded := ""
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "sha256 "); ok {
			recorded = value
		}
	}
	if recorded != hex.EncodeToString(digest[:]) {
		t.Fatalf("vector sha256 = %x; the .source file records %q", digest, recorded)
	}
	var vector webApproverVector
	if err := json.Unmarshal(raw, &vector); err != nil || vector.SchemaVersion != "web_approver_vector.v1" || len(vector.Cases) == 0 {
		t.Fatalf("vector: %v", err)
	}
	return vector
}

func verifyVectorCase(t *testing.T, vector webApproverVector, index int, at time.Time) (string, string, error) {
	t.Helper()
	jwks := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(jwks, vector.JWKS, 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{Issuer: vector.Issuer, Audience: vector.AssertionAudience, JWKSPath: jwks, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	c := vector.Cases[index]
	request := httptest.NewRequest(vector.Method, c.Path, strings.NewReader(c.Body))
	request.Header.Set(WebAssertionHeader, c.WebAssertion)
	request.Header.Set(ApproverReferenceHeader, c.ApproverReference)
	principal, err := verifier.Verify(request)
	return principal.ApproverReference, principal.Subject, err
}

func TestWebApproverVector_realSignerPairs(t *testing.T) {
	vector := readWebApproverVector(t)
	clock := time.Unix(vector.Clock, 0).UTC()
	for i, c := range vector.Cases {
		t.Run(c.Name, func(t *testing.T) {
			reference, subject, err := verifyVectorCase(t, vector, i, clock)
			accepted := err == nil && reference == c.ApproverReference
			if want := c.Expect.ACRPair == "accept"; accepted != want {
				t.Fatalf("pair accepted = %v (err %v); the vector expects %s", accepted, err, c.Expect.ACRPair)
			}
			if accepted && subject != vector.Principal.UserID {
				t.Fatalf("subject = %q; want the vector's user id %q", subject, vector.Principal.UserID)
			}
		})
	}
}

func TestWebApproverVector_refusedAfterItsLife(t *testing.T) {
	vector := readWebApproverVector(t)
	reference, _, err := verifyVectorCase(t, vector, 0, time.Unix(vector.Clock, 0).UTC().Add(40*time.Second))
	if err == nil && reference != "" {
		t.Fatal("the V1 pair was accepted 40 s after it was signed")
	}
}

// The request method of the vector is the one acr serves the approvals on.
func TestWebApproverVector_isForThePostRoutes(t *testing.T) {
	if vector := readWebApproverVector(t); vector.Method != http.MethodPost {
		t.Fatalf("method = %q", vector.Method)
	}
}
