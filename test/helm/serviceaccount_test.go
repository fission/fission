// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// allComponentsValues enables every optional component, so a render sees every
// ServiceAccount the chart can produce rather than the default subset.
const allComponentsValues = `
statestore:
  enabled: true
  mode: embedded
eventing:
  enabled: true
functionState:
  enabled: true
workflows:
  enabled: true
mcp:
  enabled: true
  allowInsecure: true
canaryDeployment:
  enabled: true
tenancy:
  mode: dynamic
`

// renderWithValues writes a values file and renders the chart against it.
func renderWithValues(t *testing.T, values string, extraArgs ...string) manifests {
	t.Helper()
	f := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(f, []byte(values), 0o600))
	return render(t, append([]string{"-f", f}, extraArgs...)...)
}

// renderErrWithValues is renderWithValues for the cases that must FAIL to render.
func renderErrWithValues(t *testing.T, values string) (string, error) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(f, []byte(values), 0o600))
	return renderErr(t, "-f", f)
}

// serviceAccounts decodes every rendered ServiceAccount.
func serviceAccounts(t *testing.T, ms manifests) []corev1.ServiceAccount {
	t.Helper()
	var out []corev1.ServiceAccount
	for _, m := range ms.ofKind("ServiceAccount") {
		out = append(out, *decodeAs[corev1.ServiceAccount](t, &m))
	}
	require.NotEmpty(t, out)
	return out
}

// saComponents is the component-key list from the fission.saComponents helper.
// It is duplicated here on purpose: TestServiceAccountEveryAccountIsAnnotatable
// proves this list and the rendered accounts agree, and a copy the test owns is
// what makes that a real assertion rather than a tautology against the helper.
var saComponents = []string{
	"builder", "buildermgr", "canaryconfig", "executor", "fetcher", "kafka",
	"kubewatcher", "mcp", "mqtKeda", "preupgrade", "router", "statestore",
	"statestoreMqt", "statesvc", "storagesvc", "tenantController", "timer",
	"webhook", "workflow",
}

// TestServiceAccountAnnotationsDefaultToNothing pins the upgrade-safety
// property: an operator who never sets `serviceAccounts` must get exactly the
// manifest they got before this feature existed. Helm's `nindent` turns an
// empty include into a line of spaces rather than nothing, which is why the
// helpers carry their own indentation — this test is what keeps that true.
func TestServiceAccountAnnotationsDefaultToNothing(t *testing.T) {
	for _, sa := range serviceAccounts(t, renderWithValues(t, allComponentsValues)) {
		for k := range sa.Annotations {
			assert.Truef(t, strings.HasPrefix(k, "helm.sh/"),
				"ServiceAccount %q carries annotation %q by default; only the chart's own helm.sh/* hook annotations may be present", sa.Name, k)
		}
		for k := range sa.Labels {
			assert.Containsf(t, []string{"svc", "application", "chart"}, k,
				"ServiceAccount %q carries label %q by default", sa.Name, k)
		}
	}
}

// TestServiceAccountChartWideMetadata covers the flat case: one value, every
// account.
func TestServiceAccountChartWideMetadata(t *testing.T) {
	ms := renderWithValues(t, allComponentsValues+`
serviceAccounts:
  annotations:
    my-org/managed-by: helm
  labels:
    my-org/owner: platform-team
`)
	accounts := serviceAccounts(t, ms)
	require.Len(t, accounts, len(saComponents), "every component key must produce exactly one account")
	for _, sa := range accounts {
		assert.Equalf(t, "helm", sa.Annotations["my-org/managed-by"], "ServiceAccount %q missing the chart-wide annotation", sa.Name)
		assert.Equalf(t, "platform-team", sa.Labels["my-org/owner"], "ServiceAccount %q missing the chart-wide label", sa.Name)
	}
}

// TestServiceAccountEveryAccountIsAnnotatable is the drift guard, and the
// reason this file exists rather than a single storagesvc test.
//
// It sets a component-unique annotation for every key in saComponents and
// asserts each one lands on exactly one account. A new ServiceAccount template
// that forgets the include fails the count assertion; a component key that no
// longer renders an account fails its own subtest. Without this, the next
// account added to the chart would silently have no annotation support — which
// is precisely how the support-dump selectors drifted to missing eight
// components before #3675.
func TestServiceAccountEveryAccountIsAnnotatable(t *testing.T) {
	var b strings.Builder
	b.WriteString(allComponentsValues)
	b.WriteString("serviceAccounts:\n")
	for _, c := range saComponents {
		fmt.Fprintf(&b, "  %s:\n    annotations:\n      fission.test/component: %s\n", c, c)
	}
	accounts := serviceAccounts(t, renderWithValues(t, b.String()))

	require.Len(t, accounts, len(saComponents),
		"the chart renders %d ServiceAccounts but fission.saComponents lists %d — add the new account's key to the helper, values.yaml and saComponents here",
		len(accounts), len(saComponents))

	got := map[string]string{}
	for _, sa := range accounts {
		c, ok := sa.Annotations["fission.test/component"]
		require.Truef(t, ok, "ServiceAccount %q has no per-component annotation — its template is missing the fission.saMetadata include", sa.Name)
		assert.NotContainsf(t, got, c, "component key %q annotated more than one account (%q and %q)", c, got[c], sa.Name)
		got[c] = sa.Name
	}
	keys := make([]string, 0, len(got))
	for c := range got {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	assert.Equal(t, saComponents, keys, "every component key must annotate exactly one rendered account")
}

// TestServiceAccountComponentOverridesChartWide pins the merge direction. This
// is the case #3166 is actually about: an IRSA role ARN is scoped to one
// component's permissions, so a chart-wide value can never be the right answer
// for it.
func TestServiceAccountComponentOverridesChartWide(t *testing.T) {
	const roleARN = "arn:aws:iam::012345678901:role/fission-storagesvc"
	ms := renderWithValues(t, allComponentsValues+`
serviceAccounts:
  annotations:
    my-org/managed-by: chart-wide
  storagesvc:
    annotations:
      eks.amazonaws.com/role-arn: `+roleARN+`
      my-org/managed-by: storagesvc-only
`)
	storagesvc := findAs[corev1.ServiceAccount](t, ms, "ServiceAccount", "fission-storagesvc")
	assert.Equal(t, roleARN, storagesvc.Annotations["eks.amazonaws.com/role-arn"], "the IRSA annotation must reach the account")
	assert.Equal(t, "storagesvc-only", storagesvc.Annotations["my-org/managed-by"], "the component value must win over the chart-wide one, key by key")

	router := findAs[corev1.ServiceAccount](t, ms, "ServiceAccount", "fission-router")
	assert.Equal(t, "chart-wide", router.Annotations["my-org/managed-by"], "an untouched account keeps the chart-wide value")
	assert.NotContains(t, router.Annotations, "eks.amazonaws.com/role-arn",
		"a component-scoped role ARN must not leak onto other accounts — that is the whole reason it is per-component")
}

// TestServiceAccountNoCrossAccountLeak proves account isolation when a
// chart-wide map and several component maps are in play at once.
//
// The helpers run once per account against the same chart-wide map, and sprig's
// merge mutates its destination — so a helm bump whose merge writes through to
// either argument would let the first account rendered contaminate the other
// eighteen. For an IRSA role ARN that is not a cosmetic bug: it would grant every
// component storagesvc's S3 permissions.
func TestServiceAccountNoCrossAccountLeak(t *testing.T) {
	exclusive := map[string]string{
		"fission-storagesvc": "only/storagesvc",
		"fission-router":     "only/router",
		"fission-kafka":      "only/kafka",
		"fission-builder":    "only/builder",
	}
	ms := renderWithValues(t, allComponentsValues+`
serviceAccounts:
  annotations:
    shared/everywhere: global
  storagesvc:
    annotations:
      only/storagesvc: s3-role
  router:
    annotations:
      only/router: router-role
  kafka:
    annotations:
      only/kafka: msk-role
  builder:
    annotations:
      only/builder: builder-role
`)
	for _, sa := range serviceAccounts(t, ms) {
		assert.Equalf(t, "global", sa.Annotations["shared/everywhere"], "ServiceAccount %q must carry the chart-wide annotation", sa.Name)
		for owner, key := range exclusive {
			if owner == sa.Name {
				assert.Containsf(t, sa.Annotations, key, "ServiceAccount %q must carry its own annotation", sa.Name)
				continue
			}
			assert.NotContainsf(t, sa.Annotations, key, "ServiceAccount %q leaked %q, which belongs to %q", sa.Name, key, owner)
		}
	}
}

// TestServiceAccountPreUpgradeKeepsHookAnnotations covers the one account that
// already owns an annotations block. User annotations merge into it; the Helm
// hook annotations that decide when the account exists must survive, because
// losing hook-weight would let the Job be ordered before the identity it runs
// as.
func TestServiceAccountPreUpgradeKeepsHookAnnotations(t *testing.T) {
	ms := renderWithValues(t, allComponentsValues+`
serviceAccounts:
  preupgrade:
    annotations:
      my-org/reviewed: "true"
`)
	sa := findAs[corev1.ServiceAccount](t, ms, "ServiceAccount", "fission-preupgrade")
	assert.Equal(t, "true", sa.Annotations["my-org/reviewed"])
	assert.Equal(t, "pre-install,pre-upgrade", sa.Annotations["helm.sh/hook"], "the hook annotation must survive the merge")
	assert.Equal(t, "-3", sa.Annotations["helm.sh/hook-weight"], "hook-weight orders the account before the Job that runs as it")
	assert.Contains(t, sa.Annotations["helm.sh/hook-delete-policy"], "hook-succeeded")
}

// TestServiceAccountLabelsMergeWithChartLabels covers the four accounts that
// already own a labels block.
func TestServiceAccountLabelsMergeWithChartLabels(t *testing.T) {
	ms := renderWithValues(t, allComponentsValues+`
serviceAccounts:
  labels:
    my-org/owner: platform-team
  workflow:
    labels:
      my-org/owner: workflow-team
`)
	wf := findAs[corev1.ServiceAccount](t, ms, "ServiceAccount", "fission-workflow")
	assert.Equal(t, "workflow-team", wf.Labels["my-org/owner"], "the component label must win over the chart-wide one")
	assert.Equal(t, "workflow", wf.Labels["svc"], "the chart's own svc label must survive")
	assert.Equal(t, "fission-workflow", wf.Labels["application"], "the chart's own application label must survive")
}

// TestServiceAccountReservedKeysFailRender pins the fail-closed guards. Both
// reserved sets would otherwise emit a DUPLICATE key in one YAML mapping, which
// neither Helm nor the API server reports — the value that wins is undefined,
// and for `svc` the loser is detached from the router NetworkPolicy and the
// support dump.
func TestServiceAccountReservedKeysFailRender(t *testing.T) {
	for _, tc := range []struct {
		name, values, wantErr string
	}{
		{
			name:    "component identity label",
			values:  "serviceAccounts:\n  router:\n    labels:\n      svc: hijacked\n",
			wantErr: "reserved",
		},
		{
			name:    "chart-wide identity label",
			values:  "serviceAccounts:\n  labels:\n    application: hijacked\n",
			wantErr: "reserved",
		},
		{
			name:    "helm hook annotation",
			values:  "serviceAccounts:\n  preupgrade:\n    annotations:\n      helm.sh/hook: post-install\n",
			wantErr: "reserved",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderErrWithValues(t, allComponentsValues+tc.values)
			require.Error(t, err, "the render must fail rather than emit a duplicate key")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestServiceAccountUnknownComponentKeyFailsRender pins the typo gate. A key
// the chart does not recognise is the worst failure mode this feature has:
// `serviceAccounts.storagesv` parses, renders, and produces an account with no
// role annotation, so the first symptom is an AccessDenied from S3 at runtime —
// a long way from the values file that caused it.
func TestServiceAccountUnknownComponentKeyFailsRender(t *testing.T) {
	_, err := renderErrWithValues(t, "serviceAccounts:\n  storagesv:\n    annotations:\n      a: b\n")
	require.Error(t, err, "an unknown component key must fail the render, not silently do nothing")
	assert.Contains(t, err.Error(), "unknown key")
	assert.Contains(t, err.Error(), "storagesvc", "the error must list the valid keys so the typo is obvious")
}

// TestServiceAccountComponentsHelperMatchesValues proves values.yaml documents
// every component key. The block is the feature's discovery surface: a key that
// works but is absent there is a feature nobody finds.
func TestServiceAccountComponentsHelperMatchesValues(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "fission-all", "values.yaml"))
	require.NoError(t, err)
	var vals struct {
		ServiceAccounts map[string]any `json:"serviceAccounts"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &vals))
	documented := make([]string, 0, len(vals.ServiceAccounts))
	for k := range vals.ServiceAccounts {
		if k != "annotations" && k != "labels" {
			documented = append(documented, k)
		}
	}
	sort.Strings(documented)
	assert.Equal(t, saComponents, documented, "values.yaml must list exactly the component keys the chart accepts")
}

// TestServiceAccountsSetNamespaceExplicitly pins the consistency fix that came
// with this feature: statestore and statesvc omitted metadata.namespace while
// their 17 siblings set it. It rendered correctly only because `helm template`
// and `helm install` both default to the release namespace — an omission that
// is invisible until someone applies the rendered manifest with a different
// active context.
func TestServiceAccountsSetNamespaceExplicitly(t *testing.T) {
	for _, sa := range serviceAccounts(t, renderWithValues(t, allComponentsValues)) {
		assert.NotEmptyf(t, sa.Namespace, "ServiceAccount %q must set metadata.namespace explicitly", sa.Name)
	}
}
