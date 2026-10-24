package genkitruntime

import (
	"bytes"
	"encoding/json"
)

const (
	llmToolInputLiftedMessage = "context fabric model reply had its tool input wrapper lifted"
	toolInputWrapperKey       = "input"
)

// liftToolInputWrapper moves the members of a root "input" object up to the
// root, the tool_use block's own field name a model on the forced-tool path
// can wrap its arguments in. It lifts one level only, never changes a value,
// and changes nothing when the schema declares "input", when "input" is not an
// object, or when any lifted key is already at the root. It returns the count
// of lifted keys; a candidate with nothing lifted is returned byte-identical.
func liftToolInputWrapper(candidate string, schema map[string]any) (string, int) {
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		return candidate, 0
	}
	if _, declared := properties[toolInputWrapperKey]; declared {
		return candidate, 0
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(candidate)))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return candidate, 0
	}
	wrapped, _ := root[toolInputWrapperKey].(map[string]any)
	if len(wrapped) == 0 {
		return candidate, 0
	}
	for key := range wrapped {
		if _, taken := root[key]; taken {
			return candidate, 0
		}
	}
	delete(root, toolInputWrapperKey)
	for key, value := range wrapped {
		root[key] = value
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(root); err != nil {
		return candidate, 0
	}
	return string(bytes.TrimSpace(out.Bytes())), len(wrapped)
}
