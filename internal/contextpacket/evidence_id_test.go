package contextpacket_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestEvidenceIDCodec_rejects_cross_org_and_tampered_handles(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	handle, err := codec.Encode("org-fixture", "repo-server-derived", "ci_pipeline_runs.v1", "acr:v1:ci:opaque-reference", false)
	if err != nil {
		t.Fatalf("encode handle: %v", err)
	}
	again, err := codec.Encode("org-fixture", "repo-server-derived", "ci_pipeline_runs.v1", "acr:v1:ci:opaque-reference", false)
	if err != nil || handle != again || !strings.HasPrefix(handle, "ev2_test_ci_") || len(handle) > 256 || strings.Contains(handle, "opaque-reference") {
		t.Fatalf("deterministic opaque handle = %q, second = %q, error = %v", handle, again, err)
	}
	parsed, parseErr := codec.Parse(handle)
	replacement := byte('A')
	if handle[len(handle)-1] == replacement {
		replacement = 'B'
	}
	tampered, tamperedErr := codec.Parse(handle[:len(handle)-1] + string(replacement))
	if parseErr != nil || codec.Matches(parsed, "org-foreign", "repo-server-derived", "acr:v1:ci:opaque-reference") || tamperedErr == nil && codec.Matches(tampered, "org-fixture", "repo-server-derived", "acr:v1:ci:opaque-reference") {
		t.Fatalf("parse = %v, tampered = %v", parseErr, tamperedErr)
	}
}

func TestEvidenceIDCodec_accepts_legacy_ev1_handles(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	handle := "ev1_test_ci_-lkdE7eXOZBS2C-1mJBXmA.uNn3lA7Lt_S8afasklI6D9DZhgi0PbjtD9quqnKVt_E"
	parsed, err := codec.Parse(handle)
	if err != nil || parsed.LocatorHash() != "" || !codec.Matches(parsed, "org-fixture", "repo-server-derived", "acr:v1:ci:opaque-reference") {
		t.Fatalf("legacy handle = %#v, error = %v", parsed, err)
	}
}

func TestEvidenceIDCodec_accepts_active_and_previous_keys(t *testing.T) {
	old, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "old", Keys: map[string][]byte{"old": []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}})
	if err != nil {
		t.Fatalf("create old codec: %v", err)
	}
	oldID, _ := old.Encode("org-fixture", "repo-server-derived", "ci_pipeline_runs.v1", "locator", false)
	rotated, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "new", Keys: map[string][]byte{"new": []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), "old": []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}})
	if err != nil {
		t.Fatalf("create rotated codec: %v", err)
	}
	activeID, _ := rotated.Encode("org-fixture", "repo-server-derived", "ci_pipeline_runs.v1", "locator", false)
	oldHandle, oldErr := rotated.Parse(oldID)
	activeHandle, activeErr := rotated.Parse(activeID)
	if oldErr != nil || activeErr != nil || !rotated.Matches(oldHandle, "org-fixture", "repo-server-derived", "locator") || !rotated.Matches(activeHandle, "org-fixture", "repo-server-derived", "locator") {
		t.Fatalf("old = %v, active = %v", oldErr, activeErr)
	}
}

func TestEvidenceIDCodec_copies_map_and_key_material(t *testing.T) {
	keys := map[string][]byte{"active": []byte("cccccccccccccccccccccccccccccccc")}
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "active", Keys: keys})
	if err != nil {
		t.Fatalf("create codec: %v", err)
	}
	before, _ := codec.Encode("org-fixture", "repo-server-derived", "ci_pipeline_runs.v1", "locator", false)
	keys["active"][0] = 'z'
	keys["active"] = []byte("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	delete(keys, "active")
	after, err := codec.Encode("org-fixture", "repo-server-derived", "ci_pipeline_runs.v1", "locator", false)
	if err != nil || before != after {
		t.Fatalf("post-mutation encode = %q, error = %v", after, err)
	}
}

func TestEvidenceIDCodec_catalog_codes_are_stable_under_reordering(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	before, _ := codec.Encode("org-fixture", "repo-server-derived", "pull_requests.v1", "locator", false)
	original := append([]contextpacket.SourceQuery(nil), contextpacket.SourceQueryCatalogV1...)
	t.Cleanup(func() { contextpacket.SourceQueryCatalogV1 = original })
	for left, right := 0, len(contextpacket.SourceQueryCatalogV1)-1; left < right; left, right = left+1, right-1 {
		contextpacket.SourceQueryCatalogV1[left], contextpacket.SourceQueryCatalogV1[right] = contextpacket.SourceQueryCatalogV1[right], contextpacket.SourceQueryCatalogV1[left]
	}
	after, err := codec.Encode("org-fixture", "repo-server-derived", "pull_requests.v1", "locator", false)
	if err != nil || before != after {
		t.Fatalf("reordered handle = %q, error = %v, want %q", after, err, before)
	}
	if _, err := codec.Parse("ev1_unknown_ci_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, contextpacket.ErrInvalidEvidenceID) {
		t.Fatalf("unknown KID error = %v", err)
	}
}

func TestEvidenceIDCodec_encodes_and_parses_every_catalog_source(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		handle, err := codec.Encode("org-fixture", "repo-server-derived", query.ID, "locator:"+query.ID, false)
		if err != nil {
			t.Fatalf("encode %s: %v", query.ID, err)
		}
		parsed, err := codec.Parse(handle)
		if err != nil || parsed.QueryID != query.ID {
			t.Fatalf("catalog parity %s = %#v, error = %v", query.ID, parsed, err)
		}
	}
}

func TestEvidenceIDCodec_rejects_delimiter_invalid_kids(t *testing.T) {
	for _, kid := range []string{"bad_kid", "bad,kid", "bad=kid", "bad:kid"} {
		_, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: kid, Keys: map[string][]byte{kid: []byte("01234567890123456789012345678901")}})
		if !errors.Is(err, contextpacket.ErrInvalidEvidenceID) {
			t.Fatalf("KID %q error = %v", kid, err)
		}
	}
}

func TestEvidenceIDCodecParsesBase64URLUnderscoresInRoutingTag(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	tag := bytes.Repeat([]byte{0xff}, 16)
	mac := bytes.Repeat([]byte{0x00}, 32)
	handle := "ev1_test_ci_" + base64.RawURLEncoding.EncodeToString(tag) + "." + base64.RawURLEncoding.EncodeToString(mac)

	parsed, err := codec.Parse(handle)

	if err != nil || !bytes.Equal(parsed.RepositoryTag, tag) {
		t.Fatalf("parsed = %#v, error = %v", parsed, err)
	}
}
