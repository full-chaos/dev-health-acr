package contractcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ValidateSerialized is the tolerant (consumer) check: an object may carry
// keys its schema does not declare when the schema leaves it open.
func ValidateSerialized(root, schemaName string, payload []byte) error {
	return validateSerialized(root, schemaName, payload, false)
}

// ValidateSerializedStrict is the producer check: every key of a served
// payload must be declared, even where the schema is open to added fields.
func ValidateSerializedStrict(root, schemaName string, payload []byte) error {
	return validateSerialized(root, schemaName, payload, true)
}

func validateSerialized(root, schemaName string, payload []byte, strict bool) error {
	resolvedRoot, err := findRoot(root)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode serialized JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return fmt.Errorf("trailing serialized JSON: %w", err)
	}
	check := &repositoryCheck{root: resolvedRoot, out: io.Discard, quiet: true}
	if err := check.loadSchemas(); err != nil {
		return err
	}
	if err := check.registry.validate(schemaName, value); err != nil {
		return fmt.Errorf("validate %s: %w", schemaName, err)
	}
	if strict {
		if err := check.registry.rejectUndeclared(schemaName, value); err != nil {
			return fmt.Errorf("validate %s: %w", schemaName, err)
		}
	}
	return nil
}
