{{/*
Private ACR chart helpers.

Fail-closed guards live here so that every security violation renders a named
error (mutable-image, invalid-secret-ref, invalid-image-pull-secret-ref,
shared-runtime-migration-dsn, injected-mcp, acr-mcp-*) before any manifest is produced.
*/}}

{{- define "acr.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "acr.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "acr.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "acr.labels" -}}
helm.sh/chart: {{ include "acr.chart" . }}
{{ include "acr.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: dev-health-acr
{{- end -}}

{{- define "acr.selectorLabels" -}}
app.kubernetes.io/name: {{ include "acr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: api
{{- end -}}

{{- define "acr.migrationSelectorLabels" -}}
app.kubernetes.io/name: {{ include "acr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: migration
{{- end -}}

{{- define "acr.migrationLabels" -}}
helm.sh/chart: {{ include "acr.chart" . }}
{{ include "acr.migrationSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: dev-health-acr
{{- end -}}

{{- define "acr.projectorSelectorLabels" -}}
app.kubernetes.io/name: {{ include "acr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: projector
{{- end -}}

{{- define "acr.projectorLabels" -}}
helm.sh/chart: {{ include "acr.chart" . }}
{{ include "acr.projectorSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: dev-health-acr
{{- end -}}

{{- define "acr.mcpSelectorLabels" -}}
app.kubernetes.io/name: {{ include "acr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: mcp
{{- end -}}

{{- define "acr.mcpLabels" -}}
helm.sh/chart: {{ include "acr.chart" . }}
{{ include "acr.mcpSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: dev-health-acr
{{- end -}}

{{- define "acr.falkordbSelectorLabels" -}}
app.kubernetes.io/name: {{ include "acr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: falkordb
{{- end -}}

{{- define "acr.falkordbLabels" -}}
helm.sh/chart: {{ include "acr.chart" . }}
{{ include "acr.falkordbSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: dev-health-acr
{{- end -}}

{{/*
FalkorDB workload image guard (CHAOS-4055). Always digest-pinned: the graph
backend was verified against one exact FalkorDB build (see
docs/design/context-fabric-falkordb-adapter.md), so no development/test tag
escape hatch exists here.
*/}}
{{- define "acr.falkordbImage" -}}
{{- $ref := .Values.contextFabric.falkordb.image | default "" -}}
{{- if not (regexMatch "@sha256:[0-9a-f]{64}$" $ref) -}}
{{- fail "mutable-image: contextFabric.falkordb.image must be an immutable @sha256 digest reference" -}}
{{- end -}}
{{- $ref -}}
{{- end -}}

{{- define "acr.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "acr.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
RFC1123 subdomain validity for Secret name references. Returns the name or
fails with the supplied violation label.
*/}}
{{- define "acr.requireSecretName" -}}
{{- $name := .name | default "" -}}
{{- $label := .label -}}
{{- $field := .field -}}
{{- if not $name -}}
{{- fail (printf "%s: %s must reference a non-empty existing Secret name" $label $field) -}}
{{- end -}}
{{- if gt (len $name) 253 -}}
{{- fail (printf "%s: %s Secret name %q exceeds 253 characters" $label $field $name) -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $name) -}}
{{- fail (printf "%s: %s Secret reference %q is not a valid RFC1123 subdomain" $label $field $name) -}}
{{- end -}}
{{- $name -}}
{{- end -}}

{{/*
Immutable image reference guard shared by every workload image. Production
requires a digest; development and test may use a local kind-loaded tag, which
has no registry manifest to resolve. Called with {ref, env, field}.
*/}}
{{- define "acr.imageRef" -}}
{{- $ref := .ref | default "" -}}
{{- $field := .field -}}
{{- if not $ref -}}
{{- fail (printf "mutable-image: %s is required and must be an immutable @sha256 digest reference" $field) -}}
{{- end -}}

{{- if not (regexMatch "@sha256:[0-9a-f]{64}$" $ref) -}}
{{- if or (eq .env "development") (eq .env "test") -}}
{{- if not (regexMatch "^[a-zA-Z0-9][a-zA-Z0-9._/-]*:[a-zA-Z0-9][a-zA-Z0-9._-]*$" $ref) -}}
{{- fail (printf "mutable-image: development/test %s %q must be a local tagged image or immutable @sha256 digest" $field $ref) -}}
{{- end -}}
{{- else -}}
{{- fail (printf "mutable-image: %s %q must be pinned to an immutable @sha256:<digest>; mutable tags are rejected" $field $ref) -}}
{{- end -}}
{{- end -}}
{{- $ref -}}
{{- end -}}

{{- define "acr.image" -}}
{{- include "acr.imageRef" (dict "ref" .Values.image.reference "env" .Values.config.environment "field" "image.reference") -}}
{{- end -}}

{{/*
acr-mcp is a separate image (its own Dockerfile target), so it carries its own
reference under the same immutability rule as the acr-api image.
*/}}
{{- define "acr.mcpImage" -}}
{{- include "acr.imageRef" (dict "ref" .Values.acrMcp.image.reference "env" .Values.config.environment "field" "acrMcp.image.reference") -}}
{{- end -}}

{{- define "acr.tokenCopyImage" -}}
{{- $ref := .Values.security.tokenCopyImage | default "" -}}
{{- if not (regexMatch "@sha256:[0-9a-f]{64}$" $ref) -}}
{{- fail "mutable-image: security.tokenCopyImage must be an immutable @sha256 digest reference" -}}
{{- end -}}
{{- $ref -}}
{{- end -}}

{{/*
imagePullSecrets guard. Every entry must carry a valid existing Secret name.
*/}}
{{- define "acr.imagePullSecrets" -}}
{{- $secrets := .Values.imagePullSecrets | default list -}}
{{- range $i, $entry := $secrets -}}
{{- $_ := include "acr.requireSecretName" (dict "name" ($entry.name | default "") "label" "invalid-image-pull-secret-ref" "field" (printf "imagePullSecrets[%d].name" $i)) -}}
{{- end -}}
{{- if $secrets -}}
imagePullSecrets:
{{- range $secrets }}
  - name: {{ include "acr.requireSecretName" (dict "name" (.name | default "") "label" "invalid-image-pull-secret-ref" "field" "imagePullSecrets[].name") }}
{{- end }}
{{- end -}}
{{- end -}}

{{/*
Credential contract guard. Validates every credential Secret reference and
enforces that the migration DSN is not the runtime DSN reference.
*/}}
{{- define "acr.validateCredentials" -}}
{{- $c := .Values.credentials -}}
{{- $runtimeSecret := include "acr.requireSecretName" (dict "name" ($c.runtime.existingSecret | default "") "label" "invalid-secret-ref" "field" "credentials.runtime.existingSecret") -}}
{{- $migrationSecret := include "acr.requireSecretName" (dict "name" ($c.migration.existingSecret | default "") "label" "invalid-secret-ref" "field" "credentials.migration.existingSecret") -}}
{{- if include "acr.remoteEntitlement" . -}}
{{- $_ := include "acr.requireSecretName" (dict "name" ($c.entitlementToken.existingSecret | default "") "label" "invalid-secret-ref" "field" "credentials.entitlementToken.existingSecret") -}}
{{- end -}}
{{- $runtimeKey := $c.runtime.postgresDsnKey | default "ACR_POSTGRES_DSN" -}}
{{- $migrationKey := $c.migration.postgresDsnKey | default "ACR_POSTGRES_MIGRATION_DSN" -}}
{{- if and (eq $runtimeSecret $migrationSecret) (eq $runtimeKey $migrationKey) -}}
{{- fail (printf "shared-runtime-migration-dsn: the migration DSN reference (%s/%s) must differ from the runtime DSN reference; a schema-owner migration credential must never reuse the least-privilege runtime credential" $migrationSecret $migrationKey) -}}
{{- end -}}
{{- end -}}

{{/*
No additional-workload guard. deployment.extraContainers is unsupported: any
additional container would bypass the restricted pod-security guarantees, so a
non-empty value fails closed. A value that names acr-mcp is called out
specifically: acr-mcp is never injected beside acr-api; it runs as its own
workload through acrMcp.enabled. The Deployment never renders extraContainers
regardless.
*/}}
{{- define "acr.validateNoMcp" -}}
{{- $extra := .Values.deployment.extraContainers | default list -}}
{{- range $i, $ctr := $extra -}}
{{- $blob := printf "%s %s %s %s" ($ctr.name | default "") ($ctr.image | default "") (join " " ($ctr.command | default list)) (join " " ($ctr.args | default list)) -}}
{{- if regexMatch "acr-mcp" $blob -}}
{{- fail (printf "injected-mcp: deployment.extraContainers[%d] would run acr-mcp beside acr-api; acr-mcp runs only as its own workload (acrMcp.enabled)" $i) -}}
{{- end -}}
{{- end -}}
{{- if $extra -}}
{{- fail "injected-mcp: deployment.extraContainers is not permitted; additional workload containers would bypass the restricted pod-security guarantees" -}}
{{- end -}}
{{- end -}}

{{/*
acr-mcp workload guard. The hosted MCP server holds no credential of its own
and forwards each caller's bearer to acr-api, so its inputs are only an origin,
a base path and a gateway reference.
*/}}
{{- define "acr.validateAcrMcp" -}}
{{- $m := .Values.acrMcp -}}
{{- if $m.enabled -}}
{{- $_ := include "acr.mcpImage" . -}}
{{- $url := $m.apiUrl | default "" -}}
{{- if and $url (not (regexMatch "^https?://[^/@?#[:space:]]+$" $url)) -}}
{{- fail (printf "acr-mcp-api-url: acrMcp.apiUrl %q must be an HTTP(S) origin only (host[:port], no userinfo, path, query, or fragment)" $url) -}}
{{- end -}}
{{- $path := $m.basePath | default "" -}}
{{- if or (not (regexMatch "^/[A-Za-z0-9/_.-]{0,127}$" $path)) (hasSuffix "/" $path) (contains "//" $path) (contains "/../" (printf "%s/" $path)) (contains "/./" (printf "%s/" $path)) (eq $path "/healthz") (eq $path "/readyz") -}}
{{- fail (printf "acr-mcp-base-path: acrMcp.basePath %q must be an absolute path without a trailing slash, at most 128 characters of [A-Za-z0-9/_.-], and not a probe path" $path) -}}
{{- end -}}
{{- if and $m.gateway.enabled (not $m.gateway.httpRoute.parentRefs) -}}
{{- fail "acr-mcp-gateway: acrMcp.gateway.enabled is true but acrMcp.gateway.httpRoute.parentRefs is empty; a caller-supplied Gateway reference is required" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
In-cluster origin of acr-api unless acrMcp.apiUrl overrides it. The Service is
in the release namespace, so its short name resolves from the acr-mcp pods.
*/}}
{{- define "acr.mcpApiUrl" -}}
{{- if .Values.acrMcp.apiUrl -}}
{{- .Values.acrMcp.apiUrl -}}
{{- else -}}
{{- printf "http://%s.%s.svc:%d" (include "acr.fullname" .) .Release.Namespace (int .Values.service.port) -}}
{{- end -}}
{{- end -}}

{{/*
Connection-kind guard. When postgresConnectionKind is pgbouncer, both the
runtime and migration pooler admin DSN keys are required so transaction-pool
validation is wired. When direct, both MUST be absent: acr-migrate and the
hosted runtime reject an admin DSN in direct mode
(runtime/postgres.ValidateConnectionKind), so a configured pooler key is a
runtime-equivalence violation and fails closed here.
*/}}
{{- define "acr.validateConnectionKind" -}}
{{- $kind := .Values.config.postgresConnectionKind | default "direct" -}}
{{- $runtimePooler := .Values.credentials.runtime.poolerAdminDsnKey | default "" -}}
{{- $migrationPooler := .Values.credentials.migration.poolerAdminDsnKey | default "" -}}
{{- if eq $kind "pgbouncer" -}}
{{- if not $runtimePooler -}}
{{- fail "pgbouncer-admin-dsn: config.postgresConnectionKind is pgbouncer but credentials.runtime.poolerAdminDsnKey is empty; a PgBouncer admin DSN reference is required" -}}
{{- end -}}
{{- if not $migrationPooler -}}
{{- fail "pgbouncer-admin-dsn: config.postgresConnectionKind is pgbouncer but credentials.migration.poolerAdminDsnKey is empty; a PgBouncer admin DSN reference is required" -}}
{{- end -}}
{{- else if eq $kind "direct" -}}
{{- if $runtimePooler -}}
{{- fail (printf "direct-mode-pooler: config.postgresConnectionKind is direct but credentials.runtime.poolerAdminDsnKey (%s) is set; acr-api rejects a PgBouncer admin DSN in direct mode" $runtimePooler) -}}
{{- end -}}
{{- if $migrationPooler -}}
{{- fail (printf "direct-mode-pooler: config.postgresConnectionKind is direct but credentials.migration.poolerAdminDsnKey (%s) is set; acr-migrate rejects a PgBouncer admin DSN in direct mode" $migrationPooler) -}}
{{- end -}}
{{- else -}}
{{- fail (printf "invalid-connection-kind: config.postgresConnectionKind %q must be direct or pgbouncer" $kind) -}}
{{- end -}}
{{- end -}}

{{/*
Device-verification URL guard. The hosted runtime requires an absolute browser
URL whenever backing stores are enabled.
*/}}
{{- define "acr.validateDeviceVerificationURL" -}}
{{- $url := .Values.config.deviceVerificationUrl | default "" -}}
{{- if and .Values.config.requireBackingStores (not (regexMatch "^https?://[^[:space:]/?#]+(:[0-9]+)?([/?#][^[:space:]]*)?$" $url)) -}}
{{- fail (printf "device-verification-url: config.deviceVerificationUrl %q must be an absolute HTTP(S) URL when config.requireBackingStores=true" $url) -}}
{{- end -}}
{{- end -}}

{{- define "acr.validateWorkloadTokenExchange" -}}
{{- if .Values.workloadTokenExchange.enabled -}}
{{- if not (.Values.workloadTokenExchange.audience | default "") -}}
{{- fail "workload-token-exchange: workloadTokenExchange.audience is required when workloadTokenExchange.enabled=true" -}}
{{- end -}}
{{- if not (.Values.workloadTokenExchange.trustDomain | default "") -}}
{{- fail "workload-token-exchange: workloadTokenExchange.trustDomain is required when workloadTokenExchange.enabled=true" -}}
{{- end -}}
{{- /*
Codex round 1 finding: the TokenReview ClusterRoleBinding targets
acr.serviceAccountName, which defaults to the literal "default" namespace
ServiceAccount whenever serviceAccount.create=false and no explicit name is
set. Binding cluster-wide TokenReview capability to a SA shared with other
workloads is a real privilege-escalation surface, so this fails closed on
the one case this chart can actually detect: create=false with no explicit
name. An explicit serviceAccount.name is trusted as the operator's own
deliberate, out-of-band-provisioned choice -- EXCEPT the literal name
"default", which this chart still rejects explicitly: that name can never
be a deliberately dedicated choice, since it is the one every namespace's
built-in ServiceAccount already carries and is almost always shared with
whatever workloads never set their own. This chart cannot detect sharing
with any OTHER explicitly named pre-existing SA -- that remains the
operator's own responsibility (codex round 2 finding 2).
*/ -}}
{{- if not .Values.serviceAccount.create -}}
{{- $name := .Values.serviceAccount.name | default "" -}}
{{- if or (not $name) (eq $name "default") -}}
{{- fail "workload-token-exchange: workloadTokenExchange.enabled=true requires either serviceAccount.create=true or an explicit, non-\"default\" serviceAccount.name -- the TokenReview ClusterRoleBinding must never land on a namespace's shared default ServiceAccount" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
CHAOS-3916/CHAOS-4147 codex round-1 findings: two Context Fabric
misconfigurations a doc comment alone cannot prevent an operator from
making.

F1: contextFabric.embed.baseURL constructs a non-nil embedder
(embedprovider.Configured() checks ONLY baseURL) regardless of whether a
real API key is behind existingSecret -- a set baseURL with an absent
existingSecret is EXACTLY the CHAOS-4147 incident shape (a silently
"configured" embedder that destroys vectors on its first failed batch).
Only guarded when contextFabric.projector.enabled, matching this chart's
existing "unused values are not yet a hazard" posture (e.g.
validateConnectionKind never fires for a Deployment that isn't rendered).

F2: contextFabric.lifecycleEnabled activates acr-projector's build-aside-
and-swap epoch mechanics from the shared flag ALONE (projector-deployment.yaml
has no readsEnabled gate), independent of acr-api's Investigator gate
(contextFabric.readsEnabled -- see values.yaml's own comment: lifecycleEnabled
is inert on acr-api without it). A projector actively flipping an
organization's active epoch while no acr-api reader can ever observe the
result is a real, silent misconfiguration, not a valid degraded state --
guard it the same way the projector-specific F1 guard above does, only
when the projector is genuinely active (enabled AND projectionEnabled).
*/}}
{{- define "acr.validateContextFabricGraph" -}}
{{- $cf := .Values.contextFabric -}}
{{- $projectorActive := and $cf.projector.enabled $cf.projector.projectionEnabled -}}
{{- if and $cf.projector.enabled ($cf.embed.baseURL | default "") -}}
{{- $_ := include "acr.requireSecretName" (dict "name" ($cf.embed.existingSecret | default "") "label" "unconfigured-embedder" "field" "contextFabric.embed.existingSecret") -}}
{{- end -}}
{{- if and $projectorActive $cf.lifecycleEnabled (not $cf.readsEnabled) -}}
{{- fail "orphaned-lifecycle-writer: contextFabric.lifecycleEnabled=true with an active projector (contextFabric.projector.enabled and projectionEnabled) requires contextFabric.readsEnabled=true -- otherwise acr-projector actively flips this organization's active epoch while no acr-api reader can ever observe the result" -}}
{{- end -}}
{{- end -}}

{{/*
Remote entitlement is selected automatically when an origin is supplied. Local
allow-all entitlement is restricted to development/test and must not retain
remote token or CA inputs.
*/}}
{{- define "acr.remoteEntitlement" -}}
{{- if (.Values.config.entitlement.url | default "") -}}true{{- end -}}
{{- end -}}

{{- define "acr.validateEntitlementOrigin" -}}
{{- $url := .Values.config.entitlement.url | default "" -}}
{{- $environment := .Values.config.environment -}}
{{- $tokenSecret := .Values.credentials.entitlementToken.existingSecret | default "" -}}
{{- $caSecret := .Values.config.entitlementCaBundle.existingSecret | default "" -}}
{{- if $url -}}
{{- if not (regexMatch "^https?://[^/@?#]+$" $url) -}}
{{- fail (printf "entitlement-origin: config.entitlement.url %q must be an HTTP(S) origin only (http or https scheme, host[:port], no userinfo, path, query, or fragment)" $url) -}}
{{- end -}}
{{- if not $tokenSecret -}}
{{- fail "entitlement-partial: credentials.entitlementToken.existingSecret is required when config.entitlement.url is set" -}}
{{- end -}}
{{- else -}}
{{- if or (eq $environment "staging") (eq $environment "production") -}}
{{- fail (printf "entitlement-remote-required: config.entitlement.url and credentials.entitlementToken.existingSecret are required in %s" $environment) -}}
{{- end -}}
{{- if $tokenSecret -}}
{{- fail "entitlement-partial: credentials.entitlementToken.existingSecret must be empty when local entitlement is selected" -}}
{{- end -}}
{{- if $caSecret -}}
{{- fail "entitlement-partial: config.entitlementCaBundle.existingSecret must be empty when local entitlement is selected" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "acr.validateLocalCompositionReadiness" -}}
{{- if .Values.config.localCompositionReady -}}
{{- if or (ne .Values.config.environment "development") .Values.config.requireBackingStores -}}
{{- fail "local-composition-ready: config.localCompositionReady requires config.environment=development and config.requireBackingStores=false" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Credentials checksum input. Rolls pods when a referenced Secret changes name or
when the operator bumps credentials.rotationRevision after rotating Secret
content (which Helm cannot observe directly).
*/}}
{{- define "acr.credentialsChecksumInput" -}}
{{- $c := .Values.credentials -}}
runtime={{ $c.runtime.existingSecret | default "" }}:{{ $c.runtime.postgresDsnKey | default "" }}:{{ $c.runtime.clickhouseDsnKey | default "" }}:{{ $c.runtime.poolerAdminDsnKey | default "" }}:{{ $c.runtime.evidenceIdActiveKidKey | default "" }}:{{ $c.runtime.evidenceIdKeysKey | default "" }}
migration={{ $c.migration.existingSecret | default "" }}:{{ $c.migration.postgresDsnKey | default "" }}:{{ $c.migration.poolerAdminDsnKey | default "" }}
entitlement={{ $c.entitlementToken.existingSecret | default "" }}:{{ $c.entitlementToken.key | default "" }}
pullSecrets={{ range .Values.imagePullSecrets }}{{ .name | default "" }},{{ end }}
rotationRevision={{ $c.rotationRevision | default "" }}
{{- end -}}

{{/*
Restricted Pod Security context shared by the API Deployment and migration Job.
*/}}
{{- define "acr.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "acr.containerSecurityContext" -}}
allowPrivilegeEscalation: false
privileged: false
readOnlyRootFilesystem: true
runAsNonRoot: true
runAsUser: 65532
capabilities:
  drop:
    - ALL
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{/*
Non-secret environment shared by both workloads, sourced from the ConfigMap.
*/}}
{{- define "acr.configEnvFrom" -}}
- configMapRef:
    name: {{ include "acr.fullname" . }}-config
{{- end -}}

{{/*
OAuth login for hosted MCP clients: the issuer and at least one resource, the
complete web-assertion configuration its consent step depends on, and the web
consent page /authorize sends the browser to.
*/}}
{{- define "acr.validateOAuth" -}}
{{- $o := .Values.config.oauth -}}
{{- $w := .Values.config.webAssertion -}}
{{- if not (regexMatch "^https://[^/?#]+$" ($o.issuer | default "")) -}}
{{- fail (printf "oauth: config.oauth.issuer %q must be an https origin with no path" ($o.issuer | default "")) -}}
{{- end -}}
{{- if not $o.resources -}}
{{- fail "oauth: config.oauth.resources must list at least one hosted MCP URL" -}}
{{- end -}}
{{- range $o.resources -}}
{{- if not (regexMatch "^https://[^?#]+$" .) -}}
{{- fail (printf "oauth: config.oauth.resources entry %q must be an https URL without query or fragment" .) -}}
{{- end -}}
{{- end -}}
{{- if not (and $w.issuer $w.audience $w.existingSecret) -}}
{{- fail "oauth: config.oauth.enabled requires config.webAssertion.issuer, audience and existingSecret (consent is approved on the web approval page)" -}}
{{- end -}}
{{- if not (regexMatch "^https://[^/?#@]+/[^?#]+$" ($o.consentUrl | default "")) -}}
{{- fail (printf "oauth: config.oauth.consentUrl %q must be the web consent page, an https URL with a path and no query or fragment" ($o.consentUrl | default "")) -}}
{{- end -}}
{{- end -}}

{{/*
The hosted MCP endpoint advertises this release's acr-api as its authorization
server, so the two must agree: acr-api serves OAuth, names this issuer, and
issues for this resource URL. A mismatch renders a deployment where every
login ends in invalid_target.
*/}}
{{- define "acr.validateMcpOAuth" -}}
{{- $m := .Values.acrMcp.oauth -}}
{{- $o := .Values.config.oauth -}}
{{- if not (and $m.resourceUrl $m.authorizationServer) -}}
{{- fail "oauth: acrMcp.oauth.resourceUrl and acrMcp.oauth.authorizationServer are set together" -}}
{{- end -}}
{{- if not $o.enabled -}}
{{- fail "oauth: acrMcp.oauth requires config.oauth.enabled (acr-api is the authorization server it advertises)" -}}
{{- end -}}
{{- if ne ($m.authorizationServer | default "") ($o.issuer | default "") -}}
{{- fail (printf "oauth: acrMcp.oauth.authorizationServer %q must equal config.oauth.issuer %q" ($m.authorizationServer | default "") ($o.issuer | default "")) -}}
{{- end -}}
{{- if not (has ($m.resourceUrl | default "") ($o.resources | default list)) -}}
{{- fail (printf "oauth: acrMcp.oauth.resourceUrl %q must be listed in config.oauth.resources" ($m.resourceUrl | default "")) -}}
{{- end -}}
{{- end -}}

{{- /*
OTLP export environment for one workload's ConfigMap. Renders nothing when
otel.enabled is false, so a disabled chart leaves every ConfigMap unchanged.
Call with (dict "root" $ "name" <service.name>).
*/ -}}
{{- define "acr.otelEnv" -}}
{{- $o := .root.Values.otel -}}
{{- if $o.enabled -}}
{{- $endpoint := trim (toString $o.endpoint) -}}
{{- /* `required` accepts a whitespace-only string, and the workloads then
     exit at startup ("OTEL_ENABLED=true requires OTEL_EXPORTER_OTLP_ENDPOINT")
     -- a restart loop instead of a refused release. Trim first, and require a
     scheme the OTLP gRPC exporter can parse. */ -}}
{{- if not $endpoint -}}
{{- fail "otel.endpoint is required when otel.enabled is true" -}}
{{- end -}}
{{- if not (or (hasPrefix "http://" $endpoint) (hasPrefix "https://" $endpoint)) -}}
{{- fail "otel.endpoint must start with http:// or https:// (http:// selects plaintext OTLP/gRPC)" -}}
{{- end -}}
{{- /* A hostless endpoint (http://:4317) parses, and the OTLP exporter then
     dials the pod's own loopback: the deployment looks configured and every
     export is refused locally. Require a host. */ -}}
{{- $authority := (splitList "/" (trimPrefix "https://" (trimPrefix "http://" $endpoint))) | first -}}
{{- if not (splitList ":" $authority | first) -}}
{{- fail "otel.endpoint must name a collector host, not just a port (http://:4317 sends to the pod's own loopback)" -}}
{{- end -}}
{{- $service := trim (toString .name) -}}
{{- if not $service -}}
{{- fail "otel.serviceNames entries must be non-empty" -}}
{{- end -}}
OTEL_ENABLED: "true"
OTEL_EXPORTER_OTLP_ENDPOINT: {{ $endpoint | quote }}
OTEL_SERVICE_NAME: {{ $service | quote }}
{{- end -}}
{{- end -}}

{{- /*
NetworkPolicy egress to the OTLP collector port, when otel.enabled.
*/ -}}
{{- define "acr.otelEgress" -}}
{{- if .Values.otel.enabled -}}
- ports:
    - protocol: TCP
      port: {{ .Values.otel.egressPort }}
{{- end -}}
{{- end -}}
