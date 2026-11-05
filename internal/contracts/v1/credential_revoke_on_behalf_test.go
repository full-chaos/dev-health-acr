package v1

import (
	"strings"
	"testing"
)

func TestCredentialRevokeOnBehalfRequestValidateAdmitsOnlyTheClosedShape(t *testing.T) {
	valid := func(actorType, actorID string) CredentialRevokeOnBehalfRequest {
		return CredentialRevokeOnBehalfRequest{SchemaVersion: CredentialRevokeOnBehalfRequestSchema, ActorType: actorType, ActorID: actorID}
	}
	for _, request := range []CredentialRevokeOnBehalfRequest{
		valid(CredentialRevokeOnBehalfActorUser, "usr_a"),
		valid(CredentialRevokeOnBehalfActorAdmin, "u"),
		valid(CredentialRevokeOnBehalfActorAdmin, strings.Repeat("é", 200)),
	} {
		if err := request.Validate(); err != nil {
			t.Errorf("%+v refused: %v", request, err)
		}
	}
	for name, request := range map[string]CredentialRevokeOnBehalfRequest{
		"schema empty":      {ActorType: "admin", ActorID: "usr_a"},
		"schema other":      {SchemaVersion: CredentialRevokeRequestSchema, ActorType: "admin", ActorID: "usr_a"},
		"type empty":        valid("", "usr_a"),
		"type operator":     valid("operator", "usr_a"),
		"type system":       valid("system", "usr_a"),
		"type credential":   valid("credential", "usr_a"),
		"type upper":        valid("ADMIN", "usr_a"),
		"type padded":       valid(" admin", "usr_a"),
		"id empty":          valid("admin", ""),
		"id blank":          valid("admin", "   "),
		"id leading space":  valid("admin", " usr_a"),
		"id trailing tab":   valid("user", "usr_a\t"),
		"id past the bound": valid("admin", strings.Repeat("u", 201)),
	} {
		if err := request.Validate(); err == nil {
			t.Errorf("%s: %+v admitted", name, request)
		}
	}
}
