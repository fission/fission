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
	"sigs.k8s.io/yaml"
)

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

// renderAs renders under a specific release name and returns parsed docs.
func renderAs(t *testing.T, release string, extraArgs ...string) []map[string]any {
	t.Helper()
	out, err := helmTemplate(t, release, extraArgs...)
	require.NoError(t, err)
	var docs []map[string]any
	for doc := range strings.SplitSeq(out, "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(doc), &m) != nil || m == nil {
			continue
		}
		docs = append(docs, m)
	}
	require.NotEmpty(t, docs)
	return docs
}

// renderErr returns the render error, for cases that must FAIL to render.
func renderErr(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()
	return helmTemplate(t, "fission", extraArgs...)
}

// render runs `helm template` on the chart with the given extra args and
// returns the manifest stream split into unstructured docs.
func render(t *testing.T, extraArgs ...string) []map[string]any {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; skipping chart drift check")
	}
	_, filename, _, _ := runtime.Caller(0) //nolint
	chart := filepath.Join(filepath.Dir(filename), "..", "..", "charts", "fission-all")
	args := append([]string{"template", "fission", chart, "--namespace", "fission"}, extraArgs...)
	cmd := exec.CommandContext(t.Context(), helm, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "helm template failed: %s", stderr.String())

	var docs []map[string]any
	for doc := range strings.SplitSeq(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(doc), &m) != nil || m == nil {
			continue
		}
		docs = append(docs, m)
	}
	require.NotEmpty(t, docs)
	return docs
}

// find returns the first doc with the given kind and metadata.name.
func find(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] != kind {
			continue
		}
		if md, ok := d["metadata"].(map[string]any); ok && md["name"] == name {
			return d
		}
	}
	return nil
}

// servicePorts returns the ports slice of a Service doc.
func servicePorts(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	require.NotNil(t, doc)
	spec := doc["spec"].(map[string]any)
	raw := spec["ports"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

// containerArgs returns the first container's args of a Deployment doc.
func containerArgs(t *testing.T, doc map[string]any) []string {
	t.Helper()
	require.NotNil(t, doc)
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	raw, _ := containers[0].(map[string]any)["args"].([]any)
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		out = append(out, fmt.Sprint(a))
	}
	return out
}

// containerPorts returns the first container's declared containerPort numbers.
func containerPorts(t *testing.T, doc map[string]any) []int {
	t.Helper()
	require.NotNil(t, doc)
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	raw, _ := containers[0].(map[string]any)["ports"].([]any)
	out := make([]int, 0, len(raw))
	for _, p := range raw {
		out = append(out, int(p.(map[string]any)["containerPort"].(float64)))
	}
	return out
}

// containerEnv returns the first container's env name→value map.
func containerEnv(t *testing.T, doc map[string]any) map[string]string {
	t.Helper()
	require.NotNil(t, doc)
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	raw, _ := containers[0].(map[string]any)["env"].([]any)
	out := map[string]string{}
	for _, e := range raw {
		m := e.(map[string]any)
		if v, ok := m["value"].(string); ok {
			out[fmt.Sprint(m["name"])] = v
		}
	}
	return out
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

// countByKindName returns how many docs have the given kind and metadata.name.
func countByKindName(docs []map[string]any, kind, name string) int {
	n := 0
	for _, d := range docs {
		if d["kind"] == kind {
			if md, ok := d["metadata"].(map[string]any); ok && md["name"] == name {
				n++
			}
		}
	}
	return n
}

// selectorSvcLabel returns a podSelector's `svc:` matchLabel ("" when absent).
func selectorSvcLabel(sel map[string]any) string {
	ml, _ := sel["matchLabels"].(map[string]any)
	s, _ := ml["svc"].(string)
	return s
}

// ingressSvcLabels collects every `svc:` value a NetworkPolicy's ingress
// allowlists ADMIT.
//
// Deliberately excludes spec.podSelector. That is the workload the policy
// PROTECTS, not one it admits, and folding it in would make npAllowsFromSvc
// report every policy as admitting its own target — the router policy targets
// `svc: router`, so the async-allowlist guard below (which asserts the router
// is NOT admitted by default) would go permanently true and stop guarding.
func ingressSvcLabels(doc map[string]any) []string {
	var out []string
	spec, _ := doc["spec"].(map[string]any)
	ingress, _ := spec["ingress"].([]any)
	for _, r := range ingress {
		rule, _ := r.(map[string]any)
		froms, _ := rule["from"].([]any)
		for _, f := range froms {
			// A non-map `from` entry is malformed chart output; skip it so the
			// caller reports a missing selector rather than panicking.
			fm, _ := f.(map[string]any)
			sel, _ := fm["podSelector"].(map[string]any)
			if v := selectorSvcLabel(sel); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// npAllowsFromSvc reports whether any ingress rule's `from` allowlist admits the
// given svc label via a podSelector.
func npAllowsFromSvc(doc map[string]any, svcLabel string) bool {
	return slices.Contains(ingressSvcLabels(doc), svcLabel)
}

// stringsOf converts a decoded YAML string list to []string.
func stringsOf(vals []any) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// jobNamesFrom extracts generated Job names, dropping the random suffix so the
// stable prefix can be compared.
func jobNamesFrom(docs []map[string]any) []string {
	var out []string
	for _, d := range docs {
		if kind, _ := d["kind"].(string); kind != "Job" {
			continue
		}
		meta, _ := d["metadata"].(map[string]any)
		if name, ok := meta["name"].(string); ok {
			if i := strings.LastIndex(name, "-"); i > 0 {
				name = name[:i]
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// podTemplates returns every rendered pod template (Deployments, Jobs, ...).
// Docs without one are skipped, which is most of a render.
func podTemplates(docs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, doc := range docs {
		spec, _ := doc["spec"].(map[string]any)
		if tmpl, ok := spec["template"].(map[string]any); ok {
			out = append(out, tmpl)
		}
	}
	return out
}

// forEachContainerEnv walks every env entry of every container in every
// rendered pod template.
func forEachContainerEnv(docs []map[string]any, fn func(map[string]any)) {
	for _, tmpl := range podTemplates(docs) {
		podSpec, _ := tmpl["spec"].(map[string]any)
		for _, key := range []string{"containers", "initContainers"} {
			cs, _ := podSpec[key].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				envs, _ := cm["env"].([]any)
				for _, e := range envs {
					if em, ok := e.(map[string]any); ok {
						fn(em)
					}
				}
			}
		}
	}
}
