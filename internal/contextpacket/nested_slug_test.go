package contextpacket

import (
	"strings"
	"testing"
)

func TestValidSlugAcceptsTwoToTwentySegments(t *testing.T) {
	for slug, want := range map[string]bool{
		"acme/web":                     true,
		"acme/grp/web":                 true,
		"acme/grp/sub/web":             true,
		strings.Repeat("g/", 19) + "x": true,
		strings.Repeat("g/", 20) + "x": false,
		"acme":                         false,
		"acme/":                        false,
		"acme//web":                    false,
		"/acme/web":                    false,
		"acme/grp/":                    false,
		"acme/g rp/web":                false,
	} {
		if got := validSlug(slug); got != want {
			t.Errorf("validSlug(%q) = %v, want %v", slug, got, want)
		}
	}
}
