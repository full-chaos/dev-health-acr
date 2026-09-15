package contextfabric

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// WorkItemTupleCensusVersion is the only census encoding this build writes
// and reads as available.
const WorkItemTupleCensusVersion = "work-item-census.v1"

// WorkItemTupleCensus is the bounded membership measurement persisted beside
// a project-to-work-item semantic reading. Retained is a count: the matching
// identity set lives in the immutable result cohort and is checked there.
//
// RequestedRepositoryScope is copied from the request so by-id can recompute
// the digest against current grants without reconstructing a request. It is
// intentionally separate from the semantic request identity.
type WorkItemTupleCensus struct {
	Version                  string                        `json:"version"`
	State                    WorkItemMembershipCensusState `json:"state"`
	Value                    int                           `json:"value"`
	Retained                 int                           `json:"retained"`
	RequestedRepositoryScope []string                      `json:"requested_repository_scope"`
	AuthorizationDigest      string                        `json:"authorization_digest"`

	// raw preserves an independently unavailable nested census while the
	// outer semantic snapshot remains readable. It is never exposed to a
	// consumer and is emitted only when decoding an existing stored row.
	raw json.RawMessage
}

// WorkItemTupleCensusReadStatus describes the independent status of the
// optional work-item census in an otherwise available semantic snapshot.
type WorkItemTupleCensusReadStatus string

const (
	WorkItemTupleCensusReadAvailable          WorkItemTupleCensusReadStatus = "available"
	WorkItemTupleCensusReadAbsent             WorkItemTupleCensusReadStatus = "absent"
	WorkItemTupleCensusReadUnsupportedVersion WorkItemTupleCensusReadStatus = "unsupported_version"
	WorkItemTupleCensusReadMalformed          WorkItemTupleCensusReadStatus = "malformed"
)

// ValidWorkItemTupleCensusReadStatus reports membership in the census read
// status vocabulary.
func ValidWorkItemTupleCensusReadStatus(status WorkItemTupleCensusReadStatus) bool {
	return slices.Contains([]WorkItemTupleCensusReadStatus{
		WorkItemTupleCensusReadAvailable,
		WorkItemTupleCensusReadAbsent,
		WorkItemTupleCensusReadUnsupportedVersion,
		WorkItemTupleCensusReadMalformed,
	}, status)
}

// MarshalJSON canonicalizes an available census, including one read through
// jsonb with a different object-key order. An unavailable stored census keeps
// its raw JSON value so its status cannot erase the outer semantic reading.
func (c WorkItemTupleCensus) MarshalJSON() ([]byte, error) {
	if len(c.raw) != 0 {
		decoded, status := decodeWorkItemTupleCensus(c.raw)
		if status != WorkItemTupleCensusReadAvailable || !workItemTupleCensusesEqual(&c, &decoded) {
			return append([]byte(nil), c.raw...), nil
		}
	}
	type wire struct {
		Version                  string                        `json:"version"`
		State                    WorkItemMembershipCensusState `json:"state"`
		Value                    int                           `json:"value"`
		Retained                 int                           `json:"retained"`
		RequestedRepositoryScope []string                      `json:"requested_repository_scope"`
		AuthorizationDigest      string                        `json:"authorization_digest"`
	}
	return json.Marshal(wire{
		Version:                  c.Version,
		State:                    c.State,
		Value:                    c.Value,
		Retained:                 c.Retained,
		RequestedRepositoryScope: append([]string{}, c.RequestedRepositoryScope...),
		AuthorizationDigest:      c.AuthorizationDigest,
	})
}

// UnmarshalJSON never rejects a valid JSON value for this nested field. The
// semantic snapshot owns its own availability; callers use
// ValidateWorkItemTupleCensus to decide whether this optional census is safe.
func (c *WorkItemTupleCensus) UnmarshalJSON(raw []byte) error {
	if c == nil {
		return nil
	}
	*c = WorkItemTupleCensus{raw: append([]byte(nil), raw...)}
	decoded, status := decodeWorkItemTupleCensus(raw)
	if status == WorkItemTupleCensusReadAvailable {
		decoded.raw = append([]byte(nil), raw...)
		*c = decoded
	}
	return nil
}

// ValidateWorkItemTupleCensus validates the optional census independently of
// the containing semantic state. A nil census is an ordinary absence. The
// helper never turns an outer available semantic reading into an error.
func ValidateWorkItemTupleCensus(census *WorkItemTupleCensus) WorkItemTupleCensusReadStatus {
	if census == nil {
		return WorkItemTupleCensusReadAbsent
	}
	if len(census.raw) == 0 {
		encoded, err := json.Marshal(census)
		if err != nil {
			return WorkItemTupleCensusReadMalformed
		}
		_, status := decodeWorkItemTupleCensus(encoded)
		return status
	}
	decoded, status := decodeWorkItemTupleCensus(census.raw)
	if status != WorkItemTupleCensusReadAvailable {
		return status
	}
	if !workItemTupleCensusesEqual(census, &decoded) {
		return WorkItemTupleCensusReadMalformed
	}
	return WorkItemTupleCensusReadAvailable
}

func decodeWorkItemTupleCensus(raw []byte) (WorkItemTupleCensus, WorkItemTupleCensusReadStatus) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadAbsent
	}
	var version struct {
		Version *string `json:"version"`
	}
	if err := json.Unmarshal(raw, &version); err != nil || version.Version == nil || *version.Version == "" {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadMalformed
	}
	if *version.Version != WorkItemTupleCensusVersion {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadUnsupportedVersion
	}
	type wire struct {
		Version                  *string                        `json:"version"`
		State                    *WorkItemMembershipCensusState `json:"state"`
		Value                    *int                           `json:"value"`
		Retained                 *int                           `json:"retained"`
		RequestedRepositoryScope *[]string                      `json:"requested_repository_scope"`
		AuthorizationDigest      *string                        `json:"authorization_digest"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value wire
	if err := decoder.Decode(&value); err != nil {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadMalformed
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadMalformed
	}
	if value.Version == nil || value.State == nil || value.Value == nil || value.Retained == nil || value.RequestedRepositoryScope == nil || value.AuthorizationDigest == nil {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadMalformed
	}
	census := WorkItemTupleCensus{
		Version:                  *value.Version,
		State:                    *value.State,
		Value:                    *value.Value,
		Retained:                 *value.Retained,
		RequestedRepositoryScope: append([]string{}, (*value.RequestedRepositoryScope)...),
		AuthorizationDigest:      *value.AuthorizationDigest,
	}
	if !validWorkItemTupleCensus(census) {
		return WorkItemTupleCensus{}, WorkItemTupleCensusReadMalformed
	}
	return census, WorkItemTupleCensusReadAvailable
}

func validWorkItemTupleCensus(census WorkItemTupleCensus) bool {
	if census.Version != WorkItemTupleCensusVersion || !validWorkItemTupleCensusDigest(census.AuthorizationDigest) {
		return false
	}
	if census.Value < 0 || census.Retained < 0 {
		return false
	}
	switch census.State {
	case WorkItemMembershipCensusExact:
		return census.Value <= WorkItemMembershipCensusLimit && census.Retained <= min(WorkItemMembershipServeLimit, census.Value)
	case WorkItemMembershipCensusFloor:
		return census.Value == WorkItemMembershipCensusLimit && census.Retained <= WorkItemMembershipServeLimit
	case WorkItemMembershipCensusUnmeasured:
		return census.Value == 0 && census.Retained == 0
	default:
		return false
	}
}

func validWorkItemTupleCensusDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func workItemTupleCensusesEqual(left, right *WorkItemTupleCensus) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Version == right.Version &&
		left.State == right.State &&
		left.Value == right.Value &&
		left.Retained == right.Retained &&
		slices.Equal(left.RequestedRepositoryScope, right.RequestedRepositoryScope) &&
		left.AuthorizationDigest == right.AuthorizationDigest
}
