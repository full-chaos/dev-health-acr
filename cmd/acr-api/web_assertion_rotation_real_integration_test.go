package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// rotationKidA is the kid TestWebAssertion_realBinary starts acr-api with.
	rotationKidA       = "current"
	rotationKidB       = "next"
	rotationRetiredLog = `"class":"web_assertion_key_retired"`
)

type rotationKey struct {
	kid         string
	public      ed25519.PublicKey
	retireAfter string
}

type rotationDrill struct {
	t        *testing.T
	baseURL  string
	jwksPath string
	process  *apiProcess
	privateA ed25519.PrivateKey
	privateB ed25519.PrivateKey
	keyA     rotationKey
	keyB     rotationKey
	serial   int
}

// runWebAssertionRotationDrill rotates the JWKS under one running acr-api (no restart, wall clock).
func runWebAssertionRotationDrill(t *testing.T, baseURL string, privateA ed25519.PrivateKey, jwksPath string, process *apiProcess) {
	t.Helper()
	publicB, privateB, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	drill := &rotationDrill{
		t: t, baseURL: baseURL, jwksPath: jwksPath, process: process, privateA: privateA, privateB: privateB,
		keyA: rotationKey{kid: rotationKidA, public: privateA.Public().(ed25519.PublicKey)},
		keyB: rotationKey{kid: rotationKidB, public: publicB},
	}

	// S0: only A is published.
	drill.swap("S0", drill.keyA)
	drill.expect("S0", "A", http.StatusOK)
	drill.expect("S0", "B", http.StatusUnauthorized)
	drill.expectRetiredLines("S0", 0)
	t.Logf("rotation S0 {A}: A=200 B=401 retired_lines=0")

	// S1: B is added and A gets a bounded overlap; A-signed calls run across the rename.
	before, after := drill.loopAcrossSwap("S1", drill.keyA.retiring(time.Now().Add(60*time.Second)), drill.keyB)
	drill.expect("S1", "A", http.StatusOK)
	drill.expect("S1", "B", http.StatusOK)
	drill.expectRetiredLines("S1", 0)
	t.Logf("rotation S1 {A retire_after=+60s, B}: loop A calls before/during swap=%d after swap=%d non_200=0; A=200 B=200 retired_lines=0", before, after)

	// S2: A retires in seconds; after retire_after A is refused and logged, B still verifies.
	retireAfter := time.Now().UTC().Add(3 * time.Second).Truncate(time.Second)
	drill.expect("S2", "A", http.StatusOK)
	wakeAt := retireAfter.Add(time.Second)
	time.Sleep(time.Until(wakeAt))
	if now := time.Now().UTC(); now.Before(wakeAt) {
		t.Fatalf("S2: wait ended at %s, before retire_after + 1s = %s", now.Format(time.RFC3339Nano), wakeAt.Format(time.RFC3339Nano))
	}
	drill.expect("S2", "A", http.StatusUnauthorized)
	drill.expect("S2", "B", http.StatusOK)
	drill.expectRetiredLines("S2", 1)
	t.Logf("rotation S2 {A retire_after=%s, B}: A before=200 A after=401 B=200 retired_lines=1 (kid %s)", retireAfter.Format(time.RFC3339), rotationKidA)

	// S3: A is dropped from the document.
	drill.swap("S3", drill.keyB)
	drill.expect("S3", "A", http.StatusUnauthorized)
	drill.expect("S3", "B", http.StatusOK)
	drill.expectRetiredLines("S3", 1)
	t.Logf("rotation S3 {B}: A=401 B=200 retired_lines=1")

	// S4: a retire_after past the 24h ceiling is refused; a malformed document fails closed.
	drill.swap("S4", drill.keyA.retiring(time.Now().Add(25*time.Hour)), drill.keyB)
	drill.expect("S4 ceiling", "A", http.StatusUnauthorized)
	drill.expect("S4 ceiling", "B", http.StatusOK)
	drill.expectRetiredLines("S4 ceiling", 2)
	drill.swap("S4 malformed", rotationKey{kid: rotationKidA, public: drill.keyA.public, retireAfter: "tomorrow"}, drill.keyB)
	drill.expect("S4 malformed", "A", http.StatusUnauthorized)
	for range 3 {
		drill.expect("S4 malformed", "B", http.StatusUnauthorized)
	}
	drill.expectRetiredLines("S4 malformed", 2)
	drill.swap("S4 restored", drill.keyB)
	drill.expect("S4 restored", "B", http.StatusOK)
	drill.expectRetiredLines("S4 restored", 2)
	t.Logf("rotation S4 {A retire_after=+25h, B}: A=401 B=200 retired_lines=2; malformed: A=401 Bx3=401 retired_lines=2; restored {B}: B=200")
}

func (k rotationKey) retiring(at time.Time) rotationKey {
	k.retireAfter = at.UTC().Format(time.RFC3339)
	return k
}

func rotationJWKS(keys ...rotationKey) ([]byte, error) {
	members := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		member := map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": key.kid, "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.public)}
		if key.retireAfter != "" {
			member["retire_after"] = key.retireAfter
		}
		members = append(members, member)
	}
	return json.Marshal(map[string]any{"keys": members})
}

// replaceFileAtomically: temp file + rename, so the per-request reread never sees a partial document.
func replaceFileAtomically(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".jwks-swap-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	_, writeErr := temporary.Write(contents)
	chmodErr := temporary.Chmod(0o600)
	closeErr := temporary.Close()
	if writeErr != nil || chmodErr != nil || closeErr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %v %v %v", name, writeErr, chmodErr, closeErr)
	}
	return os.Rename(name, path)
}

func (d *rotationDrill) swap(stage string, keys ...rotationKey) {
	d.t.Helper()
	document, err := rotationJWKS(keys...)
	if err != nil {
		d.t.Fatal(err)
	}
	if err := replaceFileAtomically(d.jwksPath, document); err != nil {
		d.t.Fatalf("%s: swap JWKS: %v", stage, err)
	}
	d.assertJWKSMode(stage)
}

func (d *rotationDrill) assertJWKSMode(stage string) {
	d.t.Helper()
	info, err := os.Stat(d.jwksPath)
	if err != nil {
		d.t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		d.t.Fatalf("%s: JWKS mode = %v, want 0600", stage, info.Mode().Perm())
	}
}

// loopAcrossSwap sends A-signed calls before, during and after the swap; every one must be 200.
func (d *rotationDrill) loopAcrossSwap(stage string, keys ...rotationKey) (before, after int) {
	d.t.Helper()
	document, err := rotationJWKS(keys...)
	if err != nil {
		d.t.Fatal(err)
	}
	swapped := make(chan error, 1)
	swapStarted, swapDone := false, false
	deadline := time.Now().Add(time.Minute)
	for after < 3 {
		if before == 3 && !swapStarted {
			swapStarted = true
			go func() { swapped <- replaceFileAtomically(d.jwksPath, document) }()
		}
		if swapStarted && !swapDone {
			select {
			case err := <-swapped:
				if err != nil {
					d.t.Fatalf("%s: swap JWKS: %v", stage, err)
				}
				swapDone = true
			default:
			}
		}
		startedAfterSwap := swapDone
		if status := d.status(d.privateA, rotationKidA); status != http.StatusOK {
			d.t.Fatalf("%s: A-signed call %d across the swap = %d, want 200", stage, before+after+1, status)
		}
		if startedAfterSwap {
			after++
		} else {
			before++
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("%s: loop did not finish across the swap (before=%d after=%d)", stage, before, after)
		}
	}
	d.assertJWKSMode(stage)
	return before, after
}

func (d *rotationDrill) private(label string) (ed25519.PrivateKey, string) {
	if label == "A" {
		return d.privateA, rotationKidA
	}
	return d.privateB, rotationKidB
}

func (d *rotationDrill) send(private ed25519.PrivateKey, kid string) *http.Response {
	d.t.Helper()
	d.serial++
	// Each call needs a fresh jti: the replay guard refuses a reused one.
	jti := fmt.Sprintf("rotation-%d-%d", d.serial, time.Now().UnixNano())
	mutate := func(claims map[string]any, header map[string]string) {
		header["kid"] = kid
		claims["jti"] = jti
	}
	return doWebAssertionRequest(d.t, d.baseURL, private, "/api/v1/agent-context/context-packets", http.MethodPost, hostedWebAssertionContextRequest(), mutate, false)
}

func (d *rotationDrill) status(private ed25519.PrivateKey, kid string) int {
	d.t.Helper()
	response := d.send(private, kid)
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		d.t.Fatal(err)
	}
	return response.StatusCode
}

func (d *rotationDrill) expect(stage, label string, want int) {
	d.t.Helper()
	private, kid := d.private(label)
	response := d.send(private, kid)
	defer response.Body.Close()
	if response.StatusCode != want {
		contents, _ := io.ReadAll(response.Body)
		d.t.Fatalf("%s: %s-signed status = %d, want %d body=%s", stage, label, response.StatusCode, want, contents)
	}
	if want == http.StatusOK {
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			d.t.Fatal(err)
		}
		return
	}
	assertSafeWebAssertionFailure(d.t, response, want)
}

func (d *rotationDrill) retiredLines() []string {
	var lines []string
	for _, line := range strings.Split(d.process.Output(), "\n") {
		if strings.Contains(line, rotationRetiredLog) {
			lines = append(lines, line)
		}
	}
	return lines
}

// expectRetiredLines checks a running total, so a stage that did not run cannot pass.
func (d *rotationDrill) expectRetiredLines(stage string, want int) {
	d.t.Helper()
	lines := d.retiredLines()
	for deadline := time.Now().Add(3 * time.Second); len(lines) < want && time.Now().Before(deadline); lines = d.retiredLines() {
		time.Sleep(50 * time.Millisecond)
	}
	if len(lines) != want {
		d.t.Fatalf("%s: %s log lines = %d, want %d: %q", stage, rotationRetiredLog, len(lines), want, lines)
	}
	forbidden := []string{
		base64.RawURLEncoding.EncodeToString(d.keyA.public), base64.RawURLEncoding.EncodeToString(d.keyB.public),
		base64.RawURLEncoding.EncodeToString(d.privateA), base64.RawURLEncoding.EncodeToString(d.privateB),
		base64.StdEncoding.EncodeToString(d.keyA.public), base64.StdEncoding.EncodeToString(d.keyB.public),
	}
	for _, line := range lines {
		if !strings.Contains(line, `"kid":"`+rotationKidA+`"`) || strings.Contains(line, `"kid":"`+rotationKidB+`"`) {
			d.t.Fatalf("%s: retired log line names the wrong kid: %s", stage, line)
		}
		for _, value := range forbidden {
			if strings.Contains(line, value) {
				d.t.Fatalf("%s: retired log line carries key material", stage)
			}
		}
	}
}
