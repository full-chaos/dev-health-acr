package identity_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// sqlFragmentReference is a literal Go transcription of the registry-pinned
// SQL parity fragment (identity.SQLSegmentEncodeFragment):
//
//	replaceAll(replaceAll(col, '%', '%25'), ':', '%3A')
//
// It exists only as an independent oracle for TestEncodeSegmentMatchesSQLParityFragment
// -- if this ever drifts from EncodeSegment, the drift is what S2's SQL
// producers would actually emit, so the test failing here is exactly the
// signal design brief §1.1 wants before it reaches ClickHouse.
func sqlFragmentReference(col string) string {
	step1 := strings.ReplaceAll(col, "%", "%25")
	return strings.ReplaceAll(step1, ":", "%3A")
}

func TestEncodeSegmentMatchesSQLParityFragment(t *testing.T) {
	vectors := []string{
		"", "plain", "a:b", "a%b", "%3A", "%25", "a:b:c",
		"100% done: really", "日本語:テスト%", ":::", "%%%", "%3A%3A",
	}
	for _, v := range vectors {
		if got, want := identity.EncodeSegment(v), sqlFragmentReference(v); got != want {
			t.Errorf("EncodeSegment(%q) = %q, sqlFragmentReference(%q) = %q", v, got, v, want)
		}
	}

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 2000; i++ {
		v := randomSegmentInput(rng)
		if got, want := identity.EncodeSegment(v), sqlFragmentReference(v); got != want {
			t.Fatalf("iteration %d: EncodeSegment(%q) = %q, sqlFragmentReference(%q) = %q", i, v, got, v, want)
		}
	}
}

// randomSegmentInput builds strings biased toward the codec's interesting
// characters (':', '%') plus multi-byte runes and plain ASCII, so the
// property tests exercise adversarial input, not just easy cases.
func randomSegmentInput(rng *rand.Rand) string {
	alphabet := []rune("ab:%日本語 \t-_/#") // includes both codec-special chars
	n := rng.Intn(24)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteRune(alphabet[rng.Intn(len(alphabet))])
	}
	return b.String()
}

// TestCodecRoundTripProperty is the property test design brief §1.1
// requires: DecodeSegment(EncodeSegment(x)) == x for arbitrary x,
// including input that already contains the literal escape sequences
// "%25"/"%3A" -- the adversarial case that would break a naively-ordered
// encode/decode pair.
func TestCodecRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		want := randomSegmentInput(rng)
		encoded := identity.EncodeSegment(want)
		got, err := identity.DecodeSegment(encoded)
		if err != nil {
			t.Fatalf("iteration %d: DecodeSegment(%q) error: %v", i, encoded, err)
		}
		if got != want {
			t.Fatalf("iteration %d: round trip broke: original %q, encoded %q, decoded %q", i, want, encoded, got)
		}
	}
}

// TestCodecRoundTripPinnedVectors pins a handful of hand-picked, readable
// cases so a future reader can see the shape of the property test without
// running it.
func TestCodecRoundTripPinnedVectors(t *testing.T) {
	vectors := []string{
		"simple", "a:b", "a%b", "a:b%c", "%3A", "%25", "%253A",
		"trailing:", ":leading", "::", "%%", "日本語", "emoji-🎉-id",
	}
	for _, v := range vectors {
		encoded := identity.EncodeSegment(v)
		got, err := identity.DecodeSegment(encoded)
		if err != nil {
			t.Fatalf("DecodeSegment(%q) error: %v", encoded, err)
		}
		if got != v {
			t.Errorf("round trip broke for %q: encoded %q, decoded %q", v, encoded, got)
		}
	}
}

// TestJoinSegmentsInjectiveProperty is the "closes the encoding-ambiguity
// class" property named in design brief §1.1: distinct segment tuples must
// join to distinct strings, even when a segment's own content could be
// confused with the ':' separator.
func TestJoinSegmentsInjectiveProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	seen := make(map[string][]string, 4000)
	for i := 0; i < 4000; i++ {
		n := 1 + rng.Intn(4)
		segments := make([]string, n)
		for j := range segments {
			segments[j] = randomSegmentInput(rng)
		}
		joined := identity.JoinSegments(segments...)
		if prior, ok := seen[joined]; ok && !equalSegments(prior, segments) {
			t.Fatalf("collision: %v and %v both join to %q", prior, segments, joined)
		}
		seen[joined] = segments
	}
}

func equalSegments(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestJoinSegmentsClosesNamedAmbiguity is the exact ambiguity design brief
// §1.1 names: (a, "b:c") and ("a:b", c) must not collide once every
// segment is uniformly encoded.
func TestJoinSegmentsClosesNamedAmbiguity(t *testing.T) {
	left := identity.JoinSegments("a", "b:c")
	right := identity.JoinSegments("a:b", "c")
	if left == right {
		t.Fatalf("ambiguity not closed: JoinSegments(%q,%q) == JoinSegments(%q,%q) == %q", "a", "b:c", "a:b", "c", left)
	}
}

func TestEncodeSegmentOrderingExamples(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a:b", "a%3Ab"},
		{"a%b", "a%25b"},
		{"a%3Ab", "a%253Ab"}, // '%' escaped first, so no accidental ':' unescape target is created
		{"", ""},
	}
	for _, c := range cases {
		if got := identity.EncodeSegment(c.in); got != c.want {
			t.Errorf("EncodeSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func ExampleJoinSegments() {
	fmt.Println(identity.JoinSegments("repo-1", "run-42"))
	// Output: repo-1:run-42
}
