{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 24 | trimSuffix "-" -}}
{{- end -}}

{{/*
fission.nameFormat selects how generated resource names are built, and is
validated here so a typo fails the render rather than silently falling back to
legacy behaviour.

  standard (default) — release-qualified names with room to stay distinct.
  legacy             — the pre-1.(N+1) names, kept as an escape hatch.

RFC-0029 phase 2; `standard` became the default in 1.(N+1), closing #2906.

Flipping the default is safe because `fullname` has exactly four consumers and
all of them are hook JOBS — no Deployment, Service, Secret, ConfigMap,
ServiceAccount, Role or CRD name is derived from it. Two of those Jobs already
carry a `randNumeric 3` suffix, so their names change on every render anyway,
and the analytics Jobs carry `hook-delete-policy: hook-succeeded`, so a
successful run leaves nothing behind under the old name.

The one residue: a hook Job that FAILED and was never cleaned up stays under
its old name after the flip. It is inert — an orphaned completed/failed Job —
but it will not be reaped by `before-hook-creation`, which matches on name.
Delete it by hand if you see one.

#2835 (two installs sharing a cluster) additionally needs the instance-class
filter and does NOT close on this: every fixed-name resource (router, executor,
the ClusterRoles) still collides.
*/}}
{{- define "fission.nameFormat" -}}
{{- $v := default "standard" .Values.nameFormat -}}
{{- if not (has $v (list "legacy" "standard")) -}}
{{- fail (printf "nameFormat must be \"legacy\" or \"standard\", got %q" $v) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{/*
Create a default fully qualified app name.

The legacy form truncates at 24 characters. That is NOT a Kubernetes limit —
names allow 253, and DNS labels 63 — it is historical, and it is what makes two
releases collide: any two release names sharing a 24-character prefix produce
the same fullname, so a second install silently reuses the first's object names.

The standard form keeps 43. That budget is derived, not chosen: the longest
suffix any consumer appends is `-<chart version>-post-install` (20 characters
at a 6-character version), and Job names must stay within 63 because the Job
controller stamps the name into the `job-name` LABEL, where 63 is a hard limit.
43 + 20 = 63. Raising it silently produces Jobs the apiserver rejects — which
is exactly what the chart test caught while this was being written.
*/}}
{{- define "fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- $full := printf "%s-%s" .Release.Name $name -}}
{{- if eq (include "fission.nameFormat" .) "standard" -}}
{{- $full | trunc 43 | trimSuffix "-" -}}
{{- else -}}
{{- $full | trunc 24 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}


{{/*
This is a template with config parameters for optional features in fission. This gets mounted on to the controller pod
as a config map.
To add new features with config parameters, create a yaml block below with the feature name and define a corresponding struct in
controller/config.go
*/}}
{{- define "config" -}}
canary:
  enabled: {{ .Values.canaryDeployment.enabled }}
  prometheusSvc: {{ .Values.prometheus.serviceEndpoint | default "" | quote }}
  {{- printf "\n" -}}
auth:
  enabled: {{ .Values.authentication.enabled | default false }}
  {{- if .Values.authentication.enabled }}
  authUriPath: {{ .Values.authentication.authUriPath | default "/auth/login" | quote}}
  jwtExpiryTime: {{ .Values.authentication.jwtExpiryTime | default 120 }}
  jwtIssuer: {{ .Values.authentication.jwtIssuer | default "fission" | quote }}
  {{- end }}
{{- end -}}

{{/*
Helper template to construct image names with repository and tag
*/}}
{{- define "imageWithTag" -}}
{{- $repository := index . 0 -}}
{{- $image := index . 1 -}}
{{- $tag := index . 2 -}}
{{- if $repository -}}
{{- printf "%s/%s%s" $repository $image (ne $tag "" | ternary (printf ":%s" $tag) "") -}}
{{- else -}}
{{- printf "%s%s" $image (ne $tag "" | ternary (printf ":%s" $tag) "") -}}
{{- end -}}
{{- end -}}

{{- define "fission-bundleImage" -}}
{{- $args := list .Values.repository .Values.image .Values.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "reporterImage" -}}
{{- $args := list .Values.repository .Values.postInstallReportImage .Values.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "fetcherImage" -}}
{{- $args := list (.Values.fetcher.repository | default .Values.repository) .Values.fetcher.image .Values.fetcher.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "preUpgradeChecksImage" -}}
{{- $args := list (.Values.preUpgradeChecks.repository | default .Values.repository) .Values.preUpgradeChecks.image .Values.preUpgradeChecks.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "opentelemtry.envs" }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: "{{ .Values.openTelemetry.otlpCollectorEndpoint }}"
- name: OTEL_EXPORTER_OTLP_INSECURE
  value: "{{ .Values.openTelemetry.otlpInsecure }}"
- name: OTEL_LOGS_ENABLED
  value: "{{ .Values.openTelemetry.logsEnabled }}"
{{- if .Values.openTelemetry.otlpHeaders }}
- name: OTEL_EXPORTER_OTLP_HEADERS
  value: "{{ .Values.openTelemetry.otlpHeaders }}"
{{- end }}
- name: OTEL_TRACES_SAMPLER
  value: "{{ .Values.openTelemetry.tracesSampler }}"
- name: OTEL_TRACES_SAMPLER_ARG
  value: "{{ .Values.openTelemetry.tracesSamplingRate }}"
- name: OTEL_PROPAGATORS
  value: "{{ .Values.openTelemetry.propagators }}"
- name: OTEL_METRICS_EXPORTER
  value: "{{ .Values.openTelemetry.metricsExporter }}"
{{- end }}

{{- define "fission-resource-namespace.envs" }}
- name: FISSION_BUILDER_NAMESPACE
  value: "{{ .Values.builderNamespace }}"
- name: FISSION_FUNCTION_NAMESPACE
  value: "{{ .Values.functionNamespace }}"
- name: FISSION_DEFAULT_NAMESPACE
  value: "{{ .Values.defaultNamespace }}"
- name: FISSION_RESOURCE_NAMESPACES
{{- if gt (len .Values.additionalFissionNamespaces) 0 }}
  value: "{{ .Values.defaultNamespace }},{{ join "," .Values.additionalFissionNamespaces }}"
{{- else }}
  value: {{ .Values.defaultNamespace }}
{{- end }}
- name: FISSION_TENANCY_MODE
  value: "{{ include "fission.tenancyMode" . }}"
{{- end }}

{{/*
fission.tenancyMode — the configured multi-namespace tenancy posture, normalised
to one of static|dynamic|cluster. Single source of truth for every gate.
*/}}
{{- define "fission.tenancyMode" -}}
{{- dig "mode" "static" (.Values.tenancy | default dict) -}}
{{- end -}}

{{/*
fission.tenancyControllerEnabled — true (non-empty) when the tenant controller and
the dynamic-cluster machinery should be rendered, i.e. tenancy.mode is dynamic OR
cluster. Empty string (falsey) for static. Use: {{- if include "fission.tenancyControllerEnabled" . }}
*/}}
{{- define "fission.tenancyControllerEnabled" -}}
{{- if ne (include "fission.tenancyMode" .) "static" -}}true{{- end -}}
{{- end -}}

{{- define "kube_client.envs" }}
- name: KUBE_CLIENT_QPS
  value: "{{ .Values.kubernetesClientQPS }}"
- name: KUBE_CLIENT_BURST
  value: "{{ .Values.kubernetesClientBurst }}"
{{- end}}

{{/*
leaderElection.envs renders the env entries that enable client-go leader
election for a control-plane controller subsystem. Pass a dict with key
"enabled" (bool). POD_NAME identifies the lease holder; the lease namespace
falls back to the in-cluster service-account namespace when POD_NAMESPACE is
unset, so it is intentionally not emitted here (some deployments already set
it). Disabled by default → behaviour is unchanged for single-replica installs.
*/}}
{{- define "leaderElection.envs" }}
- name: LEADER_ELECTION_ENABLED
  value: {{ .enabled | default false | quote }}
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
{{- end }}

{{/*
fission.internalAuthSecretName is the Secret holding the internal-auth HMAC
master: the operator's pre-created one when internalAuth.existingSecret is set,
otherwise the chart-generated "fission-internal-auth".

Every consumer must go through this. The Go side resolves the same name from
FISSION_INTERNAL_AUTH_SECRET_NAME (fv1.InternalAuthSecretName), and a
disagreement between the two does not fail loudly — the secretKeyRef is
optional, so pods start with the env var absent and every archive fetch and
builder upload 401s with nothing naming the Secret as the cause.
*/}}
{{- define "fission.internalAuthSecretName" -}}
{{- default "fission-internal-auth" .Values.internalAuth.existingSecret -}}
{{- end -}}

{{/*
fission.internalAuthGenerateInCluster is non-empty when the pre-upgrade hook,
not the template, mints the master.

Exactly one of them may provision it, and this is the ONLY place that decides
which. Three templates gate on this answer — the Secret itself, the hook's
GENERATE_AUTH_SECRET env, and the hook's RBAC — and open-coding the condition
in each is how they drift apart. Both drift directions fail silently: both
provisioning means two masters race and half the derived keys are wrong;
neither means no master at all and every internal call is unsigned.

The whitespace trimming is load-bearing. A stray newline would make the
`include` non-empty, so the helper would read as true in every case.
*/}}
{{- define "fission.internalAuthGenerateInCluster" -}}
{{- if and .Values.internalAuth.enabled .Values.internalAuth.autoGenerate (not .Values.internalAuth.existingSecret) (not .Values.internalAuth.secret) -}}
true
{{- end -}}
{{- end -}}

{{/*
internalAuth.envs renders the two env entries that wire the HMAC shared
secret into a Fission control-plane container. See the design at docs/internal-auth/00-design.md. The OLD
secret is mounted with optional: true so rotation can drop it without
forcing the chart to render an empty key.
*/}}
{{- define "internalAuth.envs" }}
{{- if .Values.internalAuth.enabled }}
- name: FISSION_INTERNAL_AUTH_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "fission.internalAuthSecretName" . }}
      key: secret
- name: FISSION_INTERNAL_AUTH_SECRET_OLD
  valueFrom:
    secretKeyRef:
      name: {{ include "fission.internalAuthSecretName" . }}
      key: oldSecret
      optional: true
# The Go side (fetcher pod-spec builder, storagesvc client) resolves the same
# name from this env var; see fv1.InternalAuthSecretName.
- name: FISSION_INTERNAL_AUTH_SECRET_NAME
  value: {{ include "fission.internalAuthSecretName" . | quote }}
{{- end }}
{{- end }}

{{/*
Define the svc's name
*/}}
{{- define "fission-webhook.svc" -}}
{{- printf "webhook-service" -}}
{{- end -}}

{{- define "fission-function-ns" -}}
{{- if .Values.functionNamespace -}}
{{- printf "%s" .Values.functionNamespace -}}
{{- else -}}
{{- printf "%s" .Values.defaultNamespace -}}
{{- end -}}
{{- end -}}

{{- define "fission-builder-ns" -}}
{{- if .Values.builderNamespace -}}
{{- printf "%s" .Values.builderNamespace -}}
{{- else -}}
{{- printf "%s" .Values.defaultNamespace -}}
{{- end -}}
{{- end -}}

{{/*
coverage.* helpers: emit GOCOVERDIR env, a hostPath volumeMount, and the
hostPath volume for integration-test binary coverage. DEV/CI ONLY — gated
by .Values.coverage.enabled (default false), so they render nothing in
production. See values.yaml `coverage`.
*/}}
{{- define "coverage.envs" }}
{{- if .Values.coverage.enabled }}
- name: GOCOVERDIR
  value: {{ .Values.coverage.mountPath | default "/coverage" | quote }}
{{- end }}
{{- end }}

{{- define "coverage.volumemount" }}
{{- if .Values.coverage.enabled }}
- name: coverage-data
  mountPath: {{ .Values.coverage.mountPath | default "/coverage" | quote }}
{{- end }}
{{- end }}

{{- define "coverage.volume" }}
{{- if .Values.coverage.enabled }}
- name: coverage-data
  hostPath:
    path: {{ .Values.coverage.hostPath | default "/fission-coverage" | quote }}
    # Directory (not DirectoryOrCreate): the dir MUST be pre-created on the
    # node owned by the pod uid (see values.yaml + the CI workflow). This
    # enforces the uid-owned/0700 contract and fails loudly if misconfigured
    # rather than letting kubelet create a root-owned dir.
    type: Directory
{{- end }}
{{- end }}

{{/*
fission.routerInternalPort is the router's internal listener port — the
/fission-function/... listener behind the GHSA-3g33-6vg6-27m8 split. Mirrored
by pkg/svcinfo.PortRouterInternal; a Go-side drift test compares the rendered
chart against those constants.
*/}}
{{- define "fission.routerInternalPort" -}}
{{ .Values.router.internalPort | default 8889 }}
{{- end -}}

{{/*
fission.routerInternalURL is the in-cluster URL internal publishers
(kubewatcher / timer / mqtrigger / mqt-keda / mcp) use to reach the router's
internal listener.
*/}}
{{- define "fission.routerInternalURL" -}}
http://router-internal.{{ .Release.Namespace }}:{{ include "fission.routerInternalPort" . }}
{{- end -}}

{{/*
fission.authenticationSecretName is the Secret the router and MCP deployments
read auth credentials (username/password) and the JWT signing key from —
either a user-managed Secret named by authentication.existingSecret (which
must carry the keys username, password and jwtSigningKey) or the
chart-generated "router" Secret (templates/router/secret.yaml).
*/}}
{{- define "fission.authenticationSecretName" -}}
{{ .Values.authentication.existingSecret | default "router" }}
{{- end -}}

{{/*
fission.podNamespaceEnv injects POD_NAMESPACE via the downward API — the
namespace fission-bundle's AddressResolver derives sibling-service URL
defaults from when a URL flag/env is not explicitly set.
*/}}
{{- define "fission.podNamespaceEnv" -}}
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
{{- end }}

{{/*
fission.routerPort is the router's public listener port (fronted by the
Service's port 80). Mirrored by pkg/svcinfo.PortRouter.
*/}}
{{- define "fission.routerPort" -}}
{{ .Values.router.port | default 8888 }}
{{- end -}}

{{/*
fission.mcpPort is the MCP tool server's port. Mirrored by
pkg/svcinfo.PortMCP.
*/}}
{{- define "fission.mcpPort" -}}
{{ .Values.mcp.port | default 8890 }}
{{- end -}}

{{/*
fission.statestorePort is the embedded statestore's capability API port.
Mirrored by pkg/svcinfo.PortStatestore (RFC-0021).
*/}}
{{- define "fission.statestorePort" -}}
{{ (.Values.statestore | default dict).port | default 8891 }}
{{- end -}}

{{/*
fission.statesvcPort is the statesvc function-facing keyed-state API port.
Mirrored by pkg/svcinfo.PortStateSvc (RFC-0023).
*/}}
{{- define "fission.statesvcPort" -}}
{{ (.Values.functionState | default dict).port | default 8893 }}
{{- end -}}

{{/*
fission.workflowPort is the workflow engine head's port. Mirrored by
pkg/svcinfo.PortWorkflow (RFC-0022).
*/}}
{{- define "fission.workflowPort" -}}
{{ (.Values.workflows | default dict).port | default 8892 }}
{{- end -}}

{{/*
fission.crdsMode is the CRD delivery mechanism (RFC-0029 §1), normalised and
validated. Single-writer by design — exactly one mechanism ever writes the CRD
objects, because two server-side appliers under different field managers is an
ownership fight nobody wins.

  hook (default) — the pre-install/pre-upgrade Job server-side-applies the
                   CRD bundle embedded in its own image, before the release
                   manifests roll. This also delivers RFC-0028's
                   CRDs-before-controllers ordering.

                   This is why the hook Job and every object it depends on
                   (ServiceAccount, ClusterRole, ClusterRoleBinding, and the
                   namespaced Role/RoleBinding) are annotated
                   pre-install AS WELL AS pre-upgrade: in this mode the hook
                   delivers the CRDs a FRESH cluster has none of, which is
                   what makes a one-shot `helm install` work. Anything the
                   Job needs must also carry a hook-weight strictly below
                   the Job's, or Helm may order the Job first.
  none           — bring your own (`kubectl create -k crds/v1` /
                   `make create-crds`); the hook only checks presence, exactly
                   as it does today.

The RFC's third mode, `manifests` (CRDs templated into the rendered set for
Argo/Flux diffing), needs the generated YAML to live inside the chart
directory — Helm's .Files cannot read outside it — so it lands with the
chart-packaging change rather than being half-shipped here.
*/}}
{{- define "fission.crdsMode" -}}
{{- $mode := dig "mode" "hook" (.Values.crds | default dict) -}}
{{- if eq $mode "manifests" -}}
{{- fail "crds.mode=manifests is not available yet (it needs the CRD manifests packaged inside the chart); use hook or none" -}}
{{- end -}}
{{- if not (has $mode (list "hook" "none")) -}}
{{- fail (printf "crds.mode must be one of hook|none, got %q" $mode) -}}
{{- end -}}
{{- $mode -}}
{{- end -}}

{{/*
fission.crdNames is the space-separated list of Fission CRD object names the
hook's RBAC fences its mutating verbs to. It must stay in lockstep with
crds/v1 — a CRD missing here is one the hook cannot update, which surfaces as
a forbidden error on the upgrade that introduces its schema change.
*/}}
{{- define "fission.crdNames" -}}
canaryconfigs.fission.io environments.fission.io fissiontenants.fission.io functionaliases.fission.io functions.fission.io functionversions.fission.io httptriggers.fission.io kuberneteswatchtriggers.fission.io messagequeuetriggers.fission.io packages.fission.io timetriggers.fission.io workflowruns.fission.io workflows.fission.io
{{- end -}}

{{/*
fission.adoptSecretNames is the space-separated list of chart-generated Secrets
the pre-upgrade hook stamps helm.sh/resource-policy=keep onto, so that moving
their generation out of the templating layer does not prune the live object on
the next upgrade (RFC-0029 §3; mechanism verified on Helm v4.2.3).

Only Secrets the chart itself may have created belong here — never one supplied
via an existingSecret value, which the operator owns and Helm never manages.
Empty when nothing needs adopting, which makes the whole migration render away.
*/}}
{{- define "fission.adoptSecretNames" -}}
{{- $names := list -}}
{{- if and .Values.internalAuth.enabled (not .Values.internalAuth.existingSecret) -}}
{{- $names = append $names (include "fission.internalAuthSecretName" .) -}}
{{- end -}}
{{- if and .Values.authentication.enabled (not .Values.authentication.existingSecret) -}}
{{- $names = append $names "router" -}}
{{- end -}}
{{- if not .Values.webhook.certManager.enabled -}}
{{- $names = append $names "fission-webhook-certs" -}}
{{- end -}}
{{- join " " $names -}}
{{- end -}}

{{/*
fission.adoptSecretNamespaces is the namespace set the retention adoption runs
over. It mirrors internal-auth-secret.yaml's own replication set exactly: under
STATIC tenancy the master is copied into defaultNamespace and every
additionalFissionNamespaces, because kubelet cannot resolve a cross-namespace
secretKeyRef. Leaving those copies out means they prune on upgrade and function
pods come up unsigned — a silent failure, described in full on
AdoptSecretsForKeep in cmd/preupgradechecks/adoptsecrets.go.

Under dynamic/cluster tenancy the tenant copies are INTENTIONALLY removed (each
tenant gets a controller-owned derived key instead), so pinning them with a keep
annotation would fight that design — hence the same tenancy gate.
*/}}
{{- define "fission.adoptSecretNamespaces" -}}
{{- $namespaces := list .Release.Namespace -}}
{{- if eq (include "fission.tenancyMode" .) "static" -}}
{{-   if and .Values.defaultNamespace (ne .Values.defaultNamespace .Release.Namespace) -}}
{{-     $namespaces = append $namespaces .Values.defaultNamespace -}}
{{-   end -}}
{{-   range $ns := .Values.additionalFissionNamespaces -}}
{{-     if not (has $ns $namespaces) -}}
{{-       $namespaces = append $namespaces $ns -}}
{{-     end -}}
{{-   end -}}
{{- end -}}
{{- join " " $namespaces -}}
{{- end -}}
