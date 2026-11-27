package devhealthfacts

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

// wireBytes is the native-format size of one sent row: each String is a uvarint
// length prefix plus its bytes, the rank one byte.
func wireBytes(key teamLinkedPullRequestKey) int {
	n := 1
	for _, s := range []string{key.team, key.repo, key.number} {
		n += binary.PutUvarint(make([]byte, binary.MaxVarintLen64), uint64(len(s))) + len(s)
	}
	return n
}

func byteEdgeSet(rows int, team string) teamLinkedPullRequests {
	keys := make([]teamLinkedPullRequestKey, rows)
	for i := range keys {
		keys[i] = teamLinkedPullRequestKey{team: team, repo: "r", number: "7"}
	}
	return teamLinkedPullRequests{keys: keys, ranks: make([]uint8, rows)}
}

// A set whose bytes without the String length prefixes fit the bound but whose
// encoded size is over it is refused; one whose encoded size fits is sent.
func TestCHAOS14627ByteGuardCountsLengthPrefixes(t *testing.T) {
	team := strings.Repeat("x", 1000)
	rowWire := wireBytes(teamLinkedPullRequestKey{team: team, repo: "r", number: "7"})
	rowBare := len(team) + 1 + 1 + 1
	over := teamLinkedPullRequestsMaxBytes / rowBare
	if over*rowBare > teamLinkedPullRequestsMaxBytes || over*rowWire <= teamLinkedPullRequestsMaxBytes {
		t.Fatalf("fixture is not at the edge: bare %d, wire %d, bound %d", over*rowBare, over*rowWire, teamLinkedPullRequestsMaxBytes)
	}
	if _, err := withTeamLinkedPullRequests(context.Background(), byteEdgeSet(over, team)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("%d rows encode to %d bytes over the %d bound; err = %v, want the named byte bound", over, over*rowWire, teamLinkedPullRequestsMaxBytes, err)
	}
	fits := teamLinkedPullRequestsMaxBytes / rowWire
	if _, err := withTeamLinkedPullRequests(context.Background(), byteEdgeSet(fits, team)); err != nil {
		t.Fatalf("%d rows encode to %d bytes inside the bound; err = %v", fits, fits*rowWire, err)
	}
}
