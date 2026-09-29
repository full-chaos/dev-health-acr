package directread

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// revealed returns every string a client could read out of a cursor token
// without a key: the token itself and the base64url decoding of each of its
// "."-separated parts.
func revealed(token string) string {
	out := token
	for _, part := range strings.Split(token, ".") {
		if raw, err := base64.RawURLEncoding.DecodeString(part); err == nil {
			out += "\n" + string(raw)
		}
	}
	return out
}

// forge tries what a client without a key can do: read the cursor as JSON,
// edit it, write it back. It returns "" when the token does not decode to
// JSON (a sealed cursor).
func forge(token string, edit func(map[string]any)) string {
	parts := strings.Split(token, ".")
	last := parts[len(parts)-1]
	raw, err := base64.RawURLEncoding.DecodeString(last)
	if err != nil {
		return ""
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	edit(fields)
	encoded, _ := json.Marshal(fields)
	parts[len(parts)-1] = base64.RawURLEncoding.EncodeToString(encoded)
	return strings.Join(parts, ".")
}

// r1 P1, repro 3 (permanent): a cursor cannot be edited to skip edges or to
// outlive its TTL, and a changed byte is refused.
func TestChaos7074_R1_CursorCannotBeEdited(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	graph := hubGraph()
	reader, _ := newRelReader(graph, &now)
	request := RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Direction: "in", Limit: 1}
	first, err := reader.Read(relCtx("seal-0"), unrestricted, request)
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("first page: %v %+v", err, first.Page)
	}
	good := first.Page.NextCursor
	refused := func(label, token string) {
		t.Helper()
		r := request
		r.Cursor = token
		_, err := reader.Read(relCtx("seal-"+label), unrestricted, r)
		var requestError *RelationshipsRequestError
		if !errors.As(err, &requestError) {
			t.Fatalf("%s: edited cursor accepted (err=%v)", label, err)
		}
	}
	if skip := forge(good, func(f map[string]any) { f["a"] = map[string]any{"r": "zzzz"} }); skip != "" {
		refused("skip to the end", skip)
	}
	if renew := forge(good, func(f map[string]any) { f["t"] = now.Add(time.Hour).Unix() }); renew != "" {
		now = now.Add(RelationshipsCursorTTL + time.Minute)
		refused("renewed past its TTL", renew)
		now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	}
	for _, index := range []int{0, len(good) / 2, len(good) - 1} {
		flipped := []byte(good)
		if flipped[index] == 'A' {
			flipped[index] = 'B'
		} else {
			flipped[index] = 'A'
		}
		refused("flipped byte", string(flipped))
	}
}

// r1 P1: the sealed cursor names no relationship id in clear, withheld or not.
func TestChaos7074_R1_CursorNamesNoRelationship(t *testing.T) {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	graph.edges = []EdgeCandidate{
		edgeBetween("rel-a-visible", "OWNED_BY_TEAM", repoA, teamT, nil),
		edgeBetween("rel-b-withheld-secret", "OWNED_BY_TEAM", repoB, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/b"}}),
		edgeBetween("rel-c-after", "OWNED_BY_TEAM", projectQ, teamT, nil),
	}
	reader, _ := newRelReader(graph, nil)
	response, err := reader.Read(relCtx("names"), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Limit: 2})
	if err != nil || response.Withheld.EdgesNotVisible != 1 || response.Page.NextCursor == "" {
		t.Fatalf("%v %+v", err, response)
	}
	if text := revealed(response.Page.NextCursor); strings.Contains(text, "rel-") {
		t.Fatalf("cursor reveals a relationship id: %q", text)
	}
}

// Rotation: a cursor sealed under a kid that is still in the keyring opens
// after the active kid changed; once the kid is removed it is refused.
func TestChaos7074_CursorKeyRotation(t *testing.T) {
	old := []byte("0123456789abcdef0123456789abcdef-old")
	next := []byte("0123456789abcdef0123456789abcdef-new")
	sealerOld, err := newCursorSealer(CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": old}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := sealerOld.seal([]byte(`{"v":1}`))
	if err != nil || !strings.HasPrefix(token, "k1.") {
		t.Fatalf("%v %q", err, token)
	}
	rotated, err := newCursorSealer(CursorKeyring{ActiveKID: "k2", Keys: map[string][]byte{"k1": old, "k2": next}})
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := rotated.open(token); err != nil || string(plain) != `{"v":1}` {
		t.Fatalf("old kid still in the keyring: %v %q", err, plain)
	}
	if fresh, _ := rotated.seal([]byte("x")); !strings.HasPrefix(fresh, "k2.") {
		t.Fatalf("new cursors must use the active kid: %q", fresh)
	}
	retired, err := newCursorSealer(CursorKeyring{ActiveKID: "k2", Keys: map[string][]byte{"k2": next}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retired.open(token); err == nil {
		t.Fatal("a cursor of a removed kid opened")
	}
	// The cursor key is DERIVED (HMAC with CursorKeyLabel), never the
	// evidence key itself: AES-GCM under the raw evidence key cannot open it.
	block, _ := aes.NewCipher(old[:32])
	raw, _ := cipher.NewGCM(block)
	payload, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "k1."))
	if _, err := raw.Open(nil, payload[:raw.NonceSize()], payload[raw.NonceSize():], cursorAAD("k1")); err == nil {
		t.Fatal("the cursor opened under the raw evidence key: the key is not derived")
	}
	// A different key does not open it.
	other, _ := newCursorSealer(CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": append([]byte{}, next...)}})
	if _, err := other.open(token); err == nil {
		t.Fatal("a cursor opened under another key")
	}
}

func TestChaos7074_CursorKeyringMustBeUsable(t *testing.T) {
	for name, keyring := range map[string]CursorKeyring{
		"empty":          {},
		"no active":      {Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}},
		"short key":      {ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("short")}},
		"active missing": {ActiveKID: "k2", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}},
		"bad kid":        {ActiveKID: "k 1", Keys: map[string][]byte{"k 1": []byte("0123456789abcdef0123456789abcdef")}},
	} {
		if _, err := NewRelationshipsReader(nil, nil, nil, keyring); !errors.Is(err, ErrCursorKeyring) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// r1 P3: the published schema bounds types (at most 12, unique).
func TestChaos7074_R1_TypesBoundedAndUnique(t *testing.T) {
	reader, _ := newRelReader(&fakeEdgeGraph{fakeGraph: graphOfOrgA()}, nil)
	base := RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}
	thirteen := make([]string, 13)
	for i := range thirteen {
		thirteen[i] = "OWNED_BY_TEAM"
	}
	for name, types := range map[string][]string{"13 values": thirteen, "a duplicate": {"BLOCKS", "PART_OF", "BLOCKS"}} {
		_, err := reader.Read(relCtx("types-"+name), unrestricted, RelationshipsRequest{Subject: base, Types: types})
		var requestError *RelationshipsRequestError
		if !errors.As(err, &requestError) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

// A reader without a sealer (never built by NewRelationshipsReader, but a
// zero-value reader must not serve) fails closed rather than paging with an
// unsealed cursor.
func TestChaos7074_ReaderWithoutSealerFailsClosed(t *testing.T) {
	graph := hubGraph()
	reader := &RelationshipsReader{gate: NewSubjectGate(graph, nil), graph: graph}
	if _, err := reader.Read(relCtx("nosealer"), unrestricted, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Limit: 1}); !errors.Is(err, ErrRelationshipsUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
