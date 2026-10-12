package devhealthfacts

import (
	"fmt"
	"strings"
	"testing"
)

func TestTeamIDChunksCoverEveryIDOnceInOrder(t *testing.T) {
	for _, n := range []int{0, 1, teamRepoKeyChunk - 1, teamRepoKeyChunk, teamRepoKeyChunk + 1, 250} {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("team-%03d", i)
		}
		var seen []string
		chunks := 0
		if err := forEachTeamIDChunk(ids, func(chunk []string) error {
			chunks++
			if len(chunk) == 0 || len(chunk) > teamRepoKeyChunk {
				t.Fatalf("n=%d: chunk of %d", n, len(chunk))
			}
			seen = append(seen, chunk...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Join(seen, ",") != strings.Join(ids, ",") {
			t.Fatalf("n=%d: chunks do not reproduce the id list", n)
		}
		if want := (n + teamRepoKeyChunk - 1) / teamRepoKeyChunk; chunks != want {
			t.Fatalf("n=%d: chunks = %d, want %d", n, chunks, want)
		}
	}
}
