{{/*
Private ACR chart helpers.

Fail-closed guards live here so that every security violation renders a named
error (mutable-image, invalid-secret-ref, invalid-image-pull-secret-ref,
shared-runtime-migration-dsn, injected-mcp) before any manifest is produced.
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
Immutable image guard. Rejects an empty or mutable (non-@sha256) reference.
*/}}
{{- define "acr.image" -}}
{{- $ref := .Values.image.reference | default "" -}}
{{- if not $ref -}}
{{- fail "mutable-image: image.reference is required and must be an immutable @sha256 digest reference" -}}
{{- end -}}
{{- if not (regexMatch "@sha256:[0-9a-f]{64}$" $ref) -}}
{{- fail (printf "mutable-image: image.reference %q must be pinned to an immutable @sha256:<digest>; mutable tags are rejected" $ref) -}}
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
{{- $_ := include "acr.requireSecretName" (dict "name" ($c.entitlementToken.existingSecret | default "") "label" "invalid-secret-ref" "field" "credentials.entitlementToken.existingSecret") -}}
{{- $runtimeKey := $c.runtime.postgresDsnKey | default "ACR_POSTGRES_DSN" -}}
{{- $migrationKey := $c.migration.postgresDsnKey | default "ACR_POSTGRES_MIGRATION_DSN" -}}
{{- if and (eq $runtimeSecret $migrationSecret) (eq $runtimeKey $migrationKey) -}}
{{- fail (printf "shared-runtime-migration-dsn: the migration DSN reference (%s/%s) must differ from the runtime DSN reference; a schema-owner migration credential must never reuse the least-privilege runtime credential" $migrationSecret $migrationKey) -}}
{{- end -}}
{{- end -}}

{{/*
No additional-workload guard. deployment.extraContainers is unsupported: any
additional container would bypass the pod-security and no-MCP guarantees, so a
non-empty value fails closed. A value that names acr-mcp is called out
specifically. The Deployment never renders extraContainers regardless.
*/}}
{{- define "acr.validateNoMcp" -}}
{{- $extra := .Values.deployment.extraContainers | default list -}}
{{- range $i, $ctr := $extra -}}
{{- $blob := printf "%s %s %s %s" ($ctr.name | default "") ($ctr.image | default "") (join " " ($ctr.command | default list)) (join " " ($ctr.args | default list)) -}}
{{- if regexMatch "acr-mcp" $blob -}}
{{- fail (printf "injected-mcp: deployment.extraContainers[%d] would run acr-mcp; the MCP sidecar is host-local and must never be deployed as a workload" $i) -}}
{{- end -}}
{{- end -}}
{{- if $extra -}}
{{- fail "injected-mcp: deployment.extraContainers is not permitted; additional workload containers would bypass the restricted pod-security and no-MCP guarantees" -}}
{{- end -}}
{{- end -}}

{{/*
Connection-kind guard. When postgresConnectionKind is pgbouncer, both the
runtime and migration pooler admin DSN keys are required so transaction-pool
validation is wired; when direct, they must be absent.
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
{{- else if ne $kind "direct" -}}
{{- fail (printf "invalid-connection-kind: config.postgresConnectionKind %q must be direct or pgbouncer" $kind) -}}
{{- end -}}
{{- end -}}

{{/*
Entitlement-origin guard. The runtime requires an origin (scheme://host[:port])
with no path; reject a URL that carries a path.
*/}}
{{- define "acr.validateEntitlementOrigin" -}}
{{- $url := .Values.config.entitlement.url | default "" -}}
{{- if $url -}}
{{- if not (regexMatch "^https?://[^/]+/?$" $url) -}}
{{- fail (printf "entitlement-origin: config.entitlement.url %q must be an origin (scheme and host only, no path); the runtime rejects a path component" $url) -}}
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
