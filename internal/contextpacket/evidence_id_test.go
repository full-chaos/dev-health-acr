package contextpacket_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestEvidenceIDCodec_rejects_cross_org_and_tampered_handles(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	evidence := evidenceIDFixture("ci_pipeline_runs.v1", "acr:v1:ci:opaque-reference")
	handle, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	if err != nil {
		t.Fatalf("encode handle: %v", err)
	}
	again, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	if err != nil || handle != again || !strings.HasPrefix(handle, "ev2_test_ci_") || len(handle) > 256 || strings.Contains(handle, "opaque-reference") {
		t.Fatalf("deterministic opaque handle = %q, second = %q, error = %v", handle, again, err)
	}
	parsed, parseErr := codec.Parse(handle)
	parts := strings.Split(handle, ".")
	sealed, err := base64.RawURLEncoding.DecodeString(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("decode sealed handle: %v", err)
	}
	sealed[0] ^= 0xff
	parts[len(parts)-1] = base64.RawURLEncoding.EncodeToString(sealed)
	tampered, tamperedErr := codec.Parse(strings.Join(parts, "."))
	if parseErr != nil || codec.Matches(parsed, "org-foreign", "repo-server-derived", evidence) || tamperedErr == nil && codec.Matches(tampered, "org-fixture", "repo-server-derived", evidence) {
		t.Fatalf("parse = %v, tampered = %v", parseErr, tamperedErr)
	}
}

func TestEvidenceIDCodec_accepts_legacy_ev1_handles(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	handle := "ev1_test_ci_-lkdE7eXOZBS2C-1mJBXmA.uNn3lA7Lt_S8afasklI6D9DZhgi0PbjtD9quqnKVt_E"
	parsed, err := codec.Parse(handle)
	if err != nil || parsed.LookupHash() != "" || !codec.Matches(parsed, "org-fixture", "repo-server-derived", evidenceIDFixture("ci_pipeline_runs.v1", "acr:v1:ci:opaque-reference")) {
		t.Fatalf("legacy handle = %#v, error = %v", parsed, err)
	}
}

func TestEvidenceIDCodec_accepts_active_and_previous_keys(t *testing.T) {
	old, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "old", Keys: map[string][]byte{"old": []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}})
	if err != nil {
		t.Fatalf("create old codec: %v", err)
	}
	evidence := evidenceIDFixture("ci_pipeline_runs.v1", "locator")
	oldID, _ := old.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	rotated, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "new", Keys: map[string][]byte{"new": []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), "old": []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}})
	if err != nil {
		t.Fatalf("create rotated codec: %v", err)
	}
	activeID, _ := rotated.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	oldHandle, oldErr := rotated.Parse(oldID)
	activeHandle, activeErr := rotated.Parse(activeID)
	if oldErr != nil || activeErr != nil || !rotated.Matches(oldHandle, "org-fixture", "repo-server-derived", evidence) || !rotated.Matches(activeHandle, "org-fixture", "repo-server-derived", evidence) {
		t.Fatalf("old = %v, active = %v", oldErr, activeErr)
	}
}

func TestEvidenceIDCodec_domain_separates_nonce_across_kids_with_same_key(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	old, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "old", Keys: map[string][]byte{"old": key}})
	if err != nil {
		t.Fatalf("create old codec: %v", err)
	}
	evidence := evidenceIDFixture("ci_pipeline_runs.v1", "locator")
	oldID, _ := old.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	rotated, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "new", Keys: map[string][]byte{"new": key, "old": key}})
	if err != nil {
		t.Fatalf("create rotated codec: %v", err)
	}
	newID, _ := rotated.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	oldPayload := strings.Split(strings.SplitN(oldID, "_", 4)[3], ".")
	newPayload := strings.Split(strings.SplitN(newID, "_", 4)[3], ".")
	oldHandle, oldErr := rotated.Parse(oldID)
	newHandle, newErr := rotated.Parse(newID)
	if oldPayload[1] == newPayload[1] || oldErr != nil || newErr != nil || !rotated.Matches(oldHandle, "org-fixture", "repo-server-derived", evidence) || !rotated.Matches(newHandle, "org-fixture", "repo-server-derived", evidence) {
		t.Fatalf("old nonce = %q, new nonce = %q, old error = %v, new error = %v", oldPayload[1], newPayload[1], oldErr, newErr)
	}
}

func TestEvidenceIDCodec_seals_scope_and_rejects_malformed_ev2_segments(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	asOf := time.Date(2026, 1, 15, 12, 0, 0, 123_000_000, time.UTC)
	evidence := evidenceIDFixture("ci_pipeline_runs.v1", "locator")
	handle, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{Branch: "main", AsOf: &asOf, RepositoryWide: true})
	if err != nil {
		t.Fatalf("encode scoped handle: %v", err)
	}
	parsed, err := codec.Parse(handle)
	branchDigest := sha256.Sum256([]byte("main"))
	if err != nil || !parsed.RepositoryWide || parsed.BranchHash() != hex.EncodeToString(branchDigest[:]) || parsed.AsOf == nil || !parsed.AsOf.Equal(asOf) || len(handle) > 256 {
		t.Fatalf("scoped handle = %#v, length = %d, error = %v", parsed, len(handle), err)
	}
	maxKID := strings.Repeat("k", 64)
	maxCodec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: maxKID, Keys: map[string][]byte{maxKID: []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatalf("create maximum KID codec: %v", err)
	}
	maxHandle, err := maxCodec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{Branch: "main", AsOf: &asOf, RepositoryWide: true})
	if err != nil || len(maxHandle) > 256 {
		t.Fatalf("maximum KID handle length = %d, error = %v", len(maxHandle), err)
	}
	prefix := strings.SplitN(handle, "_", 4)
	payload := strings.Split(prefix[3], ".")
	malformed := []string{
		strings.Join(prefix[:3], "_") + "_" + payload[0] + "." + payload[1],
		strings.Join(prefix[:3], "_") + "_" + payload[0] + ".AA." + payload[2],
		strings.Join(prefix[:3], "_") + "_" + payload[0] + "." + payload[1] + ".AA",
		handle + ".extra",
	}
	for _, value := range malformed {
		if _, err := codec.Parse(value); !errors.Is(err, contextpacket.ErrInvalidEvidenceID) {
			t.Fatalf("malformed handle error = %v for %q", err, value)
		}
	}
}

func TestEvidenceIDCodec_emits_branch_digest_only_for_branch_catalog_sources(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	for _, test := range []struct {
		name       string
		queryID    string
		wantBranch bool
	}{
		{name: "branch", queryID: "ci_pipeline_runs.v1", wantBranch: true},
		{name: "repository", queryID: "work_items.v1"},
		{name: "commit", queryID: "git_commits.v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := evidenceIDFixture(test.queryID, "locator")
			handle, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{Branch: "main"})
			if err != nil {
				t.Fatalf("encode handle: %v", err)
			}
			parsed, err := codec.Parse(handle)
			if err != nil || (parsed.BranchHash() != "") != test.wantBranch {
				t.Fatalf("parsed handle = %#v, error = %v, want branch digest = %t", parsed, err, test.wantBranch)
			}
		})
	}
}

func TestEvidenceIDCodec_copies_map_and_key_material(t *testing.T) {
	keys := map[string][]byte{"active": []byte("cccccccccccccccccccccccccccccccc")}
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "active", Keys: keys})
	if err != nil {
		t.Fatalf("create codec: %v", err)
	}
	evidence := evidenceIDFixture("ci_pipeline_runs.v1", "locator")
	before, _ := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	keys["active"][0] = 'z'
	keys["active"] = []byte("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	delete(keys, "active")
	after, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	if err != nil || before != after {
		t.Fatalf("post-mutation encode = %q, error = %v", after, err)
	}
}

func TestEvidenceIDCodec_catalog_codes_are_stable_under_reordering(t *testing.T) {
	codec := fixtureEvidenceCodec(t)
	evidence := evidenceIDFixture("pull_requests.v1", "locator")
	before, _ := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
	original := append([]contextpacket.SourceQuery(nil), contextpacket.SourceQueryCatalogV1...)
	t.Cleanup(func() { contextpacket.SourceQueryCatalogV1 = original })
	for left, right := 0, len(contextpacket.SourceQueryCatalogV1)-1; left < right; left, right = left+1, right-1 {
		contextpacket.SourceQueryCatalogV1[left], contextpacket.SourceQueryCatalogV1[right] = contextpacket.SourceQueryCatalogV1[right], contextpacket.SourceQueryCatalogV1[left]
	}
	after, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidence, contextpacket.EvidenceIDContext{})
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
		handle, err := codec.EncodeEvidence("org-fixture", "repo-server-derived", evidenceIDFixture(query.ID, "locator:"+query.ID), contextpacket.EvidenceIDContext{})
		if err != nil {
			t.Fatalf("encode %s: %v", query.ID, err)
		}
		parsed, err := codec.Parse(handle)
		if err != nil || parsed.QueryID != query.ID {
			t.Fatalf("catalog parity %s = %#v, error = %v", query.ID, parsed, err)
		}
	}
}

func evidenceIDFixture(queryID, locator string) contractsv1.EvidenceRef {
	evidence := testEvidence(locator, "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.SourceVersion = queryID
	return evidence
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
