package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A deployment whose per-organization model source is switched off cannot tell
// a saved-config organization from one with none, so even an organization
// holding both flags is refused rather than drawing the platform model.
func TestAnOffOrgModelConfigSourceNeverDrawsThePlatformModel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		off, open   bool
		wantAllowed bool
		wantErr     bool
	}{
		{name: "source off with both flags is unavailable", off: true, wantErr: true},
		{name: "source off on an open deployment keeps the platform model", off: true, open: true, wantAllowed: true},
		{name: "no source marked off keeps the platform model", wantAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), runtime: &RuntimeDependencies{
				Entitlements: &serviceModelGrants{feature: true, fallback: true}, OrgModelConfigsOff: tc.off, ServiceModelOpen: tc.open,
			}}
			_, allowed, err := app.serviceModelDecision(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.InvestigationRequest{})
			if allowed != tc.wantAllowed || (err != nil) != tc.wantErr {
				t.Fatalf("allowed = %t err = %v, want allowed %t error %t", allowed, err, tc.wantAllowed, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, errServiceModelOwnModelUnavailable) {
				t.Fatalf("err = %v, want the own-model-unavailable refusal", err)
			}
		})
	}
}
