package contextpacket

import (
	"regexp"
	"testing"
	"unicode"
)

func TestCIRunLabelSpaceClassMatchesUnicodeIsSpaceForEveryRune(t *testing.T) {
	t.Parallel()
	class := regexp.MustCompile("^" + ciRunLabelSpaceClass + "$")
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if got, want := class.MatchString(string(r)), unicode.IsSpace(r); got != want {
			t.Fatalf("U+%04X: class matches = %t, unicode.IsSpace = %t", r, got, want)
		}
	}
}
