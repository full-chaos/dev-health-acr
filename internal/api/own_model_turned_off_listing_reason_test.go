package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func turnedOffListingEntitlementReasons(logs *bytes.Buffer) (reasons, entitlementReasons []string) {
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != "context fabric service model listing decision" {
			continue
		}
		reason, _ := entry["reason"].(string)
		entitlementReason, _ := entry["entitlement_reason"].(string)
		reasons = append(reasons, reason)
		entitlementReasons = append(entitlementReasons, entitlementReason)
	}
	return reasons, entitlementReasons
}

// An unreadable entitlement keeps the tool listed for an organization whose
// own model is turned off, and the listing log names the rejection class of
// the failed read, as every other listing branch does.
func TestListingWithTheOwnModelTurnedOffLogsTheRejectionReasonOfAnEntitlementRead(t *testing.T) {
	for _, read := range []struct {
		name string
		// failOn is the entitlement whose read fails; the other reads true.
		failOn string
	}{
		{name: "acr_service_model read fails", failOn: serviceModelEntitlement},
		{name: "platform_model_fallback read fails", failOn: platformModelFallbackEntitlement},
	} {
		for _, tc := range rejectionCases {
			t.Run(read.name+"/"+tc.reason, func(t *testing.T) {
				rejecting := rejectingEntitlementClient(t, tc.status, tc.body, tc.closed)
				logs := &bytes.Buffer{}
				app := &App{logger: slog.New(slog.NewJSONHandler(logs, nil)), runtime: &RuntimeDependencies{
					Entitlements: EntitlementFunc(func(ctx context.Context, orgID, entitlement string) (bool, error) {
						if entitlement == read.failOn {
							return rejecting.HasEntitlement(ctx, orgID, entitlement)
						}
						return true, nil
					}),
					OrgModelConfigs: ownModelConfigs{err: entitlements.ErrLLMConfigTurnedOff},
				}}

				if listed := app.serviceModelListable(context.Background(), storage.Principal{OrgID: "org_1"}); !listed {
					t.Fatal("serviceModelListable() = false, want true when the entitlement is unreadable")
				}
				reasons, entitlementReasons := turnedOffListingEntitlementReasons(logs)
				if len(reasons) != 1 || reasons[0] != "entitlement_unreadable" {
					t.Fatalf("listing reasons = %v, want [entitlement_unreadable]; logs=%s", reasons, logs.String())
				}
				if entitlementReasons[0] != tc.reason {
					t.Fatalf("entitlement_reason attrs = %v, want [%s]", entitlementReasons, tc.reason)
				}
			})
		}
	}
}
