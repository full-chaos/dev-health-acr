package devhealthsource

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// cursorState is the opaque cursor contents. An empty ProjectionCheckpoint
// cursor (the zero value) means "never projected" and triggers a bounded
// full-snapshot batch; any other cursor resumes incremental projection from
// the encoded watermark.
type cursorState struct {
	Since time.Time `json:"since"`
	After string    `json:"after"`
}

func decodeCursor(raw string) (cursorState, error) {
	if raw == "" {
		return cursorState{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursorState{}, fmt.Errorf("devhealthsource: decode cursor: %w", err)
	}
	var state cursorState
	if err := json.Unmarshal(decoded, &state); err != nil {
		return cursorState{}, fmt.Errorf("devhealthsource: decode cursor: %w", err)
	}
	return state, nil
}

func encodeCursor(state cursorState) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("devhealthsource: encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// after reports whether row (at, id) sorts strictly after the cursor.
func (c cursorState) after(at time.Time, id string) bool {
	if at.After(c.Since) {
		return true
	}
	return at.Equal(c.Since) && id > c.After
}
