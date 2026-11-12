package v1

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// CHAOS-13562: one constant bounds a device approval, its hints and an issued
// credential; every device schema carries the same maxItems.
func TestRepositoryScopeBoundIsTheDeviceSchemasMaxItems(t *testing.T) {
	if MaxRepositoryScopes != 256 {
		t.Fatalf("MaxRepositoryScopes = %d, want 256 (the ops decision point's MaxRepositoryAllowlist)", MaxRepositoryScopes)
	}
	cells := []struct {
		schema string
		path   []string
	}{
		{"device_approval_request.v1.schema.json", []string{"properties", "repository_scopes", "anyOf", "0", "maxItems"}},
		{"device_approval_preview_request.v1.schema.json", []string{"properties", "repository_scopes", "anyOf", "0", "maxItems"}},
		{"device_authorization_request.v1.schema.json", []string{"properties", "repository_hints", "maxItems"}},
		{"device_approval_preview_response.v1.schema.json", []string{"properties", "repository_hints", "maxItems"}},
		{"acr_client_credential.v1.schema.json", []string{"properties", "repository_scopes", "maxItems"}},
	}
	for _, cell := range cells {
		var node any = loadSchemaDocument(t, cell.schema)
		for _, key := range cell.path {
			switch typed := node.(type) {
			case map[string]any:
				node = typed[key]
			case []any:
				var index int
				if _, err := fmt.Sscan(key, &index); err != nil || index >= len(typed) {
					t.Fatalf("%s: bad index %q", cell.schema, key)
				}
				node = typed[index]
			default:
				t.Fatalf("%s: no %q under %v", cell.schema, key, cell.path)
			}
		}
		if node != float64(MaxRepositoryScopes) {
			t.Errorf("%s %s = %v, want %d", cell.schema, strings.Join(cell.path, "."), node, MaxRepositoryScopes)
		}
	}
}

func TestRepositoryScopeBoundRefusesOneOverByName(t *testing.T) {
	scopes := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("org/repo-%04d", i)
		}
		return out
	}
	for _, cell := range []struct {
		count   int
		wantErr bool
	}{{1, false}, {100, false}, {101, false}, {MaxRepositoryScopes, false}, {MaxRepositoryScopes + 1, true}} {
		request := DeviceApprovalRequest{SchemaVersion: DeviceApprovalRequestSchema, UserCode: "BCDFGHJK", RepositoryScopes: scopes(cell.count)}
		err := request.Validate()
		if cell.wantErr != (err != nil) || (cell.wantErr && !errors.Is(err, ErrTooManyRepositoryScopes)) {
			t.Errorf("%d scopes: err = %v, want too many = %v", cell.count, err, cell.wantErr)
		}
		preview := DeviceApprovalPreviewRequest{SchemaVersion: DeviceApprovalPreviewRequestSchema, UserCode: "BCDFGHJK", RepositoryScopes: scopes(cell.count)}
		if previewErr := preview.Validate(); cell.wantErr != (previewErr != nil) || (cell.wantErr && !errors.Is(previewErr, ErrTooManyRepositoryScopes)) {
			t.Errorf("%d preview scopes: err = %v, want too many = %v", cell.count, previewErr, cell.wantErr)
		}
		hints := scopes(cell.count)
		hintErr := DeviceAuthorizationRequest{SchemaVersion: DeviceAuthorizationRequestSchema, RepositoryHints: &hints}.Validate()
		if cell.wantErr != (hintErr != nil) {
			t.Errorf("%d hints: err = %v, want refused = %v", cell.count, hintErr, cell.wantErr)
		}
		if got := validCredentialRepositoryScopes(scopes(cell.count)); got == cell.wantErr {
			t.Errorf("%d credential scopes: valid = %v, want %v", cell.count, got, !cell.wantErr)
		}
	}
	if !IsInvalidRequestReason(TooManyRepositoriesReason) {
		t.Fatal("TooManyRepositoriesReason is not in the invalid_request vocabulary")
	}
}
