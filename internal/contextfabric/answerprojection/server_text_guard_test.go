package answerprojection

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The canonical result carries text the SERVER writes (the status sentence, a
// count, a period total). A projection that does not carry a field of that
// kind hides it from every client, and a test of the engine's own value cannot
// see it. This guard walks the result's text fields: each is set to a marker,
// projected and read back from the encoded projection. A field the projection
// does not carry must be listed here with the reason, so a NEW text field
// cannot be added to the result and silently dropped.
func TestEveryResultTextFieldIsServedOrDeclaredNotServed(t *testing.T) {
	notServed := map[string]string{
		"SchemaVersion":  "a version tag; the projection has its own schema_version",
		"ResultID":       "served under its own typed handling (result_id), not as free text",
		"RequestID":      "served under its own typed handling (request_id)",
		"Question":       "served under its own typed handling (question), clamped",
		"EvidenceRefIDs": "opaque ids, never prose; served as evidence_ref_ids, filtered to the refs the projection indexed",
	}
	base := richResult()
	resultType := reflect.TypeOf(base)
	for index := 0; index < resultType.NumField(); index++ {
		field := resultType.Field(index)
		isText := field.Type.Kind() == reflect.String && field.Type.PkgPath() == ""
		isTextList := field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.String && field.Type.Elem().PkgPath() == ""
		if !isText && !isTextList {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			if reason, declared := notServed[field.Name]; declared {
				if reason == "" {
					t.Fatalf("%s is declared not served without a reason", field.Name)
				}
				return
			}
			result := base
			marker := "MARKER-" + strings.ToUpper(field.Name) + "-SERVED"
			value := reflect.ValueOf(&result).Elem().Field(index)
			if isText {
				value.SetString(marker)
			} else {
				value.Set(reflect.ValueOf([]string{marker}))
			}
			encoded, err := json.Marshal(Project(result, Budget{}))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), marker) {
				t.Errorf("result.%s (json %q) is not in the projection a client receives; carry it, or declare it in notServed with the reason",
					field.Name, field.Tag.Get("json"))
			}
		})
	}
}
