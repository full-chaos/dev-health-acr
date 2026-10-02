package factoracle

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// FixtureOrgID replaces the venue organization id in everything that is
// written to the repository.
const FixtureOrgID = "0f0f0f0f-0000-4000-8000-00000000f4c7"

// Scrubber replaces every identifier of the venue with a pseudonym. The key
// is random and lives only in the capturing process, so a pseudonym cannot be
// turned back; inside one capture the same input gives the same output, so
// the join keys of the extract and of the recorded replies still agree.
type Scrubber struct {
	key []byte
	org string
}

// NewScrubber makes a scrubber for one capture.
func NewScrubber(orgID string) (*Scrubber, error) {
	if strings.TrimSpace(orgID) == "" {
		return nil, fmt.Errorf("scrubber needs the organization id it must remove")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &Scrubber{key: key, org: orgID}, nil
}

func (s *Scrubber) mac(kind, value string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return h.Sum(nil)
}

// Org returns the fixture organization id for the venue one, and refuses any
// other value in an org column.
func (s *Scrubber) Org(value string) (string, error) {
	if value != s.org {
		return "", fmt.Errorf("an org column holds a value that is not the capture organization")
	}
	return FixtureOrgID, nil
}

// UUID maps a UUID to a pseudonym UUID. The letter case of the input is kept.
func (s *Scrubber) UUID(value string) string {
	if value == "" {
		return ""
	}
	sum := s.mac("uuid", strings.ToLower(value))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	x := hex.EncodeToString(sum[:16])
	out := x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
	if value != strings.ToLower(value) {
		return strings.ToUpper(out)
	}
	return out
}

// Token maps an opaque identifier to "<kind>-<16 hex>". The empty string
// stays empty.
func (s *Scrubber) Token(kind, value string) string {
	if value == "" {
		return ""
	}
	return kind + "-" + hex.EncodeToString(s.mac("token:"+kind, value)[:8])
}

// Slug maps a repository name. Letters become other letters, digits other
// digits; the length, the separators and the letter case stay, so a rule that
// compares names exactly, or after lower-casing, decides the same way on the
// pseudonym as on the name.
func (s *Scrubber) Slug(value string) string {
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	var stream []byte
	next := func(i int) byte {
		for len(stream) <= i {
			block := s.mac("slug", fmt.Sprintf("%d|%s", len(stream)/sha256.Size, lower))
			stream = append(stream, block...)
		}
		return stream[i]
	}
	out := make([]rune, 0, len(value))
	i := 0
	for _, r := range value {
		switch {
		case r > unicode.MaxASCII:
			out = append(out, 'x')
		case unicode.IsLetter(r):
			c := rune('a' + next(i)%26)
			if unicode.IsUpper(r) {
				c = unicode.ToUpper(c)
			}
			out = append(out, c)
		case unicode.IsDigit(r):
			out = append(out, rune('0'+next(i)%10))
		default:
			out = append(out, r)
		}
		i++
	}
	return string(out)
}

// SubjectID maps an acr subject id ("repository:<uuid>", "team:<id>").
func (s *Scrubber) SubjectID(value string) (string, bool) {
	if rest, ok := strings.CutPrefix(value, "repository:"); ok {
		return "repository:" + s.UUID(rest), true
	}
	if rest, ok := strings.CutPrefix(value, "team:"); ok {
		return "team:" + s.Token("team", rest), true
	}
	return "", false
}

var (
	evidenceUUIDRef   = regexp.MustCompile(`^([0-9a-fA-F-]{36})#pr([0-9]+)$`)
	evidenceGithubRef = regexp.MustCompile(`^ghpr:([^#]+)#([0-9]+)$`)
	evidenceGitlabRef = regexp.MustCompile(`^gitlab:([^!]+)!([0-9]+)$`)
)

// Evidence rewrites structural_evidence_json. Only the pull request
// references the repository mix reads survive (the three reference forms of
// devhealthfacts repoMixStatement), each with its repository replaced; every
// other key and every other entry is dropped.
func (s *Scrubber) Evidence(value string) string {
	var in map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &in); err != nil {
		return "{}"
	}
	out := map[string][]string{}
	if raw, ok := in["prs"]; ok {
		var refs []string
		if json.Unmarshal(raw, &refs) == nil {
			kept := []string{}
			for _, ref := range refs {
				if m := evidenceUUIDRef.FindStringSubmatch(ref); m != nil {
					kept = append(kept, s.UUID(m[1])+"#pr"+m[2])
				}
			}
			out["prs"] = kept
		}
	}
	if raw, ok := in["issues"]; ok {
		var refs []string
		if json.Unmarshal(raw, &refs) == nil {
			kept := []string{}
			for _, ref := range refs {
				if m := evidenceGithubRef.FindStringSubmatch(ref); m != nil {
					kept = append(kept, "ghpr:"+s.Slug(m[1])+"#"+m[2])
				} else if m := evidenceGitlabRef.FindStringSubmatch(ref); m != nil {
					kept = append(kept, "gitlab:"+s.Slug(m[1])+"!"+m[2])
				}
			}
			out["issues"] = kept
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// Leaks reports whether text still holds the venue organization id.
func (s *Scrubber) Leaks(text []byte) bool {
	return strings.Contains(strings.ToLower(string(text)), strings.ToLower(s.org))
}
