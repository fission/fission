// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// manifest is one rendered document: its identity (decoded eagerly, so a
// lookup by kind/name needs no type knowledge) plus the raw YAML, decoded
// into a typed object on demand by decodeAs.
type manifest struct {
	metav1.TypeMeta
	Name      string
	Namespace string
	raw       []byte
}

// manifests is a rendered chart.
type manifests []manifest

// helmTemplate runs `helm template` with an explicit release name and returns
// raw stdout, or the error with stderr attached.
func helmTemplate(t *testing.T, release string, extraArgs ...string) (string, error) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; skipping chart drift check")
	}
	_, filename, _, _ := runtime.Caller(0) //nolint
	chart := filepath.Join(filepath.Dir(filename), "..", "..", "charts", "fission-all")
	args := append([]string{"template", release, chart, "--namespace", "fission"}, extraArgs...)
	cmd := exec.CommandContext(t.Context(), helm, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return string(out), nil
}

// parseManifests splits a manifest stream into documents, keeping only those
// with a kind and a metadata.name.
func parseManifests(t *testing.T, stream string) manifests {
	t.Helper()
	var docs manifests
	for doc := range strings.SplitSeq(stream, "\n---") {
		var head struct {
			metav1.TypeMeta `json:",inline"`
			Metadata        metav1.ObjectMeta `json:"metadata"`
		}
		if yaml.Unmarshal([]byte(doc), &head) != nil || head.Kind == "" {
			continue
		}
		docs = append(docs, manifest{
			TypeMeta:  head.TypeMeta,
			Name:      head.Metadata.Name,
			Namespace: head.Metadata.Namespace,
			raw:       []byte(doc),
		})
	}
	require.NotEmpty(t, docs)
	return docs
}

// renderAs renders under a specific release name and returns parsed docs.
func renderAs(t *testing.T, release string, extraArgs ...string) manifests {
	t.Helper()
	out, err := helmTemplate(t, release, extraArgs...)
	require.NoError(t, err)
	return parseManifests(t, out)
}

// renderErr returns the render error, for cases that must FAIL to render.
func renderErr(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()
	return helmTemplate(t, "fission", extraArgs...)
}

// render runs `helm template` on the chart with the given extra args and
// returns the manifest stream split into docs.
func render(t *testing.T, extraArgs ...string) manifests {
	t.Helper()
	out, err := helmTemplate(t, "fission", extraArgs...)
	require.NoError(t, err, "helm template failed")
	return parseManifests(t, out)
}

// find returns the first doc with the given kind and metadata.name, or nil.
func (ms manifests) find(kind, name string) *manifest {
	for i := range ms {
		if ms[i].Kind == kind && ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}

// ofKind returns every doc of the given kind.
func (ms manifests) ofKind(kind string) manifests {
	var out manifests
	for _, m := range ms {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// countByKindName returns how many docs have the given kind and metadata.name.
func (ms manifests) countByKindName(kind, name string) int {
	n := 0
	for _, m := range ms {
		if m.Kind == kind && m.Name == name {
			n++
		}
	}
	return n
}

// decodeAs decodes a manifest into T. It fails the test on a nil manifest, so
// callers can chain it onto find without their own nil check.
func decodeAs[T any](t *testing.T, m *manifest) *T {
	t.Helper()
	require.NotNil(t, m, "manifest must be rendered")
	var out T
	require.NoError(t, yaml.Unmarshal(m.raw, &out), "decoding %s %q", m.Kind, m.Name)
	return &out
}

// findAs is find + decodeAs; it fails the test when the doc is absent.
func findAs[T any](t *testing.T, ms manifests, kind, name string) *T {
	t.Helper()
	m := ms.find(kind, name)
	require.NotNilf(t, m, "%s %q must be rendered", kind, name)
	return decodeAs[T](t, m)
}

// Typed lookups for the kinds the tests inspect.
func deployment(t *testing.T, ms manifests, name string) *appsv1.Deployment {
	t.Helper()
	return findAs[appsv1.Deployment](t, ms, "Deployment", name)
}

func service(t *testing.T, ms manifests, name string) *corev1.Service {
	t.Helper()
	return findAs[corev1.Service](t, ms, "Service", name)
}

func networkPolicy(t *testing.T, ms manifests, name string) *networkingv1.NetworkPolicy {
	t.Helper()
	return findAs[networkingv1.NetworkPolicy](t, ms, "NetworkPolicy", name)
}

// roles decodes every Role and ClusterRole; both share PolicyRules.
type roleDoc struct {
	Kind  string
	Name  string
	Rules []rbacv1.PolicyRule
}

func roles(t *testing.T, ms manifests) []roleDoc {
	t.Helper()
	var out []roleDoc
	for i := range ms {
		m := &ms[i]
		switch m.Kind {
		case "Role":
			r := decodeAs[rbacv1.Role](t, m)
			out = append(out, roleDoc{Kind: m.Kind, Name: m.Name, Rules: r.Rules})
		case "ClusterRole":
			r := decodeAs[rbacv1.ClusterRole](t, m)
			out = append(out, roleDoc{Kind: m.Kind, Name: m.Name, Rules: r.Rules})
		}
	}
	return out
}

// firstContainer returns a Deployment's first container.
func firstContainer(t *testing.T, d *appsv1.Deployment) corev1.Container {
	t.Helper()
	require.NotEmpty(t, d.Spec.Template.Spec.Containers, "deployment %q has no containers", d.Name)
	return d.Spec.Template.Spec.Containers[0]
}

// servicePorts returns the ports of a Service doc.
func servicePorts(t *testing.T, ms manifests, name string) []corev1.ServicePort {
	t.Helper()
	return service(t, ms, name).Spec.Ports
}

// containerArgs returns the first container's args of a Deployment doc.
func containerArgs(t *testing.T, ms manifests, name string) []string {
	t.Helper()
	return firstContainer(t, deployment(t, ms, name)).Args
}

// containerPorts returns the first container's declared containerPort numbers.
func containerPorts(t *testing.T, d *appsv1.Deployment) []int32 {
	t.Helper()
	var out []int32
	for _, p := range firstContainer(t, d).Ports {
		out = append(out, p.ContainerPort)
	}
	return out
}

// containerEnv returns the first container's literal env name→value map
// (valueFrom entries are skipped, as they carry no literal value).
func containerEnv(t *testing.T, d *appsv1.Deployment) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range firstContainer(t, d).Env {
		if e.ValueFrom == nil {
			out[e.Name] = e.Value
		}
	}
	return out
}

// envValue returns the literal value of a named env var on a container.
func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// argAfter returns the argument following flag in args ("" when absent).
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// selectorSvcLabel returns a label selector's `svc:` matchLabel ("" when absent).
func selectorSvcLabel(sel *metav1.LabelSelector) string {
	if sel == nil {
		return ""
	}
	return sel.MatchLabels["svc"]
}

// ingressSvcLabels collects every `svc:` value a NetworkPolicy's ingress
// allowlists ADMIT.
//
// Deliberately excludes spec.podSelector. That is the workload the policy
// PROTECTS, not one it admits, and folding it in would make npAllowsFromSvc
// report every policy as admitting its own target — the router policy targets
// `svc: router`, so the async-allowlist guard below (which asserts the router
// is NOT admitted by default) would go permanently true and stop guarding.
func ingressSvcLabels(np *networkingv1.NetworkPolicy) []string {
	var out []string
	for _, rule := range np.Spec.Ingress {
		for _, from := range rule.From {
			if v := selectorSvcLabel(from.PodSelector); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// npAllowsFromSvc reports whether any ingress rule's `from` allowlist admits the
// given svc label via a podSelector.
func npAllowsFromSvc(np *networkingv1.NetworkPolicy, svcLabel string) bool {
	return slices.Contains(ingressSvcLabels(np), svcLabel)
}

// jobNamesFrom extracts generated Job names, dropping the random suffix so the
// stable prefix can be compared.
func jobNamesFrom(ms manifests) []string {
	var out []string
	for _, m := range ms.ofKind("Job") {
		name := m.Name
		if i := strings.LastIndex(name, "-"); i > 0 {
			name = name[:i]
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// podTemplates returns every rendered pod template (Deployments, StatefulSets,
// DaemonSets, Jobs, CronJobs). Other kinds are skipped, which is most of a
// render.
func podTemplates(t *testing.T, ms manifests) []corev1.PodTemplateSpec {
	t.Helper()
	var out []corev1.PodTemplateSpec
	for i := range ms {
		m := &ms[i]
		switch m.Kind {
		case "Deployment":
			out = append(out, decodeAs[appsv1.Deployment](t, m).Spec.Template)
		case "StatefulSet":
			out = append(out, decodeAs[appsv1.StatefulSet](t, m).Spec.Template)
		case "DaemonSet":
			out = append(out, decodeAs[appsv1.DaemonSet](t, m).Spec.Template)
		case "Job":
			out = append(out, decodeAs[batchv1.Job](t, m).Spec.Template)
		case "CronJob":
			out = append(out, decodeAs[batchv1.CronJob](t, m).Spec.JobTemplate.Spec.Template)
		}
	}
	return out
}

// forEachContainerEnv walks every env entry of every container in every
// rendered pod template.
func forEachContainerEnv(t *testing.T, ms manifests, fn func(corev1.EnvVar)) {
	t.Helper()
	for _, tmpl := range podTemplates(t, ms) {
		for _, c := range tmpl.Spec.Containers {
			for _, e := range c.Env {
				fn(e)
			}
		}
		for _, c := range tmpl.Spec.InitContainers {
			for _, e := range c.Env {
				fn(e)
			}
		}
	}
}
