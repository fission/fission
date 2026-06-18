{{/*
Single source of truth for the fixed-name tenant-workload ClusterRoles that
dynamic multi-namespace mode relies on. Returned as a YAML array of
{name, rules} where `rules` names the shared partial carrying that role's rules
(so the static per-namespace Roles and these ClusterRoles cannot drift).

THREE consumers derive from this one list, so the role set, the controller's
`bind` resourceNames, and the admission policy's roleRef allow-list stay in
lockstep automatically:
  - dynamic-workload-roles.yaml ranges it to emit the ClusterRoles;
  - tenant-controller/rbac.yaml ranges the names into the `bind` resourceNames;
  - tenant-controller/admission-policy.yaml builds the VAP roleRef CEL list.
The Go side (pkg/apis/core/v1/const.go *TenantWorkloadClusterRole) names the
same set for the controller's binder; it cannot read Helm at runtime, so it is
the one irreducible second copy — covered by the dynamic-tenancy integration
tests (a drift would fail tenant onboarding / the admission-policy test).
*/}}
{{- define "fission.tenantWorkloadRoles" -}}
# executor/buildermgr: manage their workloads (Deployments, Services, pods, HPAs)
# in tenant namespaces — same rules as the static per-namespace kubernetes Roles.
- name: fission-executor-tenant-workload
  rules: executor-kuberules
- name: fission-buildermgr-tenant-workload
  rules: buildermgr-kuberules
# fetcher/builder/fetcher-websocket: what a function pod's sidecars may read in
# their own tenant namespace (configmaps/secrets/packages/serviceaccounts; events
# + pods for the websocket fetcher). The dynamic twin of the static Roles in
# _function-access-role.tpl; pkg/tenant/provision.go binds these by name.
- name: fission-fetcher-tenant-workload
  rules: fissionFunction.fetcherRules
- name: fission-builder-tenant-workload
  rules: fissionFunction.builderRules
- name: fission-fetcher-websocket-tenant-workload
  rules: fissionFunction.fetcherWebsocketRules
{{- end -}}
