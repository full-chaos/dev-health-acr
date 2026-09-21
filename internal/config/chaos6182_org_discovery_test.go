package config

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// CHAOS-6182: ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY's whole input
// domain against the one validation rule it changes -- the empty-allowlist
// refusal. The knob is additive and default-off, so the OFF cells must be
// byte-identical to the pre-CHAOS-6182 behavior.

func projectionEnvironment(extra map[string]string) map[string]string {
	env := map[string]string{
		"ACR_ENVIRONMENT":                       "production",
		"ACR_CLICKHOUSE_DSN":                    "https://clickhouse.internal",
		"ACR_POSTGRES_DSN":                      "postgres://db/acr",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// TestOrgDiscoveryEnabledAcceptsAnEmptyOrgAllowlist is the change: an
// operator turning discovery on no longer has to name a seed organization
// the projector will discover on its own a few seconds later.
func TestOrgDiscoveryEnabledAcceptsAnEmptyOrgAllowlist(t *testing.T) {
	cfg, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true",
	})), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector error = %v, want success with discovery enabled and no static allowlist", err)
	}
	if !cfg.OrgDiscoveryEnabled {
		t.Fatal("OrgDiscoveryEnabled must be true")
	}
	if len(cfg.OrgIDs) != 0 {
		t.Fatalf("OrgIDs = %#v, want empty", cfg.OrgIDs)
	}
}

// TestOrgDiscoveryDisabledStillRefusesAnEmptyOrgAllowlist is the guard that
// must NOT have moved: with discovery off, a projector that can name no
// organizations is still refused, with the same variable named in the
// message.
func TestOrgDiscoveryDisabledStillRefusesAnEmptyOrgAllowlist(t *testing.T) {
	for _, discovery := range []struct {
		name string
		env  map[string]string
	}{
		{name: "unset", env: map[string]string{}},
		{name: "explicit false", env: map[string]string{"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "false"}},
	} {
		t.Run(discovery.name, func(t *testing.T) {
			_, err := loadProjector(mapLookup(projectionEnvironment(discovery.env)), requiredStoresAll)
			if err == nil {
				t.Fatal("expected a refusal: projection enabled with neither an allowlist nor discovery")
			}
			if !strings.Contains(err.Error(), envContextFabricProjectorOrgs) {
				t.Fatalf("refusal must name %s; got %v", envContextFabricProjectorOrgs, err)
			}
			// The refusal must also point at the way out, or an operator
			// reading it has no reason to think discovery exists.
			if !strings.Contains(err.Error(), envContextFabricOrgDiscovery) {
				t.Fatalf("refusal must name %s as the alternative; got %v", envContextFabricOrgDiscovery, err)
			}
		})
	}
}

// TestOrgDiscoveryWithAStaticAllowlistIsAccepted: discovery and a static
// allowlist are not alternatives -- the allowlist stays a floor, so both
// set together is a legitimate configuration, not a conflict.
func TestOrgDiscoveryWithAStaticAllowlistIsAccepted(t *testing.T) {
	cfg, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true",
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":       "org-1,org-2",
	})), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector error = %v, want success", err)
	}
	if !cfg.OrgDiscoveryEnabled || len(cfg.OrgIDs) != 2 {
		t.Fatalf("config = %#v, want discovery enabled with a two-entry allowlist", cfg)
	}
}

// TestOrgDiscoveryRejectsANonBooleanValue: the knob is a bool, and an
// unparseable value must refuse the start rather than silently default to
// off -- a typo'd "ACR_..._DISCOVERY=yes" that quietly means "disabled"
// would leave an operator convinced discovery is running.
func TestOrgDiscoveryRejectsANonBooleanValue(t *testing.T) {
	// A blank/whitespace value is deliberately NOT in this list: boolValue
	// treats it as unset for every boolean in this package, so " " means
	// "default" (off), not "unparseable". Asserted as its own cell below
	// rather than left implicit.
	for _, value := range []string{"yes", "on", "1.5", "maybe"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": value,
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":       "org-1",
			})), requiredStoresAll)
			if err == nil {
				t.Fatalf("expected a refusal for %s=%q", envContextFabricOrgDiscovery, value)
			}
		})
	}
}

// TestOrgDiscoveryAcceptsTheCanonicalBooleanSpellings pins the accepted
// half of the same domain, so the refusal above is a statement about
// unparseable values rather than about this variable being unreadable.
func TestOrgDiscoveryAcceptsTheCanonicalBooleanSpellings(t *testing.T) {
	for value, want := range map[string]bool{"true": true, "TRUE": true, "1": true, "false": false, "FALSE": false, "0": false} {
		t.Run(value, func(t *testing.T) {
			cfg, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": value,
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":       "org-1",
			})), requiredStoresAll)
			if err != nil {
				t.Fatalf("loadProjector(%s=%q) error = %v", envContextFabricOrgDiscovery, value, err)
			}
			if cfg.OrgDiscoveryEnabled != want {
				t.Fatalf("OrgDiscoveryEnabled = %v, want %v", cfg.OrgDiscoveryEnabled, want)
			}
		})
	}
}

// TestOrgDiscoveryTreatsABlankValueAsUnset is the remaining domain cell:
// present-but-blank. It follows boolValue's package-wide convention (blank
// == unset == the fallback), which matters here because the fallback is
// FALSE and the empty-allowlist refusal is therefore still in force -- a
// blank value must not be a back door into accepting an empty allowlist.
func TestOrgDiscoveryTreatsABlankValueAsUnset(t *testing.T) {
	for _, value := range []string{"", " ", "\t"} {
		t.Run("blank_"+value, func(t *testing.T) {
			cfg, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": value,
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":       "org-1",
			})), requiredStoresAll)
			if err != nil {
				t.Fatalf("loadProjector error = %v", err)
			}
			if cfg.OrgDiscoveryEnabled {
				t.Fatalf("a blank %s must mean unset (off), not enabled", envContextFabricOrgDiscovery)
			}
			// ...and with the allowlist removed, the refusal is still in force.
			if _, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": value,
			})), requiredStoresAll); err == nil {
				t.Fatalf("a blank %s must not accept an empty allowlist", envContextFabricOrgDiscovery)
			}
		})
	}
}

// TestPriorsIgnoresOrgDiscoveryEntirely: the priors operator surface never
// runs the projection loop, so neither the allowlist refusal nor its
// discovery escape hatch applies to it -- the pre-existing
// required.projection gate already covers this, and must keep covering it.
func TestPriorsIgnoresOrgDiscoveryEntirely(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":                           "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND":               "direct",
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED":      "true",
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true",
	}), requiredStoresPostgresOnly)
	if err != nil {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, want success", err)
	}
	if !cfg.OrgDiscoveryEnabled {
		t.Fatal("the value is still parsed for priors; only the projection-loop validation is skipped")
	}
}

// TestSafeAttributesDisclosesOrgDiscovery: the startup line an operator
// reads to confirm what this process is doing must say whether discovery is
// on. Counts and booleans only -- never an organization identifier.
func TestSafeAttributesDisclosesOrgDiscovery(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := ProjectorConfig{OrgDiscoveryEnabled: enabled, OrgIDs: []string{"org-1"}}
		attributes := cfg.SafeAttributes()
		found := false
		for i := 0; i+1 < len(attributes); i += 2 {
			key, ok := attributes[i].(string)
			if !ok || key != "org_discovery_enabled" {
				continue
			}
			found = true
			if attributes[i+1] != enabled {
				t.Fatalf("org_discovery_enabled = %v, want %v", attributes[i+1], enabled)
			}
		}
		if !found {
			t.Fatalf("SafeAttributes must disclose org_discovery_enabled; got %#v", attributes)
		}
	}
}

// TestOrgActivityWindowDefaultMatchesTheAdapter pins the duplicated
// constant. internal/config must not import a Context Fabric adapter
// package, so defaultProjectorOrgActivityWindow restates
// devhealthsource.DefaultOrgActivityWindow -- two declarations of one value
// that a test, not hope, keeps equal.
func TestOrgActivityWindowDefaultMatchesTheAdapter(t *testing.T) {
	if defaultProjectorOrgActivityWindow != devhealthsource.DefaultOrgActivityWindow {
		t.Fatalf("config default = %s, adapter default = %s -- the two must agree",
			defaultProjectorOrgActivityWindow, devhealthsource.DefaultOrgActivityWindow)
	}
}

// TestOrgActivityWindowDomain sweeps the window's input domain. Zero is
// MEANINGFUL (the activity condition off), so it cannot double as the
// unset sentinel; unset takes the default; a negative value is refused
// rather than coerced, because coercing it would be a guess at which of
// two very different behaviors the operator meant.
func TestOrgActivityWindowDomain(t *testing.T) {
	for _, testCase := range []struct {
		name, value string
		want        time.Duration
		refused     bool
	}{
		{name: "unset", value: "", want: defaultProjectorOrgActivityWindow},
		{name: "blank", value: "   ", want: defaultProjectorOrgActivityWindow},
		{name: "explicit default", value: "720h", want: 720 * time.Hour},
		{name: "shorter", value: "168h", want: 168 * time.Hour},
		{name: "zero disables", value: "0s", want: 0},
		{name: "negative", value: "-1h", refused: true},
		{name: "unparseable", value: "forever", refused: true},
		{name: "bare number", value: "720", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := projectionEnvironment(map[string]string{
				"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true",
			})
			if testCase.value != "" {
				env["ACR_CONTEXT_FABRIC_PROJECTOR_ORG_ACTIVITY_WINDOW"] = testCase.value
			}
			cfg, err := loadProjector(mapLookup(env), requiredStoresAll)
			if testCase.refused {
				if err == nil {
					t.Fatalf("expected a refusal for %s=%q", envContextFabricOrgActivity, testCase.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadProjector error = %v", err)
			}
			if cfg.OrgActivityWindow != testCase.want {
				t.Fatalf("OrgActivityWindow = %s, want %s", cfg.OrgActivityWindow, testCase.want)
			}
		})
	}
}

// TestOrgDiscoveryDenyListParsesLikeTheAllowlist: the deny list uses the
// same comma-separated, whitespace-tolerant spelling as the allowlist, so
// an operator does not have to learn a second format for the same shape of
// value.
func TestOrgDiscoveryDenyListParsesLikeTheAllowlist(t *testing.T) {
	cfg, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY":          "true",
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY_DENY_IDS": "org-junk-1, org-junk-2 ,org-junk-3",
	})), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector error = %v", err)
	}
	if len(cfg.OrgDiscoveryDenyIDs) != 3 || cfg.OrgDiscoveryDenyIDs[0] != "org-junk-1" || cfg.OrgDiscoveryDenyIDs[2] != "org-junk-3" {
		t.Fatalf("OrgDiscoveryDenyIDs = %#v", cfg.OrgDiscoveryDenyIDs)
	}

	// Unset is empty, never a refusal: a deny list is optional.
	unset, err := loadProjector(mapLookup(projectionEnvironment(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true",
	})), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector error = %v", err)
	}
	if len(unset.OrgDiscoveryDenyIDs) != 0 {
		t.Fatalf("OrgDiscoveryDenyIDs = %#v, want empty", unset.OrgDiscoveryDenyIDs)
	}
}

// TestSafeAttributesDisclosesTheDiscoveryBounds: the startup line must say
// what discovery will actually do -- the window it enforces and how many
// organizations are denied. Counts and a duration only, never an
// organization identifier.
func TestSafeAttributesDisclosesTheDiscoveryBounds(t *testing.T) {
	cfg := ProjectorConfig{OrgDiscoveryEnabled: true, OrgActivityWindow: 168 * time.Hour, OrgDiscoveryDenyIDs: []string{"a", "b"}}
	attributes := cfg.SafeAttributes()
	values := map[string]any{}
	for i := 0; i+1 < len(attributes); i += 2 {
		if key, ok := attributes[i].(string); ok {
			values[key] = attributes[i+1]
		}
	}
	if values["org_activity_window"] != "168h0m0s" {
		t.Fatalf("org_activity_window = %v", values["org_activity_window"])
	}
	if values["org_discovery_deny_count"] != 2 {
		t.Fatalf("org_discovery_deny_count = %v, want 2", values["org_discovery_deny_count"])
	}
	for _, attribute := range attributes {
		if attribute == "a" || attribute == "b" {
			t.Fatal("SafeAttributes must never carry an organization identifier")
		}
	}
}
