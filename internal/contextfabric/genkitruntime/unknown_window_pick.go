package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12617: window_class and window_confidence are optional picks the
// engine can do without (CHAOS-3900 F5: an out-of-vocabulary pick is never a
// validation failure). The output schema keeps their enums, because a
// provider's structured output reads them as the choices it may make. But the
// schema gate in front of the sanitizer, the LLM generator's check and the
// supplied path's decode, refused a whole otherwise-valid interpretation for
// one unknown pick. That gate now validates a copy of the reply with such a
// pick removed, and the caller decodes the reply as written, so
// sanitizeWindowOutput still sees the raw pick: the class stays unset and
// window_class_unrecognized is set, as on a transport with no schema gate.

const unknownWindowPickDroppedMessage = "context fabric interpretation reply had an unknown window pick dropped before the schema check"

// windowPickFields are the optional picks an unknown value of which is dropped
// rather than refused. Each has a closed enum in the interpretation schema.
var windowPickFields = []string{"window_class", "window_confidence"}

// withoutUnknownWindowPicks returns candidate with each top-level window pick
// whose value is a string outside its schema enum removed, and the names of the
// fields it removed. Anything else is returned as written, so the schema still
// judges it: a reply that is not an object, a pick that is not a string, a field
// the schema gives no enum. It never adds or changes a value.
func withoutUnknownWindowPicks(candidate string, schema map[string]any) (string, []string) {
	properties, _ := schema["properties"].(map[string]any)
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &object); err != nil || object == nil {
		return candidate, nil
	}
	var dropped []string
	for _, field := range windowPickFields {
		raw, present := object[field]
		if !present {
			continue
		}
		// Only a JSON string is a pick: json.Unmarshal reads null into a
		// string as "", and a null is a type violation the schema judges.
		var value string
		if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '"' {
			continue
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		enum := schemaEnum(properties, field)
		if enum == nil || slices.Contains(enum, value) {
			continue
		}
		delete(object, field)
		dropped = append(dropped, field)
	}
	if len(dropped) == 0 {
		return candidate, nil
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return candidate, nil
	}
	return string(encoded), dropped
}

// schemaEnum is the string enum the schema declares for one property, or nil
// when it declares none.
func schemaEnum(properties map[string]any, field string) []string {
	property, _ := properties[field].(map[string]any)
	values, _ := property["enum"].([]any)
	if len(values) == 0 {
		return nil
	}
	enum := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			enum = append(enum, text)
		}
	}
	return enum
}

// logUnknownWindowPicksDropped writes the one Info line of a drop. It names the
// fields only: the value is model text and never reaches the log.
func logUnknownWindowPicksDropped(ctx context.Context, logger *slog.Logger, operation string, dropped []string) {
	logger.InfoContext(ctx, unknownWindowPickDroppedMessage, "operation", contextfabric.SanitizeLogAttr(operation),
		"fields", contextfabric.SanitizeLogStrings(dropped))
}
