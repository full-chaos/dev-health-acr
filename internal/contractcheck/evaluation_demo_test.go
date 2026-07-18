package contractcheck

import "testing"

func TestExampleSchemaPairs_registers_evaluation_demo_sample(t *testing.T) {
	// Given
	const example = "evaluation_demo.v1.json"

	// When
	schema, ok := exampleSchemaPairs[example]

	// Then
	if !ok || schema != "evaluation_demo.v1.schema.json" {
		t.Fatalf("evaluation demo schema pair = %q, present=%t", schema, ok)
	}
}
